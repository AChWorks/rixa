#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Interrupt one positively owned update executor inside the disposable guest."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time

spec = importlib.util.spec_from_file_location("update_smoke", Path(__file__).with_name("update-smoke.py"))
proof = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proof)
STAGES = ("payload", "stop", "compatibility", "migrate", "check", "activate", "start", "postflight", "commit")


def systemctl(*arguments):
    result = subprocess.run(["/usr/bin/systemctl", *arguments], stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15)
    if result.returncode:
        raise ValueError("exact executor systemctl observation/action failed")
    return result.stdout.decode()


def observation(unit):
    result = subprocess.run(["/usr/bin/systemctl", "show", unit,
            "--property=Id,LoadState,ActiveState,SubState,MainPID,ControlGroup,ExecStart,InvocationID"],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15)
    values = dict(line.split("=", 1) for line in result.stdout.decode().splitlines() if "=" in line)
    if result.returncode:
        # --collect may unload the unit after the acknowledged whole-cgroup
        # signal. Missing is allowed only as an observation, never ownership.
        if values.get("LoadState") != "not-found" or values.get("ActiveState") != "inactive":
            raise ValueError("exact executor observation unavailable")
        values.setdefault("MainPID", "0")
    return values


def validate_owner(record, operation, observed):
    generation = record.get("executor_generation", 0)
    manager = record.get("manager_sha256", "")
    if (record.get("schema") != "rixa.native-update.v1"
            or record.get("draft", {}).get("operation_id") != operation
            or type(generation) is not int or not 1 <= generation <= 16
            or not re.fullmatch("[a-f0-9]{64}", manager)
            or record.get("executor_acknowledged") is not True):
        raise ValueError("protected update executor identity is not positively acknowledged")
    unit = "rixa-update-" + operation + "-" + str(generation) + ".service"
    if (observed.get("Id") != unit or observed.get("ActiveState") != "active"
            or not observed.get("MainPID", "").isdigit() or int(observed["MainPID"]) < 2
            or not observed.get("InvocationID") or observed.get("ControlGroup") != "/system.slice/" + unit
            or "/opt/rixa-manager/" + manager not in observed.get("ExecStart", "")
            or " internal-update-apply " + operation not in observed.get("ExecStart", "")):
        raise ValueError("live unit does not positively match the recorded update executor")
    return unit


def cgroup_population(group):
    if not re.fullmatch(r"/system.slice/rixa-update-[a-z0-9-]+\.service", group):
        raise ValueError("unexpected executor cgroup")
    path = Path("/sys/fs/cgroup" + group) / "cgroup.events"
    try:
        events = dict(line.split() for line in path.read_text().splitlines())
    except FileNotFoundError:
        return "absent"
    if events.get("populated") not in ("0", "1"):
        raise ValueError("executor cgroup population is unknown")
    return events["populated"]


FAILURE_RECEIPT = None

def main():
    global FAILURE_RECEIPT
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--operation", required=True)
    parser.add_argument("--stage", choices=STAGES, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--timeout", type=int, default=180)
    args = parser.parse_args()
    proof.guard(args.receipt.parent)
    FAILURE_RECEIPT = args.receipt.with_name(args.receipt.name + ".failure.json")
    if not re.fullmatch("[a-z][a-z0-9-]{0,47}", args.operation) or not 1 <= args.timeout <= 600:
        raise ValueError("bounded exact operation and wait required")
    if args.receipt.exists():
        raise ValueError("prior interruption attempt exists; inspect it rather than repeat")
    state = {"schema": "rixa.update-interruption-proof.v1", "operation": args.operation,
             "requested_stage": args.stage, "state": "watching"}
    proof.save(args.receipt, state, initial=True)
    path = Path("/var/lib/rixa-manager") / (args.operation + ".update.json")
    deadline = time.monotonic() + args.timeout
    while time.monotonic() < deadline:
        if not path.exists():
            time.sleep(0.02)
            continue
        record = proof.private_json(path)
        if record.get("state") in ("installed", "removed", "reconciliation-required", "rejected"):
            raise ValueError("operation reached terminal/uncertain state before selected boundary")
        if record.get("stage") == args.stage and record.get("attempts", {}).get(args.stage) == "attempted":
            break
        time.sleep(0.02)
    else:
        raise ValueError("selected real stage boundary was not observed before deadline")
    # Stage names and target come from the protected operation; no journal edits,
    # payload wrappers, systemd drop-ins or synthetic success/failure injection.
    generation = record.get("executor_generation", 0)
    unit = "rixa-update-" + args.operation + "-" + str(generation) + ".service"
    before = observation(unit)
    validate_owner(record, args.operation, before)
    pid = int(before["MainPID"])
    executable = Path("/proc") / str(pid) / "exe"
    if hashlib.sha256(executable.read_bytes()).hexdigest() != record["manager_sha256"]:
        raise ValueError("executor process image differs from frozen manager")
    if observation(unit) != before:
        raise ValueError("executor changed during ownership observation")
    state.update(state="kill-attempted", unit=unit, before=before,
                 observed_stage=record["stage"], attempts=record["attempts"],
                 journal_sha256=hashlib.sha256(path.read_bytes()).hexdigest())
    proof.save(args.receipt, state)
    systemctl("kill", "--kill-whom=all", "--signal=KILL", unit)
    # A successful signal request is not a fence. Observe systemd and the actual
    # kernel cgroup; do not infer emptiness from only the main PID.
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        after = observation(unit)
        population = cgroup_population(before["ControlGroup"])
        if (after.get("MainPID") == "0" and after.get("ActiveState") in ("inactive", "failed")
                and population in ("0", "absent")):
            state.update(state="fenced", after=after, cgroup_population=population)
            proof.save(args.receipt, state)
            print(json.dumps({"status": "fenced", "unit": unit, "stage": record["stage"],
                              "cgroup_population": population, "main_pid": 0}))
            return 0
        time.sleep(0.05)
    raise ValueError("executor fence remains uncertain; inspect exact private receipt/unit")


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as error:
        if FAILURE_RECEIPT is not None and not FAILURE_RECEIPT.exists():
            try:
                proof.save(FAILURE_RECEIPT, {"error_type": type(error).__name__, "detail": str(error)[:8192]}, initial=True)
            except Exception:
                pass
        print("update interruption proof failed (" + type(error).__name__ + "); inspect private receipt/unit.", file=sys.stderr)
        sys.exit(1)
