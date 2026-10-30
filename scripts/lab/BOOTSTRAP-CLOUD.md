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

## New acceptance journey

After a separate exact runtime-effect grant, prepare a NEW overlay as a direct
child of the sealed task root. This preserves the original seed, key, known-host
file and immutable clean baseline; it does not inject product tools:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/lab/bootstrap-cloud-guest.py journey \
  --lab /private/new-bootstrap-lab/journey1 \
  --baseline-lab /private/new-bootstrap-lab \
  --baseline-sha256 EXACT_SEALED_BASELINE_SHA256 --scope-record EXACT_EFFECT_GRANT
```

The new overlay is backed directly by the exact sealed clean disk. Each load
rechecks its baseline digest/mode, official image identity, original fixture
inventory and overlay backing/size. Read-only `qemu-img info --force-share`
observes backing/header metadata when the positively owned guest is running;
it is not an online consistency check. Reuse the existing boot and clean-inspect
commands with the journey path and a new launch attempt. Fresh inspection must
pass before source-fixture injection or product provisioning. Preserve failed
attempts and the new disk; do not recreate a journey to conceal partial effects.

Only the separately reviewed grant can authorize fixture injection, PostgreSQL
and native installation effects through `bootstrap-proof.py`. Its private
configuration pins the source fixture, official helper and genuine public
archive/runtime/source identities. A fixture remains source-only; it does not
establish a published starter or public one-line installation.

Read-only inspection retries are spaced15seconds apart so stock startup work can
settle on the1vCPU TCG guest. If a bounded observation expires, inspect its
retained evidence first. A separately justified read-only observation can use
`--observation NEW_OBSERVATION_ID` with the same positively owned launch attempt;
this creates new receipts and never overwrites the previous observation.

For a separately granted interruption experiment, the existing lifecycle watcher
also has two fixed bootstrap profiles: `--kind postgres --stage download` and
`--kind install --stage payload`. Arm one bounded guest watcher before the
corresponding single Apply. Other stages for those profiles are refused. It
binds the protected journal, acknowledged generation, actual frozen process
image, systemd invocation and full cgroup; records freeze intent; observes kernel
freeze/population; rechecks the same attempted stage; then records and sends a
whole-unit kill. A missed boundary is recorded without a kill. A `finally`
recovery thaws only the same positively identified live invocation or records its
already observed quiescence. Unknown identity/recovery remains a visible failure
requiring inspection.

Only ordinary product Status can offer Resume, and only the operation's frozen
manager may perform it. Download/payload experiments do not establish recovery
of partial dpkg configuration, initialization, unmarked SQL ownership or unknown
authentication. Preserve all receipts and actual outcomes; a queued watcher,
signal request or unit disappearance alone is insufficient evidence.

A failed provisioned journey is preserved separately from a clean baseline.
After its explicitly authorized private export, normal poweroff and verified
QEMU/unit/cgroup/socket quiescence, record its sealed0400 disk identity in
`failed-journey.json`. The boot driver refuses both clean-baseline and
failed-journey seals. Never relabel a failed disk as clean, remove its seal to
continue in place, or use it as backing for a new clean acceptance journey.
A separately authorized next journey uses the original unchanged clean baseline.

## Journey3 populated retention

The separately granted journey3 proof uses `update-smoke.py --bootstrap-retention`
with the exact protected d97f97f fixture, original native install input and
`retention-j3.json` receipt. This bounded profile reuses the existing Media,
revision, Appearance, publication, privacy and all-schema snapshot mechanisms.
It excludes the default update proof's member-account creation and member login;
it never reinterprets an earlier default-profile receipt. Each mutation still
records its operation/attempt before the request and preserves unknown outcomes.

Only that proof process maps the two declared HTTPS origins to guest loopback.
URL, SNI, CA and hostname verification remain strict. Other destinations/ports
are refused, and the resolver is restored on exit; no OS DNS/hosts edits occur.
Snapshots retain all logical tables, runtime/config/Media/public files, physical
database/data-directory identity, static PostgreSQL files and frozen managers.

The authorized reboot is normal poweroff, actual QEMU completion/quiescence,
then relaunch of the original journey3 disk with a new launch attempt. At that
quiesced boundary only, one exclusive byte-identical populated checkpoint may be
preserved with mode0400 for a separately authorized consumer. It is not a clean baseline,
backup/restore support or permission to boot another guest. Hold populated
serving for Master browser acceptance; removal/final shutdown need continuation.
