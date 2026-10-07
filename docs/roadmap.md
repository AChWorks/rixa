# Rixa Roadmap

This document owns the durable outcome order, not live task status. [GitHub Issues](https://github.com/AChWorks/rixa/issues), PRs, CI and Releases own current contracts, dependency pins, blockers, review state and delivery evidence.

| Order | Outcome | Durable dependency / proof boundary |
| --- | --- | --- |
| 1 | [**#2 — Bounded consumer contract**](https://github.com/AChWorks/rixa/issues/2) — trusted single/two-site context, fixed initial permissions, composition boundaries and private/public Media separation | Establish the product-owned contract against intentionally public AChrix APIs before runtime implementation. |
| 2 | [**#3 — Executable composition and isolation**](https://github.com/AChWorks/rixa/issues/3) — normal pinned AChrix consumption, real PostgreSQL/private storage, single/two-site administration, lifecycle and meaningful CI | Requires a reviewed Foundation dependency that supplies the accepted Core/Identity/Audit/Media/Admin and optional Multi-Site surface. Prove real site isolation; Foundation fixtures are not product proof. |
| 3 | [**#4 — Editorial and appearance management**](https://github.com/AChWorks/rixa/issues/4) — posts/pages, revisions, visual formatted-text editing, private draft/preview and site-owned appearance/header/footer/home behavior | Builds on the executable authorization/data boundary. Real product fields may later justify reusable Settings behavior; product/domain fields remain Rixa-owned. |
| 4 | [**#5 — Coherent public publication**](https://github.com/AChWorks/rixa/issues/5) — static semantic HTML, authorized image representations, sitemap/metadata, basic SEO and truthful AI-readable meaning | Requires real content and composition. Publication owns private staging, coherent activation, source freshness, correction/withdrawal and retention. |
| 5 | [**#6 — Measured operation and site transfer**](https://github.com/AChWorks/rixa/issues/6) — aggregate resource behavior and coherent isolated site capture/restore | Uses real product composition/publication. The bounded transfer proof is development-profile evidence, not a generic updater/recovery product. |
| 6 | [**#25 — First development prerelease**](https://github.com/AChWorks/rixa/issues/25) — clean immutable Rixa package/version on the current reviewed AChrix baseline | Package the already-proven development profile without turning it into a production hosting/support promise. |
| 7 | [**#28 — Capability-driven installation core and hosting profile**](https://github.com/AChWorks/rixa/issues/28) — shared preflight/provisioning semantics and a proven supported production-hosting boundary | Distinguish available/provisionable/operator-required/unsupported capabilities before mutation; server and panel installers reuse this one core. |
| 8 | [**#29 — One-line VPS/server installer**](https://github.com/AChWorks/rixa/issues/29) — minimal-input end-to-end install from immutable release to working admin URL | Depends on #28; may provision missing supported dependencies only with sufficient privilege and verified mechanisms. |
| 9 | [**#30 — Control-panel and constrained-host installation**](https://github.com/AChWorks/rixa/issues/30) — aaPanel-first validated panel path plus evidence-driven Plesk/DirectAdmin/cPanel support | Depends on #28; panel name alone is not compatibility. Missing host capabilities must produce exact actionable prerequisite output rather than unsafe fallback. |
| 10 | [**#24 — Future AChrix upgrade compatibility**](https://github.com/AChWorks/rixa/issues/24) — retained state/isolation/readiness across the next genuine compatible reviewed Foundation release | Starts only when a real later AChrix release exists. It is independent of installer work, does not block current installation outcomes, and must never be satisfied with synthetic update evidence. |

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
