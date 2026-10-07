# Rixa development prerelease installation

This guide applies to versioned **development prerelease** packages published by Rixa GitHub Releases. It does not define a production hosting, public-internet, HA, RPO/RTO or managed-updater profile.

## Package and platform boundary

The initial package target is Linux amd64 and contains:

- `rixa` — the runnable product binary;
- `.env.example` — secret-name/value examples only;
- `config/rixa.example.json` — development-profile configuration example;
- `BUILDINFO.json` — Rixa version, pinned AChrix version, source commit, Go toolchain and target;
- `LICENSE`;
- `INSTALL.md` — this guide.

PostgreSQL, TLS certificates/keys and operator secrets are deployment inputs and are not bundled. Site transfer additionally requires compatible maintained PostgreSQL `pg_dump`/`pg_restore` tooling as documented in [site-transfer.md](site-transfer.md).

The current runtime is deliberately loopback-only. Use it as the proven development profile; do not infer public exposure or production support from the existence of a release asset.

## Verify and unpack

Download both the package and its adjacent `.sha256` file from the same GitHub Release, then verify before extraction:

```bash
sha256sum -c rixa-<version>-linux-amd64.tar.gz.sha256
tar -xzf rixa-<version>-linux-amd64.tar.gz
cd rixa-<version>-linux-amd64
./rixa version
cat BUILDINFO.json
```

The reported Rixa version must match the release tag and `BUILDINFO.json`; the AChrix version must match the pinned dependency recorded for that release.

## Install the development binary

A root-owned system location is optional; running from the unpacked directory is valid. One conventional layout is:

```bash
sudo install -m 0755 rixa /usr/local/bin/rixa
sudo install -d -m 0755 /etc/rixa
sudo install -m 0644 config/rixa.example.json /etc/rixa/rixa.json
```

Create the configured Media/Public parent directories and TLS files with deployment-appropriate ownership. Private Media/Public roots used by Rixa must satisfy the runtime's validated private-root permissions and isolation rules.

Keep database DSNs and bootstrap passwords outside the JSON configuration. Export only the environment variables named by the configuration, using a protected secret source rather than committing a populated `.env` file.

## Initialize and verify

Edit `/etc/rixa/rixa.json` for the intended loopback listener, exact HTTPS origins, database environment names, TLS paths and private roots.

Run explicit migrations:

```bash
rixa migrate --config /etc/rixa/rixa.json
```

Create the initial control administrator, then each enabled site's initial administrator:

```bash
export RIXA_BOOTSTRAP_PASSWORD='replace-with-a-real-secret'

rixa bootstrap-admin --config /etc/rixa/rixa.json \
  --scope control --login control-admin --password-env RIXA_BOOTSTRAP_PASSWORD

rixa bootstrap-admin --config /etc/rixa/rixa.json \
  --scope site:site-a --login site-a-admin --password-env RIXA_BOOTSTRAP_PASSWORD
```

Record each returned principal in the matching `admin_principal` field, clear the bootstrap password from the shell environment when finished, then verify the complete composition without opening ingress:

```bash
unset RIXA_BOOTSTRAP_PASSWORD
rixa check --config /etc/rixa/rixa.json
```

Only after `check` succeeds, run the current foreground development server:

```bash
rixa run --config /etc/rixa/rixa.json
```

Graceful SIGINT/SIGTERM triggers bounded ingress drain and runtime shutdown.

## Upgrade boundary

A Rixa release is one coherent product artifact with one pinned AChrix version for all sites. Do not mix Foundation versions per site.

When a later genuine compatible reviewed AChrix release exists, Rixa #24 owns the retained-state compatibility proof before that newer dependency is accepted for a subsequent Rixa release. The first Rixa release is intentionally allowed to use the current reviewed baseline without fabricating a future update.
