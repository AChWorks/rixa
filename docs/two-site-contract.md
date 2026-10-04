# First Two-Site Consumer Contract

This is the bounded technical contract for [Rixa #2](https://github.com/AChWorks/rixa/issues/2), under the accepted [product specification](PROJECT-SPEC.md) and [architecture](architecture.md). It describes future behavior, not a runnable implementation. GitHub owns remaining decisions/state; [AChrix #77](https://github.com/AChWorks/achrix/issues/77) owns the unresolved public-image policy. Do not silently select either option.

## Source identity and coverage

Inspection uses published AChrix `v0.2.0` at `6624e4b40de33b92e3da0d187e10a667ca798981`. Foundation main at `d41631f845ac21a4b530fca04cd7d965fce02209` adds documentation, not these missing runtime APIs.

| Existing public source | What the first consumer may use | Gap / owner |
| --- | --- | --- |
| [Core](https://github.com/AChWorks/achrix/blob/6624e4b40de33b92e3da0d187e10a667ca798981/achrix.go): `New`, `Application.Start/Ready/Authorize/Shutdown` | Immutable graph; one provider/revision per capability; caller deadline; fail-closed policy and bounded lifecycle admission | No Core tenancy extension needed. Product domain work still needs its own ingress/drain boundary. |
| [Identity](https://github.com/AChWorks/achrix/blob/6624e4b40de33b92e3da0d187e10a667ca798981/identity/identity.go): `NewService`, accounts, sessions and protected exact-login reconciliation | One database-owned identity namespace; successful login is not a grant | Fixed roles/grants and verified principal origin belong to Rixa. No shared identity service or custom-role engine is implied. |
| [Identity Web](https://github.com/AChWorks/achrix/blob/6624e4b40de33b92e3da0d187e10a667ca798981/identity/http.go): `NewWeb`, `AuthenticateRequest` | Exact HTTPS origin/Host, host-only root cookie, session-bound CSRF | Separate hostnames for separate authentication boundaries; path prefixes and different ports do not isolate cookies ([RFC 6265 §8.5](https://www.rfc-editor.org/rfc/rfc6265.html#section-8.5)). |
| [Audit](https://github.com/AChWorks/achrix/blob/6624e4b40de33b92e3da0d187e10a667ca798981/audit/audit.go): `NewService`, `Prepare`, transaction append | Identity and its Audit use the same supported database/transaction profile | Product accountability must define atomicity/reconciliation per mutation; logging is not a transaction. |
| [Admin](https://github.com/AChWorks/achrix/blob/6624e4b40de33b92e3da0d187e10a667ca798981/admin/admin.go), Identity/Media owning surfaces | Existing same-origin shell and private forms; typed product surfaces for real content/control operations | Navigation permission is not domain permission; no global account/site selector is supplied. |
| [Media](https://github.com/AChWorks/achrix/blob/6624e4b40de33b92e3da0d187e10a667ca798981/media/media.go): `Create/List/Status/Read/Delete/Reconcile` | Private attachment admission, exact-asset reads and conditional deletion | Public image representation/export is absent: AChrix #77. |
| No current Multi-Site package | Deterministic immutable address-to-site resolution is the bounded reusable need | AChrix #4; do not invent a callable API from an ownership label. |
| Current database Module configuration | Fixed max four/min zero pools and existing finite work/hash/decoder admission | Validated operational tuning: AChrix #76, after actual initial measurements. |

The existing Go declarations own exact signatures. These proposed contracts do not add methods to a published version; consume the released extension only after its own validation/review/integration/release gates.

## Request and composition boundary

Use distinct configured HTTPS hostnames for the infrastructure administration origin and each site's administration/public origin. Development names and certificates are disposable proof inputs; no real domains, proxy trust or production TLS profile are established here. An administration hostname must not be one of the site aliases. Require exact configured authority, including an intentional port; do not infer authority from redirects, forwarded headers, DNS lookup, suffix matching or attacker-selected configuration.

1. Bounded product ingress verifies real TLS and an exact allowed host, then resolves one immutable site binding. Unknown, disabled, ambiguous or malformed address claims fail before site storage or identity work.
2. A lightweight routing Application may contain only the optional Multi-Site resolver and its routing-only Policy. The trusted routing actor admits address resolution, never content/account/Media access. Public routing therefore does not need an Identity/Audit/Media database call.
3. A separate control Application owns infrastructure Identity plus its joint Audit and real product control capabilities.
4. Each enabled site's management Application explicitly wires one Identity, its joint Audit, optional Media and actual product domain components. Each gets its own database credential/profile, private Media root and product Policy. It is never two Identity/Media providers in one Application.
5. Single-site mode binds one configured site directly and omits Multi-Site. It preserves the same private/public and authorization boundaries.

Control data owns the declared site-to-database/root/host bindings, not site content or local accounts. Do not put raw DSNs/keys in a public descriptor. Site registration/schema installation is explicit operator work before readiness, not a public request or startup side effect.

A site administrator authenticates with that site's Identity/Web. An infrastructure administrator authenticates only with control Identity/Web; site cookies are not global credentials. The product creates a trusted, origin-qualified operator identity for site operations after revalidating its control session and fixed infrastructure grant. Local Identity operations retain their own account IDs/self-service semantics. Never promote a bare account ID to global authority, accept a principal/role/proof from a body/header, or confuse equal IDs from different stores.

A product-owned control surface may select a declared site by a validated stable ID. Its navigation/control capability is declared by a real product component; each selected domain call then enters that site's public Service and normal Application Policy. It does not impersonate a site session, bypass target authorization, or turn Admin into a cross-site service locator.

## Principal, capability and resource matrix

Labels below express product policy, not new AChrix constants. Actual product capabilities are declared with their real implementation, before a surface references them.

| Verified request origin / principal | Exact allowed scope | Must deny |
| --- | --- | --- |
| Control-origin infrastructure administrator | Explicit declared-site control; selected site's actual content/publication, Identity management and Media operations under that site's Policy | Unknown/disabled sites; arbitrary DB/path/config target; caller-supplied global role; raw shell/root access |
| Site A origin / Site A administrator | A's content/preview/publish and appearance; A's separately granted private `media.List`, exact-ID `media.Read/Delete`, `media.Create`; only the explicitly delegated local account actions | B's collections, bytes, accounts, grants and settings; control authority; global role assignment; publication inferred from upload/read |
| Site B origin / Site B administrator | The symmetric B-only permissions | The symmetric A/control denials |
| Anonymous reader at a known site | Only the active site's declared published HTML/asset manifest | Identity/account/Media collection access, originals, drafts, previews, control surfaces, private metadata |
| Identity `PublicPrincipal` | Only `identity.Authentication` with its expected empty target, in that Identity's own Application | Every product/domain right and success identity |
| Trusted operator maintenance command | Only its explicitly selected site, migration/reconciliation capability and bounded lifecycle context | Inferred authority from filesystem access; replay of an unknown mutation; unreviewed restore/release |

No grant follows merely from a filename, uploader, opaque ID, navigation visibility or address resolution. Collection metadata permission and exact asset-byte/deletion permission stay separate. Permission revocation/disabled accounts fail on the next authenticated operation; in-flight mutations require their declared precondition/fence and cannot claim instant cancellation of committed effects.

A real negative test matrix covers A-token-at-B, B-token-at-A, site-token-at-control, control-token-at-site-login, unknown/disabled host, duplicate mapping, forged site/principal/role/forwarded context, other-site IDs and collection routes, denied read/delete versus allowed list, and drafts/assets absent from anonymous output. Equal-looking local IDs never change the trusted store/binding.

## Bounded optional Multi-Site slice

[AChrix #4](https://github.com/AChWorks/achrix/issues/4) owns the reusable implementation contract:

- An immutable explicitly supplied finite site/address inventory returns a stable site identifier for one exact known active address. Validate malformed/duplicate/ambiguous site/address configuration before runtime effects; return no secret binding data.
- Address resolution grants no domain authority. Its typed Service, if composed through Core, uses a separately declared routing capability and normal deadline/admission Policy. The ingress/transport trust boundary remains product-owned.
- No database, site lifecycle manager, roles/accounts, CMS routes, storage router, dynamic registry, DNS/proxy authority, server administration or mandatory Core Site type belongs to this first slice.
- No implicit code/config discovery or process-global registration. Configuration changes construct a new validated composition; omission remains valid in the real single-site consumer.
- Rixa owns the stable identifier-to-Application/credentials/root bindings, site-owned state and aggregate resource/drain budget. Media owns its files; Multi-Site does not reach into them.

Order: resolve this consumer contract and Foundation's bounded contract; integrate/release the minimal required Foundation implementation; consume it in Rixa #3; then prove shared-update/retained-state acceptance. Closure of the complete Foundation #4 outcome is not a prerequisite for the consumer proof that closes it. The public-image decision gates image preparation/publication; it does not invent a Core or Gateway dependency.

## Lifecycle, resources and configuration

Explicit bounded migrations precede construction/activation. Construct selected instances/services, start under finite phase budgets, verify readiness, then admit management traffic. Constructors can perform real setup (including Identity hash work); do not advertise zero construction cost. Media starts its exclusive root and pools perform their required startup checks.

The first proof starts only declared active management compositions. Omitted features create no corresponding Module instance/pool/store/worker. Started-but-idle instances retain resources: minimum-zero pool configuration is neither zero connection count after startup nor hot unloading. Public artifact reads do not invoke these management Services.

Two sites with Identity/Audit/Media and control Identity/Audit mean eight participating Module pools: fixed maxima could permit 32 connections in total, not 32 eagerly opened connections. Product persistence, migration/maintenance access and replicas are additional. Rixa #3 records an initial idle/demand sample; #6 owns aggregate budget/fairness/saturation/deadline/reclamation evidence. #76 receives exact missing knobs, not a speculative adaptive controller. No fixed fleet/site-count support is promised.

Validate operator configuration before owned effects: unique hosts/site IDs; distinct exclusive absolute private roots; explicit least-privilege database/secret references; finite startup/shutdown, ingress, pending work and expensive computation budgets; selected features and exact dependency identities. Domain quotas and site appearance are separate from deployment secrets/security floors.

During stop/replacement, close ingress, drain/cancel product work and publication, then shut down the affected Applications in bounded order. Partial start cleans acquired resources. Failed cooperative drain/stop remains explicit unavailable/operator recovery state; a context is not a forced I/O/decoder kill. Activation does not replay unknown effects or migrate automatically.

## Content and static publication contract

Rixa owns stable site-local content IDs, immutable saved revisions and a compare-and-set head. A saved unpublished revision never changes the active public revision. Conflict/lost acknowledgement requires reading the retained operation/revision, never blind replay. The actual schema/operation IDs are supplied by #4, not by a universal content registry.

Private preview renders a selected saved revision under site authorization, no-store and safe output rules; no public preview token or SQL-backed public URL is assumed. Fixed editor/publisher rights are explicit; a future author role does not inherit publication by name.

Publish renders a declared source revision plus theme/config version and selected authorized image representations into a bounded private staging artifact set. HTML, metadata, truthful structured data, sitemap and asset manifest share one generation. Activate only a complete durable generation, with an expected current generation and an operation identity; define restart/commit-acknowledgement reconciliation in #5. Old edits do not overwrite a newer active generation.

Correct/reroute/withdraw updates the active route/manifest coherently. Public serving accepts only active routes and referenced asset entries; old guesses/history directories are not enumerable serving roots. Withdrawal removes local route/asset reachability when references no longer require an asset. Browser/search/external copies cannot be retroactively erased; avoid long immutable public caching in the first withdrawal-sensitive profile and declare actual cache freshness. Public historical URLs require a separate accepted policy.

Original deletion is denied while retained content/public representations require it, unless an explicit domain operation first reconciles those relationships. The stock Media deletion surface must not bypass that product retention rule. Publication copies never mutate private originals. Site capture includes the relevant source, representations, manifests, identity/grant and runtime/key dependencies.

## Public-image decision and release boundary

Media's existing `Read` remains an authenticated original attachment. No StorageRoot serving, guessed export, opaque-inline WebP/AVIF/SVG or read-to-publish permission is allowed. The concrete proposed clean representation versus original export policy and its tradeoffs live only in [AChrix #77](https://github.com/AChWorks/achrix/issues/77), pending owner decision. It changes processing/privacy scope and must be explicit before implementation.

This decision needs no generic processor, video/document conversion, registry, queue or new repository. Other common attachments stay private in the first profile. Future formats get their own validity, metadata, appearance, work and compatibility contract.

After the decision, update the affected product specification only for the accepted product-level change and finalize the smallest Media contract. Exact HIGH runtime candidates need independent read-only security/correctness review and applicable integration approval. New published APIs require a reviewed release before Rixa consumes them; neither these docs nor Foundation main update `v0.2.0`.
