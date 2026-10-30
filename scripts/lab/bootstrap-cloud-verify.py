#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Inspect and seal only the owned fresh cloud guest; injects no product tools."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import shlex
import socket
import subprocess
import sys
import time

spec = importlib.util.spec_from_file_location("cloud_lab", Path(__file__).with_name("bootstrap-cloud-guest.py"))
cloud = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cloud)


def positive_launch(lab, attempt):
    if not re.fullmatch("[a-z][a-z0-9-]{0,62}", attempt):
        raise ValueError("exact stable launch attempt required")
    value = json.loads(cloud.small_text(lab / (attempt + ".launch.json"), 65536))
    if value.get("state") != "qemu-running-acknowledged" or value.get("image") != str(lab / "guest.qcow2"):
        raise ValueError("owned running launch is not acknowledged")
    process = Path("/proc") / str(value.get("pid"))
    if process.stat().st_uid != os.getuid() or (process / "exe").resolve().name != "qemu-system-x86_64":
        raise ValueError("launch PID is not the exact owned QEMU")
    arguments = (process / "cmdline").read_bytes().split(b"\0")
    expected = ("file=" + str(lab / "guest.qcow2") + ",format=qcow2,if=virtio,index=0").encode()
    if expected not in arguments:
        raise ValueError("owned QEMU is using another image")
    return value


def inspect(args):
    lab, data = cloud.load(args.lab)
    launch = positive_launch(lab, args.attempt)
    target = lab / (args.attempt + ".inspection.json")
    if target.exists():
        raise ValueError("an inspection receipt already exists; inspect retained evidence")
    record = lab / (args.attempt + ".inspection-attempt.json")
    cloud.save(record, {"state": "read-only-inspection-attempted", "attempt": args.attempt,
                       "launch_pid": launch["pid"]}, initial=True)
    source = Path(__file__).with_name("bootstrap-cloud-inspect.py").read_text()
    command = ["ssh", "-n", "-p", str(cloud.SSH_PORT), "-i", str(lab / "id_ed25519"),
               "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
               "-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=" + str(lab / "known_hosts"),
               "root@127.0.0.1", "python3 -c " + shlex.quote(source)]
    deadline = time.monotonic() + 360
    while time.monotonic() < deadline:
        positive_launch(lab, args.attempt)
        try:
            result = subprocess.run(command, stdin=subprocess.DEVNULL, capture_output=True, timeout=210)
        except subprocess.TimeoutExpired:
            # Only read-only OS observations may be repeated. No product login,
            # mutation, package installation or service restart is present.
            time.sleep(1)
            continue
        (lab / (args.attempt + ".inspection-last.stderr")).write_bytes(result.stderr)
        if result.returncode:
            time.sleep(2)
            continue
        value = json.loads(result.stdout)
        if (value.get("schema") != "rixa.bootstrap-clean-inspection.v1" or value.get("status") != "verified"
                or value.get("instance_id") != data["instance_id"] or value.get("product_tools_injected") is not False
                or value.get("postgresql") != "absent" or value.get("rixa") != "absent"):
            raise ValueError("guest did not verify this exact clean instance")
        value.update(launch_attempt=args.attempt, launch_pid=launch["pid"],
                     trust_method=data["trust_method"], image_sha512=data["image"]["sha512"])
        cloud.save(target, value, initial=True)
        cloud.save(record, {"state": "read-only-inspection-verified", "attempt": args.attempt,
                           "boot_id": value["boot_id"]})
        print(json.dumps({key: value[key] for key in ("status", "debian_version", "architecture", "systemd",
                         "cloud_init", "instance_id", "postgresql", "rixa", "unknown_listeners",
                         "identified_stock_os_sockets", "package_count", "package_inventory_sha256")}))
        return
    raise ValueError("bounded clean inspection failed; inspect retained stderr/launch, never recreate the guest blindly")


def seal(args):
    lab, data = cloud.load(args.lab)
    inspection = json.loads(cloud.small_text(lab / (args.attempt + ".inspection.json"), 1 << 20))
    launch = json.loads(cloud.small_text(lab / (args.attempt + ".launch.json"), 65536))
    if (inspection.get("status") != "verified" or inspection.get("product_tools_injected") is not False
            or inspection.get("instance_id") != data["instance_id"]
            or launch.get("state") != "qemu-exited" or launch.get("exit_code") != 0):
        raise ValueError("verified clean guest and actual successful QEMU exit required")
    if (lab / "clean-baseline.json").exists():
        raise ValueError("clean baseline was already sealed")
    with cloud._common.launch_lock(lab) as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if any((lab / name).exists() for name in ("qmp.sock", "console.sock")):
            raise ValueError("owned launch sockets remain; reconcile actual exit")
        for port in (cloud.SSH_PORT, cloud.HTTPS_PORT):
            probe = socket.socket()
            try:
                probe.bind(("127.0.0.1", port))
            finally:
                probe.close()
        cloud.save(lab / "seal-attempt.json", {"state": "clean-baseline-seal-attempted",
                   "launch_attempt": args.attempt, "boot_id": inspection["boot_id"]}, initial=True)
        image = cloud.regular(lab / "guest.qcow2")
        info = json.loads(subprocess.run(["qemu-img", "info", "--output=json", str(image)],
                                        check=True, capture_output=True, text=True).stdout)
        if info.get("backing-filename") != str(lab / data["image"]["filename"]):
            raise ValueError("clean overlay backing identity changed")
        image.chmod(0o400)
        value = {"schema": "rixa.bootstrap-clean-baseline.v1", "status": "sealed",
                 "baseline_image": str(image), "baseline_sha256": cloud.digest(image),
                 "mode": "0400", "uid": os.getuid(), "virtual_size": info["virtual-size"],
                 "official_backing": data["image"], "trust_method": data["trust_method"],
                 "openpgp_signature_claimed": False, "scope_record": data.get("scope_record"),
                 "inspection_receipt": str(lab / (args.attempt + ".inspection.json")),
                 "instance_id": data["instance_id"], "boot_id": inspection["boot_id"],
                 "package_count": inspection["package_count"],
                 "package_inventory_sha256": inspection["package_inventory_sha256"],
                 "postgresql": "absent", "rixa": "absent", "product_tools_injected": False,
                 "actual_qemu_exit": 0, "launch_lock": "positively held", "sockets": "absent",
                 "forward_ports": "22691/22643 free", "next_phase": "separate Master candidate/effect authorization required"}
        cloud.save(lab / "clean-baseline.json", value, initial=True)
        cloud.save(lab / "seal-attempt.json", {"state": "sealed", "baseline_sha256": value["baseline_sha256"]})
        print(json.dumps(value))


def main():
    if os.geteuid() == 0:
        raise ValueError("host verification must run as unprivileged owner")
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("inspect", "seal"))
    parser.add_argument("--lab", required=True)
    parser.add_argument("--attempt", required=True)
    args = parser.parse_args()
    inspect(args) if args.action == "inspect" else seal(args)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("bootstrap cloud verify: " + str(error), file=sys.stderr)
        sys.exit(1)
