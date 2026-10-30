# SPDX-License-Identifier: MPL-2.0
import importlib.util
import json
import os
from pathlib import Path
import struct
import tempfile
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


if __name__ == "__main__":
    unittest.main()
