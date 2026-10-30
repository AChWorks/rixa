#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Prepare a fresh Debian cloud laboratory; never a product/bootstrap installer."""
import argparse
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shlex
import socket
import stat
import subprocess
import sys
import time
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

_spec = importlib.util.spec_from_file_location("debian_lab", Path(__file__).with_name("debian-guest.py"))
_common = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_common)
directory, regular, digest, small_text = _common.directory, _common.regular, _common.digest, _common.small_text

SCHEMA = "rixa.bootstrap-cloud-lab.v1"
TRUST = "debian-cloud-official-https"
ORIGIN = "https://cloud.debian.org"
POLICY = ORIGIN + "/images/cloud/HEADER.html"
SSH_PORT, HTTPS_PORT = 22691, 22643


def save(path, value, initial=False):
    temporary = path if initial else path.with_name(path.name + ".tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as output:
        json.dump(value, output, indent=2)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    if not initial:
        os.replace(temporary, path)
    parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(parent)
    finally:
        os.close(parent)


def validate_url(url):
    parsed = urlsplit(url)
    if (parsed.scheme != "https" or parsed.hostname not in ("cloud.debian.org", "cdimage.debian.org")
            or parsed.port not in (None, 443) or parsed.username or parsed.query or parsed.fragment
            or not parsed.path.startswith("/images/cloud/")
            or not re.fullmatch(r"/[A-Za-z0-9_./-]+", parsed.path)
            or any(part in (".", "..") for part in parsed.path.split("/"))):
        raise ValueError("only fixed official Debian cloud HTTPS origins are permitted")
    return url


class OfficialRedirects(HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        validate_url(new_url)
        return super().redirect_request(request, fp, code, message, headers, new_url)


def download(url, destination, maximum):
    validate_url(url)
    opener = build_opener(OfficialRedirects())
    with opener.open(Request(url, headers={"User-Agent": "Rixa-owned-cloud-lab/1"}), timeout=60) as response:
        validate_url(response.geturl())
        length = response.headers.get("Content-Length")
        if length and int(length) > maximum:
            raise ValueError("official download exceeds its bound")
        fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        total = 0
        deadline = time.monotonic() + 600
        with os.fdopen(fd, "wb") as output:
            while True:
                if time.monotonic() > deadline:
                    raise ValueError("official download exceeded its deadline")
                chunk = response.read(min(1 << 20, maximum + 1 - total))
                if not chunk:
                    break
                total += len(chunk)
                if total > maximum:
                    raise ValueError("official download exceeds its bound")
                output.write(chunk)
            output.flush()
            os.fsync(output.fileno())
        if total == 0:
            raise ValueError("empty official download")


def manifest_digest(text, filename):
    matches = []
    for line in text.splitlines():
        fields = line.split()
        if len(fields) != 2 or not re.fullmatch("[a-f0-9]{128}", fields[0]):
            raise ValueError("unsupported checksum manifest entry")
        if fields[1] == filename:
            matches.append(fields[0])
    if len(matches) != 1:
        raise ValueError("checksum manifest lacks one unambiguous exact file")
    return matches[0]


def metadata_info(data, build):
    value = json.loads(data)
    records = [item for item in value["items"] if item.get("kind") == "Build"]
    if len(records) != 1:
        raise ValueError("official metadata lacks one exact build")
    info = records[0]["data"]["info"]
    expected = {"arch": "amd64", "release": "trixie", "release_id": "13",
                "type": "official", "vendor": "genericcloud", "version": build}
    if any(info.get(key) != wanted for key, wanted in expected.items()):
        raise ValueError("official metadata does not identify the selected Debian13 build")
    packages = records[0]["data"]["packages"]
    if not 1 <= len(packages) <= 4096:
        raise ValueError("unsupported official package inventory")
    if any(item["name"].split(":")[0].startswith(("postgresql", "rixa")) for item in packages):
        raise ValueError("official build already contains product/database packages")
    return info, packages


def seed_data(public_key, instance):
    if (not re.fullmatch(r"ssh-ed25519 [A-Za-z0-9+/=]+ [a-z0-9-]+", public_key)
            or not re.fullmatch("[a-z][a-z0-9-]{0,62}", instance)):
        raise ValueError("exact fixture public key and ASCII instance identifier required")
    # JSON strings are valid YAML scalars. No private key, password, token,
    # database URL, package install or product executable enters the seed.
    user = """#cloud-config
hostname: rixa-bootstrap-lab
manage_etc_hosts: true
disable_root: false
ssh_pwauth: false
package_update: false
package_upgrade: false
users:
  - name: root
    lock_passwd: true
    ssh_authorized_keys:
      - PUBLIC_KEY
write_files:
  - path: /etc/rixa-disposable-lab
    owner: root:root
    permissions: '0600'
    content: |
      rixa.disposable-lab.v1
  - path: /etc/rixa-bootstrap-lab
    owner: root:root
    permissions: '0600'
    content: |
      rixa.bootstrap-lab.v1
  - path: /etc/ssh/sshd_config.d/rixa-lab.conf
    owner: root:root
    permissions: '0644'
    content: |
      PasswordAuthentication no
      KbdInteractiveAuthentication no
      PermitRootLogin prohibit-password
runcmd:
  - [systemctl, reload, ssh.service]
""".replace("PUBLIC_KEY", json.dumps(public_key))
    meta = "instance-id: " + instance + "\nlocal-hostname: rixa-bootstrap-lab\n"
    return user, meta


def prepare(args):
    if (args.trust_method != TRUST or not re.fullmatch("[0-9]{8}-[0-9]+", args.build)
            or not re.fullmatch("[a-f0-9]{128}", args.image_sha512)
            or not re.fullmatch("[a-f0-9]{64}", args.manifest_sha256)):
        raise ValueError("explicit official HTTPS trust, immutable build and selected digests required")
    lab = directory(args.lab, create=True)
    attempt = {"schema": SCHEMA, "uid": os.getuid(), "state": "prepare-attempted",
               "build": args.build, "trust_method": TRUST, "policy_url": POLICY,
               "scope_record": args.scope_record,
               "openpgp_signature": "not published for current official cloud images; not claimed"}
    save(lab / "attempt.json", attempt, initial=True)
    def stage(name):
        attempt["state"] = name + "-attempted"
        save(lab / "attempt.json", attempt)
    prefix = ORIGIN + "/images/cloud/trixie/" + args.build + "/"
    name = "debian-13-genericcloud-amd64-" + args.build
    stage("official-policy-and-manifest")
    download(POLICY, lab / "official-policy.html", 131072)
    download(prefix + "SHA512SUMS", lab / "SHA512SUMS", 65536)
    if digest(lab / "SHA512SUMS") != args.manifest_sha256:
        raise ValueError("versioned manifest differs from independently selected bytes")
    manifest = small_text(lab / "SHA512SUMS", 65536)
    selected = manifest_digest(manifest, name + ".qcow2")
    if selected != args.image_sha512:
        raise ValueError("selected image digest differs from official versioned manifest")
    stage("official-metadata")
    download(prefix + name + ".json", lab / "image-metadata.json", 65536)
    if digest(lab / "image-metadata.json", "sha512") != manifest_digest(manifest, name + ".json"):
        raise ValueError("official metadata checksum failed before JSON parsing")
    info, packages = metadata_info(small_text(lab / "image-metadata.json", 65536), args.build)
    stage("official-image-download")
    image = lab / (name + ".qcow2")
    download(prefix + image.name, image, 3 << 30)
    if digest(image, "sha512") != selected:
        raise ValueError("official image checksum failed; image will not be parsed or booted")
    image.chmod(0o400)
    # Only authenticated bytes are passed to a qcow2 parser.
    inspected = subprocess.run(["qemu-img", "info", "--output=json", str(image)],
                               check=True, capture_output=True, text=True)
    qinfo = json.loads(inspected.stdout)
    if qinfo.get("format") != "qcow2" or qinfo.get("backing-filename") or not 0 < qinfo["virtual-size"] <= 8 << 30:
        raise ValueError("official image format/backing/size is unsupported")
    stage("fixture-client-key")
    subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", args.instance,
                    "-f", str(lab / "id_ed25519")], check=True)
    public = small_text(lab / "id_ed25519.pub", 4096).strip()
    user, meta = seed_data(public, args.instance)
    (lab / "user-data").write_text(user)
    (lab / "meta-data").write_text(meta)
    stage("standard-cidata-seed")
    subprocess.run(["cloud-localds", str(lab / "seed.iso"), str(lab / "user-data"),
                    str(lab / "meta-data")], check=True, capture_output=True)
    stage("new-overlay")
    subprocess.run(["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b",
                    str(image), str(lab / "guest.qcow2")], check=True, capture_output=True)
    subprocess.run(["qemu-img", "resize", str(lab / "guest.qcow2"), "8G"],
                   check=True, capture_output=True)
    (lab / "guest.qcow2").chmod(0o600)
    receipt = dict(attempt, state="prepared", image={"filename": image.name, "url": prefix + image.name,
                   "sha512": selected, "sha256": digest(image), "virtual_size": qinfo["virtual-size"]},
                   manifest_sha256=args.manifest_sha256, metadata=info,
                   official_package_count=len(packages), instance_id=args.instance,
                   files={name: digest(lab / name) for name in
                          ("image-metadata.json", "seed.iso", "user-data", "meta-data", "id_ed25519.pub")},
                   fixture_seed="standard CIDATA; public fixture key and OS/SSH/markers only",
                   ports={"ssh": SSH_PORT, "https": HTTPS_PORT, "seed_service": "unused"},
                   product_or_postgresql_injection=False)
    save(lab / "cloud-lab.json", receipt, initial=True)
    attempt["state"] = "prepared"
    save(lab / "attempt.json", attempt)
    print(json.dumps({"status": "prepared", "build": args.build, "image_sha512": selected,
                      "trust_method": TRUST, "detached_signature_claimed": False}))


