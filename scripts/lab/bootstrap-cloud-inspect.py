#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Read-only guest inspection; executed over the owned fixture's SSH connection."""
import grp
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import subprocess

OS_RESOLVER = "/usr/lib/systemd/systemd-resolved"
OS_NETWORK = {"/usr/lib/systemd/systemd-networkd", "/usr/sbin/dhcpcd", "/usr/sbin/dhclient"}


def allowed_socket(protocol, endpoint, executables):
    try:
        address, port = endpoint.rsplit(":", 1)
        port = int(port)
        address = address.strip("[]").split("%")[0]
    except (ValueError, TypeError):
        return False
    images = set(executables)
    if not images:
        return False
    if protocol == "tcp" and port == 22:
        return images <= {"/usr/sbin/sshd", "/usr/lib/systemd/systemd"}
    if port == 53:
        try:
            return images == {OS_RESOLVER} and ipaddress.ip_address(address).is_loopback
        except ValueError:
            return False
    if protocol == "udp" and port in (68, 546):
        return images <= OS_NETWORK
    # These are identified stock OS name/time clients, not application servers.
    if protocol in ("tcp", "udp") and port in (5353, 5355):
        return images == {OS_RESOLVER}
    return protocol == "udp" and images == {"/usr/lib/systemd/systemd-timesyncd"}


def command(arguments, timeout=30):
    result = subprocess.run(arguments, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise ValueError("guest OS observation failed: " + arguments[0])
    return result.stdout


def inspect():
    if (os.geteuid() != 0
            or Path("/etc/rixa-bootstrap-lab").read_text().strip() != "rixa.bootstrap-lab.v1"
            or Path("/etc/rixa-disposable-lab").read_text().strip() != "rixa.disposable-lab.v1"):
        raise ValueError("exact root-owned bootstrap fixture markers required")
    command(["cloud-init", "status", "--wait"], 180)
    release = dict(line.split("=", 1) for line in Path("/etc/os-release").read_text().splitlines() if "=" in line)
    if release.get("ID", "").strip('"') != "debian" or release.get("VERSION_ID", "").strip('"') != "13":
        raise ValueError("Debian13 required")
    if command(["uname", "-m"]).strip() != "x86_64" or Path("/proc/1/comm").read_text().strip() != "systemd":
        raise ValueError("amd64/systemd guest required")
    if command(["systemctl", "is-system-running"]).strip() != "running":
        raise ValueError("guest system manager is not running")
    inventory = {}
    rows = command(["dpkg-query", "-W", "-f=${binary:Package}\t${Status}\t${Version}\n"])
    for line in rows.splitlines():
        name, status, version = line.split("\t")
        if status == "install ok installed":
            inventory[name] = version
    if not 1 <= len(inventory) <= 4096 or any(name.split(":")[0].startswith(("postgresql", "rixa")) for name in inventory):
        raise ValueError("PostgreSQL/Rixa packages are present or inventory unavailable")
    for name in ("rixa", "postgres"):
        for lookup in (pwd.getpwnam, grp.getgrnam):
            try:
                lookup(name)
            except KeyError:
                continue
            raise ValueError("product/database OS account or group already exists")
    for executable in ("rixa", "psql", "postgres", "pg_ctl"):
        if shutil.which(executable):
            raise ValueError("product/database executable already exists")
    paths = ("/etc/rixa", "/opt/rixa", "/opt/rixa-manager", "/var/lib/rixa",
             "/var/lib/rixa-manager", "/etc/postgresql", "/usr/lib/postgresql", "/var/lib/postgresql")
    if any(Path(path).exists() or Path(path).is_symlink() for path in paths):
        raise ValueError("product/database filesystem scope already exists")
    if list(Path("/usr/local/bin").glob("rixa*")):
        raise ValueError("product tool was injected before clean baseline")
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            name = (entry / "comm").read_text().strip()
        except FileNotFoundError:
            continue
        if name.startswith(("postgres", "rixa")):
            raise ValueError("product/database process already exists")
    listeners = []
    for line in command(["ss", "-H", "-lntup"]).splitlines():
        fields = line.split()
        if len(fields) < 6:
            raise ValueError("unsupported guest socket observation")
        protocol, endpoint = fields[0], fields[4]
        pids = sorted(set(int(pid) for pid in re.findall(r"pid=([0-9]+)", line)))
        try:
            images = sorted(set(str((Path("/proc") / str(pid) / "exe").resolve(strict=True)) for pid in pids))
        except FileNotFoundError:
            raise ValueError("socket owner changed during observation") from None
        if not allowed_socket(protocol, endpoint, images):
            raise ValueError("unrecognized guest listener; retain observations before changing OS")
        listeners.append({"protocol": protocol, "local": endpoint, "executables": images})
    policy = dict(line.split(" ", 1) for line in command(["sshd", "-T"]).splitlines() if " " in line)
    if (policy.get("passwordauthentication") != "no" or policy.get("kbdinteractiveauthentication") != "no"
            or policy.get("permitrootlogin") not in ("prohibit-password", "without-password")):
        raise ValueError("fixture SSH is not root public-key-only")
    packages_json = json.dumps(inventory, sort_keys=True).encode()
    return {"schema": "rixa.bootstrap-clean-inspection.v1", "status": "verified",
            "os": "Debian13", "debian_version": Path("/etc/debian_version").read_text().strip(),
            "architecture": "amd64", "systemd": "running",
            "cloud_init": "completed successfully",
            "instance_id": Path("/var/lib/cloud/data/instance-id").read_text().strip(),
            "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
            "postgresql": "absent", "rixa": "absent", "product_tools_injected": False,
            "unknown_listeners": [], "identified_stock_os_sockets": listeners,
            "ssh": "root public-key-only; password and keyboard-interactive disabled",
            "ssh_host_public_key_sha256": hashlib.sha256(Path("/etc/ssh/ssh_host_ed25519_key.pub").read_bytes()).hexdigest(),
            "package_count": len(inventory), "package_inventory_sha256": hashlib.sha256(packages_json).hexdigest(),
            "packages": inventory}


if __name__ == "__main__":
    try:
        print(json.dumps(inspect()))
    except Exception as error:
        print("clean cloud inspection: " + str(error), file=__import__("sys").stderr)
        raise SystemExit(1)
