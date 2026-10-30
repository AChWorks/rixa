#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Fixed guest-only bootstrap proof steps. Runtime use needs separate effect authorization."""
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
from urllib.parse import urlsplit

spec = importlib.util.spec_from_file_location("bootstrap_content", Path(__file__).with_name("content-smoke.py"))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)
save, private_json = common.save, common.private_json
SCHEMA = "rixa.bootstrap-proof.v1"
STATE = Path("/var/lib/rixa-manager")
MANAGERS = Path("/opt/rixa-manager")
PG_STAGES = ("download", "common", "suppress-main", "server", "cluster", "start",
             "control-role", "site-role", "control-db", "site-db", "permissions", "verify")
ACTIONS = ("pg-draft", "pg-review", "pg-apply", "pg-status", "pg-resume", "pg-evidence",
           "inspect", "prepare", "route", "install-draft", "install-review", "install-apply",
           "install-status", "install-resume", "install-remove")
READ_ONLY = {"pg-status", "pg-evidence", "inspect", "route", "install-status"}


def sha256(path):
    value = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            value.update(chunk)
    return value.hexdigest()


def protected(path, private=False, executable=False):
    if not path.is_absolute() or path.resolve() != path:
        raise ValueError("canonical absolute protected path required")
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & (0o077 if private else 0o022):
        raise ValueError("root-owned protected regular file required")
    if executable and (info.st_mode & 0o777) != 0o755:
        raise ValueError("exact protected executable mode 0755 required")
    for parent in path.parents:
        info = parent.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError("protected root-owned ancestors required")
    return path


def validate_config(c):
    wanted = {"schema", "scope_record", "fixture_source", "fixture_sha256", "fixture",
              "helper_sha256", "helper", "pg_operation", "pg_port", "installation_operation",
              "release", "source_commit", "archive_sha256", "runtime_sha256", "receipts",
              "operator_draft"}
    if set(c) != wanted or c["schema"] != SCHEMA:
        raise ValueError("exact proof configuration fields required")
    for key in ("fixture_sha256", "helper_sha256", "archive_sha256", "runtime_sha256"):
        if not re.fullmatch("[a-f0-9]{64}", c[key]):
            raise ValueError("exact SHA256 pin required")
    for key in ("fixture_source", "source_commit"):
        if not re.fullmatch("[a-f0-9]{40}", c[key]):
            raise ValueError("exact source commit required")
    for key in ("pg_operation", "installation_operation"):
        if not re.fullmatch("[a-z0-9][a-z0-9-]{0,47}", c[key]):
            raise ValueError("explicit bounded operation identifier required")
    if c["pg_operation"] == c["installation_operation"]:
        raise ValueError("separate prerequisite and installation operations required")
    if type(c["pg_port"]) is not int or not 1024 <= c["pg_port"] <= 65535:
        raise ValueError("explicit unprivileged guest PostgreSQL port required")
    if not re.fullmatch(r"v[0-9]+[.][0-9]+[.][0-9]+(?:-[A-Za-z0-9.-]+)?", c["release"]):
        raise ValueError("exact public release required; no implicit channel selection")
    if not re.fullmatch(r"https://github[.]com/AChWorks/rixa/(?:issues/29|pull/[0-9]+)#issuecomment-[0-9]+", c["scope_record"]):
        raise ValueError("exact owning scope record required")
    for key in ("fixture", "helper", "receipts", "operator_draft"):
        p = Path(c[key])
        if not p.is_absolute() or ".." in p.parts or str(p) != c[key]:
            raise ValueError("canonical absolute private fixture paths required")
    if Path(c["fixture"]).parent != Path(c["helper"]).parent:
        raise ValueError("fixture manager and verified helper must share the protected fixture directory")
    return c


