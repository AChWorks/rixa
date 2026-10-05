# Rixa Roadmap

This document owns the durable outcome order, not live task status. [GitHub Issues](https://github.com/AChWorks/rixa/issues), PRs, CI and Releases own current contracts, dependency pins, blockers, review state and delivery evidence.

| Order | Outcome | Durable dependency / proof boundary |
| --- | --- | --- |
| 1 | [**#2 — Bounded consumer contract**](https://github.com/AChWorks/rixa/issues/2) — trusted single/two-site context, fixed initial permissions, composition boundaries and private/public Media separation | Establish the product-owned contract against intentionally public AChrix APIs before runtime implementation. |
| 2 | [**#3 — Executable composition and isolation**](https://github.com/AChWorks/rixa/issues/3) — normal pinned AChrix consumption, real PostgreSQL/private storage, single/two-site administration, lifecycle and meaningful CI | Requires a reviewed Foundation dependency that supplies the accepted Core/Identity/Audit/Media/Admin and optional Multi-Site surface. Prove real site isolation; Foundation fixtures are not product proof. |
| 3 | [**#4 — Editorial and appearance management**](https://github.com/AChWorks/rixa/issues/4) — posts/pages, revisions, visual formatted-text editing, private draft/preview and site-owned appearance/header/footer/home behavior | Builds on the executable authorization/data boundary. Real product fields may later justify reusable Settings behavior; product/domain fields remain Rixa-owned. |
| 4 | [**#5 — Coherent public publication**](https://github.com/AChWorks/rixa/issues/5) — static semantic HTML, authorized image representations, sitemap/metadata, basic SEO and truthful AI-readable meaning | Requires real content and composition. Publication owns private staging, coherent activation, source freshness, correction/withdrawal and retention. |
| 5 | [**#6 — Measured operation and site independence**](https://github.com/AChWorks/rixa/issues/6) — aggregate resource behavior, coherent isolated site transfer/restore and retained state across a genuine compatible reviewed Foundation update | Uses real product composition/publication and a real later compatible release when available. Do not fabricate update evidence or turn a controlled transfer proof into a generic updater/recovery product. |

## Foundation boundaries used by the roadmap

Rixa consumes stable public AChrix contracts rather than mirroring AChrix Issue state in this roadmap:

| Need | Durable owner / constraint |
| --- | --- |
| Composition, authorization and lifecycle | [AChrix public contracts](https://github.com/AChWorks/achrix/blob/HEAD/docs/architecture/contracts-and-interfaces.md); product policy and domain behavior remain Rixa-owned. |
| Optional exact authority-to-site resolution | [AChrix Multi-Site](https://github.com/AChWorks/achrix/blob/HEAD/multisite/README.md); address resolution grants no site/domain authority. |
| Identity, Audit, Admin and private Media | Their public AChrix Module contracts; Rixa owns roles/grants, site binding, content relationships and deployment. |
| Resource tuning | AChrix typed Module resource controls and [operations contract](https://github.com/AChWorks/achrix/blob/HEAD/docs/operations/operability-performance.md); Rixa still measures aggregate/fairness behavior. |
| Public image preparation and private SVG | [AChrix Media](https://github.com/AChWorks/achrix/blob/HEAD/media/README.md); Rixa owns publication, staging, freshness, withdrawal and retention. |
| Search, Settings, Notifications, Gateway and generic lifecycle/recovery | Demand/evidence driven. Do not create empty shared Modules or activate deferred infrastructure merely because Rixa is a CMS. |

Exact Foundation release identity belongs to machine/runtime dependency state and the active implementation evidence. A compatible release change should not require rewriting this roadmap unless the accepted product outcome or public-contract requirement itself changes.
