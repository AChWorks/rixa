# Rixa Product Specification

This is the single canonical product-level specification. It records accepted intent, not runtime readiness or live task status. [README](../README.md) is the usage/navigation entry; [architecture](architecture.md) owns technical detail; GitHub Issues/PRs/CI/releases own execution and evidence.

## Purpose and users

Build a simple, efficient CMS for blogs and small sites, reusable as a maintained product that may remain small or grow substantially. Start with meaningful real behavior, clean code and supported extension boundaries. WordPress is a familiar feature example, not a compatibility target or a commitment to reproduce its ecosystem.

Users are the infrastructure administrator managing declared sites, independent site administrators managing their own content, public readers, and future explicitly authorized machine clients. Public registration and broader site-user workflows may be absent initially.

## First usable outcome

- A single compatible product build supports a real single-site profile and a two-site profile using AChrix Core and only necessary optional Modules.
- An infrastructure administrator can manage its declared sites; site administrators have independent, fixed initial permissions. Capability/resource grants—not display role names—govern behavior.
- Site-owned posts and pages have a simple visual formatted-text editor, private draft/preview, and explicit manual publication/correction/withdrawal.
- A home page, post/page presentation, images, header and footer are enough for a simple blog. Shared template/theme code permits different site appearance through site-owned configuration/assets.
- Ordinary public output is static semantic HTML; it does not require browser JavaScript for primary content or database work for ordinary public reads.
- Basic SEO, truthful structured data and understandable public meaning for AI retrieval are built into actual publication, with a path to later improvement.
- The development profile demonstrates scoped authorization, independent site data/assets, finite aggregate resources and coherent isolated transfer/restore. It makes no production delivery or fleet-capacity promise.

