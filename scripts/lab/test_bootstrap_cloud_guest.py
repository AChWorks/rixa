# SPDX-License-Identifier: MPL-2.0
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("cloud_lab", Path(__file__).with_name("bootstrap-cloud-guest.py"))
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class CloudTrustAndSeedTests(unittest.TestCase):
    def test_only_official_https_locations_without_credentials_or_traversal(self):
        for url in ("https://cloud.debian.org/images/cloud/trixie/20261026-2643/SHA512SUMS",
                    "https://cdimage.debian.org/images/cloud/HEADER.html"):
            self.assertEqual(lab.validate_url(url), url)
        for url in ("http://cloud.debian.org/images/cloud/trixie/x", "https://evil.test/images/cloud/x",
                    "https://user@cloud.debian.org/images/cloud/x", "https://cloud.debian.org:444/images/cloud/x",
                    "https://cloud.debian.org/images/cloud/../x", "https://cloud.debian.org/images/cloud/%2e%2e/x",
                    "https://cloud.debian.org/images/cloud/x?token=secret"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                lab.validate_url(url)

    def test_manifest_must_bind_one_exact_file(self):
        digest = "a" * 128
        self.assertEqual(lab.manifest_digest(digest + "  image.qcow2\n", "image.qcow2"), digest)
        for text in (digest + "  other.qcow2\n", (digest + "  image.qcow2\n") * 2, "invalid  image.qcow2\n"):
            with self.subTest(text=text[:20]), self.assertRaises(ValueError):
                lab.manifest_digest(text, "image.qcow2")

    def test_seed_is_public_key_only_os_ssh_marker_configuration(self):
        public = "ssh-ed25519 QUJDREVGRw== rixa-bootstrap-clean"
        user, meta = lab.seed_data(public, "rixa-bootstrap-clean")
        self.assertIn(json.dumps(public), user)
        self.assertIn("package_update: false", user)
        self.assertIn("package_upgrade: false", user)
        self.assertIn("KbdInteractiveAuthentication no", user)
        self.assertIn("rixa.bootstrap-lab.v1", user)
        self.assertNotIn("BEGIN OPENSSH PRIVATE KEY", user)
        self.assertNotIn("password:", user)
        self.assertNotIn("postgresql", user)
        self.assertNotIn("packages:", user)
        self.assertEqual(meta, "instance-id: rixa-bootstrap-clean\nlocal-hostname: rixa-bootstrap-lab\n")
        for instance in ("x\nruncmd: evil", "123", "../escape"):
            with self.subTest(instance=instance), self.assertRaises(ValueError):
                lab.seed_data(public, instance)

    def test_metadata_rejects_wrong_scope_or_preinstalled_product(self):
        info = {"arch": "amd64", "release": "trixie", "release_id": "13",
                "type": "official", "vendor": "genericcloud", "version": "20261026-2643"}
        def data(packages, override=None):
            return json.dumps({"items": [{"kind": "Build", "data": {"info": dict(info, **(override or {})),
                                "packages": packages}}]})
        normal = [{"name": "cloud-init", "version": "1"}]
        self.assertEqual(lab.metadata_info(data(normal), "20261026-2643")[0], info)
        for packages, override in [([{"name": "postgresql-18", "version": "1"}], {}),
                                  ([{"name": "rixa", "version": "1"}], {}), (normal, {"arch": "arm64"})]:
            with self.subTest(packages=packages, override=override), self.assertRaises(ValueError):
                lab.metadata_info(data(packages, override), "20261026-2643")

    def test_unverified_image_is_not_parsed_or_executed_and_attempt_survives(self):
        metadata = json.dumps({"items": [{"kind": "Build", "data": {
            "info": {"arch": "amd64", "release": "trixie", "release_id": "13",
                     "type": "official", "vendor": "genericcloud", "version": "20261026-2643"},
            "packages": [{"name": "cloud-init", "version": "1"}]}}]}).encode()
        selected = hashlib.sha512(b"intended verified image").hexdigest()
        prefix = "debian-13-genericcloud-amd64-20261026-2643"
        manifest = (selected + "  " + prefix + ".qcow2\n" +
                    hashlib.sha512(metadata).hexdigest() + "  " + prefix + ".json\n").encode()
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / "new-lab"
            args = argparse.Namespace(lab=str(target), trust_method=lab.TRUST, build="20261026-2643",
                image_sha512=selected, manifest_sha256=hashlib.sha256(manifest).hexdigest(),
                instance="rixa-bootstrap-clean", scope_record="fixture")
            def download(url, path, maximum):
                content = manifest if url.endswith("SHA512SUMS") else metadata if url.endswith(".json") else b"corrupt image"
                path.write_bytes(content)
            with patch.object(lab, "download", side_effect=download), patch.object(lab.subprocess, "run") as execute:
                with self.assertRaisesRegex(ValueError, "checksum failed"):
                    lab.prepare(args)
                execute.assert_not_called()
            self.assertEqual(json.loads((target / "attempt.json").read_text())["state"],
                             "official-image-download-attempted")
            self.assertFalse((target / "guest.qcow2").exists())

    def test_existing_state_is_never_overwritten_on_prepare(self):
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / "existing"
            target.mkdir(mode=0o700)
            (target / "owned").write_text("preserve")
            args = argparse.Namespace(lab=str(target), trust_method=lab.TRUST, build="20261026-2643",
                image_sha512="a" * 128, manifest_sha256="b" * 64, instance="rixa-bootstrap-clean", scope_record="")
            with self.assertRaises(FileExistsError):
                lab.prepare(args)
            self.assertEqual((target / "owned").read_text(), "preserve")


if __name__ == "__main__":
    unittest.main()