def load(raw):
    lab = directory(raw)
    data = json.loads(small_text(lab / "cloud-lab.json", 65536))
    if data.get("schema") != SCHEMA or data.get("uid") != os.getuid() or data.get("trust_method") != TRUST:
        raise ValueError("cloud laboratory identity/trust differs")
    required = {"image-metadata.json", "seed.iso", "user-data", "meta-data", "id_ed25519.pub"}
    if set(data.get("files", {})) != required:
        raise ValueError("cloud laboratory fixture inventory is incomplete")
    expected_name = "debian-13-genericcloud-amd64-" + str(data.get("build", "")) + ".qcow2"
    if not re.fullmatch(r"debian-13-genericcloud-amd64-[0-9]{8}-[0-9]+[.]qcow2", expected_name) or data.get("image", {}).get("filename") != expected_name:
        raise ValueError("unexpected cloud image name")
    for name, wanted in data["files"].items():
        if name not in ("image-metadata.json", "seed.iso", "user-data", "meta-data", "id_ed25519.pub"):
            raise ValueError("unexpected cloud laboratory input name")
        if digest(regular(lab / name)) != wanted:
            raise ValueError("cloud laboratory fixture changed")
    image = regular(lab / data["image"]["filename"])
    if image.stat().st_mode & 0o222 or digest(image, "sha512") != data["image"]["sha512"]:
        raise ValueError("official immutable backing image changed")
    regular(lab / "guest.qcow2")
    key = regular(lab / "id_ed25519")
    if key.stat().st_mode & 0o077:
        raise ValueError("fixture private client key is not private")
    return lab, data


