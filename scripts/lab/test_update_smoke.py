# SPDX-License-Identifier: MPL-2.0
import importlib.util
import json
import os
from pathlib import Path
import struct
import tempfile
from types import SimpleNamespace
from unittest.mock import patch
import unittest
import zlib

spec = importlib.util.spec_from_file_location("update_smoke", Path(__file__).with_name("update-smoke.py"))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class UpdateProofTests(unittest.TestCase):
    def test_mutation_attempt_is_durable_before_request_and_remains_uncertain_on_failure(self):
        with tempfile.TemporaryDirectory() as root:
            receipt = Path(root) / "receipt.json"
            state = {"state": "prepared", "attempts": {}}
            smoke.save(receipt, state, initial=True)
            def lost_ack(operation):
                saved = json.loads(receipt.read_text())
                self.assertEqual(saved["state"], "upload-attempted")
                self.assertEqual(saved["attempts"]["upload"], {"id": operation, "status": "attempted"})
                raise TimeoutError("unknown response")
            with self.assertRaises(TimeoutError):
                smoke.attempt(receipt, state, "upload", lost_ack)
            self.assertEqual(json.loads(receipt.read_text())["attempts"]["upload"]["status"], "attempted")
            self.assertEqual(receipt.stat().st_mode & 0o777, 0o600)

    def test_snapshot_comparison_detects_addition_deletion_and_row_change(self):
        delta = smoke.differences({"old": {"rows": 2}, "stable": 1, "changed": "a"},
                                  {"new": {"rows": 1}, "stable": 1, "changed": "b"})
        self.assertEqual(set(delta), {"old", "new", "changed"})
        self.assertEqual(delta["old"]["after"], None)
        self.assertEqual(delta["changed"], {"before": "a", "after": "b"})

    def test_retained_file_scope_rejects_missing_roots_and_symlinks(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            with self.assertRaises(ValueError):
                smoke.file_snapshot([root / "missing"])
            (root / "file").write_bytes(b"retained")
            (root / "link").symlink_to("file")
            with self.assertRaises(ValueError):
                smoke.file_snapshot([root])
            (root / "link").unlink()
            before = smoke.file_snapshot([root])
            (root / "file").write_bytes(b"changed")
            self.assertNotEqual(before, smoke.file_snapshot([root]))

    def test_media_fixture_has_valid_png_chunks_and_nonempty_pixels(self):
        self.assertEqual(smoke.PNG[:8], b"\x89PNG\r\n\x1a\n")
        position, pixels = 8, b""
        while position < len(smoke.PNG):
            length = struct.unpack("!I", smoke.PNG[position:position + 4])[0]
            chunk = smoke.PNG[position + 4:position + 8 + length]
            crc = struct.unpack("!I", smoke.PNG[position + 8 + length:position + 12 + length])[0]
            self.assertEqual(zlib.crc32(chunk) & 0xffffffff, crc)
            if chunk[:4] == b"IDAT":
                pixels += zlib.decompress(chunk[4:])
            position += length + 12
        self.assertEqual(len(pixels), 32 * (1 + 32 * 3))


    def test_bootstrap_profile_rejects_every_pin_and_external_target(self):
        root = smoke.BOOTSTRAP_ROOT
        args = SimpleNamespace(draft=root / "receipts/install-input.json", ca=root / "tls-cert.pem",
                               receipt=root / "receipts/retention-j3.json", before=None)
        smoke.validate_bootstrap_profile(args, dict(smoke.BOOTSTRAP_PINS))
        for key in smoke.BOOTSTRAP_PINS:
            changed = dict(smoke.BOOTSTRAP_PINS, **{key: "different"})
            with self.subTest(pin=key), self.assertRaises(ValueError):
                smoke.validate_bootstrap_profile(args, changed)
        for key in ("draft", "ca", "receipt", "before"):
            changed = SimpleNamespace(**vars(args))
            setattr(changed, key, Path("/private/unowned-target"))
            with self.subTest(path=key), self.assertRaises(ValueError):
                smoke.validate_bootstrap_profile(changed, smoke.BOOTSTRAP_PINS)

    def test_bootstrap_seed_stops_at_media_with_no_account_or_member_effect(self):
        class Boundary(Exception):
            pass
        class FakeClient:
            calls = []
            def __init__(self, origin, ca):
                self.origin, self.csrf, self.cookies = origin, "", []
            def login(self, login, password):
                self.calls.append(("login", login))
                self.csrf = "private-test-token"
            def post(self, path, body):
                self.calls.append(("post", path))
                return {}
        draft = {"control_origin": "https://control.rixa.test", "site_origin": "https://site.rixa.test",
                 "control_login": "control", "control_password": "private",
                 "site_login": "site", "site_password": "private"}
        for bootstrap in (True, False):
            with self.subTest(bootstrap=bootstrap), tempfile.TemporaryDirectory() as directory:
                FakeClient.calls = []
                args = SimpleNamespace(receipt=Path(directory) / "seed.json", ca=Path(directory) / "ca",
                                       bootstrap_retention=bootstrap)
                with patch.object(smoke, "Client", FakeClient), patch.object(smoke, "upload", side_effect=Boundary):
                    with self.assertRaises(Boundary):
                        smoke.seed(args, draft)
                state = json.loads(args.receipt.read_text())
                self.assertEqual(state["attempts"]["media"]["status"], "attempted")
                account_effect = ("post", "/admin/sites/account-create") in FakeClient.calls
                self.assertEqual(account_effect, not bootstrap)
                self.assertEqual("member_password" in state, not bootstrap)
                if bootstrap:
                    self.assertEqual(set(state["attempts"]), {"control-login", "site-login", "media"})
                    self.assertEqual(state["profile"], "bootstrap-retention-j3")

    def test_loopback_mapping_is_exact_and_restored_after_failure(self):
        original = smoke.socket.getaddrinfo
        with self.assertRaises(RuntimeError):
            with smoke.bootstrap_loopback_origins():
                for origin in ("control.rixa.test", "site.rixa.test"):
                    values = smoke.socket.getaddrinfo(origin, 443, type=smoke.socket.SOCK_STREAM)
                    self.assertTrue(values)
                    self.assertEqual({v[4][0] for v in values}, {"127.0.0.1"})
                for host, port in (("other.test", 443), ("127.0.0.1", 443), ("site.rixa.test", 80)):
                    with self.subTest(host=host, port=port), self.assertRaises(ValueError):
                        smoke.socket.getaddrinfo(host, port)
                raise RuntimeError("restore after failed proof")
        self.assertIs(smoke.socket.getaddrinfo, original)


if __name__ == "__main__":
    unittest.main()