def guard(c):
    if os.geteuid() != 0:
        raise ValueError("disposable guest root only")
    for filename, value in (("/etc/rixa-disposable-lab", "rixa.disposable-lab.v1"),
                            ("/etc/rixa-bootstrap-lab", "rixa.bootstrap-lab.v1")):
        marker = protected(Path(filename), private=True)
        if marker.read_text().strip() != value:
            raise ValueError("explicit disposable bootstrap guest markers required")
    root = Path(c["receipts"])
    info = root.lstat()
    if root.resolve() != root or not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o077:
        raise ValueError("existing root-owned private receipt directory required")
    for name, pin in (("fixture", "fixture_sha256"), ("helper", "helper_sha256")):
        protected(Path(c[name]), executable=True)
        if sha256(Path(c[name])) != c[pin]:
            raise ValueError("Master-supplied source fixture or verified helper identity changed")


def validate_route(c, prepared):
    if prepared.get("schema") != "rixa.native-bootstrap.v1" or prepared.get("state") != "prepared":
        raise ValueError("successful authenticated bootstrap preparation required")
    r = prepared.get("route", {})
    if (r.get("operation_kind") != "install" or r.get("operation_id") != c["installation_operation"]
            or r.get("release") != c["release"] or r.get("manager_sha256") != c["runtime_sha256"]
            or prepared.get("source_commit") != c["source_commit"]):
        raise ValueError("selected public runtime identity differs from the explicit pins")
    expected_root = Path("/opt/rixa-bootstrap") / c["archive_sha256"]
    if Path(r.get("manager", "")) != expected_root / "rixa":
        raise ValueError("authenticated content-addressed selected manager required")
    args = r.get("arguments", [])
    if len(args) != 6 or args[:5] != ["manage", "menu", "--operation", c["installation_operation"], "--prefill"]:
        raise ValueError("fixed normal installation menu route required")
    prefill = Path(args[5])
    if prefill.parent.parent != STATE / "bootstrap" or not prefill.parent.name.startswith("selection-") or prefill.name != "prefill.json":
        raise ValueError("protected bootstrap-owned prefill required")
    return r, prefill


def check_handoff(c, prepared, draft):
    r, prefill = validate_route(c, prepared)
    fields = {"operation_id": c["installation_operation"], "release": c["release"],
              "source_commit": c["source_commit"], "archive": prepared["archive"], "bundle": prepared["bundle"]}
    if any(draft.get(k) != v for k, v in fields.items()):
        raise ValueError("private handoff does not bind selected authenticated package")
    identities = []
    for key in ("control_dsn", "site_dsn"):
        url = urlsplit(draft.get(key, ""))
        if (url.scheme != "postgres" or url.hostname != "127.0.0.1" or url.port != c["pg_port"]
                or not url.username or not url.password or not url.path.startswith("/rixa_")):
            raise ValueError("private generated local PostgreSQL DSN handoff required")
        identities.append((url.username, url.path))
    if identities[0][0] == identities[1][0] or identities[0][1] == identities[1][1]:
        raise ValueError("distinct generated roles and databases required")
    return r, prefill


