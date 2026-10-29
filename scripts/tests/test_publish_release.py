# SPDX-License-Identifier: MPL-2.0
"""Publication-state tests with fake API/crypto boundaries, not signing proof."""

import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import subprocess
from types import SimpleNamespace
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("publish_release", Path(__file__).parents[1] / "publish-release.py")
publish = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publish)

REAL_VERIFY_BUNDLE = publish.verify_bundle

SOURCE = "a" * 40
VERSION = "v0.2.0"
ENV = {"GITHUB_REPOSITORY": "AChWorks/rixa", "GITHUB_REPOSITORY_ID": "1403734531",
    "GITHUB_REPOSITORY_OWNER_ID": "80599060", "GITHUB_EVENT_NAME": "push",
    "GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": VERSION, "GITHUB_REF": "refs/tags/" + VERSION,
    "GITHUB_SHA": SOURCE, "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1",
    "GITHUB_SERVER_URL": "https://github.com", "GITHUB_API_URL": "https://api.github.com",
    "GITHUB_WORKFLOW_REF": "AChWorks/rixa/.github/workflows/release.yml@refs/tags/" + VERSION,
    "GITHUB_WORKFLOW_SHA": SOURCE}


class GitHubState:
    def __init__(self):
        self.releases = []
        self.events = []
        self.fault = ""
        self.uploads = 0
        self.tag_reads = 0
        self.annotated = False

    def call(self, method, path, body=None, content_type="application/json", upload=False):
        self.events.append((method, path))
        if method == "GET" and path.startswith("/git/ref/tags/"):
            self.tag_reads += 1
            digest = "b" * 40 if self.fault == "tag-drift" and self.tag_reads > 1 else SOURCE
            if self.annotated:
                return {"object": {"type": "tag", "sha": "c" * 40}}
            return {"object": {"type": "commit", "sha": digest}}
        if method == "GET" and path == "/git/tags/" + "c" * 40:
            return {"object": {"type": "commit", "sha": SOURCE}}
        if method == "GET" and path.startswith("/releases?"):
            return copy.deepcopy(self.releases)
        if method == "POST" and path == "/releases":
            if self.fault == "create-missing":
                raise publish.APIError("lost create acknowledgement")
            value = dict(body, id=42, assets=[], published_at=None)
            if self.fault == "normalized-target":
                value["target_commitish"] = "main"
            self.releases.append(value)
            if self.fault == "create-applied":
                raise publish.APIError("lost create acknowledgement")
            return copy.deepcopy(value)
        if method == "GET" and path == "/releases/42":
            return copy.deepcopy(self.releases[0])
        if method == "POST" and path.startswith("/releases/42/assets?"):
            self.assert_upload(upload, content_type)
            self.uploads += 1
            if self.fault == "upload-missing":
                raise publish.APIError("lost upload acknowledgement")
            name = publish.parse.parse_qs(publish.parse.urlparse(path).query)["name"][0]
            digest = hashlib.sha256(body).hexdigest()
            if self.fault == "asset-drift":
                digest = "0" * 64
            self.releases[0]["assets"].append({"name": name, "size": len(body), "state": "uploaded", "digest": "sha256:" + digest})
            if self.fault == "unexpected-asset":
                self.releases[0]["assets"].append({"name": "foreign", "size": 1, "state": "uploaded", "digest": "sha256:" + "0" * 64})
            if self.fault == "upload-applied" and self.uploads == 1:
                raise publish.APIError("lost upload acknowledgement")
            return copy.deepcopy(self.releases[0]["assets"][-1])
        if method == "PATCH" and path == "/releases/42":
            if self.fault == "publish-missing":
                raise publish.APIError("lost publish acknowledgement")
            self.releases[0].update(body, published_at="2026-10-29T00:00:00Z")
            if self.fault == "publish-applied":
                raise publish.APIError("lost publish acknowledgement")
            return copy.deepcopy(self.releases[0])
        raise AssertionError((method, path))

    @staticmethod
    def assert_upload(upload, content_type):
        if not upload or content_type != "application/octet-stream":
            raise AssertionError("asset upload used wrong endpoint/content type")

    def mutations(self):
        return [(method, path) for method, path in self.events if method != "GET"]