def owned_qemu():
    found = []
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            if entry.stat().st_uid == os.getuid() and (entry / "exe").resolve().name == "qemu-system-x86_64":
                found.append(int(entry.name))
        except (FileNotFoundError, PermissionError):
            pass
    return found


def boot(args):
    lab, _ = load(args.lab)
    if not re.fullmatch("[a-z][a-z0-9-]{0,62}", args.attempt):
        raise ValueError("stable ASCII launch attempt identifier required")
    with _common.launch_lock(lab) as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if owned_qemu() or any((lab / name).exists() for name in ("qmp.sock", "console.sock")):
            raise ValueError("reconcile existing guest/process/sockets before another launch")
        for port in (SSH_PORT, HTTPS_PORT):
            probe = socket.socket()
            try:
                probe.bind(("127.0.0.1", port))
            finally:
                probe.close()
        serial = lab / (args.attempt + "-serial.log")
        with serial.open("xb"):
            pass
        receipt = lab / (args.attempt + ".launch.json")
        state = {"schema": SCHEMA, "state": "qemu-start-attempted", "attempt": args.attempt,
                 "uid": os.getuid(), "serial_log": str(serial), "image": str(lab / "guest.qcow2")}
        save(receipt, state, initial=True)
        command = ["qemu-system-x86_64", "-name", "rixa-bootstrap-clean-lab",
                   "-machine", "pc,accel=tcg", "-cpu", "max", "-m", "1024", "-smp", "1",
                   "-display", "none", "-chardev", "socket,id=labserial,path=" + str(lab / "console.sock") +
                   ",server=on,wait=off,logfile=" + str(serial) + ",logappend=on",
                   "-serial", "chardev:labserial", "-monitor", "none",
                   "-drive", "file=" + str(lab / "guest.qcow2") + ",format=qcow2,if=virtio,index=0",
                   "-drive", "file=" + str(lab / "seed.iso") + ",format=raw,if=virtio,index=1,readonly=on",
                   "-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:" + str(SSH_PORT) +
                   "-:22,hostfwd=tcp:127.0.0.1:" + str(HTTPS_PORT) + "-:443",
                   "-device", "virtio-net-pci,netdev=net0",
                   "-qmp", "unix:" + str(lab / "qmp.sock") + ",server=on,wait=off"]
        process = None
        print("serial_log=" + str(serial), flush=True)
        try:
            process = subprocess.Popen(command)
            state.update(state="qemu-running-acknowledged", pid=process.pid)
            save(receipt, state)
            code = process.wait()
            state.update(state="qemu-exited", exit_code=code)
            save(receipt, state)
            if code:
                raise ValueError("owned QEMU exited nonzero; inspect retained launch evidence")
        finally:
            if process is not None and process.poll() is not None:
                for name in ("qmp.sock", "console.sock"):
                    path = lab / name
                    if path.exists() and stat.S_ISSOCK(path.lstat().st_mode) and path.lstat().st_uid == os.getuid():
                        path.unlink()


def main():
    if os.geteuid() == 0:
        raise ValueError("host fixture must run as unprivileged owner")
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    actions = parser.add_subparsers(dest="action", required=True)
    prep = actions.add_parser("prepare")
    prep.add_argument("--lab", required=True)
    prep.add_argument("--build", required=True)
    prep.add_argument("--image-sha512", required=True)
    prep.add_argument("--manifest-sha256", required=True)
    prep.add_argument("--instance", required=True)
    prep.add_argument("--trust-method", choices=(TRUST,), required=True)
    prep.add_argument("--scope-record", default="")
    start = actions.add_parser("boot")
    start.add_argument("--lab", required=True)
    start.add_argument("--attempt", required=True)
    args = parser.parse_args()
    prepare(args) if args.action == "prepare" else boot(args)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print("bootstrap cloud lab: " + str(error), file=sys.stderr)
        sys.exit(1)
