# SPDX-License-Identifier: MPL-2.0
import argparse
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


probe = load("cloud_inspect", "bootstrap-cloud-inspect.py")
verify = load("cloud_verify", "bootstrap-cloud-verify.py")


class CleanBaselineBoundaryTests(unittest.TestCase):
    def test_only_identified_stock_os_sockets_are_allowed(self):
        self.assertTrue(probe.allowed_socket("tcp", "0.0.0.0:22", ["/usr/sbin/sshd"]))
        self.assertTrue(probe.allowed_socket("tcp", "127.0.0.53%lo:53", [probe.OS_RESOLVER]))
        self.assertTrue(probe.allowed_socket("udp", "0.0.0.0:5355", [probe.OS_RESOLVER]))
        self.assertTrue(probe.allowed_socket("udp", "0.0.0.0:68", ["/usr/sbin/dhcpcd"]))
        for protocol, endpoint, images in [
            ("tcp", "0.0.0.0:5432", ["/usr/lib/postgresql/18/bin/postgres"]),
            ("tcp", "0.0.0.0:443", ["/opt/rixa/rixa"]),
            ("tcp", "0.0.0.0:53", [probe.OS_RESOLVER]),
            ("tcp", "127.0.0.53:53", ["/tmp/systemd-resolved"]),
            ("tcp", "0.0.0.0:22", []), ("tcp", "malformed", ["/usr/sbin/sshd"]),
        ]:
            with self.subTest(endpoint=endpoint, images=images):
                self.assertFalse(probe.allowed_socket(protocol, endpoint, images))

    def test_a_matching_owner_pid_without_qemu_identity_cannot_authorize_ssh(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            (root / "firstboot.launch.json").write_text(json.dumps({
                "state": "qemu-running-acknowledged", "image": str(root / "guest.qcow2"), "pid": os.getpid()}))
            with self.assertRaisesRegex(ValueError, "exact owned QEMU"):
                verify.positive_launch(root, "firstboot")

    def test_running_or_unknown_launch_cannot_seal_or_chmod_a_disk(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            image = root / "guest.qcow2"
            image.write_bytes(b"retained")
            image.chmod(0o600)
            (root / "firstboot.inspection.json").write_text(json.dumps({
                "status": "verified", "product_tools_injected": False, "instance_id": "fixture"}))
            (root / "firstboot.launch.json").write_text(json.dumps({
                "state": "qemu-running-acknowledged", "exit_code": None}))
            with patch.object(verify.cloud, "load", return_value=(root, {"instance_id": "fixture"})):
                with self.assertRaisesRegex(ValueError, "actual successful QEMU exit"):
                    verify.seal(argparse.Namespace(lab=str(root), attempt="firstboot"))
            self.assertEqual(image.stat().st_mode & 0o777, 0o600)
            self.assertFalse((root / "clean-baseline.json").exists())

    def test_sealed_baseline_cannot_be_rebooted_in_place(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            (root / "clean-baseline.json").write_text("{}")
            with patch.object(verify.cloud, "load", return_value=(root, {})):
                with self.assertRaisesRegex(ValueError, "sealed clean baseline"):
                    verify.cloud.boot(argparse.Namespace(lab=str(root), attempt="firstboot"))


if __name__ == "__main__":
    unittest.main()
