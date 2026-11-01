# Rixa

**Rixa** (ریکسا), or **Rixa CMS**, is a lightweight CMS for blogs and small sites, built on AChrix. It combines independent site administration with semantic static publication.

The native manager's source-level, initial-install optional two-site path,
its separate PostgreSQL/TLS prerequisites and its unreleased acceptance status
are documented in the [native operator guide](docs/server-management.md#optional-second-site-during-a-new-native-installation-development-source).
Do not infer support in previously published binaries or after-install Add Site.

## What you can do

- Run one site or several declared sites from one compatible product build, with separate site databases, accounts and private Media.
- Write posts and pages with a simple formatted-text editor, immutable revisions and private preview.
- Configure each site's title, language, home page, header, footer and light/dark appearance.
- Publish, correct, move or withdraw content as coherent static HTML, metadata, sitemap and authorized PNG/JPEG images while retaining private originals.
- Choose English or Persian administration and install additional interface language files without rebuilding a compatible executable. The CLI and terminal stay English.
- Use separate control and site logins. From the control interface, select one site to create and edit saved content, browse its history, privately preview revisions, and separately review/apply a coherent site-wide public generation without another site login. Saving a draft never publishes it; site accounts remain independent.
- Capture one quiesced site and restore it into a separate compatible empty target in the documented development profile.

The runnable profiles are for **development** on Linux amd64 with PostgreSQL and private local storage. The manual runtime offers loopback HTTPS or explicit direct TLS. Builds containing `rixa manage` also provide a guided terminal and JSON CLI for a prepared Debian 13/systemd server: authenticate a selected release, configure and install its service, inspect progress, apply a compatible retained-state update, continue safely fenced operations and remove the owned runtime while retaining data. The [native operator guide](docs/server-management.md) defines its required local PostgreSQL 18 databases, TLS material, trusted verifier and infrastructure authority.

Installation behavior follows the independently trusted executable and guides supplied with the selected package. The [bootstrap starter and PostgreSQL prerequisite path](docs/server-management.md#bootstrap-starter-and-postgresql-prerequisites) requires its own host profile. The [authenticated server entry](docs/bootstrap-entry.md) uses an immutable generation and a literal trust anchor; use the published generation and its linked host acceptance evidence.

Builds exposing `rixa manage web --help` also offer a [temporary browser console](docs/management-web.md) over the same native operations. The [aaPanel-assisted native profile](docs/management-web.md#aapanel-assisted-native-profile) describes its required host capabilities; use the selected release's linked acceptance evidence for tested combinations. Production hosting, unattended updates and destructive purge remain separate work.

## Get started

Download a versioned package and its adjacent checksum from [Releases](https://github.com/AChWorks/rixa/releases). Follow the included `INSTALL.md` and, when supplied, `MANAGE.md` / `VERIFY.md`, or read the [development installation guide](docs/release-install.md) for this source version. Verify the checksum before unpacking; releases with provenance bundles also support [publisher verification](docs/release-verification.md) before payload execution. Follow the evidence supplied by the exact selected release.

You supply:

- distinct HTTPS hostnames for control and each enabled site, with suitable TLS certificate/key material;
- compatible PostgreSQL databases for control and each enabled site; packages with the bounded clean-host prerequisite operation can create the initial control/site pair after explicit review;
- exclusive private Media/Public roots for a manual runtime; native management creates its documented fixed private roots;
- database secrets from protected environment sources, outside the JSON configuration and repository.

The [example configuration](config/rixa.example.json) names those inputs. The installation guide walks through explicit migration, initial administrator creation, configuration of the returned principals, readiness checking and foreground startup. Disabled declared sites retain configuration and acquire no site runtime resources.

Changing only a port does not isolate browser sessions. The current runtime verifies database-target and private-root isolation and does not infer authority from forwarded headers or caller-supplied site/principal values.

## Build from source

Use the Go toolchain required by `go.mod`:

```bash
git clone https://github.com/AChWorks/rixa.git
cd rixa
go build -o ./rixa ./cmd/rixa
./rixa version
```

Source builds report `development`; release builds embed their Rixa version. Both expose the selected AChrix component identities. Building a binary does not provision PostgreSQL, TLS, storage or a persistent system service.

## Learn more

| Topic | Guide |
| --- | --- |
| Purpose, supported direction and product constraints | [Product specification](docs/PROJECT-SPEC.md) |
| Composition, permissions, site isolation and component ownership | [Architecture](docs/architecture.md), [two-site contract](docs/two-site-contract.md) |
| Interface languages and post-build language installation | [Localization](docs/localization.md) |
| Publication and controlled site transfer | [Static publication](docs/static-publication.md), [site transfer](docs/site-transfer.md) |
| Release publisher and artifact verification | [Release verification](docs/release-verification.md) |
| Selected-site saved content and recovery | [Control content guide](docs/selected-site-content.md) |
| Guided native server management and recovery | [Native operator guide](docs/server-management.md), [bootstrap recovery](docs/bootstrap-operations.md) |
| Temporary browser management and the aaPanel native profile | [Browser console guide](docs/management-web.md) |
| Shared assessment and protected configuration preparation | [Setup operations](docs/setup-operations.md) |
| Intended server/CLI/web installation and management experience | [Installation and management](docs/installation-and-management.md) |
| Development and contribution | [Contributing](CONTRIBUTING.md), [AGENTS](AGENTS.md), [roadmap](docs/roadmap.md) |

Rixa consumes pinned public AChrix Go modules; content, themes, publication and deployment stay product-owned. Exact selected dependencies live in `go.mod`, `go.sum` and [achworks.yaml](achworks.yaml). [Issues](https://github.com/AChWorks/rixa/issues), [PRs](https://github.com/AChWorks/rixa/pulls), [CI](https://github.com/AChWorks/rixa/actions) and [Releases](https://github.com/AChWorks/rixa/releases) provide current work, validation and delivery evidence.

First-party content is [MPL-2.0](LICENSE) unless explicitly scoped otherwise; packaged third-party licenses/notices also apply.
