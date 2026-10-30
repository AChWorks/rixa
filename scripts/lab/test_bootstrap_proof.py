# SPDX-License-Identifier: MPL-2.0
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("bootstrap_proof", Path(__file__).with_name("bootstrap-proof.py"))
proof = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proof)


def config(root="/root/proof"):
    return {"schema": proof.SCHEMA,
            "scope_record": "https://github.com/AChWorks/rixa/issues/29#issuecomment-123",
            "fixture_source": "a" * 40, "fixture_sha256": "b" * 64, "fixture": root + "/rixa",
            "helper_sha256": "c" * 64, "helper": root + "/gh", "pg_operation": "bootstrap-pg",
            "pg_port": 55431, "installation_operation": "bootstrap-install", "release": "v0.4.0",
            "source_commit": "d" * 40, "archive_sha256": "e" * 64, "runtime_sha256": "f" * 64,
            "receipts": root + "/receipts", "operator_draft": root + "/operator.json"}


def prepared(c):
    return {"schema": "rixa.native-bootstrap.v1", "state": "prepared", "source_commit": c["source_commit"],
            "archive": "/var/lib/rixa-manager/bootstrap/selection-123/archive.tar.gz",
            "bundle": "/var/lib/rixa-manager/bootstrap/selection-123/bundle.json",
            "route": {"operation_kind": "install", "operation_id": c["installation_operation"],
                      "release": c["release"], "manager_sha256": c["runtime_sha256"],
                      "manager": "/opt/rixa-bootstrap/" + c["archive_sha256"] + "/rixa",
                      "arguments": ["manage", "menu", "--operation", c["installation_operation"],
                                    "--prefill", "/var/lib/rixa-manager/bootstrap/selection-123/prefill.json"]}}


class BootstrapProofTests(unittest.TestCase):
    def test_configuration_requires_exact_provenance_scope_and_distinct_operations(self):
        c = config()
        self.assertEqual(proof.validate_config(c), c)
        for key, value in (("runtime_sha256", "unpublished"), ("source_commit", "main"),
                           ("pg_port", True), ("installation_operation", c["pg_operation"]),
                           ("scope_record", "https://evil.test"), ("fixture", "/root/../tmp/rixa"),
                           ("release", "development")):
            with self.subTest(key=key), self.assertRaises(ValueError):
                proof.validate_config(dict(c, **{key: value}))
        with self.assertRaises(ValueError):
            proof.validate_config(dict(c, arbitrary_command="shell"))

    def test_route_is_fixed_typed_and_binds_the_public_package_and_private_prefill(self):
        c = config()
        p = prepared(c)
        proof.validate_route(c, p)
        for key, value in (("operation_id", "foreign"), ("manager", "/tmp/rixa"),
                           ("manager_sha256", "1" * 64), ("release", "v0.3.0"),
                           ("arguments", ["sh", "-c", "untrusted"])):
            bad = dict(p, route=dict(p["route"], **{key: value}))
            with self.subTest(key=key), self.assertRaises(ValueError):
                proof.validate_route(c, bad)
        bad = json.loads(json.dumps(p))
        bad["route"]["arguments"][-1] = "/root/prefill.json"
        with self.assertRaises(ValueError):
            proof.validate_route(c, bad)

    def test_private_handoff_preserves_package_identity_and_distinct_local_credentials(self):
        c, p = config(), prepared(config())
        draft = {"operation_id": c["installation_operation"], "release": c["release"],
                 "source_commit": c["source_commit"], "archive": p["archive"], "bundle": p["bundle"],
                 "control_dsn": "postgres://rixa_c_owned:private@127.0.0.1:55431/rixa_c_owned?sslmode=disable",
                 "site_dsn": "postgres://rixa_s_owned:private@127.0.0.1:55431/rixa_s_owned?sslmode=disable"}
        proof.check_handoff(c, p, draft)
        for key, value in (("archive", "/tmp/foreign.tar.gz"), ("control_dsn", draft["site_dsn"]),
                           ("site_dsn", draft["site_dsn"].replace("127.0.0.1", "remote.test"))):
            with self.subTest(key=key), self.assertRaises(ValueError):
                proof.check_handoff(c, p, dict(draft, **{key: value}))
        summary = proof.safe_result(dict(draft, state="prepared", code="ok", review_token="opaque"))
        self.assertNotIn("control_dsn", summary)
        self.assertNotIn("review_token", summary)

    def test_unknown_effect_is_durable_and_never_replayed(self):
        with tempfile.TemporaryDirectory() as root:
            c = config(root)
            c["receipts"] = root
            driver = proof.Driver(c)
            def unknown(argv, **kwargs):
                saved = json.loads((Path(root) / "pg-apply.json").read_text())
                self.assertEqual(saved["state"], "attempted")
                self.assertEqual(argv[1:4], ["manage", "prerequisites", "apply"])
                self.assertEqual(argv[-1], "--non-interactive")
                raise subprocess.TimeoutExpired(argv[0], 1)
            with patch.object(proof.subprocess, "run", side_effect=unknown) as run:
                with self.assertRaises(subprocess.TimeoutExpired):
                    driver.call("pg-apply", Path(c["fixture"]), ["manage", "prerequisites", "apply"])
                with self.assertRaises(FileExistsError):
                    driver.call("pg-apply", Path(c["fixture"]), ["manage", "prerequisites", "apply"])
                self.assertEqual(run.call_count, 1)
            self.assertEqual((Path(root) / "pg-apply.json").stat().st_mode & 0o777, 0o600)

    def test_returned_failure_is_preserved_but_not_reported_as_success(self):
        with tempfile.TemporaryDirectory() as root:
            c = config(root)
            c["receipts"] = root
            driver = proof.Driver(c)
            result = subprocess.CompletedProcess([], 1, b'{"state":"blocked","code":"inspect"}', b"private diagnostic")
            with patch.object(proof.subprocess, "run", return_value=result):
                with self.assertRaises(ValueError):
                    driver.call("pg-review", Path(c["fixture"]), ["manage", "prerequisites", "review"])
            saved = json.loads((Path(root) / "pg-review.json").read_text())
            self.assertEqual((saved["state"], saved["exit_code"], saved["result"]["state"]), ("returned", 1, "blocked"))
            self.assertEqual((Path(root) / "pg-review.stderr").read_bytes(), b"private diagnostic")

    def test_pg_review_exact_packages_precede_apply_and_resume_requires_product_offer(self):
        with tempfile.TemporaryDirectory() as root:
            c = config(root)
            driver = proof.Driver(c)
            with patch.object(driver, "receipt", return_value={"result": {"state": "blocked"}}), patch.object(driver, "call") as run:
                with self.assertRaises(ValueError):
                    driver.run("pg-apply", "check")
                run.assert_not_called()
            j = {"manager_sha256": "a" * 64, "executor_generation": 1}
            with patch.object(driver, "journal", return_value=j), patch.object(driver, "frozen", return_value=(Path("/opt/rixa-manager/" + "a" * 64), j)), patch.object(driver, "call", return_value={"state": "applying", "actions": ["status"]}) as run:
                with self.assertRaises(ValueError):
                    driver.run("pg-resume", "check")
                self.assertEqual(run.call_count, 1)
                self.assertEqual(run.call_args.args[2][2], "status")


if __name__ == "__main__":
    unittest.main()
