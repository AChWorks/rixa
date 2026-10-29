#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Owned Debian 13 laboratory only; never a product installer or host provisioner."""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys

SCHEMA = "rixa.debian-lab.v1"
SIGNER = "DF9B9C49EAA9298432589D76DA87E80D6294BE9B"
SSH_PORT = 22291
HTTPS_PORT = 22443


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def digest(path, algorithm="sha256"):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, algorithm).hexdigest()


def directory(raw, create=False):
    path = Path(raw)
    if not path.is_absolute() or path.resolve() != path or not re.fullmatch(r"/[A-Za-z0-9_./-]+", raw):
        raise ValueError("laboratory path must be absolute, canonical and contain no links")
    if create:
        path.mkdir(mode=0o700)
    info = path.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError("laboratory must be an existing private directory owned by this UID")
    return path


def regular(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
        raise ValueError("unexpected file ownership/type: " + path.name)
    return path


def download(url, target, limit):
    run(["curl", "--proto", "=https", "--tlsv1.2", "--fail", "--silent",
         "--show-error", "--location", "--max-time", "600", "--max-filesize",
         str(limit), url, "--output", str(target)])
    regular(target)
    if not 0 < target.stat().st_size <= limit:
        raise ValueError("download exceeds its laboratory bound")


def authenticate(lab, iso, keyring):
    regular(lab / "SHA512SUMS")
    regular(lab / "SHA512SUMS.sign")
    result = run(["gpgv", "--status-fd", "1", "--keyring", str(keyring),
                  str(lab / "SHA512SUMS.sign"), str(lab / "SHA512SUMS")],
                 text=True, capture_output=True)
    valid = [line.split()[2] for line in result.stdout.splitlines()
             if line.startswith("[GNUPG:] VALIDSIG ")]
    if valid != [SIGNER]:
        raise ValueError("checksum signer is not the reviewed Debian CD signer")
    matches = []
    for line in (lab / "SHA512SUMS").read_text().splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[1] == iso.name:
            matches.append(parts[0])
    if len(matches) != 1 or not re.fullmatch(r"[a-f0-9]{128}", matches[0]):
        raise ValueError("signed manifest has no unique exact image")
    if digest(regular(iso), "sha512") != matches[0]:
        raise ValueError("ISO digest does not match signed checksum")
    return matches[0]


def prepare(args):
    if not re.fullmatch(r"13\.[0-9]+\.[0-9]+", args.version):
        raise ValueError("an explicit Debian 13 point release is required")
    keyring = Path(args.keyring)
    if not keyring.is_absolute() or keyring.resolve() != keyring or not keyring.is_file():
        raise ValueError("supply an independently trusted regular Debian role keyring")
    lab = directory(args.lab, create=True)  # Refuse existing state; never overwrite it.
    iso = lab / ("debian-" + args.version + "-amd64-netinst.iso")
    base = "https://cdimage.debian.org/debian-cd/current/amd64/iso-cd/"
    for name in ("SHA512SUMS", "SHA512SUMS.sign"):
        download(base + name, lab / name, 1 << 20)
    download(base + iso.name, iso, 2 << 30)
    iso_digest = authenticate(lab, iso, keyring)
    for source, target in (("/install.amd/vmlinuz", "vmlinuz"),
                           ("/install.amd/initrd.gz", "initrd.gz")):
        with (lab / target).open("xb") as output:
            run(["isoinfo", "-R", "-i", str(iso), "-x", source], stdout=output)
        if not 0 < (lab / target).stat().st_size < 128 << 20:
            raise ValueError("installer kernel/initrd extraction failed")
    run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C",
         "rixa-disposable-lab", "-f", str(lab / "id_ed25519")])
    key = (lab / "id_ed25519.pub").read_text().strip()
    password_hash = run(["openssl", "passwd", "-6", "-stdin"],
                        input=secrets.token_urlsafe(40) + "\n", text=True,
                        capture_output=True).stdout.strip()
    preseed = """d-i debian-installer/locale string en_US.UTF-8
d-i keyboard-configuration/xkb-keymap select us
d-i netcfg/choose_interface select auto
d-i netcfg/get_hostname string rixa-lab
d-i netcfg/get_domain string invalid
d-i mirror/country string manual
d-i mirror/http/hostname string deb.debian.org
d-i mirror/http/directory string /debian
d-i mirror/http/proxy string
d-i mirror/suite string trixie
d-i passwd/root-login boolean true
d-i passwd/root-password-crypted password {password_hash}
d-i passwd/make-user boolean false
d-i clock-setup/utc boolean true
d-i time/zone string UTC
d-i clock-setup/ntp boolean false
d-i partman-auto/disk string /dev/vda
d-i partman-auto/method string regular
d-i partman-auto/choose_recipe select atomic
d-i partman-partitioning/confirm_write_new_label boolean true
d-i partman/choose_partition select finish
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true
d-i partman-lvm/device_remove_lvm boolean true
d-i partman-md/device_remove_md boolean true
d-i partman-basicfilesystems/no_swap boolean false
tasksel tasksel/first multiselect
d-i pkgsel/include string openssh-server ca-certificates curl python3 openssl
d-i pkgsel/upgrade select none
popularity-contest popularity-contest/participate boolean false
d-i apt-setup/non-free-firmware boolean false
d-i apt-setup/cdrom/set-first boolean false
d-i grub-installer/only_debian boolean true
d-i grub-installer/bootdev string /dev/vda
d-i preseed/late_command string echo rixa.disposable-lab.v1 > /target/etc/rixa-disposable-lab; chmod 600 /target/etc/rixa-disposable-lab; mkdir -p /target/root/.ssh; chmod 700 /target/root/.ssh; echo '{key}' > /target/root/.ssh/authorized_keys; chmod 600 /target/root/.ssh/authorized_keys; echo 'PermitRootLogin prohibit-password' > /target/etc/ssh/sshd_config.d/rixa-lab.conf; echo 'PasswordAuthentication no' >> /target/etc/ssh/sshd_config.d/rixa-lab.conf; in-target systemctl enable ssh
d-i finish-install/reboot_in_progress note
""".format(password_hash=password_hash, key=key)
    (lab / "preseed.cfg").write_text(preseed)
    archive = run(["cpio", "--quiet", "--format=newc", "--create"],
                  cwd=lab, input=b"preseed.cfg\n", capture_output=True).stdout
    import gzip
    with (lab / "initrd-preseed.gz").open("xb") as output:
        output.write((lab / "initrd.gz").read_bytes())
        output.write(gzip.compress(archive, mtime=0))
    run(["qemu-img", "create", "-f", "qcow2", str(lab / "guest.qcow2"), "8G"])
    receipt = {"schema": SCHEMA, "uid": os.getuid(), "iso": iso.name,
               "iso_sha512": iso_digest, "signer": SIGNER,
               "files": {name: digest(lab / name) for name in
                         ("vmlinuz", "initrd-preseed.gz", "id_ed25519.pub")}}
    (lab / "lab.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print("Prepared authenticated disposable lab; run install, then boot after clean installer exit.")


def load(lab):
    data = json.loads(regular(lab / "lab.json").read_text())
    if data.get("schema") != SCHEMA or data.get("uid") != os.getuid():
        raise ValueError("laboratory receipt ownership/schema mismatch")
    if data.get("signer") != SIGNER or not re.fullmatch(
            r"debian-13\.[0-9]+\.[0-9]+-amd64-netinst\.iso", data.get("iso", "")):
        raise ValueError("unexpected laboratory image")
    for name in ("vmlinuz", "initrd-preseed.gz", "id_ed25519.pub"):
        if digest(regular(lab / name)) != data.get("files", {}).get(name):
            raise ValueError("laboratory input changed: " + name)
    regular(lab / "guest.qcow2")
    regular(lab / "id_ed25519")
    return data


def boot(args):
    lab = directory(args.lab)
    data = load(lab)
    with (lab / "qemu.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if (lab / "qmp.sock").exists():
            raise ValueError("QMP socket exists; reconcile the prior guest before another launch")
        if args.action == "install":
            iso = regular(lab / data["iso"])
            if digest(iso, "sha512") != data["iso_sha512"]:
                raise ValueError("installer ISO changed")
        command = ["qemu-system-x86_64", "-name", "rixa-disposable-lab",
                   "-machine", "pc,accel=tcg", "-cpu", "max", "-m", "1024",
                   "-smp", "1", "-display", "none", "-serial",
                   "file:" + str(lab / (args.action + "-serial.log")),
                   "-monitor", "none", "-drive",
                   "file=" + str(lab / "guest.qcow2") + ",format=qcow2,if=virtio",
                   "-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:" +
                   str(SSH_PORT) + "-:22,hostfwd=tcp:127.0.0.1:" +
                   str(HTTPS_PORT) + "-:443", "-device", "virtio-net-pci,netdev=net0",
                   "-qmp", "unix:" + str(lab / "qmp.sock") + ",server=on,wait=off"]
        if args.action == "install":
            command += ["-no-reboot", "-cdrom", str(lab / data["iso"]),
                        "-kernel", str(lab / "vmlinuz"),
                        "-initrd", str(lab / "initrd-preseed.gz"), "-append",
                        "auto=true priority=critical console=ttyS0,115200n8 DEBIAN_FRONTEND=text net.ifnames=0"]
        try:
            run(command)
        finally:
            # QEMU has exited and this instance still holds its launch lock.
            sock = lab / "qmp.sock"
            if sock.exists() and stat.S_ISSOCK(sock.lstat().st_mode):
                sock.unlink()


def main():
    if os.geteuid() == 0:
        raise ValueError("run the host laboratory as an unprivileged dedicated user")
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    prep = sub.add_parser("prepare")
    prep.add_argument("--lab", required=True)
    prep.add_argument("--version", required=True)
    prep.add_argument("--keyring", required=True)
    for action in ("install", "boot"):
        sub.add_parser(action).add_argument("--lab", required=True)
    args = parser.parse_args()
    if args.action == "prepare":
        prepare(args)
    else:
        boot(args)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print("debian lab: " + str(error), file=sys.stderr)
        sys.exit(1)
