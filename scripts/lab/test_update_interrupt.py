# SPDX-License-Identifier: MPL-2.0
import copy
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("update_interrupt", Path(__file__).with_name("update-interrupt.py"))
interrupt = importlib.util.module_from_spec(spec)
spec.loader.exec_module(interrupt)


class ExecutorOwnershipTests(unittest.TestCase):
    def setUp(self):
        self.record = {"schema": "rixa.native-update.v1", "draft": {"operation_id": "lab-update"},
                       "executor_generation": 1, "executor_acknowledged": True, "manager_sha256": "a" * 64}
        self.unit = "rixa-update-lab-update-1.service"
        self.observed = {"Id": self.unit, "ActiveState": "active", "MainPID": "123",
                         "InvocationID": "owned-invocation", "ControlGroup": "/system.slice/" + self.unit,
                         "ExecStart": "/opt/rixa-manager/" + "a" * 64 + " manage internal-update-apply lab-update"}

    def test_only_acknowledged_exact_live_executor_is_accepted(self):
        self.assertEqual(interrupt.validate_owner(self.record, "lab-update", self.observed), self.unit)
        for field, value in (("executor_acknowledged", False), ("executor_generation", True),
                             ("executor_generation", 17), ("manager_sha256", "foreign")):
            record = copy.deepcopy(self.record)
            record[field] = value
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                interrupt.validate_owner(record, "lab-update", self.observed)

    def test_main_pid_alone_cannot_establish_ownership(self):
        for field, value in (("Id", "foreign.service"), ("ControlGroup", "/system.slice/foreign.service"),
                             ("ExecStart", "/tmp/foreign manage internal-update-apply lab-update"),
                             ("InvocationID", ""), ("ActiveState", "inactive"), ("MainPID", "0")):
            observed = dict(self.observed, **{field: value})
            with self.subTest(field=field), self.assertRaises(ValueError):
                interrupt.validate_owner(self.record, "lab-update", observed)

    def test_cgroup_population_is_kernel_observed_and_unknown_is_rejected(self):
        with patch.object(Path, "read_text", return_value="populated 1\nfrozen 0\n"):
            self.assertEqual(interrupt.cgroup_population("/system.slice/" + self.unit), "1")
        with patch.object(Path, "read_text", return_value="populated 0\nfrozen 0\n"):
            self.assertEqual(interrupt.cgroup_population("/system.slice/" + self.unit), "0")
        with patch.object(Path, "read_text", return_value="unknown 0\n"), self.assertRaises(ValueError):
            interrupt.cgroup_population("/system.slice/" + self.unit)
        for group in ("/", "/system.slice/other.service", "/system.slice/../rixa-update-lab-1.service"):
            with self.subTest(group=group), self.assertRaises(ValueError):
                interrupt.cgroup_population(group)


if __name__ == "__main__":
    unittest.main()