class PackageFixture:
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.archive = self.root / ("rixa-" + VERSION + "-linux-amd64.tar.gz")
        base = self.archive.name.removesuffix(".tar.gz")
        info = {"rixa": VERSION, "achrix": "v0.4.0", "achrix_components": {"github.com/AChWorks/achrix": "v0.4.0"}, "source_commit": SOURCE, "target": "linux-amd64"}
        raw = json.dumps(info).encode()
        with tarfile.open(self.archive, "w:gz") as tar:
            entry = tarfile.TarInfo(base + "/BUILDINFO.json")
            entry.size = len(raw)
            tar.addfile(entry, io.BytesIO(raw))
        digest = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        Path(str(self.archive) + ".sha256").write_text(digest + "  " + self.archive.name + "\n")
        Path(str(self.archive) + ".sigstore.json").write_text(json.dumps({"verificationMaterial": {"fixture": True}, "dsseEnvelope": {"fixture": True}}))
        self.crypto = mock.patch.object(publish, "verify_bundle")
        self.verify = self.crypto.start()
        self.addCleanup(self.crypto.stop)
        self.package = publish.Package(self.archive, ENV)
        self.api = GitHubState()
        self.publisher = publish.Publisher(self.api, self.package)

class PublishTests(PackageFixture, unittest.TestCase):
    def test_complete_draft_precedes_single_publication(self):
        result = self.publisher.publish()
        self.assertEqual(result, (42, False))
        self.assertFalse(self.api.releases[0]["draft"])
        self.assertTrue(self.api.releases[0]["prerelease"])
        self.assertEqual(len(self.api.releases[0]["assets"]), 3)
        methods = [method for method, _ in self.api.mutations()]
        self.assertEqual(methods, ["POST", "POST", "POST", "POST", "PATCH"])
        self.verify.assert_called_once_with(self.package)

    def test_existing_draft_or_release_is_never_modified(self):
        for draft in (True, False):
            with self.subTest(draft=draft):
                self.api.releases = [{"tag_name": VERSION, "id": 7, "draft": draft}]
                self.api.events.clear()
                with self.assertRaisesRegex(publish.PublishError, "already exists"):
                    self.publisher.publish()
                self.assertEqual(self.api.mutations(), [])

    def test_invalid_crypto_prevents_even_release_creation(self):
        self.verify.side_effect = publish.PublishError("invalid attestation")
        with self.assertRaisesRegex(publish.PublishError, "invalid attestation"):
            self.publisher.publish()
        self.assertEqual(self.api.mutations(), [])

    def test_unknown_create_never_replays(self):
        for outcome in ("create-missing", "create-applied"):
            with self.subTest(outcome=outcome):
                self.api = GitHubState()
                self.api.fault = outcome
                self.publisher = publish.Publisher(self.api, self.package)
                with self.assertRaisesRegex(publish.PublishError, "creation acknowledgement uncertain"):
                    self.publisher.publish()
                self.assertEqual(self.api.mutations(), [("POST", "/releases")])
                if self.api.releases:
                    self.assertTrue(self.api.releases[0]["draft"])
                    self.assertEqual(self.api.releases[0]["assets"], [])

    def test_unknown_applied_upload_is_observed_without_reupload(self):
        self.api.fault = "upload-applied"
        self.assertEqual(self.publisher.publish(), (42, True))
        self.assertEqual(self.api.uploads, 3)
        self.assertFalse(self.api.releases[0]["draft"])

    def test_unknown_absent_upload_is_not_retried_or_published(self):
        self.api.fault = "upload-missing"
        with self.assertRaisesRegex(publish.PublishError, "upload outcome uncertain"):
            self.publisher.publish()
        self.assertEqual(self.api.uploads, 1)
        self.assertTrue(self.api.releases[0]["draft"])
        self.assertFalse(any(method == "PATCH" for method, _ in self.api.events))

    def test_different_or_unknown_assets_block_publication_without_removal(self):
        for fault in ("asset-drift", "unexpected-asset"):
            with self.subTest(fault=fault):
                self.api = GitHubState()
                self.api.fault = fault
                self.publisher = publish.Publisher(self.api, self.package)
                with self.assertRaises(publish.PublishError):
                    self.publisher.publish()
                self.assertTrue(self.api.releases[0]["draft"])
                self.assertTrue(self.api.releases[0]["assets"])
                self.assertFalse(any(method in ("PATCH", "DELETE") for method, _ in self.api.events))

    def test_unknown_applied_publish_is_verified_without_republication(self):
        self.api.fault = "publish-applied"
        self.assertEqual(self.publisher.publish(), (42, True))
        self.assertEqual(sum(method == "PATCH" for method, _ in self.api.events), 1)

    def test_unknown_unapplied_publish_preserves_complete_draft(self):
        self.api.fault = "publish-missing"
        with self.assertRaisesRegex(publish.PublishError, "inspect release"):
            self.publisher.publish()
        self.assertTrue(self.api.releases[0]["draft"])
        self.assertEqual(len(self.api.releases[0]["assets"]), 3)
        self.assertEqual(sum(method == "PATCH" for method, _ in self.api.events), 1)

    def test_tag_drift_blocks_final_transition(self):
        self.api.fault = "tag-drift"
        with self.assertRaisesRegex(publish.PublishError, "checked release source"):
            self.publisher.publish()
        self.assertTrue(self.api.releases[0]["draft"])
        self.assertFalse(any(method == "PATCH" for method, _ in self.api.events))

    def test_uploaded_bytes_are_the_validated_snapshot(self):
        original = self.package.files[self.archive.name][0]
        self.archive.write_bytes(b"changed after validation")
        self.publisher.publish()
        asset = self.api.releases[0]["assets"][0]
        self.assertEqual(asset["digest"], "sha256:" + hashlib.sha256(original).hexdigest())
        self.assertEqual(asset["size"], len(original))

    def test_wrong_workflow_context_tag_or_source_is_rejected(self):
        for key, value in (("GITHUB_REPOSITORY", "other/rixa"), ("GITHUB_REPOSITORY_ID", "1"), ("GITHUB_REPOSITORY_OWNER_ID", "1"), ("GITHUB_EVENT_NAME", "workflow_dispatch"), ("GITHUB_REF_TYPE", "branch"), ("GITHUB_REF_NAME", "v1.2.3\narchive=evil"), ("GITHUB_SHA", "b" * 40), ("GITHUB_SERVER_URL", "https://other.example"), ("GITHUB_WORKFLOW_REF", "AChWorks/rixa/.github/workflows/other.yml@refs/tags/" + VERSION), ("GITHUB_WORKFLOW_SHA", "b" * 40)):
            with self.subTest(key=key):
                env = dict(ENV, **{key: value})
                with self.assertRaises(publish.PublishError):
                    publish.Package(self.archive, env)

    def test_checksum_change_and_unsafe_bundle_block_effects(self):
        Path(str(self.archive) + ".sha256").write_text("unverified")
        with self.assertRaises(publish.PublishError):
            publish.Package(self.archive, ENV)
        Path(str(self.archive) + ".sigstore.json").write_text("{}")
        with self.assertRaisesRegex(publish.PublishError, "incomplete"):
            self.publisher.publish()
        self.assertEqual(self.api.mutations(), [])

    def test_tag_resolution_handles_annotated_tags(self):
        self.api.annotated = True
        self.publisher.publish()
        self.assertFalse(self.api.releases[0]["draft"])
        self.assertEqual(sum(path.startswith("/git/tags/") for _, path in self.api.events), 2)

    def test_descriptive_target_normalization_does_not_replace_tag_proof(self):
        self.api.fault = "normalized-target"
        self.publisher.publish()
        self.assertEqual(self.api.releases[0]["target_commitish"], "main")
        self.assertEqual(self.api.tag_reads, 2)

    def test_malformed_inventory_cannot_prove_absence(self):
        self.api.releases = [{"id": 1}]
        with self.assertRaisesRegex(publish.PublishError, "absence unproven"):
            self.publisher.publish()
        self.assertEqual(self.api.mutations(), [])

    def test_duplicate_tag_records_block_all_effects(self):
        self.api.releases = [{"tag_name": VERSION, "id": 1}, {"tag_name": VERSION, "id": 2}]
        with self.assertRaisesRegex(publish.PublishError, "multiple releases"):
            self.publisher.publish()
        self.assertEqual(self.api.mutations(), [])

    def test_bundle_fifo_and_symlink_are_rejected_without_opening_payload(self):
        path = Path(str(self.archive) + ".sigstore.json")
        path.unlink()
        os.mkfifo(path, 0o600)
        with self.assertRaisesRegex(publish.PublishError, "unsafe"):
            self.publisher.publish()
        path.unlink()
        path.symlink_to(self.archive)
        with self.assertRaises(publish.PublishError):
            self.publisher.publish()
        self.assertEqual(self.api.mutations(), [])

    def test_workflow_rerun_cannot_replay_an_uncertain_prior_create(self):
        for attempt in (None, "", "2", "01", "malformed"):
            with self.subTest(attempt=attempt):
                env = dict(ENV)
                if attempt is None:
                    del env["GITHUB_RUN_ATTEMPT"]
                else:
                    env["GITHUB_RUN_ATTEMPT"] = attempt
                with self.assertRaisesRegex(publish.PublishError, "inspect and settle the original run/draft"):
                    publish.Package(self.archive, env)
        self.assertEqual(self.api.events, [])

    def test_check_is_read_only_and_does_not_require_a_bundle(self):
        Path(str(self.archive) + ".sigstore.json").unlink()
        self.publisher.check()
        self.assertEqual(self.api.mutations(), [])
        self.verify.assert_not_called()


