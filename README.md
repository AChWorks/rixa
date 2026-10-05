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