class Driver:
    def __init__(self, config):
        self.c = config
        self.root = Path(config["receipts"])

    def receipt(self, key):
        return private_json(protected(self.root / (key + ".json"), private=True))

    def call(self, key, manager, args):
        # O_EXCL receipt is durable before invoking even a potentially mutating
        # review/download. Unknown effects are inspected, never replayed here.
        target = self.root / (key + ".json")
        state = {"schema": SCHEMA, "step": key, "state": "attempted",
                 "fixture_source": self.c["fixture_source"], "fixture_sha256": self.c["fixture_sha256"],
                 "operation": self.c["pg_operation"] if key.startswith("pg-") else self.c["installation_operation"]}
        save(target, state, initial=True)
        env = dict(os.environ, PATH=str(Path(self.c["helper"]).parent) + ":/usr/sbin:/usr/bin:/sbin:/bin",
                   LANG="C.UTF-8", LC_ALL="C.UTF-8")
        result = subprocess.run([str(manager), *args, "--non-interactive"], stdin=subprocess.DEVNULL,
                                capture_output=True, env=env, timeout=1200)
        if len(result.stdout) > 1 << 20 or len(result.stderr) > 1 << 20:
            raise ValueError("bounded CLI output exceeded; inspect uncertain step")
        # Raw output is always private, including errors and opaque review tokens.
        for suffix, data in (("stdout", result.stdout), ("stderr", result.stderr)):
            with (self.root / (key + "." + suffix)).open("xb") as stream:
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
        state.update(state="returned", exit_code=result.returncode)
        try:
            state["result"] = json.loads(result.stdout)
        except (ValueError, UnicodeDecodeError):
            save(target, state)
            raise ValueError("CLI returned no valid JSON; inspect private step") from None
        save(target, state)
        if result.returncode:
            raise ValueError("CLI failed; inspect private result before any further effect")
        return state["result"]

    def frozen(self, operation, kind):
        journal = self.journal(operation, kind)
        image = MANAGERS / journal["manager_sha256"]
        protected(image, executable=True)
        if sha256(image) != journal["manager_sha256"]:
            raise ValueError("exact original frozen manager changed")
        return image, journal

    def journal(self, operation, kind):
        path = STATE / ("postgres/" + operation + ".json" if kind == "pg" else operation + ".json")
        j = private_json(protected(path, private=True))
        expected = "rixa.native-postgresql.v1" if kind == "pg" else "rixa.native-install.v1"
        if j.get("schema") != expected or j.get("draft", {}).get("operation_id") != operation:
            raise ValueError("protected journal identity differs")
        return j

    def selected(self):
        p = self.receipt("prepare")["result"]
        r, prefill = validate_route(self.c, p)
        image = protected(Path(r["manager"]), executable=True)
        if sha256(image) != self.c["runtime_sha256"]:
            raise ValueError("selected public manager bytes changed")
        if sha256(protected(Path(p["archive"]), private=True)) != self.c["archive_sha256"]:
            raise ValueError("selected archive bytes changed")
        return image, p, prefill

    def run(self, action, observation):
        c, pg, install = self.c, self.c["pg_operation"], self.c["installation_operation"]
        key = action + ("-" + observation if action in READ_ONLY else "")
        manager = Path(c["fixture"])
        if action in ("inspect", "prepare", "route"):
            args = ["bootstrap", action]
            if action == "prepare":
                args += ["--release", c["release"], "--operation", install, "--postgres", pg]
            elif action == "route":
                args += ["--operation", install]
            result = self.call(key, manager, args)
            if action == "prepare":
                _, prepared, prefill = self.selected()
                draft = private_json(protected(prefill, private=True))
                check_handoff(c, prepared, draft)
                return {"status": "verified", "private_dsn_handoff": "verified; values remain private",
                        "published_runtime": c["release"], "source_fixture": c["fixture_source"]}
            if action == "route":
                frozen, _ = self.frozen(install, "install")
                r = result.get("route", {})
                if result.get("code") != "route-ready" or r.get("manager") != str(frozen) or r.get("manager_sha256") != sha256(frozen):
                    raise ValueError("bootstrap route did not select the original frozen installation manager")
            return safe_result(result)
        if action.startswith("pg-"):
            verb = action[3:]
            if verb == "evidence":
                return self.pg_evidence(key)
            if verb == "draft":
                source = self.root / "pg-input.json"
                save(source, {"operation_id": pg, "port": c["pg_port"], "dedicated_host": True}, initial=True)
                args = ["manage", "prerequisites", "draft", "--source", str(source)]
            else:
                args = ["manage", "prerequisites", verb, "--operation", pg]
                if verb == "apply":
                    reviewed = self.receipt("pg-review")["result"]
                    if reviewed.get("state") != "ready" or "apply" not in reviewed.get("actions", []) or not reviewed.get("review_token"):
                        raise ValueError("exact successful prerequisite review required")
                    args += ["--review-token", reviewed["review_token"]]
                if verb in ("status", "resume") and self.journal(pg, "pg").get("manager_sha256"):
                    manager, j = self.frozen(pg, "pg")
                    if verb == "resume":
                        # Status is a fresh ordinary product observation. It owns
                        # fencing/reconciliation; this driver cannot force Resume.
                        prior = self.call("pg-resume-check-" + str(j["executor_generation"]), manager,
                                          ["manage", "prerequisites", "status", "--operation", pg])
                        if "resume" not in prior.get("actions", []):
                            raise ValueError("product did not offer Resume; inspect exact executor and retained effects")
                        key += "-" + str(j["executor_generation"] + 1)
            return safe_result(self.call(key, manager, args))
        verb = action[len("install-"):]
        if verb in ("status", "resume", "remove"):
            manager, _ = self.frozen(install, "install")
        else:
            manager, prepared, prefill = self.selected()
        args = ["manage", verb, "--operation", install]
        if verb == "draft":
            partial = private_json(protected(prefill, private=True))
            check_handoff(c, prepared, partial)
            operator = private_json(protected(Path(c["operator_draft"]), private=True))
            allowed = {"control_origin", "site_origin", "listen", "tls_cert", "tls_key",
                       "control_login", "site_login", "control_password", "site_password", "language"}
            if set(operator) != allowed or operator["control_origin"] != "https://control.rixa.test" or operator["site_origin"] != "https://site.rixa.test":
                raise ValueError("exact local fixture operator input required; package and DSNs cannot be overridden")
            source = self.root / "install-input.json"
            save(source, dict(partial, **operator), initial=True)
            args += ["--source", str(source)]
        elif verb == "apply":
            reviewed = self.receipt("install-review")["result"]
            if reviewed.get("state") != "ready" or not reviewed.get("review_token"):
                raise ValueError("exact successful native installation review required")
            args += ["--review-token", reviewed["review_token"]]
        elif verb == "remove":
            args += ["--confirm-remove"]
        elif verb == "resume":
            _, j = self.frozen(install, "install")
            prior = self.call("install-resume-check-" + str(j["executor_generation"]), manager,
                              ["manage", "status", "--operation", install])
            if "resume" not in prior.get("actions", []):
                raise ValueError("product did not offer Resume; preserve exact executor and effects")
            key += "-" + str(j["executor_generation"] + 1)
        return safe_result(self.call(key, manager, args))

    def pg_evidence(self, key):
        c = self.c
        image, j = self.frozen(c["pg_operation"], "pg")
        status = self.call(key, image, ["manage", "prerequisites", "status", "--operation", c["pg_operation"]])
        if status.get("state") != "installed" or status.get("code") != "healthy" or any(j["attempts"].get(s) != "verified" for s in PG_STAGES):
            raise ValueError("actual prerequisite did not verify every owned stage")
        targets = j.get("database_targets", {})
        if (set(targets) != {"control", "site-a"} or len(set(targets.values())) != 2
                or any(not v.startswith(j.get("cluster_id", "") + ":") for v in targets.values())):
            raise ValueError("distinct same-cluster physical database targets required")
        # Product Status just authenticated packages, loaded cluster, identities,
        # privileges and password connections. Export only allowlisted evidence.
        value = {k: j.get(k) for k in ("schema", "state", "stage", "attempts", "manager_sha256",
                 "executor_generation", "executor_acknowledged", "key_sha256", "lists_sha256",
                 "packages", "common_packages", "cluster_id", "data_identity", "config_sha256",
                 "hba_sha256", "binary_sha256", "database_targets")}
        value.update(status="verified", cluster=status["postgres_plan"]["cluster"],
                     port=c["pg_port"], generated_credentials="retained privately; never exported",
                     least_privilege="ordinary product Status verified; no independent SQL claim")
        save(self.root / (key + ".sanitized.json"), value, initial=True)
        return value


def safe_result(result):
    return {k: result[k] for k in ("operation_id", "state", "stage", "code", "actions") if k in result}


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=ACTIONS)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--observation", default="check")
    args = parser.parse_args()
    if not re.fullmatch("[a-z0-9][a-z0-9-]{0,47}", args.observation):
        raise ValueError("bounded observation identifier required")
    c = validate_config(private_json(protected(args.config, private=True)))
    guard(c)
    print(json.dumps(Driver(c).run(args.action, args.observation)))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Arbitrary subprocess/parse errors may contain protected input.
        print("bootstrap proof failed (" + type(error).__name__ + "); inspect private receipt.", file=sys.stderr)
        sys.exit(1)