class VerifierReceiptTests(PackageFixture, unittest.TestCase):
    # These isolate the trusted-tool receipt boundary, not cryptographic proof.
    def receipt(self):
        return {"schema": "rixa.artifact-verification.v1", "status": "verified", "code": "ok",
            "sha256": self.package.files[self.archive.name][1], "release": VERSION,
            "source_ref": "refs/tags/" + VERSION, "source_commit": SOURCE,
            "repository": "AChWorks/rixa", "workflow": "AChWorks/rixa/.github/workflows/release.yml",
            "runner": "github-hosted", "run_url": "https://github.com/AChWorks/rixa/actions/runs/123/attempts/1"}

    def run_receipt(self, receipt, exit_code=0, callback=None):
        self.package.bundle()
        verifier = self.root / "source-built-verifier"
        verifier.write_text("trusted executable is mocked only for this receipt unit test")
        verifier.chmod(0o700)
        result = SimpleNamespace(returncode=exit_code,
            stdout=json.dumps(receipt).encode(), stderr=b"invalid attestation" if exit_code else b"")
        def run(args, **kwargs):
            self.assertEqual(args[:3], [str(verifier), "artifact", "verify"])
            self.assertNotIn("GH_TOKEN", kwargs["env"])
            self.assertNotIn("GITHUB_TOKEN", kwargs["env"])
            frozen = Path(args[args.index("--archive") + 1])
            self.assertEqual(frozen.read_bytes(), self.package.files[self.archive.name][0])
            self.assertEqual(args[args.index("--sha256") + 1], self.package.files[self.archive.name][1])
            if callback:
                callback(args)
            return result
        with mock.patch.dict(os.environ, {"RIXA_PROVENANCE_VERIFIER": str(verifier), "GH_TOKEN": "private-job-token", "GITHUB_TOKEN": "private-job-token"}), mock.patch.object(publish.subprocess, "run", side_effect=run):
            REAL_VERIFY_BUNDLE(self.package)

    def test_exact_success_receipt_accepts_only_frozen_intent(self):
        self.run_receipt(self.receipt())

    def test_zero_exit_alone_is_not_verified_authority(self):
        for key in self.receipt():
            with self.subTest(key=key):
                bad = self.receipt()
                bad[key] = "different"
                with self.assertRaises(publish.PublishError):
                    self.run_receipt(bad)

    def test_failed_policy_receipt_preserves_reason_without_job_token(self):
        receipt = self.receipt()
        receipt.update(status="not-verified", code="policy-mismatch",
            field="certificate.sourceRepositoryIdentifier",
            detail="Inspect the publisher identity; private-job-token must stay private.")
        with self.assertRaises(publish.PublishError) as caught:
            self.run_receipt(receipt, exit_code=1)
        message = str(caught.exception)
        self.assertIn("policy-mismatch", message)
        self.assertIn("certificate.sourceRepositoryIdentifier", message)
        self.assertIn("Inspect the publisher identity", message)
        self.assertNotIn("private-job-token", message)

    def test_valid_requested_identity_with_nonzero_exit_is_rejected(self):
        with self.assertRaisesRegex(publish.PublishError, "attestation verification failed"):
            self.run_receipt(self.receipt(), exit_code=1)


