# Rixa development prerelease installation

This guide applies to versioned **development prerelease** packages published by Rixa GitHub Releases. It does not define a production hosting, public-internet, HA, RPO/RTO or managed-updater profile.

## Package and platform boundary

The initial package target is Linux amd64 and contains:

- `rixa` — the runnable product binary;
- `.env.example` — secret-name/value examples only;
- `config/rixa.example.json` — development-profile configuration example;
- `BUILDINFO.json` — Rixa version, pinned AChrix Core version, complete `achrix_components` map, source commit, Go toolchain and target;
- `THIRD_PARTY_MODULES.txt` plus `licenses/` — the packaged Go dependency inventory and discovered root license/notice files;
- `LICENSE`;
- `INSTALL.md` — this guide.

PostgreSQL, TLS certificates/keys and operator secrets are deployment inputs and are not bundled. Site transfer additionally requires compatible maintained PostgreSQL `pg_dump`/`pg_restore` tooling; consult `docs/site-transfer.md` in the source tree for the exact release tag when using that development-profile feature.

The default runtime profile is loopback development. An explicit `hosting_profile: "direct_tls"` enables the validated direct TLS listener boundary in versions that contain it; it does not establish a production hosting or persistent-service profile. Use the guide included with the exact selected release; older published binaries do not gain new commands or profiles from source documentation.

## Verify and unpack

Download both the package and its adjacent `.sha256` file from the same GitHub Release, then verify before extraction:

```bash
sha256sum -c rixa-<version>-linux-amd64.tar.gz.sha256
tar -xzf rixa-<version>-linux-amd64.tar.gz
cd rixa-<version>-linux-amd64
./rixa version
cat BUILDINFO.json
```

The reported Rixa version must match the release tag and `BUILDINFO.json`; the Core `achrix` value and complete `achrix_components` map must match the selected pinned dependency set recorded for that release. Older published packages retain their original metadata format; use the guide shipped with the selected package.

## Install the development binary

A root-owned system location is optional; running from the unpacked directory is valid. One conventional layout is:

```bash
sudo install -m 0755 rixa /usr/local/bin/rixa
sudo install -d -m 0700 /etc/rixa
sudo install -m 0600 config/rixa.example.json /etc/rixa/rixa.json
```

Create the configured Media/Public parent directories and TLS files with deployment-appropriate ownership. Private Media/Public roots used by Rixa must satisfy the runtime's validated private-root permissions and isolation rules.

Keep database DSNs and bootstrap passwords outside the JSON configuration. Export only the environment variables named by the configuration, using a protected secret source rather than committing a populated `.env` file.

## Initialize and verify

Edit `/etc/rixa/rixa.json` for the selected listener profile, exact HTTPS origins, database environment names, TLS paths and private roots. Direct TLS requires an explicit profile, matching listener/origin ports, a currently valid server certificate covering control and every enabled site, and a protected private key. Rixa serves the validated keypair without trusting forwarded headers.

Where supplied by the selected binary, [shared setup commands in the source guide](https://github.com/AChWorks/rixa/blob/HEAD/docs/setup-operations.md) assess prerequisites and create a new protected configuration without overwriting an existing target. This bounded preparation precedes the explicit steps below; it is not the complete installer or an existing-configuration updater.

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

A Rixa release is one coherent product artifact with one selected compatible Core/component set for all sites. Independent components may have different version numbers; no site in the shared installation uses a different version of the same module.

Accept a dependency update only after genuine prior-release retained-state proof covers accounts/credentials and fixed grants, content/references, original Media, appearance/configuration, publication, exact migration ledgers, sessions, readiness and site isolation. New source or an upstream fixture is not that product proof. Ordinary upgrades and controlled transfer have different session/compatibility semantics; consult the source-version `docs/site-transfer.md` before restoring a bundle.

The accepted future CLI/TUI/web management experience is documented in source as `docs/installation-and-management.md`. This manual development-package procedure does not provide that installer/updater or establish production support.
