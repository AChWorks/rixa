# SPDX-License-Identifier: MPL-2.0
"""Exercise the immutable entry's byte/path boundaries without host privilege."""
import hashlib
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tarfile
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "install.sh"
body = SCRIPT.read_text().split("<<'RIXA_BOOTSTRAP_PYTHON'\n", 1)[1].rsplit("\nRIXA_BOOTSTRAP_PYTHON", 1)[0]
entry = types.ModuleType("rixa_entry_test")
exec(compile(body, str(SCRIPT), "exec"), entry.__dict__)


def archive(entries):
    target = io.BytesIO()
    with tarfile.open(fileobj=target, mode="w:gz") as tar:
        for name, data, kind in entries:
            item = tarfile.TarInfo(name)
            item.type = kind
            item.size = len(data)
            tar.addfile(item, io.BytesIO(data))
    return target.getvalue()


class EntryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.owner = os.getuid()

    def tearDown(self):
        self.temp.cleanup()

    def fixture(self):
        starter, helper = b"bounded trusted starter fixture", b"bounded trusted helper fixture"
        prefix = "rixa-" + entry.STARTER_RELEASE + "-linux-amd64/"
        metadata = json.dumps({"rixa": entry.STARTER_RELEASE, "source_commit": entry.STARTER_SOURCE,
                               "target": "linux-amd64"}).encode()
        archives = [
            archive([(prefix + "rixa", starter, tarfile.REGTYPE),
                     (prefix + "BUILDINFO.json", metadata, tarfile.REGTYPE)]),
            archive([("gh_" + entry.HELPER_VERSION + "_linux_amd64/bin/gh", helper, tarfile.REGTYPE)]),
        ]
        pins = dict(STARTER_ARCHIVE=hashlib.sha256(archives[0]).hexdigest(),
                    HELPER_ARCHIVE=hashlib.sha256(archives[1]).hexdigest(),
                    STARTER_BINARY=hashlib.sha256(starter).hexdigest(),
                    HELPER_BINARY=hashlib.sha256(helper).hexdigest())
        return archives, pins

    def curl(self, payloads):
        payloads = iter(payloads)
        def run(command, **options):
            self.assertEqual(command[0], "/usr/bin/curl")
            self.assertEqual(options["env"], entry.BASE_ENV)
            self.assertEqual(options["stdin"], subprocess.DEVNULL)
            self.assertTrue(options["check"])
            self.assertLessEqual(options["timeout"], 200)
            options["stdout"].write(next(payloads))
            return subprocess.CompletedProcess(command, 0)
        return run

    def test_corrupt_download_is_preserved_and_never_parsed(self):
        path = self.root / "cache"
        with patch.object(entry.subprocess, "run", self.curl([b"corrupt"])), patch.object(entry, "member") as parse:
            with self.assertRaises(entry.EntryError):
                entry.prepare(path, self.owner)
        parse.assert_not_called()
        self.assertEqual((path / "starter.tar.gz").read_bytes(), b"corrupt")
        self.assertFalse((path / "rixa").exists())
        with patch.object(entry, "download") as download, self.assertRaises(entry.EntryError):
            entry.prepare(path, self.owner)
        download.assert_not_called()

    def test_transport_failure_preserves_partial_state_and_cannot_execute(self):
        path = self.root / "cache"
        def fail(command, **options):
            options["stdout"].write(b"partial")
            raise subprocess.CalledProcessError(22, command)
        with patch.object(entry.subprocess, "run", fail), patch.object(entry, "member") as parse:
            with self.assertRaises(subprocess.CalledProcessError):
                entry.prepare(path, self.owner)
        parse.assert_not_called()
        self.assertEqual((path / "starter.tar.gz").read_bytes(), b"partial")
        self.assertFalse((path / "gh").exists())

    def test_complete_cache_is_reused_without_network_and_drift_is_not_replaced(self):
        payloads, pins = self.fixture()
        path = self.root / "cache"
        with patch.multiple(entry, **pins), patch.object(entry.subprocess, "run", self.curl(payloads)):
            self.assertEqual(entry.prepare(path, self.owner), path / "rixa")
        before = {p.name: (p.read_bytes(), p.stat().st_mtime_ns) for p in path.iterdir()}
        with patch.multiple(entry, **pins), patch.object(entry, "download") as download:
            entry.prepare(path, self.owner)
            download.assert_not_called()
        self.assertEqual(before, {p.name: (p.read_bytes(), p.stat().st_mtime_ns) for p in path.iterdir()})
        (path / "gh").chmod(0o600)
        (path / "gh").write_bytes(b"changed")
        (path / "gh").chmod(0o500)
        with patch.multiple(entry, **pins), patch.object(entry, "download") as download, self.assertRaises(entry.EntryError):
            entry.prepare(path, self.owner)
        download.assert_not_called()
        self.assertEqual((path / "gh").read_bytes(), b"changed")

    def test_exact_archive_member_rejects_links_duplicates_oversize_and_missing(self):
        for entries, maximum in [
            ([("known", b"x", tarfile.SYMTYPE)], 10),
            ([("known", b"x", tarfile.REGTYPE)] * 2, 10),
            ([("known", b"large", tarfile.REGTYPE)], 2),
            ([("../known", b"x", tarfile.REGTYPE)], 10),
        ]:
            with self.subTest(entries=entries), self.assertRaises(entry.EntryError):
                entry.member(archive(entries), "known", maximum)
        self.assertEqual(entry.member(archive([("known", b"safe", tarfile.REGTYPE)]), "known", 10), b"safe")

    def test_cache_paths_reject_links_shared_permissions_hardlinks_and_unknown_files(self):
        target = self.root / "target"; target.mkdir()
        linked = self.root / "linked"; linked.symlink_to(target, target_is_directory=True)
        with self.assertRaises(entry.EntryError):
            entry.prepare(linked, self.owner)
        target.chmod(0o755)
        with self.assertRaises(entry.EntryError):
            entry.private_directory(target, self.owner)
        target.chmod(0o700)
        (target / "unknown").write_bytes(b"preserve")
        with self.assertRaises(entry.EntryError):
            entry.prepare(target, self.owner)
        first = self.root / "first"; first.write_bytes(b"same"); first.chmod(0o400)
        second = self.root / "second"; os.link(first, second)
        with self.assertRaises(entry.EntryError):
            entry.checked_bytes(second, hashlib.sha256(b"same").hexdigest(), 0o400, self.owner)

    def test_hash_verification_enforces_bound_mode_and_no_symlink(self):
        value = self.root / "value"; value.write_bytes(b"safe"); value.chmod(0o400)
        sha = hashlib.sha256(b"safe").hexdigest()
        self.assertEqual(entry.checked_bytes(value, sha, 0o400, self.owner), b"safe")
        for maximum in (1, 3):
            with self.assertRaises(entry.EntryError):
                entry.checked_bytes(value, sha, 0o400, self.owner, maximum)
        link = self.root / "link"; link.symlink_to(value)
        with self.assertRaises(OSError):
            entry.checked_bytes(link, sha, 0o400, self.owner)
        value.chmod(0o600)
        with self.assertRaises(entry.EntryError):
            entry.checked_bytes(value, sha, 0o400, self.owner)

    @unittest.skipIf(os.geteuid() == 0, "actual privileged entry requires a separately granted disposable guest")
    def test_real_launcher_ignores_injected_python_modules_and_refuses_unprivileged_host(self):
        (self.root / "hashlib.py").write_text('raise RuntimeError("injected module executed")\n')
        environment = dict(os.environ, PYTHONPATH=str(self.root), PYTHONHOME=str(self.root),
                           BASH_ENV=str(self.root / "hashlib.py"))
        result = subprocess.run(["/bin/sh", str(SCRIPT), "inspect", "--non-interactive"],
                                cwd=self.root, env=environment, capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("authorized root infrastructure operator", result.stderr)
        self.assertNotIn("injected module executed", result.stderr)

    def test_documented_literal_anchor_matches_exact_script(self):
        digest = hashlib.sha256(SCRIPT.read_bytes()).hexdigest()
        text = (ROOT / "docs/bootstrap-entry.md").read_text()
        self.assertIn(digest, text)
        self.assertIn("https://raw.githubusercontent.com/AChWorks/rixa/bootstrap-v1/install.sh", text)
        self.assertNotIn("SCRIPT_SHA256", text)
        subprocess.run(["/bin/sh", "-n", str(SCRIPT)], check=True, timeout=10)

    @unittest.skipUnless(os.environ.get("RIXA_ENTRY_PUBLIC_ARCHIVES"), "actual public archives not supplied")
    def test_actual_pinned_public_archives(self):
        root = Path(os.environ["RIXA_ENTRY_PUBLIC_ARCHIVES"])
        starter = entry.checked_bytes(root / "starter.tar.gz", entry.STARTER_ARCHIVE, 0o400, self.owner)
        helper = entry.checked_bytes(root / "helper.tar.gz", entry.HELPER_ARCHIVE, 0o400, self.owner)
        prefix = "rixa-" + entry.STARTER_RELEASE + "-linux-amd64/"
        metadata = json.loads(entry.member(starter, prefix + "BUILDINFO.json", 65536))
        self.assertEqual(metadata["source_commit"], entry.STARTER_SOURCE)
        self.assertEqual(hashlib.sha256(entry.member(starter, prefix + "rixa", entry.MAXIMUM)).hexdigest(), entry.STARTER_BINARY)
        self.assertEqual(hashlib.sha256(entry.member(helper, "gh_" + entry.HELPER_VERSION + "_linux_amd64/bin/gh",
                                                   entry.MAXIMUM)).hexdigest(), entry.HELPER_BINARY)


if __name__ == "__main__":
    unittest.main()