class HTTPTransportTests(unittest.TestCase):
    def test_truncated_response_is_an_unknown_acknowledgement(self):
        api = publish.GitHub("private-job-token")
        response = mock.MagicMock()
        response.__enter__.return_value.read.side_effect = publish.http.client.IncompleteRead(b"partial")
        api.opener = mock.Mock()
        api.opener.open.return_value = response
        with self.assertRaisesRegex(publish.APIError, "acknowledgement unavailable"):
            api.call("POST", "/releases", {"draft": True})

    def test_incomplete_error_body_retains_status_without_token_or_traceback(self):
        api = publish.GitHub("private-job-token")
        body = mock.Mock()
        body.read.side_effect = publish.http.client.IncompleteRead(b"partial")
        failure = publish.error.HTTPError("https://api.github.com", 502, "Bad Gateway", None, body)
        api.opener = mock.Mock()
        api.opener.open.side_effect = failure
        with self.assertRaises(publish.APIError) as result:
            api.call("POST", "/releases", {"draft": True})
        self.assertIn("HTTP 502", str(result.exception))
        self.assertNotIn("private-job-token", str(result.exception))


class HelperPreparationTests(unittest.TestCase):
    def test_corrupt_download_never_reaches_archive_parser(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tools = root / "bin"
            tools.mkdir()
            marker = root / "parser-ran"
            curl = tools / "curl"
            curl.write_text('#!/bin/sh\nwhile [ "$#" -gt 0 ]; do\n if [ "$1" = -o ]; then shift; printf corrupt > "$1"; exit 0; fi\n shift\ndone\nexit 2\n')
            curl.chmod(0o700)
            python = tools / "python3"
            python.write_text("#!/bin/sh\nprintf parsed > '" + str(marker) + "'\nexit 99\n")
            python.chmod(0o700)
            env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ.get("PATH", ""))
            result = subprocess.run([str(Path(__file__).parents[1] / "fetch-verification-helper.sh"), str(root / "helper")], env=env, capture_output=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists())
            self.assertFalse((root / "helper" / "gh").exists())

    def test_existing_tool_directory_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            helper = root / "helper"
            helper.mkdir()
            retained = helper / "gh"
            retained.write_bytes(b"retained tool")
            result = subprocess.run([str(Path(__file__).parents[1] / "fetch-verification-helper.sh"), str(helper)], capture_output=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(retained.read_bytes(), b"retained tool")


if __name__ == "__main__":
    unittest.main()
