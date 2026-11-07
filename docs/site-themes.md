# Code-first site themes: accepted design contract

**State:** Owner-approved product direction; **NOT implemented in the current released development profile**. [Rixa #74](https://github.com/AChWorks/rixa/issues/74) owns implementation, exact tests, review and delivery. This document is the product-owned architectural contract, not evidence that any UI, HTTP endpoint, MCP tool or published code-theme artifact exists. The [human and AI usage guide](site-themes-usage.md) gives future operator journeys and examples; use the exact selected release's instructions when this feature ships.

## Goal and ownership

Let a trusted site administrator build a distinctive, responsive public website with **complete authored HTML, CSS and browser JavaScript**, while Rixa continues to own content, revisions, site settings, identity, private Media, SEO truth, and safe static publication. A post/page editor is not a template-code editor. This must work for a single site or an isolated site in a multi-site Rixa installation, without rebuilding Rixa for every design change.

**Rixa owns** theme source, site-owned configuration and assets, template bindings, permissions, rendering, publication, export and restore. Reuse the existing Rixa editorial operation ledger, publication snapshots/generations and supported AChrix components. Do **not** create an AChrix Theme Module, new Foundation, runtime plugin loader, page builder, frontend framework requirement, PHP/Go code execution, server-side JavaScript interpreter, or arbitrary SQL/file/network access from templates.

The first code theme and the built-in default presentation must use one publication pipeline; the built-in profile remains available as a safe fallback. Existing installations keep their effective appearance until their operator explicitly enables a code theme. Source revisions and public activation are separate.

## Model: six source slots, one site-owned theme

| Source slot | Purpose | Notes |
| --- | --- | --- |
| `head.html` | Additive public `<head>` markup | Rixa retains canonical URL, title/description defaults, robots policy, trustworthy structured data, and CSP authority. Conflicting reserved metadata must be rejected or diagnosed, not silently override Rixa. |
| `header.html` | Public header/navigation markup | Full trusted HTML, optional data bindings; not the authenticated admin shell. |
| `body.html` | One shared **public page layout** | Receives a discriminated home / post / page render context. Must insert required public content (or home listing) exactly once; no hidden parallel templates for the first release. |
| `footer.html` | Public footer markup | Same trusted rendering and scope. |
| `site.css` | Authored site-wide CSS | Emitted as a content-addressed public asset, including usable RTL, responsive and print rules when authored. |
| `site.js` | Authored browser JavaScript | Emitted as a content-addressed public asset, with explicit execution/security policy. Runs in the **public visitor** context only, never the Go process or an authenticated management origin. |

A theme is a single site-scoped, immutable revision of these six inputs, with explicit bounded referenced assets and any associated theme settings. Empty optional slots are valid; the Body template and its required content rendering are not optional. A **required portable source import/export format** contains the six relative files, a small version descriptor, and authorized referenced assets; it is source material, **not** an executable plugin. Keep parsing bounded, reject path traversal/symlinks and reject unsupported format revisions. Import/export must be implemented and tested for the intended human/AI workflow; it is not a present command.

Use Go's supported `html/template` engine for HTML template slots, with a deliberately small registered read-only function/binding set. Do not expose `text/template` unsafely for HTML, reflection-like object lookup, runtime source loading, filesystem paths, environment variables, arbitrary template-defined privileged functions, direct database queries, or arbitrary network access. Template parse, bounds, missing bindings and output validation must fail clearly before publication.

### Site render context

This is the **proposed versioned public binding contract** for the first implementation; no released Rixa build supports these template expressions yet. Field names or semantics may be refined in #74 before the first public implementation, with matching docs/tests/versioning.

| Namespace | Deliberate safe fields | Authority |
| --- | --- | --- |
| `.Site` | `Title`, `Description`, `Language`, `Direction`, `HomeURL`, `Origin` | Site's current public configuration, never secret/private settings |
| `.Content` | `Kind` (`post` or `page`), `Title`, `BodyHTML`, `CanonicalURL`, `PublishedAt`, `ModifiedAt` | Selected **published** revision; nil on latest-posts home listing |
| `.Home` | `Items` with `Title` and `URL` | Authoritatively selected public home listing; empty on content pages |
| `.Theme.Settings` | Bounded named, typed site-owned appearance values | Public-only operator-configured keys, never database table names/queries |
| `asset` | Pure lookup of a declared authorized theme/public asset URL | Versioned output only; no disk access or arbitrary remote retrieval |

Example for the **body slot**; this is illustrative, not a currently executable Rixa configuration:

```html
{{if .Content}}
<main id="main"><article>
  <h1>{{.Content.Title}}</h1>
  <div class="entry">{{.Content.BodyHTML}}</div>
</article></main>
{{else}}
<main id="main"><h1>{{.Site.Title}}</h1>
  <ol>{{range .Home.Items}}<li><a href="{{.URL}}">{{.Title}}</a></li>{{end}}</ol>
</main>
{{end}}
```

Only Rixa's **sanitized and exact published editorial HTML** may be passed to the renderer as a trusted HTML fragment; arbitrary strings/DB input must remain escaped by `html/template`. Templates may choose layout and presentation but cannot grant content visibility, authorize Media, alter publication identity, or suppress mandatory canonical/security metadata. Rixa derives visible content, sitemap and JSON-LD coherently from one selected publication snapshot. If a custom layout omits essential visible content, valid URL metadata, accessible structure or mandatory publication elements, provide a concrete validation error; do not claim SEO or accessibility from templating alone.

### Site appearance settings and assets

A small **typed** public settings collection (for example `brand_color`, `support_url` or a logo reference) is sufficient. Type, name, default, scope and bounds are explicit; publishing captures their exact version. No generic Settings Module, table browser or arbitrary template-specified SQL is needed. Existing Appearance fields stay supported with a documented mapping/fallback; custom settings do not silently replace real editorial or security policy.

Reuse private AChrix Media and Rixa's authorized clean PNG/JPEG public-image flow for images. A bounded theme-owned public asset lane is permissible only for formats actually proved needed (for example WOFF2 font files) and securely admitted/served. Publish assets under immutable names with content hashes, correct MIME and `nosniff`, and no path traversal/symlink/private-original exposure. Do not promise SVG, arbitrary uploads or third-party assets before an exact safe publication profile is reviewed. Unknown/missing assets must produce actionable errors. An explicit external-resource allowlist can be added where a concrete design needs it; it is not blanket remote script or tracking permission.

## Lifecycle and durable state

1. **Read:** resolve the actual authorized site and current theme draft/head, active published theme revision and current static generation; return explicit revisions.
2. **Save draft:** validate all changed slots, settings and declared assets together. Require current edit capability, stable `operation_id` and expected source revision (optimistic concurrency). Commit an immutable site-owned theme revision without changing public output.
3. **Preview:** render an exact draft revision plus an explicitly selected published or authorized draft content revision into a temporary, **isolated preview origin**. No live public activation, admin cookies, unrestricted SQL access, arbitrary private Media or leakage into sitemap/crawler paths. Include preview errors and which revisions were rendered.
4. **Publish:** under a distinct publish permission and explicit action, bind exact theme revision, appearance/settings/content/media snapshot and `expected_public_generation`. Render/validate HTML, CSS, JS, assets, public policy and page meaning into the existing staging/generation system; check freshness and atomically activate only one coherent new generation. A Theme save **never** publishes automatically.
5. **Inspect:** the caller can inspect only its authorized site, operation, expected revisions and receipt. Lost acknowledgement, in-progress commit or a 503 does **not** prove nonexecution; inspect/reconcile the **same** operation identity before retry. Never silently mint a fresh ID for uncertain writes.
6. **Rollback:** select an older retained immutable Theme revision and **republish** it as a new coherent public generation (including asset references). Do not edit published history, swap arbitrary filesystem symlinks or pretend a source revert reverses DB migrations. Existing static generation/fallback recovery rules stay authoritative.

Theme history, active generation, settings and asset dependencies are scoped to one physical site's data/public roots. An independent site transfer/capture/restore must include the selected theme source/revision graph, typed settings and necessary assets, validating their compatibility and references. No cross-site import may accidentally reuse private principals, foreign Media IDs or privileged grants. Keep a bounded history/retention policy consistent with current Rixa publication semantics; don't invent a second deployment ledger.

## Critical security boundary: public code versus management

**Blocking condition for JavaScript delivery:** current Rixa site management `/admin` and `/auth` share the site's public Origin (`internal/product/components.go`), while the public CSP currently sets `default-src 'none'` and has no JavaScript allowance (`internal/product/publication_files.go`). Simply enabling inline scripts or loosening site CSP would put privileged management at risk.

Before claiming any working executable theme, prove all of the following:

- Public website JavaScript runs on a **different Origin** from authenticated Site Admin, Control Admin and any credential-bearing management endpoints. Select and document a workable management-host mapping in the first implementation; different URL paths or ports are not enough. Public visitor requests have no management session credential. Enforce host-only, Secure, HttpOnly and suitable SameSite management cookies, strict CSRF and trusted origin checks, no credentialed cross-origin access from public sites, and unambiguous verified site/user/session routing. Preserve independent Control/Site accounts and permissions.
- Draft preview JavaScript runs on another unprivileged isolated Origin, not in the administrator DOM or on the privileged management Origin. Any time-limited preview ticket is scoped to exact site/revision/content and is not an admin bearer credential; prevent private resource enumeration, cross-site reuse, caching, referrer leakage and indexing. Never execute raw Theme JS in the management editor merely to show a preview.
- Theme code authoring/publishing has its own sensitive, site-scoped capability (for example `rixa.theme.edit` and `rixa.theme.publish`), **not** inherited from `rixa.content.edit`. Trusted administrator code can influence/collect what visitors see; provide clear risk messaging and provenance, not a false claim that arbitrary third-party JS is safe. Ordinary content stays on the current sanitized markup policy.
- For public responses, generate CSP deliberately from the exact approved static source. Serve versioned first-party scripts and styles from self; explicitly account for any allowed inline code with hashes/nonces compatible with static publication. Do not globally enable `unsafe-inline` for scripts. Admit external scripts/styles/fonts only by a documented operator-owned policy, with visible failures for disallowed resources; do not silently strip author code.
- Never allow HTML template code or JS to call privileged Rixa application services, read secrets/environment/source files, browse directories, query SQL, or impersonate users. No admin permissions are inferred from site host, user-supplied actor field, filenames or content.

## Human, programmatic and AI operation boundary

The **one Rixa Theme Application service** owns authorization, validation, site selection, revision admission and mutation results. Its conceptual operations are `ReadBundle/History`, `SaveDraft`, `Preview`, `Publish`, `InspectOperation` and `RepublishRevision` (and bounded theme asset handling when necessary). These are semantic requirements, **not existing Go APIs or public endpoints**. UI, authenticated HTTP/API, and supported MCP are adapters to these operations and must not duplicate policy or publish by writing files directly.

The HTTP API needs authenticated exact-user/site/channel binding, request/response schema, operation identity and useful typed errors; the MCP binding must describe the same explicit side effects, permissions and confirmation requirements. Code-bundle import/export is allowed for a human or an offline AI generator; **machine code generation is not authorization** to publish.

Remote user-authorized operation delivery is dependent on [Rixa #49](https://github.com/AChWorks/rixa/issues/49) (Bridge/HTTP/MCP), the consumed AChrix contracts and the Gateway owner. It must verify the actual account, issuer/audience, grant/site/channel scope, revocation and current permissions. An AI client, human using Gateway and direct API caller receive the **same domain decision** as the UI. A shared privileged Control credential plus a caller-provided username is prohibited. Do not advertise direct ChatGPT, Claude or Gateway support before actually proving that installed-client path. A valid API schema or standalone service test is not hosted-client acceptance.

The release/documentation must separately label **source support**, **published artifact support**, **installed runtime**, **verified UI**, **verified HTTP/MCP client** and **Production**. The source inspected for this design and its then-current released profile provided none of the new code-theme operations; exact future release evidence must determine when they become available.

## Required acceptance evidence

The delivery Issue [#74](https://github.com/AChWorks/rixa/issues/74) owns executable acceptance, not this document. At minimum: one real creative code theme across home/post/page; responsive and RTL/English; CSS, JS, public-safe asset and typed variable behavior; SEO/structured-data consistency; unchanged private editorial content; draft/preview/publish/rollback and unknown-operation recovery; old-generation integrity on failure; site A/B isolation; post-install transfer and retained-state update; real Chromium/HTTPS/PostgreSQL checks; strict management/preview/public origin, cookie/CSP/CSRF negatives; member-vs-Theme-author permissions; actual user-authorized HTTP and MCP paths after #49, including revoked/foreign/forged users; fresh independent review and signed package provenance for the exact candidate. Product code and published package must precede instructions that claim a tested working command.

## Evolution

Professional authored code and minimal public data bindings are the accepted first outcome. Rich navigation settings or custom fields should expand only when a concrete theme needs them. Reconsider visual builders, more complex theme assets, many template types or plugin marketplaces **only** after a real demonstrated need; their absence must not prevent high-quality handcrafted sites. Preserve the small typed contract and portable source representation so optional future design tools could consume it without replacing publication or authorization.