[The milestone](https://github.com/AChWorks/rixa/milestone/1) groups implementation acceptance. Production install/distribution, hosting support and release readiness require executable evidence and a separately authorized delivery profile.

## Site independence on shared infrastructure

Each site owns its content, Media, users/accounts when enabled, roles/grants and future domain state. A future commerce feature may be active on one site and absent on another. Shared implementation/infrastructure must not mix site data or silently introduce undeclared global dependencies.

A site must be transferable or captured independently using its coherent data/files/configuration and declared dependencies on compatible infrastructure. Preserve or explicitly remap author/account/grant references, theme/custom-asset dependencies and protected key/runtime requirements. Shared infrastructure administration remains distinct from site-owned users.

A separate database and exclusive private Media root per site on one PostgreSQL installation is the starting profile to evaluate, not a hardware-capacity guarantee or a database-engine security boundary. The architecture/consumer proof must establish its real resource and operational costs before broader support.

All sites on one shared installation initially use one compatible Core/Module/product release. Per-site feature activation is required where justified; arbitrary simultaneous Module versions and process isolation per site are not current requirements.

## Foundation use and extension

AChrix remains a reusable Foundation independent of Rixa and of CMS semantics. Rixa consumes a pinned released Go dependency, selected reusable Modules and intentionally public extension seams. Product source, domain schemas, theme/content behavior and deployment ownership stay in Rixa.

Updates deliberately rebuild and validate a coherent product artifact; product extensions/configuration/data remain independently owned. API/schema changes may require adaptation. A source rollback is not data rollback.

Optional Multi-Site is accepted in the existing AChrix repository/Go module/shared release. Its bounded real-consumer contract/proof is still required. No mandatory Core tenancy, new empty Module, separate repository/service or runtime plugin loader follows from that decision.

## Media publication policy

The owner selected bounded clean PNG/JPEG representations on 2026-10-04. Retain private originals; prepare a separate same-format representation under explicit exact-asset authorization, remove nonessential embedded metadata and follow the finite orientation/color/animation/output/resource profile framed in [the consumer contract](two-site-contract.md#accepted-public-image-policy-and-release-boundary). Preparation and product publication use distinct permissions; exact source binding and private staging never imply future freshness or activation. This is a deliberately narrow preparation flow; general resizing/conversion/transcoding and background processors remain deferred. Public activation, content references, withdrawal and retention stay Rixa-owned. [AChrix #77](https://github.com/AChWorks/achrix/issues/77) owns the exact reusable contract and implementation.

The current reviewed AChrix public surface includes the explicitly selected private-attachment SVG extension accepted under [AChrix #78](https://github.com/AChWorks/achrix/issues/78). It does not silently join the existing common/default upload selection. Structural admission and private attachment do not establish safe inline/public SVG. First public images remain PNG/JPEG; future public SVG needs a real passive publication profile and separate reviewed transport/resource contract.

## Public meaning, SEO and AI retrieval

This requirement applies to the delivered public product, beyond AI-friendly code/project documentation or management APIs.

Keep primary information in readable semantic HTML, with a clear subject and correct canonical URL, language, author/publisher and genuine publication/modification dates when applicable. Preserve useful source context and accessible media descriptions. Derive supported Article/BlogPosting/BreadcrumbList data from the same authoritative content as the visible page.

Corrections, withdrawal and route changes must keep page content, metadata, structured data, sitemap and assets coherent. Never fabricate reviews, prices, availability, authors or other schema facts. Future real commerce pages follow the same truthfulness rule.

Operators own appropriate discovery/index/snippet and provider-specific model-training controls. Robots/noindex and crawler user-agent identity are not access control. Private drafts, previews and site-private data must not leak through any public representation.

Use supported web standards/provider guidance; no separate universal AI schema/file is assumed. No indexing, citation, ranking, recommendation or perfect AI interpretation is guaranteed. Detailed Foundation guidance: [AChrix Web](https://github.com/AChWorks/achrix/blob/main/docs/web/seo-and-semantic-web.md#public-content-and-ai-retrieval).

## Security, resource and engineering constraints

- Human/UI/API/CLI/job/future MCP paths use the same normal Application authorization and domain invariants. AI never receives an implicit permission or consent bypass.
- Validate input, bound work, escape/sanitize real rich output, protect sessions/CSRF/secrets, preserve private storage and use explicit deadlines/error categories.
- Preserve Unicode/Persian/English, RTL/LTR/mixed-text and accessibility in actual supported flows; do not add translations or UI promises without evidence.
- Keep implementation readable, testable and small. Discover existing/native capabilities before dependencies or custom frameworks; preserve compatible behavior and immutable migration identity.
- Omit unused runtime Modules/subsystems. Code presence, capability availability, runtime activation, permission and durable-data removal are distinct. Disabling a feature preserves its state.
- Capacity is hardware/workload-dependent, with no arbitrary built-in site-count ceiling. Illustrations such as 20 sites on 4 GiB/two cores or 1000 on a stronger machine are not measured support claims.
- Higher permitted capacity must not eagerly allocate the maximum. Use native on-demand acquisition/reuse/idle reclamation plus finite admission/backpressure, deadlines and aggregate budgets before considering an adaptive controller.
- Safe operational limits should be explicitly configurable where real deployment needs require it, with validated bounds/defaults and lifecycle semantics. Security floors/invariants are not tuning knobs; future domain quotas may have a separately authorized Settings workflow.
- Select meaningful tests/CI from affected behavior and risk. Reuse unchanged evidence and avoid repetitive broad tests, artificial matrices or absent-product checks.

## Deferred flows and owner gates

| Topic | Preserved direction / execution condition |
| --- | --- |
| Custom roles and site users/registration | Future roles bundle capability/resource grants with bounded delegation/revocation/audit; site admins cannot grant global authority. First release uses fixed permissions and no public registration. |
| Common Media attachments | Reuse the finite AChrix common upload profile; conversion/transcoding/extraction are not currently needed. Recognition is not safety/preview validation. The current reviewed AChrix public surface includes explicit private SVG opt-in under #78; SVGZ and other excluded active formats remain outside that profile. Public images use the selected clean PNG/JPEG seam; upload admission does not grant publication. |
| Search / Settings / Notifications | Implement only a real search flow, concrete application-level settings or real delivery flow; do not create empty shared Modules. Product/domain settings remain product-owned. |
| Commerce and larger CMS | Preserve isolated/extensible site ownership; no empty commerce Module, page builder, marketplace or WordPress feature-parity commitment. |
| Gateway and AI content management | Desired optional installation/composition through normal permissions, supporting future manual/AI management. AChrix #5 still requires its separate ownership/packaging/security decision and real flow; Gateway-side work is out of this repository's authority. |
| Product install/update/recovery UI | Desired simple supported product experience, not server commands for routine users; AChrix #19 stays paused pending separate owner instruction, and #49 stays downstream. Controlled site-transfer proof does not activate them. |
| Hosting | Prefer a small Go management process and static serving. An extra web server is optional when TLS/operations/performance justify it; no claim that web serving is free or that no TLS ingress is needed. |

## Material assumptions

The current reviewed Foundation release, whose exact dependency pin is owned by active implementation state rather than this durable specification, has a Linux/PostgreSQL/private filesystem profile that fits the first consumer. The required Multi-Site resolver, resource configuration and safe public-image preparation seams are present in the selected release; Rixa still owns real product composition, isolation and capacity proof. A shared process is a common failure/maintenance boundary; stronger tenant isolation, huge database fleets or different per-site versions require new evidence and design.

Rixa / Rixa CMS is the accepted name, public under independently selected MPL-2.0. Existing RIXA/Rixa technology name uses were discovered; no trademark/domain availability determination or registration is claimed.

## Documentation authority

Update this specification only for accepted product-level intent changes. Architecture owns detailed decisions/profile; Issues own unresolved work/dependencies/risks; Git/PR/CI/releases own implementation/validation/delivery. The AChrix intake is provenance, not a second Rixa specification. Recover current truth from these sources before acting.
