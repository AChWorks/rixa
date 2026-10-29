#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Publish one verified development package; never overwrite a prior release.

Only the tag-triggered GitHub release job invokes this script. Artifact signing
and local cryptographic verification happen in the workflow before publish.
"""

import hashlib
import io
import json
import os
from pathlib import Path
import re
import sys
import stat
import subprocess
import tempfile
import tarfile
from urllib import error, parse, request

REPOSITORY = "AChWorks/rixa"
API_ROOT = "https://api.github.com/repos/" + REPOSITORY
UPLOAD_ROOT = "https://uploads.github.com/repos/" + REPOSITORY
MAX_FILE = 128 << 20
MAX_RESPONSE = 2 << 20
VERSION = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+(?:[.-][0-9A-Za-z][0-9A-Za-z.-]*)?\Z")
SHA = re.compile(r"[0-9a-f]{40}\Z")


class PublishError(Exception):
    pass


class APIError(PublishError):
    pass


class NoRedirect(request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class GitHub:
    def __init__(self, token):
        if not token:
            raise PublishError("GH_TOKEN is required")
        self.token = token
        self.opener = request.build_opener(NoRedirect)

    def call(self, method, path, body=None, content_type="application/json", upload=False):
        root = UPLOAD_ROOT if upload else API_ROOT
        if not path.startswith("/") or ".." in path:
            raise PublishError("invalid release API path")
        data = body
        if body is not None and content_type == "application/json":
            data = json.dumps(body).encode()
        req = request.Request(root + path, data=data, method=method, headers={
            "Authorization": "Bearer " + self.token,
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "Content-Type": content_type,
            "User-Agent": "rixa-release-publisher",
        })
        try:
            with self.opener.open(req, timeout=30) as response:
                raw = response.read(MAX_RESPONSE + 1)
        except error.HTTPError as exc:
            # Preserve useful provider diagnostics without emitting headers/token.
            try:
                value = json.loads(exc.read(4096))
                detail = json.dumps({"message": value.get("message"), "errors": value.get("errors")}, ensure_ascii=True)
            except (ValueError, AttributeError):
                detail = "request rejected"
            detail = str(detail).replace(self.token, "[redacted]")[:512]
            detail = "".join(c if c.isprintable() else " " for c in detail)
            request_id = str(exc.headers.get("X-GitHub-Request-Id", "unavailable"))
            if not re.fullmatch(r"[A-Za-z0-9:_-]{1,128}", request_id):
                request_id = "unavailable"
            diagnostic = f"GitHub HTTP {exc.code} (request {request_id}): {detail}".replace(self.token, "[redacted]")
            raise APIError(diagnostic) from None
        except (error.URLError, TimeoutError, OSError):
            raise APIError("GitHub acknowledgement unavailable") from None
        if len(raw) > MAX_RESPONSE:
            raise APIError("GitHub response exceeds the release metadata bound")
        try:
            return json.loads(raw)
        except ValueError:
            raise APIError("GitHub returned invalid release metadata") from None


class Package:
    def __init__(self, archive, env):
        if env.get("GITHUB_REPOSITORY") != REPOSITORY or env.get("GITHUB_REPOSITORY_ID") != "1403734531" or env.get("GITHUB_REPOSITORY_OWNER_ID") != "80599060" or env.get("GITHUB_EVENT_NAME") != "push" or env.get("GITHUB_REF_TYPE") != "tag":
            raise PublishError("publication requires this repository's tag-push workflow")
        if env.get("GITHUB_SERVER_URL") != "https://github.com" or env.get("GITHUB_API_URL") != "https://api.github.com":
            raise PublishError("publication requires the supported GitHub.com publisher")
        self.version = env.get("GITHUB_REF_NAME", "")
        self.source = env.get("GITHUB_SHA", "")
        self.run = env.get("GITHUB_RUN_ID", "")
        self.attempt = env.get("GITHUB_RUN_ATTEMPT", "")
        # A fresh process after an uncertain write has no non-application fence.
        # Workflow reruns are not release reconciliation. Settle the original
        # run/draft first; deleting/recreating its tag is not a retry mechanism.
        if self.attempt != "1":
            raise PublishError("publication is first-attempt only; inspect and settle the original run/draft before recovery; do not delete/recreate the same tag as a retry")
        if not VERSION.fullmatch(self.version) or not SHA.fullmatch(self.source) or not re.fullmatch(r"[1-9][0-9]{0,31}", self.run):
            raise PublishError("invalid release tag, source or workflow run identity")
        if env.get("GITHUB_REF") != "refs/tags/" + self.version:
            raise PublishError("release source ref disagrees with tag")
        if env.get("GITHUB_WORKFLOW_REF") != REPOSITORY + "/.github/workflows/release.yml@refs/tags/" + self.version or env.get("GITHUB_WORKFLOW_SHA") != self.source:
            raise PublishError("publication requires the exact release workflow source")
        self.archive = Path(archive)
        self.base = "rixa-" + self.version + "-linux-amd64"
        if self.archive.name != self.base + ".tar.gz":
            raise PublishError("archive name disagrees with release tag")
        self.files = {}
        self.add(self.archive)
        self.add(Path(str(self.archive) + ".sha256"), 4096)
        expected = self.files[self.archive.name][1] + "  " + self.archive.name + "\n"
        if self.files[self.archive.name + ".sha256"][0] != expected.encode():
            raise PublishError("release checksum does not match the exact archive")
        try:
            with tarfile.open(fileobj=io.BytesIO(self.files[self.archive.name][0]), mode="r:gz") as tar:
                member = tar.getmember(self.base + "/BUILDINFO.json")
                if not member.isfile() or member.size > 65536:
                    raise PublishError("invalid packaged build identity")
                info = json.load(tar.extractfile(member))
        except (OSError, KeyError, ValueError, tarfile.TarError):
            raise PublishError("packaged build identity is unavailable") from None
        if not isinstance(info, dict) or info.get("rixa") != self.version or info.get("source_commit") != self.source or info.get("target") != "linux-amd64" or not isinstance(info.get("achrix_components"), dict) or not info["achrix_components"]:
            raise PublishError("packaged build identity disagrees with release source")
        self.core = info.get("achrix", "")
        if not isinstance(self.core, str) or not VERSION.fullmatch(self.core) or info["achrix_components"].get("github.com/AChWorks/achrix") != self.core:
            raise PublishError("packaged Foundation identity is inconsistent")
        self.notes = (f"Rixa {self.version} is a development prerelease.\n\n"
            "- Packaged target: Linux amd64\n"
            f"- Foundation Core: AChrix {self.core}; complete selected component identities are recorded in BUILDINFO.json\n"
            f"- Source commit: {self.source}\n"
            "- Runtime boundary: current validated development profile; no production hosting, HA or RPO/RTO support is claimed.\n\n"
            "Verify the SHA-256 checksum and adjacent Sigstore provenance bundle before extraction; follow INSTALL.md inside the package.\n\n"
            f"Publisher run: https://github.com/{REPOSITORY}/actions/runs/{self.run}\n"
            f"Publisher attempt: {self.attempt}\n")

    def add(self, path, maximum=MAX_FILE):
        try:
            fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
            with os.fdopen(fd, "rb") as stream:
                info = os.fstat(stream.fileno())
                if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= maximum:
                    raise PublishError("release file is unsafe or oversized")
                data = stream.read(maximum + 1)
        except OSError as exc:
            raise PublishError(f"release file unavailable (errno {exc.errno})") from None
        if not 0 < len(data) <= maximum:
            raise PublishError("release file changed or exceeds the bound")
        self.files[path.name] = (data, hashlib.sha256(data).hexdigest())

    def bundle(self):
        path = Path(str(self.archive) + ".sigstore.json")
        self.add(path, 16 << 20)
        try:
            bundle = json.loads(self.files[path.name][0])
        except ValueError:
            raise PublishError("attestation bundle is not JSON") from None
        if not isinstance(bundle, dict) or not bundle.get("verificationMaterial") or not bundle.get("dsseEnvelope"):
            raise PublishError("attestation bundle is incomplete")


def verify_bundle(package):
    # This verifier is built from the checked protected source, never extracted
    # from the candidate. It owns the single Go certificate/provenance policy.
    verifier = Path(os.environ.get("RIXA_PROVENANCE_VERIFIER", ""))
    try:
        info = verifier.lstat()
    except OSError:
        raise PublishError("trusted source-built provenance verifier is unavailable") from None
    if not verifier.is_absolute() or not stat.S_ISREG(info.st_mode) or not info.st_mode & stat.S_IXUSR or info.st_mode & 0o022 or info.st_uid != os.geteuid():
        raise PublishError("trusted source-built provenance verifier is unsafe")
    with tempfile.TemporaryDirectory(prefix="rixa-publish-") as temporary:
        root = Path(temporary)
        archive = root / package.archive.name
        bundle = root / (package.archive.name + ".sigstore.json")
        archive.write_bytes(package.files[archive.name][0])
        bundle.write_bytes(package.files[bundle.name][0])
        env = dict(os.environ)
        for key in ("GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"):
            env.pop(key, None)
        digest = package.files[archive.name][1]
        try:
            result = subprocess.run([str(verifier), "artifact", "verify",
                "--archive", str(archive), "--bundle", str(bundle),
                "--release", package.version, "--source-commit", package.source,
                "--sha256", digest], env=env, timeout=120,
                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        except (OSError, subprocess.TimeoutExpired):
            raise PublishError("trusted provenance verifier unavailable or timed out; no release created") from None
        if result.returncode != 0:
            detail = result.stderr.decode(errors="replace")[-4096:]
            detail = "".join(c if c.isprintable() or c == "\n" else " " for c in detail)
            raise PublishError("attestation verification failed; no release created: " + detail)
        if len(result.stdout) > 65536:
            raise PublishError("provenance verifier receipt exceeds the bound")
        try:
            receipt = json.loads(result.stdout)
        except ValueError:
            raise PublishError("provenance verifier receipt is invalid") from None
        expected = {"schema": "rixa.artifact-verification.v1", "status": "verified", "code": "ok",
            "sha256": digest, "release": package.version, "source_ref": "refs/tags/" + package.version,
            "source_commit": package.source, "repository": REPOSITORY,
            "workflow": REPOSITORY + "/.github/workflows/release.yml", "runner": "github-hosted"}
        if not isinstance(receipt, dict) or any(receipt.get(key) != value for key, value in expected.items()):
            raise PublishError("provenance verifier receipt does not authorize this exact package")
        run = receipt.get("run_url", "")
        if not isinstance(run, str) or not re.fullmatch(r"https://github\.com/AChWorks/rixa/actions/runs/[0-9]+(?:/attempts/[0-9]+)?", run):
            raise PublishError("provenance verifier receipt lacks an authenticated publisher run")


class Publisher:
    def __init__(self, github, package):
        self.github, self.package = github, package

    def release_for_tag(self):
        matches = []
        for page in range(1, 101):
            releases = self.github.call("GET", f"/releases?per_page=100&page={page}")
            if not isinstance(releases, list):
                raise APIError("GitHub release inventory is invalid")
            if any(not isinstance(r, dict) or not isinstance(r.get("tag_name"), str) for r in releases):
                raise APIError("release inventory is incomplete or malformed; absence unproven")
            matches.extend(r for r in releases if r["tag_name"] == self.package.version)
            if len(releases) < 100:
                if len(matches) > 1:
                    raise PublishError("multiple releases use this tag; inspect before continuing")
                return matches[0] if matches else None
        raise PublishError("release inventory bound exceeded; absence is unproven")

    def tag_matches(self):
        tag = parse.quote(self.package.version, safe="")
        ref = self.github.call("GET", "/git/ref/tags/" + tag)
        obj = ref.get("object", {}) if isinstance(ref, dict) else {}
        for _ in range(8):
            if not isinstance(obj, dict):
                break
            if obj.get("type") == "commit":
                if obj.get("sha") == self.package.source:
                    return
                break
            if obj.get("type") != "tag" or not SHA.fullmatch(str(obj.get("sha", ""))):
                break
            ref = self.github.call("GET", "/git/tags/" + obj["sha"])
            obj = ref.get("object", {}) if isinstance(ref, dict) else {}
        raise PublishError("current remote tag does not resolve to the checked release source")

    def check(self):
        self.tag_matches()
        if self.release_for_tag() is not None:
            raise PublishError("release already exists; inspect it instead of overwriting or republishing")

    def observe(self, release_id):
        try:
            release = self.github.call("GET", f"/releases/{release_id}")
        except APIError as exc:
            raise PublishError(f"release {release_id} observation unavailable: {exc}; inspect retained state before retry") from None
        p = self.package
        if not isinstance(release, dict) or release.get("id") != release_id or release.get("tag_name") != p.version or release.get("body") != p.notes or release.get("name") != "Rixa " + p.version or release.get("prerelease") is not True or not isinstance(release.get("draft"), bool):
            raise PublishError("release identity changed; inspect retained state")
        return release

    def assets_match(self, release, complete):
        assets = release.get("assets")
        if not isinstance(assets, list):
            raise PublishError("release asset inventory is unavailable")
        found = set()
        for asset in assets:
            if not isinstance(asset, dict):
                raise PublishError("release asset inventory is malformed")
            name = asset.get("name")
            if name not in self.package.files or name in found:
                raise PublishError("release contains unexpected or duplicate assets; preserving them")
            data, digest = self.package.files[name]
            if asset.get("state") != "uploaded" or asset.get("size") != len(data) or asset.get("digest") != "sha256:" + digest:
                raise PublishError("release asset identity is unverified; preserving it")
            found.add(name)
        if complete and found != set(self.package.files):
            raise PublishError("release asset set is incomplete; publication blocked")
        return found

    def publish(self):
        p = self.package
        p.bundle()
        verify_bundle(p)
        self.check()  # Fresh absence/tag checks immediately before the first effect.
        payload = {"tag_name": p.version, "target_commitish": p.source,
            "name": "Rixa " + p.version, "body": p.notes,
            "draft": True, "prerelease": True, "make_latest": "false"}
        try:
            created = self.github.call("POST", "/releases", payload)
        except APIError as exc:
            # A timed-out create can still arrive later. Never issue another POST.
            try:
                observed = self.release_for_tag()
            except PublishError as inspect_error:
                raise PublishError(f"creation acknowledgement uncertain: {exc}; observation also failed: {inspect_error}; inspect this tag before retry") from None
            suffix = f"; observed release id {observed.get('id')}" if observed else "; no release observed yet"
            raise PublishError(str(exc) + suffix + "; creation acknowledgement uncertain, inspect before retry") from None
        release_id = created.get("id") if isinstance(created, dict) else None
        if type(release_id) is not int or release_id <= 0:
            raise PublishError("draft creation acknowledgement invalid; inspect releases before retry")
        release = self.observe(release_id)
        if release["draft"] is not True or self.assets_match(release, False):
            raise PublishError("new draft has unexpected state; preserving it")
        reconciled = False
        for name, (data, _) in p.files.items():
            upload_error = None
            try:
                self.github.call("POST", f"/releases/{release_id}/assets?name=" + parse.quote(name, safe=""), data, "application/octet-stream", upload=True)
            except APIError as exc:
                upload_error = exc
            release = self.observe(release_id)
            if not release["draft"]:
                raise PublishError("release left draft state during asset upload")
            found = self.assets_match(release, False)
            if name not in found:
                reason = str(upload_error) if upload_error else "asset acknowledgement did not match observation"
                raise PublishError(reason + "; upload outcome uncertain, inspect draft before retry")
            if upload_error is not None:
                reconciled = True
                print(json.dumps({"event": "upload_acknowledgement_reconciled", "release_id": release_id,
                    "run_id": p.run, "run_attempt": p.attempt, "asset": name, "diagnostic": str(upload_error)}), file=sys.stderr)
        self.tag_matches()
        release = self.observe(release_id)
        self.assets_match(release, True)
        if not release["draft"]:
            raise PublishError("release left draft state before final publication")
        matching = self.release_for_tag()
        if matching is None or matching.get("id") != release_id:
            raise PublishError("release inventory changed before publication")
        publish_error = None
        try:
            self.github.call("PATCH", f"/releases/{release_id}", {"draft": False, "prerelease": True, "make_latest": "false"})
        except APIError as exc:
            publish_error = exc
        final = self.observe(release_id)
        self.assets_match(final, True)
        if final["draft"] or not final.get("published_at"):
            reason = str(publish_error) if publish_error else "publication is not observed"
            raise PublishError(reason + "; inspect release before retrying publication")
        if publish_error is not None:
            reconciled = True
            print(json.dumps({"event": "publication_acknowledgement_reconciled", "release_id": release_id,
                "run_id": p.run, "run_attempt": p.attempt, "diagnostic": str(publish_error)}), file=sys.stderr)
        return release_id, reconciled


def main(argv):
    if len(argv) != 3 or argv[1] not in ("check", "publish"):
        raise PublishError("usage: publish-release.py <check|publish> <archive>")
    package = Package(argv[2], os.environ)
    publisher = Publisher(GitHub(os.environ.get("GH_TOKEN", "")), package)
    if argv[1] == "check":
        publisher.check()
        print("Release source/package identity and absence verified; no release mutation.")
    else:
        release_id, reconciled = publisher.publish()
        print(json.dumps({"status": "published", "release_id": release_id, "reconciled": reconciled,
            "tag": package.version, "source_commit": package.source, "archive_sha256": package.files[package.archive.name][1]}))


if __name__ == "__main__":
    try:
        main(sys.argv)
    except PublishError as exc:
        run = os.environ.get("GITHUB_RUN_ID", "")
        attempt = os.environ.get("GITHUB_RUN_ATTEMPT", "")
        run = run if re.fullmatch(r"[0-9]{1,32}", run) else "unavailable"
        attempt = attempt if re.fullmatch(r"[0-9]{1,8}", attempt) else "unavailable"
        print(f"rixa release (run {run}, attempt {attempt}): {exc}", file=sys.stderr)
        sys.exit(1)
