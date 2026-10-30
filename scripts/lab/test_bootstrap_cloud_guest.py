# SPDX-License-Identifier: MPL-2.0
import argparse
import hashlib
import importlib.util
import json
import io
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
            def download(url, path, maximum, image_sha512=None):
                content = manifest if url.endswith("SHA512SUMS") else metadata if url.endswith(".json") else b"corrupt image"
                path.write_bytes(content)
            with patch.object(lab, "download", side_effect=download), patch.object(lab.subprocess, "run") as execute:
                with self.assertRaisesRegex(ValueError, "checksum failed"):
                    lab.prepare(args)
                execute.assert_not_called()
            self.assertEqual(json.loads((target / "attempt.json").read_text())["state"],
                             "official-image-download-attempted")
            self.assertFalse((target / "guest.qcow2").exists())

    def test_redirected_image_transport_is_not_metadata_authority(self):
        mirror = "https://saimei.ftp.acc.umu.se/images/cloud/trixie/image.qcow2"
        self.assertEqual(lab.validate_image_transport(mirror), mirror)
        with self.assertRaises(ValueError):
            lab.validate_url(mirror)
        with self.assertRaises(ValueError):
            lab.OfficialRedirects("not-a-sha512")
        for url in ("http://mirror.test/image", "https://localhost/image", "https://127.0.0.1/image",
                    "https://user@mirror.test/image", "https://10.0.0.1/image"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                lab.validate_image_transport(url)

    def test_redirected_corrupt_image_fails_mandatory_pin_before_return(self):
        class Response(io.BytesIO):
            headers = {"Content-Length": "3"}
            def geturl(self):
                return "https://mirror.example.net/image.qcow2"
        with tempfile.TemporaryDirectory() as root, patch.object(lab, "build_opener") as build:
            build.return_value.open.return_value = Response(b"bad")
            with self.assertRaisesRegex(ValueError, "image checksum failed"):
                lab.download("https://cloud.debian.org/images/cloud/image.qcow2",
                             Path(root) / "image", 1024, hashlib.sha512(b"good").hexdigest())

    def test_continuation_refuses_partial_image_or_later_effects(self):
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / "existing"
            target.mkdir(mode=0o700)
            for name in ("official-policy.html", "SHA512SUMS", "image-metadata.json", "partial-image.qcow2"):
                (target / name).write_text("retained")
            state = {"state": "official-image-download-attempted", "build": "20261026-2643", "trust_method": lab.TRUST}
            (target / "attempt.json").write_text(json.dumps(state))
            args = argparse.Namespace(lab=str(target), trust_method=lab.TRUST, build="20261026-2643",
                image_sha512="a" * 128, manifest_sha256="b" * 64, instance="rixa-bootstrap-clean",
                scope_record="fixture", continue_image_download=True)
            with patch.object(lab, "download") as download, patch.object(lab.subprocess, "run") as execute:
                with self.assertRaisesRegex(ValueError, "exact metadata-only"):
                    lab.prepare(args)
                download.assert_not_called()
                execute.assert_not_called()
            self.assertEqual((target / "partial-image.qcow2").read_text(), "retained")

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


    def test_sealed_baseline_requires_exact_bytes_mode_and_clean_evidence(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            image = base / "guest.qcow2"
            image.write_bytes(b"sealed clean disk")
            image.chmod(0o400)
            pin = hashlib.sha256(image.read_bytes()).hexdigest()
            data = {"instance_id": "clean-instance"}
            seal = {"schema": "rixa.bootstrap-clean-baseline.v1", "status": "sealed",
                    "baseline_image": str(image), "baseline_sha256": pin,
                    "product_tools_injected": False, "actual_qemu_exit": 0,
                    "instance_id": "clean-instance"}
            (base / "clean-baseline.json").write_text(json.dumps(seal))
            with patch.object(lab, "load", return_value=(base, data)):
                self.assertEqual(lab.sealed_baseline(str(base), pin)[2], image)
                for field, value in (("product_tools_injected", True), ("actual_qemu_exit", 1),
                                     ("instance_id", "another-instance"), ("baseline_sha256", "a" * 64)):
                    (base / "clean-baseline.json").write_text(json.dumps(dict(seal, **{field: value})))
                    with self.subTest(field=field), self.assertRaises(ValueError):
                        lab.sealed_baseline(str(base), pin)
                (base / "clean-baseline.json").write_text(json.dumps(seal))
                image.chmod(0o600)
                with self.assertRaises(ValueError):
                    lab.sealed_baseline(str(base), pin)
                image.write_bytes(b"changed disk")
                image.chmod(0o400)
                with self.assertRaises(ValueError):
                    lab.sealed_baseline(str(base), pin)

    def test_journey_refuses_existing_state_before_overlay_effect(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            target = base / "journey1"
            target.mkdir(mode=0o700)
            marker = target / "preserve"
            marker.write_text("failed prior evidence")
            args = argparse.Namespace(lab=str(target), baseline_lab=str(base), baseline_sha256="a" * 64,
                    scope_record="https://github.com/AChWorks/rixa/pull/45#issuecomment-123")
            with patch.object(lab, "sealed_baseline", return_value=(base, {}, base / "guest.qcow2")), \
                 patch.object(lab, "owned_qemu", return_value=[]), patch.object(lab.socket, "socket"), \
                 patch.object(lab.subprocess, "run") as execute:
                with self.assertRaises(FileExistsError):
                    lab.journey(args)
                execute.assert_not_called()
            self.assertEqual(marker.read_text(), "failed prior evidence")

    def test_journey_rejects_unrelated_target_and_unscoped_locator(self):
        args = argparse.Namespace(lab="/outside/journey1", baseline_lab="/private/base",
                baseline_sha256="a" * 64, scope_record="https://github.com/AChWorks/rixa/pull/45#issuecomment-123")
        with patch.object(lab, "sealed_baseline", return_value=(Path("/private/base"), {}, Path("/private/base/guest.qcow2"))), \
             patch.object(lab.subprocess, "run") as execute:
            with self.assertRaisesRegex(ValueError, "direct child"):
                lab.journey(args)
            execute.assert_not_called()
        args.scope_record = "https://evil.test/pull/45#issuecomment-123"
        with patch.object(lab, "sealed_baseline") as baseline:
            with self.assertRaisesRegex(ValueError, "scope locator"):
                lab.journey(args)
            baseline.assert_not_called()


if __name__ == "__main__":
    unittest.main()
