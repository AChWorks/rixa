#!/bin/sh
# SPDX-License-Identifier: MPL-2.0
# bootstrap-v1: the caller authenticates this file before invoking it.
set -eu
unset ENV BASH_ENV LD_PRELOAD LD_LIBRARY_PATH
exec /usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin TERM="${TERM:-dumb}" NO_COLOR="${NO_COLOR:-}" /usr/bin/python3 -I - "$@" <<'RIXA_BOOTSTRAP_PYTHON'
import fcntl
import hashlib
import io
import json
import os
import signal
from pathlib import Path
import stat
import subprocess
import sys
import tarfile

GENERATION = "bootstrap-v1"
STARTER_RELEASE = "v0.6.0"
STARTER_SOURCE = "50c0c3b4d0ac1bd73562d36a5b96b9a1f7a0d58d"
STARTER_ARCHIVE = "544555483b4a7d7c9dea09d25c1042e8bd4670d1a757c524b829ce894519828f"
STARTER_BINARY = "504ad8a99bb002d3bfb897a8d48a1e98a087006ec8e7faec6cdd27b7ad3f5655"
HELPER_VERSION = "2.103.3"
HELPER_ARCHIVE = "4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d"
HELPER_BINARY = "b768184570b12536be4ac1528893f6340d1c6a7b40b56ad45bca9cd0e87ad34d"
CACHE_ROOT = Path("/opt/rixa-entry")
MAXIMUM = 64 << 20
BASE_ENV = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "HOME": "/root", "LANG": "C.UTF-8"}


class EntryError(Exception):
    pass


class EntryInterrupted(Exception):
    def __init__(self, number):
        self.number = number


def interrupt(number, frame):
    raise EntryInterrupted(number)


def directory(path, owner, create=False):
    if create:
        try:
            path.mkdir(mode=0o700)
        except FileExistsError:
            pass
    info = path.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != owner
            or info.st_mode & 0o022):
        raise EntryError("Unsafe directory; inspect ownership and permissions: " + str(path))
    return info


def private_directory(path, owner, create=False):
    info = directory(path, owner, create)
    if stat.S_IMODE(info.st_mode) != 0o700:
        raise EntryError("A private 0700 directory is required: " + str(path))


def checked_bytes(path, expected, mode, owner, maximum=MAXIMUM):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != owner or info.st_nlink != 1
                or stat.S_IMODE(info.st_mode) != mode or not 0 < info.st_size <= maximum):
            raise EntryError("Unsafe pinned file: " + str(path))
        data = stream.read(maximum + 1)
        after = os.fstat(stream.fileno())
    named = path.lstat()
    if (len(data) != info.st_size or info.st_dev != named.st_dev
            or info.st_ino != named.st_ino or info.st_mtime_ns != after.st_mtime_ns
            or hashlib.sha256(data).hexdigest() != expected):
        raise EntryError("Pinned file identity differs; do not replace it: " + str(path))
    return data


def download(url, path, expected, owner):
    # curl's own total deadline covers slow/trickled response bodies; Python
    # additionally bounds the subprocess and waits for it before reporting.
    with path.open("xb") as target:
        os.chmod(path, 0o600)
        subprocess.run([
            "/usr/bin/curl", "--disable", "--proto", "=https", "--proto-redir", "=https",
            "--tlsv1.2", "--fail", "--silent", "--show-error", "--location",
            "--max-redirs", "5", "--connect-timeout", "15", "--max-time", "180",
            "--max-filesize", str(MAXIMUM), "--output", "-", url,
        ], stdin=subprocess.DEVNULL, stdout=target, env=BASE_ENV, timeout=195, check=True)
        target.flush()
        os.fsync(target.fileno())
    os.chmod(path, 0o400)
    return checked_bytes(path, expected, 0o400, owner)


def member(archive, name, maximum):
    # This only extracts the independently pinned starter/helper, after their
    # whole-archive hashes pass. S owns all candidate provenance/package policy.
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as source:
        matches = [entry for entry in source if entry.name == name]
        if len(matches) != 1 or not matches[0].isfile() or not 0 < matches[0].size <= maximum:
            raise EntryError("Pinned archive has an unexpected exact member: " + name)
        with source.extractfile(matches[0]) as stream:
            data = stream.read(maximum + 1)
        if len(data) != matches[0].size:
            raise EntryError("Pinned archive member size differs: " + name)
        return data


def new_file(path, data, mode):
    with path.open("xb") as target:
        os.chmod(path, 0o600)
        target.write(data)
        target.flush()
        os.fsync(target.fileno())
    os.chmod(path, mode)


