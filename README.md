# Rixa

**Rixa** (ریکسا), or **Rixa CMS**, is a lightweight AChWorks CMS being built on AChrix. It targets simple blogs and pages, independently owned site data on shared infrastructure, and semantic static publication that people, search engines and AI retrieval can understand.

## Readiness and usage

This repository carries the accepted product specification, architecture and delivery guidance. GitHub [Issues](https://github.com/AChWorks/rixa/issues), [PRs](https://github.com/AChWorks/rixa/pulls), CI and [Releases](https://github.com/AChWorks/rixa/releases) own live implementation state, exact dependency evidence, runnable availability and delivery proof. A documented contract or planned outcome does not by itself prove that a runnable CMS or installation package exists.

To inspect/contribute, clone the public repository:

```bash
git clone https://github.com/AChWorks/rixa.git
cd rixa
```

Start from the relevant Issue and [Contributing](CONTRIBUTING.md). Use only build/run/configuration commands and checks that exist in the selected implementation/release state; do not infer runnable setup from planned architecture.

## Development runtime

The first executable slice provides an explicitly migrated management runtime for control administration plus isolated site administration. It is a development profile, not a production distribution or completed CMS.

Use [the example configuration](config/rixa.example.json) as a starting point. Configuration stores only environment-variable names for database credentials; keep real DSNs and bootstrap passwords outside Git. Control and every declared site require distinct HTTPS **hostnames**; changing only the port is not a separate browser authentication boundary. Each enabled site requires its own PostgreSQL database and an existing exclusive private Media directory. Runtime preflight first applies the pinned AChrix PostgreSQL DSN/TLS safety boundary, then connects to each selected database and compares the PostgreSQL system identity plus database OID, so unsafe remote/fallback transport is rejected before any probe and equivalent endpoint aliases cannot collapse site isolation. Media roots must be real, symlink-free, non-overlapping `0700` directories. Runtime TLS uses the exact configured HTTPS authorities and a trusted certificate/key pair. The current development listener is deliberately limited to loopback.

The supported operator sequence is:

```bash
# 1. Export the database DSNs named by the configuration.
# 2. Run explicit AChrix schema migrations before startup.
go run ./cmd/rixa migrate --config /absolute/path/to/rixa.json

# 3. Create the fixed initial administrators. Password values stay in environment.
export RIXA_BOOTSTRAP_PASSWORD='replace-with-a-real-secret'
go run ./cmd/rixa bootstrap-admin --config /absolute/path/to/rixa.json \
  --scope control --login control-admin --password-env RIXA_BOOTSTRAP_PASSWORD
go run ./cmd/rixa bootstrap-admin --config /absolute/path/to/rixa.json \
  --scope site:site-a --login site-a-admin --password-env RIXA_BOOTSTRAP_PASSWORD

# Record each returned principal in the matching admin_principal field.
# Repeat the site bootstrap for every enabled site.

# 4. Verify migrations, configuration, private roots, composition and readiness
#    without opening HTTP ingress.
go run ./cmd/rixa check --config /absolute/path/to/rixa.json

# 5. Start bounded HTTPS management ingress.
go run ./cmd/rixa run --config /absolute/path/to/rixa.json
```

A single declared site omits the optional Multi-Site resolver. Disabled declared sites keep their configuration but do not resolve database secrets or construct Identity/Audit/Media runtime resources. Control and site sessions remain distinct; forwarded headers, raw site IDs and caller-supplied principals never establish authority. After signing in to the control origin, the `Sites` administration surface can create an account in an enabled declared site. The surface derives the actor only from the authenticated control session, then `ControlService` re-authorizes the site target and enters that site's normal Application policy using the bounded site operator principal.

The exact AChrix dependency identity is machine-owned by `achworks.yaml`, `go.mod` and `go.sum`. The runtime CI verifies ordinary checksum-backed module consumption with no `replace` or `go.work`, then runs the real PostgreSQL/isolation/TLS lifecycle proof.

## Development prerelease packages

When GitHub Releases publishes a Rixa development prerelease, the supported release asset is a clean Linux-amd64 product package rather than a repository snapshot. The package carries the runnable binary, example configuration/environment files, license, build identity and concise [release installation guidance](docs/release-install.md). Published release assets remain bounded by the development profile: they do not imply production hosting support, public-internet exposure, RPO/RTO or an in-product updater.

Release builds embed their Rixa version while ordinary source/development builds continue to report `development`. The package build records the exact source commit and pinned AChrix dependency; GitHub Releases and CI remain the delivery/evidence owners.

## Where truth lives

| Question | Authoritative source |
| --- | --- |
| Purpose, users, accepted scope, success and non-goals | [Product specification](docs/PROJECT-SPEC.md) |
| Composition, site isolation, publication and dependency upgrade | [Architecture](docs/architecture.md), [two-site contract](docs/two-site-contract.md) |
| Outcome order and Foundation coordination | [Roadmap](docs/roadmap.md) |
| Development, validation, integration and secrets | [Contributing](CONTRIBUTING.md), [AGENTS](AGENTS.md) |
| Live work, dependencies, review, validation and delivery | [Issues](https://github.com/AChWorks/rixa/issues), [milestone](https://github.com/AChWorks/rixa/milestone/1), [PRs](https://github.com/AChWorks/rixa/pulls), Git/CI, Releases |
| Product identity and discovery | [achworks.yaml](achworks.yaml) |
| License | [MPL-2.0](LICENSE) |

AChrix is consumed as a normal pinned Go dependency through public contracts. Machine/runtime dependency state—`achworks.yaml` and, when Go runtime exists, product `go.mod`/`go.sum`—plus active PR/CI evidence own the exact reviewed Foundation identity. Rixa owns content, templates, publication and deployment; it neither copies Core nor becomes a requirement for other AChrix products. [AChrix consumption](https://github.com/AChWorks/achrix/blob/HEAD/docs/architecture/consumption-and-packaging.md) owns its supported usage.

The repository is public. First-party repository content is MPL-2.0 unless an explicit file/artifact notice says otherwise; third-party licenses/notices remain applicable. Naming does not claim domain or trademark clearance.
