# Independent site transfer

This is the bounded **development-profile** transfer owned by Rixa Issue #6. It is not a generic Backup & Recovery engine, live migration, production RPO/RTO promise, or activation of AChrix #19/#49.

## Captured site boundary

A completed capture contains one enabled site's:

- complete site PostgreSQL database through native `pg_dump`, including Identity/Audit/Media/Rixa schemas and immutable migration ledgers;
- site accounts/credentials and retained audit history;
- Rixa content/revisions/media references/appearance/theme/operation state;
- exact private Media originals;
- the private Rixa public artifact tree when publication is enabled, preserving active generation, route/withdrawal history and source relationships;
- the exact site ID, public origin, site administrator principal, public policy, transfer-schema identity, Rixa build-version string, exact AChrix version and bounded data/ledger summary.

The bundle deliberately excludes deployment DSNs/credentials, TLS material, the control database/control administrator, deployment-specific root paths and **all Identity session rows**. Restored browser/API sessions therefore do not remain valid. A new compatible target control administrator is separately bootstrapped and its site operator identity is derived again; historical audit actors remain historical evidence, not active grants.

The first profile requires the same site ID, public origin, site administrator principal and public policy on the compatible target. Root paths and PostgreSQL database identity may change. Keeping the origin stable avoids pretending already-rendered canonical URLs can be silently rewritten during a storage transfer.

## Consistency boundary

Capture is intentionally quiesced:

1. stop public/admin ingress and drain the Rixa runtime;
2. while the source Application is still available, reconcile Media unfinished work until no processed/busy work remains, then stop it;
3. run `rixa capture-site`.

Before creating any bundle directory, the tool validates every enabled declared Media/Public root as canonical private storage, requires a canonical existing parent for the new bundle, and rejects a bundle destination that contains, equals or sits inside any of those roots. This prevents a failed capture attempt from mutating the source tree merely because an unsafe destination was chosen. The tool independently fails closed when it sees another PostgreSQL session on the site database, pending/deleting Media state, a busy/invalid private Media root, or a changing database/private tree. After the no-other-session check it holds one PostgreSQL transaction with `SHARE` locks over every current Identity/Audit/Media/Rixa site-owned table for the whole dump/filesystem capture window. Native `pg_dump` can still take its compatible read locks, while concurrent row writers and schema changes cannot slip through between the before/after observations. Media/public trees are re-inspected again after the final database snapshot check so a late tree mutation is not silently omitted from the completed bundle. It takes an exclusive Linux `flock` on the Media root for the capture; the public root gets a transfer lock too, but the publication runtime does not consume that lock, so process quiescence remains an explicit requirement rather than a false live-snapshot guarantee.

`pg_dump` uses a custom archive with `--no-owner --no-privileges --exclude-table-data=identity.sessions`. Media/public trees use standard uncompressed tar plus a per-entry SHA-256 manifest. No custom database/archive format is invented.

A bundle is restorable only after `manifest.json` is written and hashed by `complete.json`. Missing/tampered completion state is rejected. The development profile bounds the database dump to 2 GiB, each private archive to 8 GiB, each archive to 100,000 entries and each relative path to 512 bytes. These are safety limits for this proof, not product/fleet capacity claims.

## Restore boundary

Restore accepts only:

- a canonical private completed bundle directory that is disjoint from the target Media/Public roots;
- a completed hash-valid bundle for the current transfer schema, Rixa build-version string and exact AChrix identity;
- a separate empty local PostgreSQL target database;
- nonexistent target Media/public roots under canonical existing parents;
- the same site identity/origin/admin/public policy.

Archive extraction is staged into newly created private directories. Absolute/traversal/duplicate paths, symlinks, hardlinks, special files, unexpected entries, wrong permissions, size/hash mismatch and extra/missing files are rejected. Media files must exactly match restored ready metadata and Rixa content-to-Media references.

Native `pg_restore` uses one transaction with `--exit-on-error --no-owner --no-privileges`. After restore the tool verifies exact captured migration ledgers, site data counts, appearance/theme, ready Media metadata, zero restored Identity session rows and coherent content/Media references. Before activating private roots it builds the normal current Rixa site composition over the restored database and staged Media/Public roots, then starts/readiness-checks the AChrix Application, Identity/Audit/Media Modules, Rixa Editorial ledger and active Publication generation without exposing ingress. A self-consistent but incompatible dump/manifest therefore cannot activate roots merely by agreeing with itself.

The target is still **not ingress-ready merely because restore returned**. Recreate/bootstrap the target control administrator, set the preserved site administrator principal in target configuration, run normal `rixa check`, and only then open ingress. The integration proof reconstructs the normal pinned runtime and verifies account/grant behavior, old-session rejection, fresh login, exact Media bytes, content/appearance/public output and target control-operator remapping.

A failed/unknown final root activation leaves the target isolated and reports failure/unknown outcome; do not infer rollback or replay into the now-nonempty database. When the second root rename fails after Media activation, the tool renames Media back and fsyncs that parent before returning an ordinary failure; an unconfirmed rollback is reported as unknown. Use a new empty target for another restore attempt.

## Tooling

The first profile is deliberately local PostgreSQL only and requires an explicit single loopback/Unix endpoint with `sslmode=disable`. Database credentials are supplied to maintained PostgreSQL tools through a minimal child-process allowlist containing only the required `PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE/PGSSLMODE/PGCONNECT_TIMEOUT` values plus fixed C locale; unrelated parent environment variables and Rixa secrets are not inherited. Credentials are not placed in command-line DSNs or bundle metadata.

Examples:

```bash
rixa capture-site --config /etc/rixa/rixa.json --site site-a --output /private/new-site-a-transfer
rixa restore-site --config /etc/rixa/target.json --site site-a --input /private/new-site-a-transfer
```

`RIXA_PG_DUMP` / `RIXA_PG_RESTORE` or the corresponding flags may select compatible maintained PostgreSQL binaries. CI uses PostgreSQL 18.6 tooling against the same disposable PostgreSQL 18.6 profile; this does not establish remote/production transfer support.


## Identity interpretation

The bundle is a compatibility record, not a source-control attestation. Release packaging may set the Rixa build version; development builds deliberately report `development`, so that string alone is not an exact commit identity. Transfer compatibility is therefore fail-closed on the versioned transfer schema, exact AChrix version, exact Identity/Audit/Media/Rixa migration ledgers, captured site/public-policy identity, artifact hashes and successful current-composition readiness before activation. The exact Rixa candidate SHA used for repository acceptance remains CI/PR evidence rather than being invented inside a development bundle.
