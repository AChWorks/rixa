# Rixa

**Rixa** (ریکسا), or **Rixa CMS**, is a lightweight CMS for blogs and small sites, built on AChrix. It combines independent site administration with semantic static publication.

## What you can do

- Run one site or several declared sites from one compatible product build, with separate site databases, accounts and private Media.
- Write posts and pages with a simple formatted-text editor, immutable revisions and private preview.
- Configure each site's title, language, home page, header, footer and light/dark appearance.
- Publish, correct, move or withdraw content as coherent static HTML, metadata, sitemap and authorized PNG/JPEG images while retaining private originals.
- Use separate control and site logins. The current control interface lists declared sites and can create a site account.
- Capture one quiesced site and restore it into a separate compatible empty target in the documented development profile.

The current runnable profiles are for **development**: Linux amd64, PostgreSQL and private local storage, with default loopback HTTPS or an explicit direct TLS runtime profile. Shared setup assessment can prepare a new protected configuration when the provider has supplied the prerequisites; it does not provision a host or persistent service. Production hosting, a one-line installer, a full-screen terminal menu, panel/web setup and an in-product updater are accepted development directions, not capabilities supplied by the current package.

## Get started

Download a versioned package and its adjacent checksum from [Releases](https://github.com/AChWorks/rixa/releases). Follow the included `INSTALL.md`, or read the [development installation guide](docs/release-install.md) for this source version. Verify the checksum before unpacking; releases with provenance bundles also support [publisher verification](docs/release-verification.md) before payload execution. Follow the evidence supplied by the exact selected release.

You supply:

- distinct HTTPS hostnames for control and each enabled site, with suitable TLS certificate/key material;
- a PostgreSQL database for control and a separate database for each enabled site;
- exclusive private Media roots and, when publication is enabled, private publication roots;
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
| Publication and controlled site transfer | [Static publication](docs/static-publication.md), [site transfer](docs/site-transfer.md) |
| Release publisher and artifact verification | [Release verification](docs/release-verification.md) |
| Shared assessment and protected configuration preparation | [Setup operations](docs/setup-operations.md) |
| Intended server/CLI/web installation and management experience | [Installation and management](docs/installation-and-management.md) |
| Development and contribution | [Contributing](CONTRIBUTING.md), [AGENTS](AGENTS.md), [roadmap](docs/roadmap.md) |

Rixa consumes pinned public AChrix Go modules; content, themes, publication and deployment stay product-owned. Exact selected dependencies live in `go.mod`, `go.sum` and [achworks.yaml](achworks.yaml). [Issues](https://github.com/AChWorks/rixa/issues), [PRs](https://github.com/AChWorks/rixa/pulls), [CI](https://github.com/AChWorks/rixa/actions) and [Releases](https://github.com/AChWorks/rixa/releases) provide current work, validation and delivery evidence.

First-party content is [MPL-2.0](LICENSE) unless explicitly scoped otherwise; packaged third-party licenses/notices also apply.