def prepare(cache, owner):
    expected = {
        "starter.tar.gz": (STARTER_ARCHIVE, 0o400),
        "helper.tar.gz": (HELPER_ARCHIVE, 0o400),
        "rixa": (STARTER_BINARY, 0o500),
        "gh": (HELPER_BINARY, 0o500),
    }
    if cache.exists() or cache.is_symlink():
        private_directory(cache, owner)
        if set(p.name for p in cache.iterdir()) != set(expected):
            raise EntryError("Incomplete or unknown entry cache; inspect it without deleting retained consumers: " + str(cache))
    else:
        cache.mkdir(mode=0o700)
        starter = download(
            "https://github.com/AChWorks/rixa/releases/download/" + STARTER_RELEASE
            + "/rixa-" + STARTER_RELEASE + "-linux-amd64.tar.gz",
            cache / "starter.tar.gz", STARTER_ARCHIVE, owner)
        helper = download(
            "https://github.com/cli/cli/releases/download/v" + HELPER_VERSION
            + "/gh_" + HELPER_VERSION + "_linux_amd64.tar.gz",
            cache / "helper.tar.gz", HELPER_ARCHIVE, owner)
        prefix = "rixa-" + STARTER_RELEASE + "-linux-amd64/"
        metadata = json.loads(member(starter, prefix + "BUILDINFO.json", 65536))
        if (metadata.get("rixa") != STARTER_RELEASE
                or metadata.get("source_commit") != STARTER_SOURCE
                or metadata.get("target") != "linux-amd64"):
            raise EntryError("Pinned starter source/release/platform differs")
        new_file(cache / "rixa", member(starter, prefix + "rixa", MAXIMUM), 0o500)
        new_file(cache / "gh", member(helper, "gh_" + HELPER_VERSION + "_linux_amd64/bin/gh", MAXIMUM), 0o500)
        fd = os.open(cache, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    for name, (digest, mode) in expected.items():
        checked_bytes(cache / name, digest, mode, owner)
    return cache / "rixa"


def main(arguments):
    os.umask(0o077)
    if os.geteuid() != 0:
        raise EntryError("Run the authenticated entry as the authorized root infrastructure operator.")
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, interrupt)
    if os.uname().sysname != "Linux" or os.uname().machine != "x86_64":
        raise EntryError("This entry requires Debian 13 Linux amd64.")
    os_release = Path("/etc/os-release").read_text()
    fields = dict(line.split("=", 1) for line in os_release.splitlines() if "=" in line)
    if fields.get("ID", "").strip('"') != "debian" or fields.get("VERSION_ID", "").strip('"') != "13":
        raise EntryError("This entry requires Debian 13; it does not install or change the operating system.")
    for path in (Path("/"), Path("/opt")):
        directory(path, 0)
    private_directory(CACHE_ROOT, 0, create=True)
    lock_fd = os.open(CACHE_ROOT / "prepare.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    with os.fdopen(lock_fd, "r+b") as lock:
        info = os.fstat(lock.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1
                or stat.S_IMODE(info.st_mode) != 0o600):
            raise EntryError("Unsafe initializer lock; inspect " + str(CACHE_ROOT))
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise EntryError("Another entry is preparing the pinned tools; wait and retry.") from None
        cache = CACHE_ROOT / GENERATION
        starter = prepare(cache, 0)
    # Retain verified immutable tools: saved native reviews can still name this
    # helper before Apply freezes its own copy. The entry never removes them.
    environment = dict(BASE_ENV, PATH=str(cache) + ":" + BASE_ENV["PATH"])
    environment["TERM"] = os.environ.get("TERM", "dumb")
    if os.environ.get("NO_COLOR"):
        environment["NO_COLOR"] = "1"
    print("Pinned Rixa starter " + STARTER_RELEASE + " authenticated; opening the shared bootstrap.", file=sys.stderr, flush=True)
    os.execve(starter, [str(starter), "bootstrap", *arguments], environment)


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except EntryInterrupted as error:
        print("Rixa entry: initialization interrupted; private cache state was retained for inspection.", file=sys.stderr)
        sys.exit(128 + error.number)
    except (EntryError, OSError, ValueError, tarfile.TarError, subprocess.SubprocessError) as error:
        print("Rixa entry: " + str(error), file=sys.stderr)
        print("No cache, native journal or frozen manager was reset. Inspect retained state before retrying.", file=sys.stderr)
        sys.exit(1)
RIXA_BOOTSTRAP_PYTHON
