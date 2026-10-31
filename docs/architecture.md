# Rixa Architecture and Consumption

This document owns the accepted ownership model and starting technical profile. It is not an implemented API inventory. The [two-site consumer contract](two-site-contract.md) records the source-backed request/authorization/composition matrix and bounded Foundation needs for [#2](https://github.com/AChWorks/rixa/issues/2). That contract specifies required behavior; the selected implementation and release own actual runtime availability.

## Ownership and first shape

Use a modular Go product with shared code and a single compatible release. Public HTML artifacts and private management/persistence have distinct admission paths. A maintained lightweight front server may later serve/TLS-route artifacts; native Go serving is the first candidate, not an unsupported zero-web-server promise.

| Owner | Responsibility |
| --- | --- |
| AChrix Core | Composition, capability/resource authorization, bounded lifecycle admission; no CMS model, tenant mandate or crawler subsystem. |
| Optional AChrix Modules | Existing Identity, Audit, Media and Admin seams; Multi-Site through its own accepted contract; real Search/Settings/Notifications/Recovery slices only when their gates are met. |
| Rixa | Site registry/trusted ingress integration, role meanings/policy, content/revisions, themes/configuration, preview, public publication/freshness, operation identity, product delivery and recovery policy. |
| Infrastructure | PostgreSQL and private/public storage, secrets, HTTPS/TLS, optional serving/fronting and resource provisioning; no generic root/shell authority enters Core. |

Keep the product interface small and coherent: reuse supported Admin shell/owning Module seams, improve real editorial/Media/appearance/publication/control tasks, and preserve explicit site/action scope. Modern minimal presentation, recoverable state and Persian/English keyboard/RTL/LTR behavior are product requirements; a frontend framework or shared-shell fork is not implied. The [installation and management contract](installation-and-management.md) owns the accepted terminal/web experience.

## Trusted sites and identity

A request selects an allowlisted configured site through trusted ingress/host routing. An arbitrary Host, forwarded header, site ID, path, asset ID or caller-supplied role cannot establish authorization. Unknown/disabled sites fail closed. Product bindings carry the verified site scope into every operation and owned cache/job/asset path.

Infrastructure operators and site-owned identities/grants are distinct. The first profile needs fixed infrastructure administrator and site administrator permissions; not a universal RBAC/ABAC engine. The consumer contract separates control Identity/Audit from site-owned Identity/Audit and uses distinct exact HTTPS hostnames, verified principal origin and fixed bounded delegation. Path prefixes or different ports cannot isolate the existing host-only root session cookie.

Capabilities identify behavior; permissions/grants allow a principal to act on a resource/scope; roles bundle those grants. Check capabilities/resources, not translated role labels. Site administrators cannot assign global authority or grant rights beyond their delegation. A future custom-role flow must not silently inherit new capabilities.

The first Rixa-owned site-user grant slice stores only explicit per-account `rixa.content.read` and `rixa.content.edit` grants in each site's own database; **no general roles or public registration are introduced**. The same site Application policy checks the account's current AChrix Identity enabled state and the current site grant for every relevant editorial call. Read covers list/head/revision/private preview; edit covers text-only draft create/save and readback of the **same enabled account's own committed private create/save operation receipt**, but does **not** imply content read, Media privileges, publication-intent, public activation, appearance, account administration or unrestricted operation-ledger inspection. Member receipt lookup requires a CURRENT edit grant and a matching saved-revision actor in the owning site's DB; unknown, foreign, publication and appearance operations are never exposed to members. A missing receipt while a write might still be in flight does **not** establish nonexecution or permit retry with a new operation ID. Grant management is a bounded administrator-only in-process service with CAS revision and an atomic site-owned history; no public grant UI, OAuth issuer, MCP endpoint or automatic enrollment follows. An unknown grant-write acknowledgement requires an authorized current-grant read, not a fresh-version retry. A disabled account cannot use retained grants; if explicitly re-enabled its still-allowed grants become effective again. Do not cache grant decisions or convert the control operator into a claimed ordinary user. External identity/account linking and transport-authenticated operation inspection remain separately gated by [Rixa #49](https://github.com/AChWorks/rixa/issues/49) and shared optional OAuth/Bridge investigation by [AChrix #5](https://github.com/AChWorks/achrix/issues/5).


## Site data and composition profile

Start the proof with one PostgreSQL installation and separately addressable site databases plus exclusive private Media roots. Use least-privilege database credentials and explicit site bindings; shared database process/host/admin credentials remain common infrastructure, not hard sandbox isolation.

Each site owns content, local accounts/sessions when enabled, authorization state, Media metadata/originals and future domain schemas. Identity and its Audit stay in the same supported database/transaction boundary. Shared control-plane/operator/site-domain mapping data has declared dependencies; a portable site must not secretly require source-installation global account IDs.

AChrix Application composition permits one provider per capability ID per Application. Use explicit site-bound compositions for single-provider Modules; do not duplicate providers in one Application, tenant-filter SQL by a path prefix, or change qualified tables by search_path. Reusable address-resolution semantics belong to the [AChrix Multi-Site contract](https://github.com/AChWorks/achrix/blob/HEAD/multisite/README.md), not a copied product tenancy framework.

Single-site composition remains supported without Multi-Site. Omit unneeded runtime constructors. Independent module packaging identifies the selected code; it is separate from per-site composition, resource activation, authorization and data removal. Changes to the active graph construct/validate/activate a new Application instance/artifact and retain data by default.

The optional managed Multi-Site contract can authorize inventory and propose validated per-site Module selections. It does not mutate an active Application. Rixa owns persisted revision/CAS, fresh activation authorization, domain draining, controlled reconstruction, readiness, activation state and reconciliation. The current resolver composition remains valid without managed planning. No arbitrary hot loader or per-site version mixing is implied.

## Content, appearance and publication

Rixa owns typed posts/pages, stable identities/revisions and its minimal draft/preview/published/withdrawn state. Real field/default/transition/error semantics are fixed by the consumer contract and editorial implementation contract, not a generic content framework.

Keep one authoritative content source and derive HTML/metadata/structured data from it. Shared theme/template source may produce different site-owned appearance; configuration/custom assets and version dependencies are part of transfer. Do not accept arbitrary uploaded executable templates as a first-version extension mechanism.

Preview is authenticated/private. Publish/correct/withdraw uses explicit capability/resource grants, version/precondition semantics and bounded operation identity. Serialize/reconcile interrupted or unknown effects; do not infer success/rollback from a lost HTTP result or blindly replay uploads/publication.

Rendering is output-specific safe serialization/sanitization with bounded input/work. Public artifact activation must preserve a coherent page/metadata/sitemap/asset revision and explicit freshness/cache/withdrawal behavior, including restart or partial failure. Normal public reads serve only declared public artifacts and need no content SQL or activation of unused management Modules.

Apply [public SEO/AI meaning](PROJECT-SPEC.md#public-meaning-seo-and-ai-retrieval) to actual visible facts. Keep private drafts, preview tokens, site configuration/secrets and other sites' data out of every public representation.

## Media: attachment versus public representation

[AChrix Media](https://github.com/AChWorks/achrix/blob/HEAD/media/README.md) owns a private authorized original-attachment library. Its default profile is PNG/JPEG; explicit common-profile opt-in includes finite common images, documents, archives/audio/video. It does not supply general conversion, sanitization, safe preview or public static URLs. The reviewed public Media surface includes explicit private SVG opt-in while preserving the existing default/common selection; SVGZ/HTML/JavaScript/executables and explicit macro-enabled Office extensions remain excluded, and private SVG grants no rendering/publication permission. Non-PNG/JPEG images in the common profile remain opaque.

Collection discovery, original byte read, mutation and public publication permissions are distinct. Uploader identity/opaque IDs do not grant ownership. Common upload recognition is not malware scanning, complete validity or safety for public inline use.

Never expose private StorageRoot or change authenticated attachment headers into public inline serving by assumption. The source-backed consumer investigation established the need for a separate public-representation preparation boundary. The owner selected bounded clean PNG/JPEG representations with retained private originals on 2026-10-04. [The consumer contract](two-site-contract.md#accepted-public-image-policy-and-release-boundary) maps the required AChrix Media preparation API to distinct preparation permission, exact source binding, finite image/color/privacy behavior and private staging. Public image support is not achieved by documenting a URL. General conversion remains deferred; only the accepted narrow same-format preparation is in scope.

Content references, publication copies and deletion/retention must agree. A private original may need to remain retained independently of a published representation; withdrawal/export cleanup and stale artifact behavior require explicit supported semantics.

## Interface localization

Use the optional released AChrix Admin `localize` package for product and shared presentation catalogs. Product domain resources are composed once per immutable instance, with an optional dedicated private operator catalog root. Locale metadata drives interface language and direction; English is the default/fallback, and installed data-only UI languages require reconstruction rather than rebuilding. UI preferences do not alter site content language, formatting or authorization. The operator CLI/terminal always use English. The [localization guide](localization.md) owns selection, installation and recovery instructions. No new Core locale service, copied browser runtime, watcher or preference database is implied.

## Finite demand-based resources

Foundation database Modules use finite per-instance connection/admission defaults owned by the AChrix operations contract. Those limits are neither user/site counts nor a fleet budget; changing a DSN does not override typed Module bounds. Native pools acquire/reuse/prune connections under their bounds, and started Modules still retain lifecycle resources. Construction itself may also cost work.

The product budget accounts for active sites, all Module/product pools, replicas, migration/maintenance access, hashing/decoding/uploads/publication and serving work. Do not size every site as if it owns the whole host. Bound ingress, concurrency, queued/backlogged work, deadlines and fairness; protect other sites during a busy site's load.

Prefer native demand-based mechanisms and explicit finite operator ceilings. Raising a maximum may permit higher peak usage and incur setup overhead; it is not throughput or zero-memory proof. Distinguish reserve/minimum from maximum/current use, verify post-burst reclamation and avoid promising immediate RSS reduction. No centralized adaptive controller is a bootstrap requirement.

Typed validated operator tuning should preserve documented defaults and compatibility, reject invalid/unbounded input and define construction/activation semantics. Pool and expensive-work admission must be considered together. Domain quotas may later expose an authorized settings workflow; secrets, authentication floors, protocol/storage invariants and raw deployment budgets are not site-editor settings.

AChrix exposes instance-owned typed `MaxConns`/`MaxOperations` with preserved finite defaults, nested-lease minima and independent security/decoder bounds. Foundation default/raised composition observations verify native demand/reuse/reclamation; Rixa's executable and operational proof still owns actual product/aggregate evidence, not a fleet promise from a Foundation fixture.

## Supported starting development profile

Executable Rixa must use the selected Foundation dependency's [supported environment](https://github.com/AChWorks/achrix/blob/HEAD/docs/operations/operability-performance.md#supported-environment) and the required private-filesystem durability/locking semantics, rather than copying those version pins into this product architecture. Rixa verifies its own product-specific profile through executable evidence.

Use verified PostgreSQL TLS for remote hosts, protected secrets and trusted HTTPS ingress. Explicit migrations run with deadlines before activation; request handling/startup does not install schema. Preserve immutable migration identities and retained relationships. Readiness precedes ingress; ingress/domain draining precedes Module shutdown. Context cancellation does not forcibly interrupt arbitrary blocking I/O.

No production hosting, RPO/RTO, live upgrade or off-host recovery profile is established by documentation.

## Dependency upgrades and extension ownership

The reviewed starting baseline for executable Rixa work is the exact released AChrix dependency recorded by the active implementation Issue and machine-readable dependency metadata. That selected release must supply the required resolver, resource configuration, private SVG, clean public-image preparation and reviewed Module error-privacy corrections. Initial executable consumption must pin that exact released dependency with product-owned go.mod/go.sum. Do not use replacement workspaces, copied Core runtime, private imports or edited dependency caches. Core and the selected official components have independent Go module/version units in one repository. Pin one compatible version per selected module, retain checksums and record the complete selected component set alongside product/build and immutable migration identities. A monolithic-to-split transition adopts the split root and its compatible selected children together; mixing an old root that contains those packages with split child modules is unsupported.

To extend, use public interfaces/typed collaborators. Keep content/theme/product policy local. When no public seam fits, record a concrete minimal AChrix contract gap and obtain a compatible released dependency before claiming it is available. Changing Foundation repository source or documentation does not update an immutable release or a deployed product.

An update reviews the exact released target and relevant API/dependency/schema changes, adapts only affected product code, rebuilds/tests a coherent artifact and stages explicit migrations/activation under the supported lifecycle profile. Product source/configuration is not replaced by a dependency update. Published compatibility commitments remain immutable; breaking Foundation changes follow the Foundation lifecycle/versioning policy.

## Selected-site draft presentation

The control-origin saved-content/publication workspace delegates through normal ControlService selected-site authorization to the existing site-owned EditorialService/PublicationService and origin-qualified operator. The Admin request retains the authenticated control principal. One validated path supplies the site target; no global selection or universal site session is introduced. Direct-site and selected-control adapters reuse one product content presenter with an optional typed publication seam; the selected control adapter rechecks site authorization and performs no independent publish/intent implementation. Selected-site public changes bind a reviewed content head/intent revision and version to the currently active generation under the existing publication source gate; site-owned generation activation always covers the coherent site-wide public snapshot, never only one row. Bounded keyset content/history pages preserve existing APIs and immutable revisions. Recovery retains exact decimal CAS and immutable same-operation submitted data; missing acknowledgements are inspected before deliberate same-ID replay. Preview link resolution is product-owned and preserves stored source and private originals. [The workflow guide](selected-site-content.md) owns user-facing behavior.

## Installation and hosting boundary

Installation is a deployment concern and must not leak host/root authority into Core or site capabilities. AChrix Installation owns pure requirement/evidence/adapter/progress assessment, immutable plans and rechecking. It performs no host discovery, provisioning, persistence, migration or activation. Rixa owns trustworthy environment probes, selected supported adapters, protected configuration, durable intent/progress, locking/fencing, actual effects, migration/bootstrap reconciliation, readiness and activation. All install surfaces consume those same product operations.

Preflight classifies each mandatory capability as already available, safely provisionable by the installer, operator/provider-required, or unsupported. On a privileged supported server the installer may provision missing dependencies through validated OS/service mechanisms. On constrained hosting it performs no privilege bypass; it returns the exact requirements the operator or hosting provider must supply.

Control-panel adapters reuse this installer domain and only integrate through supported panel/host mechanisms. Detection of aaPanel, Plesk, DirectAdmin, cPanel or another panel is contextual evidence, not proof that the account can run a persistent Go service, obtain PostgreSQL, store secrets privately, or receive safe reverse-proxy/TLS ingress. Existing compatible panel-managed services should be reused rather than replaced.

The target user experience includes a one-line server/VPS bootstrap and panel-assisted upload/terminal/web setup where the validated environment permits it. A browser setup wizard may collect installation inputs only after the environment can actually host the persistent Rixa runtime; it is not a workaround for an incompatible shared-hosting model.

Install, update and uninstall share product operation identity, progress and diagnostics while retaining distinct effects and recovery semantics. The [experience contract](installation-and-management.md) owns the stable bootstrap, guided TUI/CLI/web flow, state-preserving update and ownership-aware removal. A web operation uses a narrow supported deployment mechanism that survives application replacement; it grants no root/shell proxy. Actual implementations and support remain profile-specific and evidence-gated. The [shared setup operation](setup-operations.md) assesses supplied runtime prerequisites and durably creates one protected configuration, binding the exact executing artifact, candidate bytes and target under an installation lock. It does not execute a generic workflow or provision a service. Its readiness concerns configuration preparation; actual runtime readiness still precedes TLS bind, and a bind failure shuts down the runtime within its deadline. No universal updater, registry or production recovery promise follows from this architecture.


The concrete native development updater uses the same lifecycle lock with a separate strict update journal and pending/active receipts. It preserves the original installation/frozen manager and binds actual retained configuration, physical stores and authenticated candidate compiled identity. The candidate runs its own check before explicit migration under a disabled, quiescent prior service; activation uses the exact candidate and postflight. This first same-schema profile rejects unsupported transitions. After migration intent, only fenced forward continuation is supported; source/binary reversal is not database recovery. [Native management](server-management.md#compatible-native-update) owns supported inputs and actionable limits.

The Rixa-owned bootstrap router discovers protected native installation, pending/active update and PostgreSQL prerequisite records before selecting an interface. It validates operation identity and frozen manager ownership; ambiguous IDs or unknown material fail closed. Retained work routes to its exact manager. Preparing a new install or explicit update authenticates protected archive copies through the existing publisher policy and stages a content-addressed manager with atomic no-replace activation. Preparation itself does not apply or resume lifecycle effects.

The immutable initial entry authenticates its pinned starter/helper archives before exact-member extraction, then delegates candidate selection and all operations to the starter. Its root-private `/opt/rixa-entry/<generation>` cache retains verified tools because saved reviews may still name the initial helper before Apply freezes a separate copy. The initializer lock protects only those cache bytes; it is not a native lifecycle lock or another provenance/operation engine. The [entry guide](bootstrap-entry.md) owns generation-specific anchors and operator requirements.

`internal/releasepackage` is the single Rixa-owned authenticated package parser used by native installation, update and bootstrap staging. It checks bounded extraction, source/target metadata, actual compiled component identity and retained legal material after publisher authentication. Consumer-specific compatibility checks remain at the native operation boundary: the legacy installation profile retains its exact manager component set, while supported candidate profiles bind their complete authenticated graph. The parser grants no deployment authority and is not another provenance verifier or Foundation runtime.

The narrow PostgreSQL prerequisite adapter has a separate strict private journal under the native manager state root and shares the native lifecycle lock. It consumes the released AChrix Installation assessment/progress contracts, records attempts before fixed package/cluster/role/database effects, and uses a supervised exact-manager executor plus a read-only exact-package guard. Existing/shared services and ambiguous partial states require compatible supplied databases or operator reconciliation. Generated credentials flow only through a verified private handoff into the native install draft; public discovery/status and normal application capabilities do not expose them or gain root authority. This is an explicit product adapter, separate from the configuration-only shared setup operation. Source/unit evidence alone does not establish a supported provisioning profile; [native management](server-management.md#bootstrap-starter-and-postgresql-prerequisite-source) owns its operational limits.

## Portable site capture and controlled restore

The product capture profile includes the site database/Module ledgers, content/Media references and original files, users/grants, theme/config/custom-asset dependencies and protected runtime/key requirements. Global operator references must be recreated/remapped explicitly; do not put raw secrets in a public descriptor/manifest. Disabling a Module retains its state.

A database dump alone or files copied at a different point is not coherent site recovery. Use maintained native tools with declared quiescence/consistency, integrity and scope. Restore only into empty separate compatible test targets in the initial proof; check safe entries/permissions, identities/migration compatibility, references and assets, credentials/session/revocation implications, then readiness and authorized behavior before ingress.

This limited independence proof does not by itself activate a generic updater/recovery subsystem, production recovery promises or live destructive restore/activation.

The later operational-independence outcome also owns retained shared-update proof: consume one genuine compatible reviewed released Foundation update through the shared product build and verify every site's data/assets/grants/configuration and Module ledgers. Initial executable composition does not wait for this later proof; an unavailable compatible update remains a narrow outstanding criterion, without fabricating a release or activating a generic update UI.

## Open coordination

The detailed consumer contract is [two-site-contract.md](two-site-contract.md); its public-image and Module-safety requirements are supplied by the selected reviewed AChrix dependency, while executable Rixa consumption/product proof remains GitHub-owned work, linked by [Roadmap](roadmap.md). Koinon defines ecosystem discovery/contract guidance but is not a runtime dependency or writable target. Gateway-side work remains separately owned and requires its own ownership/security decision before implementation.
