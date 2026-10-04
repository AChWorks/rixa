# Rixa Architecture and Consumption

This document owns the accepted ownership model and starting technical profile. It is not an implemented API inventory. The [two-site consumer contract](two-site-contract.md) records the source-backed request/authorization/composition matrix and bounded Foundation needs for [#2](https://github.com/AChWorks/rixa/issues/2). It is a design contract, not implemented runtime.

## Ownership and first shape

Use a modular Go product with shared code and a single compatible release. Public HTML artifacts and private management/persistence have distinct admission paths. A maintained lightweight front server may later serve/TLS-route artifacts; native Go serving is the first candidate, not an unsupported zero-web-server promise.

| Owner | Responsibility |
| --- | --- |
| AChrix Core | Composition, capability/resource authorization, bounded lifecycle admission; no CMS model, tenant mandate or crawler subsystem. |
| Optional AChrix Modules | Existing Identity, Audit, Media and Admin seams; Multi-Site through its own accepted contract; real Search/Settings/Notifications/Recovery slices only when their gates are met. |
| Rixa | Site registry/trusted ingress integration, role meanings/policy, content/revisions, themes/configuration, preview, public publication/freshness, operation identity, product delivery and recovery policy. |
| Infrastructure | PostgreSQL and private/public storage, secrets, HTTPS/TLS, optional serving/fronting and resource provisioning; no generic root/shell authority enters Core. |

The UI is deliberately small: reuse the Admin shell/owning Module surfaces where they fit; add the actual visual editorial surface without adopting a frontend framework merely for a hypothetical larger CMS.

## Trusted sites and identity

A request selects an allowlisted configured site through trusted ingress/host routing. An arbitrary Host, forwarded header, site ID, path, asset ID or caller-supplied role cannot establish authorization. Unknown/disabled sites fail closed. Product bindings carry the verified site scope into every operation and owned cache/job/asset path.

Infrastructure operators and site-owned identities/grants are distinct. The first profile needs fixed infrastructure administrator and site administrator permissions; not a universal RBAC/ABAC engine. The consumer contract separates control Identity/Audit from site-owned Identity/Audit and uses distinct exact HTTPS hostnames, verified principal origin and fixed bounded delegation. Path prefixes or different ports cannot isolate the existing host-only root session cookie.

Capabilities identify behavior; permissions/grants allow a principal to act on a resource/scope; roles bundle those grants. Check capabilities/resources, not translated role labels. Site administrators cannot assign global authority or grant rights beyond their delegation. A future custom-role flow must not silently inherit new capabilities.

## Site data and composition profile

Start the proof with one PostgreSQL installation and separately addressable site databases plus exclusive private Media roots. Use least-privilege database credentials and explicit site bindings; shared database process/host/admin credentials remain common infrastructure, not hard sandbox isolation.

Each site owns content, local accounts/sessions when enabled, authorization state, Media metadata/originals and future domain schemas. Identity and its Audit stay in the same supported database/transaction boundary. Shared control-plane/operator/site-domain mapping data has declared dependencies; a portable site must not secretly require source-installation global account IDs.

AChrix currently permits one provider per capability ID per Application. Use explicit site-bound compositions for current single-library Modules; do not duplicate providers in one Application, tenant-filter SQL by a path prefix, or change qualified tables by search_path. Any shared Multi-Site contract refinement is owned by [AChrix #4](https://github.com/AChWorks/achrix/issues/4), not a copied product tenancy framework.

Single-site composition remains supported without Multi-Site. Omit unneeded imports/runtime constructors where useful. Packaged-but-disabled code does not mean independent package versions, hot unloading, deleted data or bypassed dependencies. Changes to the active graph normally construct/validate/activate a new Application instance/artifact. Deferred activation is earned by measured resource savings and must retain bounded readiness, draining and cleanup; do not assume it exists in v0.2.0.

## Content, appearance and publication

Rixa owns typed posts/pages, stable identities/revisions and its minimal draft/preview/published/withdrawn state. Real field/default/transition/error semantics are fixed by the consumer contract and editorial Issue, not a generic content framework.

Keep one authoritative content source and derive HTML/metadata/structured data from it. Shared theme/template source may produce different site-owned appearance; configuration/custom assets and version dependencies are part of transfer. Do not accept arbitrary uploaded executable templates as a first-version extension mechanism.

Preview is authenticated/private. Publish/correct/withdraw uses explicit capability/resource grants, version/precondition semantics and bounded operation identity. Serialize/reconcile interrupted or unknown effects; do not infer success/rollback from a lost HTTP result or blindly replay uploads/publication.

Rendering is output-specific safe serialization/sanitization with bounded input/work. Public artifact activation must preserve a coherent page/metadata/sitemap/asset revision and explicit freshness/cache/withdrawal behavior, including restart or partial failure. Normal public reads serve only declared public artifacts and need no content SQL or activation of unused management Modules.

Apply [public SEO/AI meaning](PROJECT-SPEC.md#public-meaning-seo-and-ai-retrieval) to actual visible facts. Keep private drafts, preview tokens, site configuration/secrets and other sites' data out of every public representation.

## Media: attachment versus public representation

[AChrix Media](https://github.com/AChWorks/achrix/blob/v0.2.0/media/README.md) owns a private authorized original-attachment library. Its default profile is PNG/JPEG; explicit common-profile opt-in includes finite common images, documents, archives/audio/video. It does not supply conversion, sanitization, safe preview or public static URLs. SVG/SVGZ/HTML/JavaScript/executables and explicit macro-enabled Office extensions are excluded. Non-PNG/JPEG images in the common profile remain opaque.

Collection discovery, original byte read, mutation and public publication permissions are distinct. Uploader identity/opaque IDs do not grant ownership. Common upload recognition is not malware scanning, complete validity or safety for public inline use.

Never expose private StorageRoot or change authenticated attachment headers into public inline serving by assumption. The source-backed consumer investigation identified the missing public representation/export contract, owned by [AChrix #77](https://github.com/AChWorks/achrix/issues/77). Its concrete processing/privacy policy requires owner decision before implementation. Public image support is blocked on that contract, not achieved by documenting a URL. Conversion remains deferred; do not build a processor for a hypothetical requirement.

Content references, publication copies and deletion/retention must agree. A private original may need to remain retained independently of a published representation; withdrawal/export cleanup and stale artifact behavior require explicit supported semantics.

## Finite demand-based resources

Current v0.2 database Modules force per-instance maximum four connections with minimum zero. Those limits are neither user/site counts nor a fleet budget; changing the DSN does not override the Module cap. Current native pools already acquire/reuse/prune connections under their bounds, and started Modules still retain lifecycle resources. Construction itself may also cost work.

The product budget accounts for active sites, all Module/product pools, replicas, migration/maintenance access, hashing/decoding/uploads/publication and serving work. Do not size every site as if it owns the whole host. Bound ingress, concurrency, queued/backlogged work, deadlines and fairness; protect other sites during a busy site's load.

Prefer native demand-based mechanisms and explicit finite operator ceilings. Raising a maximum may permit higher peak usage and incur setup overhead; it is not throughput or zero-memory proof. Distinguish reserve/minimum from maximum/current use, verify post-burst reclamation and avoid promising immediate RSS reduction. No centralized adaptive controller is a bootstrap requirement.

Typed validated operator tuning should preserve current defaults and compatibility, reject invalid/unbounded input and define construction/activation semantics. Pool and expensive-work admission must be considered together. Domain quotas may later expose an authorized settings workflow; secrets, authentication floors, protocol/storage invariants and raw deployment budgets are not site-editor settings.

The real consumer proof owns initial measurements and aggregate limits; [AChrix #76](https://github.com/AChWorks/achrix/issues/76) owns proven missing reusable configuration seams. [#6](https://github.com/AChWorks/rixa/issues/6) owns product evidence and links its Foundation dependency.

## Supported starting development profile

The selected Foundation developer line is Go 1.27.1, PostgreSQL 18 UTF-8, Linux amd64 and private local filesystem storage with its required durability/locking semantics. Follow the [owning supported matrix](https://github.com/AChWorks/achrix/blob/v0.2.0/docs/operations/operability-performance.md#supported-environment), not an invented broader hosting promise. First runnable Rixa will verify its product-specific profile.

Use verified PostgreSQL TLS for remote hosts, protected secrets and trusted HTTPS ingress. Explicit migrations run with deadlines before activation; request handling/startup does not install schema. Preserve immutable migration identities and retained relationships. Readiness precedes ingress; ingress/domain draining precedes Module shutdown. Context cancellation does not forcibly interrupt arbitrary blocking I/O.

No production hosting, RPO/RTO, live upgrade or off-host recovery profile is established by documentation.

## Dependency upgrades and extension ownership

Initial executable consumption pins `github.com/AChWorks/achrix@v0.2.0` with its own go.mod/go.sum. Do not use replacement workspaces, copied Core runtime, private imports or edited dependency caches. Official packages in one Foundation Go module share a compatible version; retain source-backed product/Core/Module/migration identities.

To extend, use public interfaces/typed collaborators. Keep content/theme/product policy local. When no public seam fits, record a concrete minimal AChrix contract gap and obtain a compatible released dependency before claiming it is available. Adding docs to Foundation main does not update an immutable release or a deployed product.

An update reviews the exact released target and relevant API/dependency/schema changes, adapts only affected product code, rebuilds/tests a coherent artifact and stages explicit migrations/activation under the supported lifecycle profile. Product source/configuration is not replaced by a dependency update. Existing v0 published compatibility lines are preserved; a breaking Foundation change uses its next appropriate v0 minor.

The desired later simple in-product install/update experience is separately gated by AChrix #19. This bootstrap supplies no updater, build service, registry or generic host control.

## Portable site capture and controlled restore

The product capture profile includes the site database/Module ledgers, content/Media references and original files, users/grants, theme/config/custom-asset dependencies and protected runtime/key requirements. Global operator references must be recreated/remapped explicitly; do not put raw secrets in a public descriptor/manifest. Disabling a Module retains its state.

A database dump alone or files copied at a different point is not coherent site recovery. Use maintained native tools with declared quiescence/consistency, integrity and scope. Restore only into empty separate compatible test targets in the initial proof; check safe entries/permissions, identities/migration compatibility, references and assets, credentials/session/revocation implications, then readiness and authorized behavior before ingress.

This limited independence proof does not activate paused [AChrix #19](https://github.com/AChWorks/achrix/issues/19), downstream [#49](https://github.com/AChWorks/achrix/issues/49), production recovery promises or live destructive activation.

## Open coordination

The detailed consumer contract is [two-site-contract.md](two-site-contract.md); its unresolved public-image decision and executable proof remain GitHub-owned work, linked by [Roadmap](roadmap.md). Koinon defines ecosystem discovery/contract guidance but is not a runtime dependency or writable target. Gateway-side work remains separately owned; optional future integration still needs AChrix #5's owner/security decision.
