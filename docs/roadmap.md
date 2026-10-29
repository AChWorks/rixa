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
| 7 | [**#24 — Retained released-component adoption**](https://github.com/AChWorks/rixa/issues/24) — preserve real prior product data, sessions, authorization, ledgers and readiness through a genuine compatible Core/component update | Adopt a compatible split dependency set and complete component identity before building new installation/management paths on it. Use an actual prior product release, not a synthetic version transition or same-version restore. |
| 8 | [**#28 — Capability-driven installation and hosting profile**](https://github.com/AChWorks/rixa/issues/28) — consume reusable assessment and define trustworthy product evidence, supported adapters and operation state | AChrix owns pure assessment/planning/recheck; Rixa owns discovery, durable intent, effects and activation. Preserve the existing browser trust boundary and validate the selected ingress profile. |
| 9 | [**#29 — Server installation and management**](https://github.com/AChWorks/rixa/issues/29) — stable version-aware bootstrap and guided CLI/TUI over common product operations | Depends on #28 and a verified compatible runtime. Start with one concrete supported server profile, then prove interrupted install, persistent restart, update and ownership-aware removal under their accepted work scopes. |
| 10 | [**#30 — Panel and constrained-host installation**](https://github.com/AChWorks/rixa/issues/30) — aaPanel-first upload/start/web setup plus evidence-driven additional panel profiles | Also depends on #28; it is not architecturally dependent on every server adapter. Prove persistent Go execution, protected setup and exact ingress/TLS trust. Missing capabilities yield actionable provider requirements. |
| Parallel design | [**#34 — Control and per-site feature experience**](https://github.com/AChWorks/rixa/issues/34) — selected-site administration and retained-state activation semantics | Reconcile product UX with the released Multi-Site contract. Bulk operations, additional lifecycle states and individual feature implementations remain bounded decisions, not consequences of a managed inventory API. |

## Foundation boundaries used by the roadmap

Rixa consumes public versioned AChrix contracts rather than mirroring AChrix Issue state in this roadmap:

| Need | Durable owner / constraint |
| --- | --- |
| Composition, authorization and lifecycle | [AChrix public contracts](https://github.com/AChWorks/achrix/blob/HEAD/docs/architecture/contracts-and-interfaces.md); product policy and domain behavior remain Rixa-owned. |
| Optional authority-to-site resolution and managed selection planning | [AChrix Multi-Site](https://github.com/AChWorks/achrix/blob/HEAD/multisite/README.md); neither resolution nor a selection plan grants domain authority or executes persisted activation. |
| Identity, Audit, Admin and private Media | Their public AChrix Module contracts; Rixa owns roles/grants, site binding, content relationships and deployment. |
| Installation assessment and re-entry planning | [AChrix Installation](https://github.com/AChWorks/achrix/blob/HEAD/installation/README.md); product probes, supported adapters, locks, durable progress and actual effects stay Rixa-owned. |
| Resource tuning | AChrix typed Module resource controls and [operations contract](https://github.com/AChWorks/achrix/blob/HEAD/docs/operations/operability-performance.md); Rixa still measures aggregate/fairness behavior. |
| Public image preparation and private SVG | [AChrix Media](https://github.com/AChWorks/achrix/blob/HEAD/media/README.md); Rixa owns publication, staging, freshness, withdrawal and retention. |
| Search, Settings, Notifications, Gateway and generic lifecycle/recovery | Demand/evidence driven. Do not create empty shared Modules or activate deferred infrastructure merely because Rixa is a CMS. |

Exact Foundation release identity belongs to machine/runtime dependency state and the active implementation evidence. A compatible release change should not require rewriting this roadmap unless the accepted product outcome or public-contract requirement itself changes.


## Product and interface evolution

CMS work remains a first-class product track alongside operational delivery: improve real content, Media, appearance, publication and authorized selected-site tasks without inventing unrelated features. The product specification owns that durable direction; current Issues own the next bounded accepted slice.

The [installation and management contract](installation-and-management.md) defines shared CLI/TUI/web operation behavior and modern, accessible, understandable product interactions. Design core flows before polishing the surfaces; deliver each supported subset truthfully. Avoid speculative infrastructure, empty Modules and unmeasured optimization claims. Installation, update and uninstall are distinct effects even when they share operation state and diagnostics.
