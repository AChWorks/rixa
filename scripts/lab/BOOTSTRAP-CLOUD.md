# Fresh Debian cloud bootstrap laboratory

This fixture prepares an owned Debian13 amd64/systemd guest **without PostgreSQL
or Rixa**. It does not install product tools or establish a published bootstrap
starter, prerequisite provisioner, panel or one-line installation. Those phases
need a separate Master candidate and exact effect authorization. Preserve the
existing [signed netinst laboratory](README.md) and all earlier native evidence.

## Trust

Read Debian's current [primary cloud-image policy](https://cloud.debian.org/images/cloud/HEADER.html).
Current per-distribution cloud images use checksums from official Debian HTTPS
origins; detached signatures are described for legacy OpenStack images. Do not
claim GPG verification when no signature exists. Using this official
HTTPS/checksum method requires explicit task acceptance and truthful receipts.
It never changes Rixa's mandatory signed publisher policy.

Select an immutable versioned directory under the official
[trixie cloud directory](https://cloud.debian.org/images/cloud/trixie/), then pin
its exact manifest SHA256 and genericcloud-amd64 image SHA512. The fixture checks
metadata before JSON parsing and the entire image before any qcow2 parser, key,
seed, overlay or boot.

Policy, manifest and metadata remain on fixed official HTTPS origins. Official
image redirects may transport bytes over bounded certificate-validated HTTPS
only with the already selected image SHA512. The resolved URL is recorded; a
mirror is never a checksum authority. Different bytes fail before parsing.

## Prepare

Discover existing jobs/processes/sockets, images and assigned ports first. Use
existing Python3, QEMU/qemu-img, SSH/ssh-keygen and cloud-localds/ISO tooling.
Do not install host tools, change shared services/network/firewalls/DNS, mount
host filesystems, create TAP interfaces or modify an Agent control plane.

The seed uses standard [NoCloud CIDATA](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html)
through cloud-localds. It contains only a fixture public key, OS/SSH settings and
markers. Private keys, passwords, database URLs, package installs and product
payloads are absent. No custom filesystem writer or metadata service is needed.

Use a NEW canonical private task root and replace the placeholders with
independently selected exact values and the real scope record:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/lab/bootstrap-cloud-guest.py prepare \
  --lab /private/new-bootstrap-lab --build BUILD \
  --manifest-sha256 MANIFEST_SHA256 --image-sha512 IMAGE_SHA512 \
  --instance rixa-bootstrap-clean-unique \
  --trust-method debian-cloud-official-https --scope-record SCOPE_RECORD
```

Preparation refuses existing state. An explicitly authorized
`--continue-image-download` is restricted to the exact four retained verified
metadata/attempt files at the recorded failed image-download phase, with positive
absence of image/key/seed/disk. It rechecks the same pins and records continuation
before repeating only the immutable GET. A partial image or later effect blocks
continuation. Never delete/recreate evidence or blindly repeat uncertain effects.

## Boot and inspect

Use stable persistent job/operation IDs. The foreground QEMU process is limited
to1vCPU/1024MiB TCG, a new8GiB overlay backed by the verified0400 official image,
and loopback22691→SSH22 /22643→futureHTTPS443. It binds no seed service on22680.
Existing listeners are never replaced; another owned QEMU blocks launch.

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/lab/bootstrap-cloud-guest.py boot \
  --lab /private/new-bootstrap-lab --attempt clean-firstboot-unique
```

A queued job is not QEMU/SSH readiness. Once the launch receipt positively
acknowledges the exact owned QEMU PID/image, run:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/lab/bootstrap-cloud-verify.py inspect \
  --lab /private/new-bootstrap-lab --attempt clean-firstboot-unique
```

The bounded SSH probe is read-only. First-use host-key acceptance applies only to
the positively owned local forward; retain the known-host file. It waits for
successful cloud-init, verifies Debian13/amd64/systemd and root public-key-only
SSH, inventories packages and proves no PostgreSQL/Rixa packages, executables,
accounts, processes or managed paths exist. Socket owners must match actual
stock OS SSH/resolver/DHCP/time executables. Report those OS sockets honestly;
unknown application listeners fail. No product login, package installation,
database mutation or service restart occurs.

## Preserve the clean baseline

Record a normal poweroff attempt before sending `systemctl poweroff` through
the owned SSH connection. Verify the actual persistent QEMU job/launch exited0;
SSH disconnect or request acknowledgement alone is insufficient.

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/lab/bootstrap-cloud-verify.py seal \
  --lab /private/new-bootstrap-lab --attempt clean-firstboot-unique
```

Sealing requires verified clean inspection, actual QEMU exit0, exclusive launch
lock, absent sockets and free forwards. It verifies backing identity, makes the
quiesced overlay0400 and records its full SHA256, official backing digest and
trust method. This immutable baseline cannot be rebooted in place through the
fixture. A later authorized phase must use a new owned overlay and preserve it
before injecting product tools or provisioning PostgreSQL.

Keep private keys/logs outside Git and GitHub. Persist sanitized source, image,
trust, job and readiness identities through the owning Issue/Master. Retain all
resources until their consumers release them; unrelated cleanup is out of scope.
