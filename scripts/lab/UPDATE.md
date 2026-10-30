# Native update and recovery laboratory

This runbook supplements the [disposable Debian guest](README.md). It is an
isolated development experiment, never an instruction to upgrade a live service.
Use a newly owned guest and private task directory. Discover existing jobs,
listeners, disks, sockets and receipts before provisioning. Do not replace an
unknown listener or restart an unrelated guest.

## Artifact and manager identity

Authenticate the published v0.3.0 archive independently before extracting its
manager. Record the exact public tag/source/archive digest, compiled executable
digest and publisher receipt. Install that exact manager under root-owned,
non-writable ancestors in the guest, alongside the independently trusted official
GitHub verifier. The manager still verifies its selected serving archive through
the normal publisher policy.

Prepare the genuine signed public v0.4.0 archive and adjacent provenance bundle
separately. The new update implementation may initially be a reviewed **source
fixture manager**; record its exact commit, clean build command and binary digest.
Do not describe that locally built manager as a signed publication, or construct
a locally signed/unsigned runtime archive to bypass package verification.

The published runtime identity and the operation manager identity are separate.
Keep the original installation's frozen manager, helper, journal, owner marker
and physical database identities. A newer manager must use the explicit managed
update adoption contract; it must not rewrite the old manager binding.

## Baseline state and retained credentials

Provision PostgreSQL 18 and laboratory TLS **inside the marked guest** using the
explicit fixture described in README.md. These fixtures do not establish that
Rixa provisions databases or obtains public certificates. Create two independent
database roles/databases and grant the fixture roles permission to inspect
`pg_control_system()`. Keep SQL, database URLs, bootstrap credentials, CA private
key, cookies and draft files root-private, outside repositories and web roots.

Use the published v0.3.0 manager's ordinary draft, Review, Apply and Status path.
A queued executor is not a successful installation. Inspect the actual unit,
process, protected journal, authenticated HTTPS and compiled runtime identity.

Transfer `content-smoke.py` and `update-smoke.py` together into the private
`/root/lab` directory. After a verified initial installation:

```bash
python3 /root/lab/update-smoke.py seed --draft /root/lab/draft.json \
  --ca /root/lab/ca.pem --receipt /root/lab/retained-http.json
python3 /root/lab/update-smoke.py snapshot --draft /root/lab/draft.json \
  --receipt /root/lab/before-update.json
```

Seed creates a normal site member without editor permission, retained control,
administrator and member sessions, a separate revoked session, a nonempty PNG
Media object, two content revisions, selected publication intent, an Appearance
revision and real static publication. Persian text is user-content fixture data.
All requests use normal HTTPS Identity/CSRF/product routes. Session cookies and
CSRF tokens remain only in the private receipt.

Every seed mutation, including non-idempotent login and upload, receives a
durable attempted record **before** its request. If any acknowledgement is lost,
inspect the recorded ID and actual product state. The script refuses a second
seed receipt and never retries unknown mutations. Do not delete the receipt,
choose a new ID or rerun the seed to hide an unresolved attempt.

Snapshot records cluster system identifier, database OID and server version,
every application table's sorted row count/digest (including migration and
operation ledgers), and retained configuration/Media/public directory metadata
and file bytes. It prints digests and physical identities, never raw database
rows or credentials. Take it while the isolated fixture is idle. It is a
comparison receipt, **not a backup or restore image**.

## Managed update and interruptions

Use the exact tested source fixture manager supplied by the integration owner.
Create an update draft containing `operation_id`, `installation_operation`,
`archive`, `bundle`, `release` and `source_commit`; all file paths select exact
already-authenticated public v0.4.0 bytes. Exercise the ordinary
`manage update draft|review|apply|status|resume` path and guided menu.

Before each potentially interrupted request, persist the operation ID and
reviewed inputs privately. Record the returned executor unit/generation and
positive acknowledgement. An interrupted SSH client does not prove that its
executor stopped. Inspect the exact owned unit, cgroup descendants, active
runtime and protected journal before recovery.

For a real interruption, observe the implementation's durable stage/attempt
boundary and interrupt only the positively owned update executor in the guest.
Do not edit journals, replace payload executables with wrappers, rewrite ledgers
or add a fake success marker. Record what effects were positively observed, what
remains uncertain, the kernel-observed executor fence and the resulting offered
actions. Resume only when the backend offers it. An unknown launch/ownership or
unreconciled effect requires inspection, not a blind repeat.

Exercise the retained original manager/installation mismatch and changed
inputs/ownership cases through public commands. Such probes must return useful
bounded denials without changing retained state. Restore only the precisely
owned test input changed by a deliberate probe; do not force an unsafe result.

There is no claim that reversing an executable reverses database migration.
Unknown migration outcomes use the authentic migration-ledger contract;
operator-required outcomes remain explicit. Do not label a retained snapshot as
backup-as-restore or binary reversal as database rollback.

## Acceptance and ordinary removal

Immediately after successful managed update, before further HTTP activity, take
the second snapshot and compare it with the baseline:

```bash
python3 /root/lab/update-smoke.py snapshot --draft /root/lab/draft.json \
  --receipt /root/lab/after-update.json
python3 /root/lab/update-smoke.py compare --before /root/lab/before-update.json \
  --receipt /root/lab/after-update.json
python3 /root/lab/update-smoke.py check --draft /root/lab/draft.json \
  --ca /root/lab/ca.pem --receipt /root/lab/retained-http.json
```

A changed snapshot is a reported delta requiring explanation; it is not silently
accepted. Verify the live executable and package/component identities separately
from retained state. The HTTP check reuses the old sessions, verifies identical
public and original Media bytes and authenticated preview, and checks anonymous,
revoked-session, wrong-CSRF, foreign-scope cookie and ungranted-member denials.
It does not create new sessions or successful writes.

Use a real served browser separately for the menu-to-web journey, both control
and site surfaces, English/LTR default, selectable Persian/RTL, keyboard/reflow
and relevant errors. This stdlib HTTP helper does not establish browser or
accessibility conformance.

Finally exercise `manage update remove` through its normal explicit confirmation
gate. Verify that only the owned executable/unit disappear and that databases,
configuration, Media, publication, identities and management evidence remain.
Compare retained snapshots again before treating removal as accepted.

Retain private resources while another consumer needs them. When the integration
owner releases the lab, shut down the exact guest, verify its QEMU job exited,
retain only sanitized evidence and delete only the positively owned task disk,
keys, certificates and private fixtures. Never clean historical unrelated
worktrees, the Owner demo or unrelated services.
