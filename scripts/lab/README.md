# Disposable Debian server laboratory

This is a developer validation tool, not Rixa's installer, a production deployment path or a supported-host claim. It runs one Debian 13 amd64 guest as an unprivileged host user, using QEMU TCG, one virtual CPU, 1024 MiB RAM and an 8 GiB sparse disk. The guest may change its own packages, users, databases and system services. The host receives no package installation, privileged mounts, TAP interfaces or root commands.

Current Debian cloud downloads provide TLS and checksums without detached checksum signatures. This laboratory deliberately uses the official Debian netinst ISO and its signed checksum instead. See Debian's [cloud-image explanation](https://cloud.debian.org/images/cloud/HEADER.html), [image authentication guide](https://www.debian.org/CD/verify) and [QEMU/preseed guidance](https://wiki.debian.org/DebianInstaller/Qemu). The reviewed CD signing fingerprint is enforced in the script; a future key rotation needs review.

## Prepare

Use an existing Debian developer host with `python3`, `curl`, `gpgv`, `dpkg-deb`, `isoinfo`, `cpio`, `openssl`, `ssh-keygen`, `ssh`, `qemu-img` and `qemu-system-x86_64`. Do not install or reconfigure a shared host merely to run this experiment.

Obtain an independently trusted Debian role keyring. On a host with authenticated Debian APT indexes, downloading and extracting `debian-keyring` in a private task directory needs no root:

```bash
umask 077
mkdir /private/task-keyring
cd /private/task-keyring
apt-get download debian-keyring
dpkg-deb -x debian-keyring_*.deb extracted
```

Do not enable unauthenticated APT downloads. Verify the selected package against the trusted APT index and, where needed for evidence, validate that index's signed Release metadata using the installed distribution archive keyring. A key downloaded alongside an unknown image is not an independent trust root.

Choose an explicit currently published Debian 13 point release and a new private canonical path (no spaces, commas or symbolic links):

```bash
python3 scripts/lab/debian-guest.py prepare \
  --lab /private/rixa-lab \
  --version 13.POINT.0 \
  --keyring /private/task-keyring/extracted/usr/share/keyrings/debian-role-keys.pgp
```

Replace `13.POINT.0` with the real version listed in the official ISO directory. Preparation downloads at most a 2 GiB ISO from the fixed Debian HTTPS origin, verifies its exact signed manifest entry, extracts only the installer kernel/initrd, generates private laboratory SSH credentials and embeds a fixed preseed in the initrd. It refuses an existing destination rather than overwriting unknown state. The receipt contains the image, signer and input digests, never passwords or private keys.

The preseed partitions only the guest's virtual disk, installs a minimal SSH-capable system and allows root SSH by its generated key; password SSH is disabled. This root access belongs only to the disposable guest.

## Run

First discover and reconcile prior laboratory jobs/processes. Keep the foreground QEMU command in a supported persistent task/service so a client disconnect does not create an untracked VM:

```bash
python3 scripts/lab/debian-guest.py install --lab /private/rixa-lab
```

The installer exits on its first reboot. Inspect the printed private serial-log path and the real process exit before starting the installed disk:

```bash
python3 scripts/lab/debian-guest.py boot --lab /private/rixa-lab
```

Only host loopback TCP ports `22291` (guest SSH 22) and `22443` (guest HTTPS 443) are forwarded. Existing listeners cause launch failure, not replacement. There is no shared host directory. A live launch holds a private lock; an existing console/QMP socket requires reconciliation before another launch. The private `console.sock` keeps the serial console available for explicit diagnosis while each boot retains a separate logfile. Installer arguments after `---` preserve the serial console and `net.ifnames=0` in the installed kernel so its declared `eth0` remains consistent across reboot.

Use a private laboratory host-key file and key-only login:

```bash
ssh -p 22291 -i /private/rixa-lab/id_ed25519 \
  -o IdentitiesOnly=yes -o BatchMode=yes \
  -o StrictHostKeyChecking=accept-new \
  -o UserKnownHostsFile=/private/rixa-lab/known_hosts \
  root@127.0.0.1
```

For automated SSH commands, use `ssh -n` unless an explicitly supplied input stream is intended; otherwise SSH can consume a surrounding shell script.

First-use host-key acceptance here is limited to the local owned VM forward; retain that file so a subsequent unexpected host-key change fails. Do not use this setting as general remote-host identity proof.

## Product evidence and cleanup

Provision PostgreSQL 18 and laboratory certificates inside the guest as explicit fixtures. The optional `provision-postgres.sh` fixture is guarded by the explicit private guest marker, QEMU/Debian13 identity, reviewed PGDG key digest and a one-attempt record. It requires root-private `/root/lab/pgdg.asc` and an explicitly prepared `databases.sql` with two independent roles/databases; an interrupted fixture is inspected rather than silently recreated. Generated credentials and SQL stay outside Git. Use a laboratory CA and appropriate server certificate chain for the declared hostnames; never claim public trust from it. This does not prove Rixa provisions PostgreSQL or obtains public certificates. Transfer the independently trusted source-built manager separately from the exact published runtime archive/bundle. The manager must authenticate that signed archive before staging or executing it; a locally built unsigned archive is not a signature bypass.

After installing the exact manager/runtime candidate, the guarded `content-smoke.py` fixture can run inside the marked guest using the original root-private draft and laboratory CA. It uses normal HTTPS login/CSRF and content routes to create/save/select/publish one test document, read its authenticated private preview, verify the same preview returns an explicit bounded authentication denial without credentials or private bytes, read the anonymous public page, then revoke both scope sessions. A private durable receipt retains attempted mutation IDs before requests; an existing or unresolved attempt is never silently recreated. Its `check` action verifies identical public bytes and authenticated preview after restart without creating new content. This is HTTP/persistence evidence, not browser or accessibility conformance.

```bash
python3 /root/lab/content-smoke.py publish --draft /root/lab/draft.json \
  --ca /root/lab/ca.pem --receipt /root/lab/content-receipt.json
# After the real guest/service restart:
python3 /root/lab/content-smoke.py check --draft /root/lab/draft.json \
  --ca /root/lab/ca.pem --receipt /root/lab/content-receipt.json
```

Prove real installation, protected configuration, service restart, TLS and administrator login, interrupted-operation reconciliation and ordinary removal/data preservation against the exact candidate. Record source/artifact identities, real exits, checks and remaining limits in the owning Issue/PR; mock checks or an SSH-ready guest do not establish product acceptance.

Shut down the owned guest via `systemctl poweroff` over that SSH connection, then verify the QEMU task exited. If SSH is unavailable, reconcile the exact owned QMP/job before requesting ACPI shutdown or a bounded task stop. Never kill unrelated QEMU processes. Retain unique failure evidence while needed; remove the private guest disk, credentials, ISO and extracted keyring only after no consumer needs them. No automatic recursive deletion or guest reuse as production is provided.
