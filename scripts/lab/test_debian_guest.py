# SPDX-License-Identifier: MPL-2.0
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("debian_guest", Path(__file__).with_name("debian-guest.py"))
labtool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(labtool)


class LaboratoryBoundaryTests(unittest.TestCase):
    def test_refuses_relative_and_qemu_option_paths(self):
        for value in ("relative", "/tmp/lab,if=virtio", "/tmp/lab space"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                labtool.directory(value)

    def test_refuses_links_and_shared_permissions(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            private = root / "private"
            private.mkdir(mode=0o700)
            link = root / "link"
            link.symlink_to(private, target_is_directory=True)
            with self.assertRaises(ValueError):
                labtool.directory(str(link))
            private.chmod(0o755)
            with self.assertRaises(ValueError):
                labtool.directory(str(private))

    def test_prepare_never_overwrites_an_existing_directory(self):
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw)
            sentinel = path / "unrelated"
            sentinel.write_text("retain")
            with self.assertRaises(FileExistsError):
                labtool.directory(str(path), create=True)
            self.assertEqual(sentinel.read_text(), "retain")

    def test_refuses_a_linked_launch_lock(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            sentinel = root / "unrelated"
            sentinel.write_text("retain")
            (root / "qemu.lock").symlink_to(sentinel)
            with self.assertRaises(OSError):
                labtool.launch_lock(root)
            self.assertEqual(sentinel.read_text(), "retain")

    def test_refuses_changed_boot_inputs(self):
        with tempfile.TemporaryDirectory() as raw:
            import hashlib
            import json
            path = Path(raw)
            files = {name: hashlib.sha256(b"original").hexdigest()
                     for name in ("vmlinuz", "initrd-preseed.gz", "id_ed25519.pub")}
            for name in files:
                (path / name).write_bytes(b"original")
            (path / "lab.json").write_text(json.dumps({
                "schema": labtool.SCHEMA, "uid": os.getuid(),
                "signer": labtool.SIGNER, "iso": "debian-13.7.0-amd64-netinst.iso",
                "files": files}))
            (path / "vmlinuz").write_bytes(b"replaced")
            with self.assertRaisesRegex(ValueError, "input changed"):
                labtool.load(path)


if __name__ == "__main__":
    unittest.main()
