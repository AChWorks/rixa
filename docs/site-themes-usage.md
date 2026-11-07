# Code-first Site Themes — operator and AI authoring reference

**Availability / source identity:** This guide describes the executable candidate developed for [Rixa #74](https://github.com/AChWorks/rixa/issues/74), **not** an assertion that published Rixa v0.8.2 or any deployed instance already supports it. Verify the exact installed binary and completed acceptance/release evidence. The separate authenticated **remote** HTTP client/MCP contract remains blocked on [#49](https://github.com/AChWorks/rixa/issues/49); do not advertise a live remote agent connection on the strength of these browser endpoints. The branch includes the site-admin UI and its same-origin, session-authenticated HTTP routes. Gateway, ChatGPT, Claude and direct MCP usage have **not** been validated or shipped.

The architectural acceptance reference is [site-themes.md](site-themes.md). Site content, publication and site-transfer contracts remain in [selected-site-content.md](selected-site-content.md), [static-publication.md](static-publication.md) and [site-transfer.md](site-transfer.md).

## Site prerequisites

The site must have a public static-publication root, a configured site admin, TLS and **three distinct hostnames** for the same site. Configure the existing site JSON with these additional keys (the ordinary `origin` remains public):

```json
{
  "id": "example",
  "origin": "https://www.example.test:19443",
  "management_origin": "https://manage.example.test:19443",
  "preview_origin": "https://preview.example.test:19443",
  "database_env": "RIXA_EXAMPLE_POSTGRES",
  "media_root": "/srv/example/media",
  "public_root": "/srv/example/public"
}
```

This is a **site entry fragment**, not a complete standalone Rixa config. Retain the real host's existing TLS, listener, permissions and public policy. The management and preview origins must be configured together; hostnames cannot overlap any site or Control origin. For direct TLS the installed certificate must cover all three hostnames and they must use the actual listener port. For any other hosting profile, prove the same origin and session separation at the real ingress before enabling code Themes. Do not change live DNS/TLS/network or run a migration in Production without separate approval.

With a Rixa binary that implements this candidate, use the existing controlled `migrate` operation after inspecting the migration plan; site schema v4 adds `rixa.theme_revisions` and `rixa.theme_head`, scoped to that site's PostgreSQL DB. Existing published static output and existing editorial content are not modified by merely adding Theme drafts. Site instances without the isolated origins preserve the older code-free experience and do not expose Theme management.

**Security model:** only the authenticated site's administrator is initially granted these *separate* product operations: `rixa.theme.read`, `rixa.theme.edit`, `rixa.theme.preview`, `rixa.theme.publish` against `site-theme`. `content.edit`, a Control operator and a different site's principal do not imply Theme permissions. The public host cannot serve `/admin` or `/auth`; the management host does not serve public output; the Preview host serves only temporary unprivileged snapshots. Public code never executes on the management origin.

## Human workflow on the management Origin

1. Log in at `https://manage.example.test:19443/admin/login`. Open **Code-first Site Theme** at `/admin/site-theme/`. The page shows the saved Theme head revision, active public Theme revision and active generation. The initial built-in source is revision `0` even though no Theme revision has been saved.
2. Edit the six independent code fields shown below. Typed public settings are editable as JSON. Imported image/font assets are retained by source revision when editing text; use ZIP import to replace the complete asset inventory. Changing code here does not affect visitors.
3. Click **Save Theme draft**. Verify the returned new revision and operation receipt. Each draft is immutable; a stale `expected_head` yields conflict, not an overwrite. After saving, reload or inspect the new exact revision. Do not use the ordinary sanitized Content editor or Appearance's tagline/footer as HTML slots.
4. Use **Show bounded source diff** to compare the current unsaved editor fields against a saved revision or immutable built-in revision 0. The comparison includes asset inventory changes. It is read-only, bounded and text-only; source is never executed on the management Origin.\n5. Use **Generate preview** for the exact revision. Open the returned `https://preview.../entry/<opaque-ticket>` only in the browser. The browser exchanges the ticket for a temporary **unprivileged** host-only cookie; the temporary preview is no-store/noindex, expires after 10 minutes and does not change the public generation. Preview uses the selected Theme revision with a publication-transformed, Media-resolved snapshot of current site publication intent. It is not a private raw-Media or arbitrary draft-content preview. Recheck mobile/desktop, Persian/English, links, console, SEO, HTML, CSS, JS and assets.
6. Review the explicit public mutation. Enter revision `1` or another exact saved number (or `0` for the built-in appearance), keep the current expected generation, check confirmation, and click **Publish this revision**. Verify the acknowledged *new* generation on the unauthenticated public hostname. The same operation can also **republish a prior revision**; it always creates a new generation, rather than changing an old immutable manifest or deleting newer drafts.
7. For an uncertain Save/Publish acknowledgement, **keep the original operation ID**, inspect it using **Inspect operation**, and only then consider an exact same-ID retry. Do not generate a new ID to blindly replay a potentially committed publication.

All operations are limited to the actual authenticated site. ZIP import and export never read file paths on the Rixa server. Failed publications retain the previous active generation.

## The six independent source files

The source bundle has exactly these six top-level entries:

```text
head.html       additive code inside the Rixa-owned <head>
header.html     code inside the public <body>, before Body
body.html       one shared home/post/page layout with typed binding context
footer.html     code inside the public <body>, after Body
site.css        plain browser CSS (published as a content-hashed external file)
site.js         plain browser JavaScript (published as a content-hashed deferred file)
```

Example `head.html`:

```html
<meta name="theme-color" content="#153048">
```

Example `header.html`:

```html
<header class="site-header"><a href="{{.Site.HomeURL}}">{{.Site.Title}}</a></header>
```

Example `body.html`:

```html
{{if .Content}}
<main class="container">
  <article>
    <h1>{{.Content.Title}}</h1>
    <div class="entry">{{.Content.BodyHTML}}</div>
  </article>
</main>
{{else}}
<main class="container">
  <h1>{{.Site.Title}}</h1>
  <ul>{{range .Home.Items}}<li><a href="{{.URL}}">{{.Title}}</a></li>{{end}}</ul>
</main>
{{end}}
```

The exact `.Content.BodyHTML` binding is **required once**, and must appear in the actual rendered post/page view; omitting it fails validation/publication. Home is fully editable, so a custom home need not include the item list, but if the list is used, `.Home.Items` contains only published items. `Content` is nil on a latest-posts home view and non-nil on post/page views and a selected home page.

Example `footer.html`:

```html
<footer class="site-footer"><p>&copy; {{.Site.Title}}</p></footer>
```

Example `site.css`:

```css
* { box-sizing: border-box; }
body { margin: 0; color: #172c40; font: 1rem/1.65 system-ui, sans-serif; }
.container, .site-header, .site-footer { width: min(100% - 2rem, 72rem); margin-inline: auto; }
@media(max-width: 42rem) { .container { width: min(100% - 1rem, 72rem); } }
[dir="rtl"] body { text-align: start; }
```

Example `site.js`:

```js
document.querySelectorAll("[data-menu-toggle]").forEach(button => {
  button.addEventListener("click", () => {
    const next = button.getAttribute("aria-expanded") !== "true";
    button.setAttribute("aria-expanded", String(next));
  });
});
```

This is trusted public browser code, *not* server-side Go, Python, SQL, PHP or shell. Use semantic markup, responsive CSS and keyboard-friendly interactions. Browser JavaScript runs only on the public and isolated Preview hosts. It cannot access Rixa management sessions.

### Exact supported template bindings

Templates use Go's contextual `html/template`. Normal values receive context-sensitive escaping. There are no arbitrary function imports, database queries, environment variables or server-side executions.

| Binding | Kind | Meaning |
| --- | --- | --- |
| `.Site.Title`, `.Site.Description` | string | Current site public appearance |
| `.Site.Language`, `.Site.Direction` | string | `en`/`fa` and `ltr`/`rtl` public document context |
| `.Site.HomeURL`, `.Site.Origin` | string | `/` and exact public Origin |
| `.Content` | optional object | Selected published post/page, nil for latest-posts home |
| `.Content.Kind`, `.Content.Title` | string | `post` or `page` and selected title |
| `.Content.CanonicalURL` | string | Rixa-generated canonical URL |
| `.Content.PublishedAt`, `.Content.ModifiedAt` | string | ISO 8601 timestamps |
| `.Content.BodyHTML` | safe HTML | Already sanitized, publication-transformed, Media-resolved public fragment |
| `.Home.Items` | list | Published home item objects with `.Title` and `.URL` |
| `.Theme.Settings` | map | Site-local declared public typed settings |
| `asset "logo.png"` | function | URL of explicitly included safe public Theme asset |

An optional `theme.json` `settings` example:

```json
{
  "brand_color": {"type": "color", "value": "#153048"},
  "hero_text": {"type": "text", "value": "Welcome"},
  "contact_url": {"type": "url", "value": "https://www.example.test/contact/"}
}
```

Read a public setting using `{{(index .Theme.Settings "brand_color").Value}}`. Supported types: `text` (up to 256 visible characters), six-digit `color`, sanitized `url`. These values are public; never store credentials or private details in Theme settings. Public CSS may be edited independently; asset URLs are resolved at render time for HTML and CSS via `asset`.

Rixa always owns document doctype, `html` language/direction, `title`, `description`, canonical, robots and structured JSON-LD metadata; do **not** supply replacement canonical/title/robots/description in any slot. The static publisher transforms sanitized editorial HTML and private Media references into authorized public image representations **before** calling the Theme renderer.


For approved bundled images/fonts in **site.css**, use the same bounded lookup
inside CSS source; the static publisher resolves and hashes the final CSS:

```css
.brand { background-image: url({{asset "logo.png"}}); }
@font-face { font-family: Custom; src: url({{asset "custom.woff2"}}); }
```

Unknown asset names cause draft validation failure. Browsers only request the
content-hashed, site-owned public asset URLs; this does not allow private Media
IDs or remote file paths.


### Portable ZIP format v1

A ZIP file has these **exact root paths**: `theme.json`, the six files above, plus optionally `assets/<name>` for bounded public assets. The descriptor must be:

```json
{
  "format": "rixa.code-theme.v1",
  "settings": {
    "brand_color": {"type": "color", "value": "#153048"}
  },
  "assets": {
    "logo.png": "image/png"
  }
}
```

The file `assets/logo.png` must be present and decodable as the declared MIME. Allowed assets: validated `.png`/`image/png`, `.jpg`/`.jpeg`/`image/jpeg`, `.woff2`/`font/woff2`. Up to 24 assets, 512 KiB per asset, 1 MiB per source slot, 4 MiB total, and bounded output. No symlinks, traversal, executable/shell files or private Media paths. ZIP import creates a **new draft**, never publishes. ZIP export includes source, settings and approved public assets, not private credentials.

## Actual browser session HTTP endpoints in the candidate

Every URL below is **relative to the site's management Origin**. These are **same-origin Site Admin UI endpoints**, NOT remote developer API endpoints. They require the current Site Admin session; JSON mutations require AChrix's same-origin CSRF token and `X-Identity-Request: 1` header. A client outside a properly authorized browser session cannot simply copy these requests and claim remote integration. UI authentication/CSRF and Origin verification are owned by AChrix Identity/Admin.

| Method/path | Required capability | Request / result |
| --- | --- | --- |
| `GET /admin/site-theme/` | `rixa.theme.read` | Human editor with head, history and generation |
| `GET /admin/site-theme/bundle/0` | `rixa.theme.read` | Immutable built-in six-source JSON, even after saved drafts exist |
| `GET /admin/site-theme/bundle/{revision}` | `rixa.theme.read` | Exact saved `ThemeBundle` JSON |
| `GET /admin/site-theme/export/{revision}` | `rixa.theme.read` | `application/zip` portable bundle |
| `GET /admin/site-theme/theme.js` | Session / view grant | Module-owned editor asset |
| `POST /admin/site-theme/save` | `rixa.theme.edit` | Six source fields + `settings_json`, stable operation ID, expected head |
| `POST /admin/site-theme/import` | `rixa.theme.edit` | Base64 ZIP, stable operation ID, expected head |
| `POST /admin/site-theme/preview` | `rixa.theme.preview` | Exact revision, isolated 10-min URL receipt |
| `POST /admin/site-theme/publish` | `rixa.theme.publish` and Rixa publication apply | Exact revision + expected public generation + explicit confirmation |
| `POST /admin/site-theme/operation` | Scoped Theme read (and publication operation read for published operations) | Reconcile original operation ID |

**Save draft payload**, strings are exact decimals (no JavaScript floating point round-trip):

```json
{
  "operation_id": "xxxxxxxx-xxxx-4xxx-8xxx-xxxxxxxxxxxx",
  "expected_head": "0",
  "head_html": "<meta name=\"theme-color\" content=\"#153048\">",
  "header_html": "<header>{{.Site.Title}}</header>",
  "body_html": "{{if .Content}}<article>{{.Content.BodyHTML}}</article>{{else}}<main>Home</main>{{end}}",
  "footer_html": "<footer>{{.Site.Title}}</footer>",
  "site_css": "body{margin:0}",
  "site_js": "document.body.dataset.ready='yes';",
  "settings_json": "{}"
}
```

The `operation_id` example is a **shape placeholder**: generate a real fresh random version-4 UUID or the accepted 26-character base32 ID, never submit literal `x`. A successful Save returns `{"operation_id":"...","revision":"1","source_hash":"<sha256>"}`. Concurrent mismatched head returns HTTP `409` / `conflict`. The web adapter preserves asset inventory from the exact expected source revision.

**Import payload**:

```json
{"operation_id":"<new-uuid-v4>","expected_head":"1","archive_base64":"<base64-of-zipped-source>"}
```

**Preview payload** `{"theme_revision":"1"}`, response includes `url`, numeric `theme_revision`, `source_fingerprint` and `expires_at`. Open the returned URL from the user browser; no Admin auth cookie belongs on Preview. Source or Media identifier leaks are a security failure.

**Publish/republish payload**:

```json
{
  "operation_id": "<new-uuid-v4>",
  "theme_revision": "1",
  "expected_generation": "<current-active-26-char-generation-or-empty>",
  "confirm": true
}
```

Revision `0` requests restoring built-in appearance as a **new publication generation**. Success returns `operation_id`, `generation`, `theme_revision`. Inspect using `{"operation_id":"<the-original-id>"}`: a draft returns `result_revision`, a publication returns `generation` and `theme_revision`. On lost acknowledgement, do **not** change either the ID or reviewed expected generation for an exact same-ID retry.

Standard error body is `{"code":"invalid_input|permission_denied|not_found|conflict|limited|unknown_outcome|unavailable","operation_id":"..."}`. Relevant HTTP statuses: 400 invalid, 403 denied, 404 unknown, 409 conflict, 429 bounded capacity, 503 unavailable/unknown committed outcome. A `not_found` reply while a request may still be in flight is not proof that it will never commit. Reconcile against public state and retry only with original ID/payload after ambiguity clears.

## AI integration: supported vs not supported

**Offline AI authoring is supported as a workflow.** Give a code assistant the current six source files or ZIP (without private secrets), this reference, intended site navigation/content, styling requirements and desired language/RTL. Ask for six updated files and an inspectable diff. A site administrator reviews it, imports it as a draft, previews, then publishes. AI-generated source text is not a live site operation.

**Authenticated remote AI clients, direct MCP and Gateway-mediated calls are NOT supported in this candidate.** Issue #49 must deliver verified product user/tenant, actual client and connection identity, narrowed site grants, revocation and trusted channel, and the Rixa adapter must dispatch the **same** ThemeService operations without trusting caller labels. No fake MCP tool names, tokens or ad hoc user impersonation are authorized. Do not assert that an AI or Gateway published a Theme until an actual end-to-end receipt plus independent browser verification exists.

## Recovery and troubleshooting

| Symptom | Correct action |
| --- | --- |
| Theme navigation missing | Verify installed build, explicit management/preview hostname config, PublicRoot and site-admin login. |
| UI source fields empty | Wait for authenticated `GET /bundle/{revision}` hydration; a failed load disables Save; inspect response and login. |
| Save returns conflict | Fetch latest head, compare sources, manually rebase; don't overwrite or fabricate expected revision. |
| Preview link unavailable | Check actual revision exists, site-owned public publication readiness, and allowed temporary preview capacity. |
| Preview looks current but public unchanged | Correct: Preview is isolated and does not activate a generation. |
| JavaScript blocked | Inspect `site.js` hashed path, public Origin and strict CSP; do **not** use blanket `unsafe-inline` scripts or restore Admin on public host. |
| Public layout missing post body | Ensure the actual selected page layout renders `{{.Content.BodyHTML}}`; invalid Theme publication leaves the old generation active. |
| CSS/image/font not loading | Check `asset` reference names and ZIP asset MIME, actual public hashed routes and network status; private Media IDs cannot be used as public URLs. |
| Publish response unknown | Inspect the original operation ID, active generation and route; retry only with original ID and identical reviewed request. |
| Need older look | Republish older immutable revision with current generation CAS and verify a *new* generation. |
| Public page responds at /admin or carries session cookie | Treat as a trust-boundary defect; stop enablement/release until fixed. |

**Still required before acceptance:** actual PostgreSQL and retained-state/transfer proof, real Chromium HTTPS evidence, security-negative testing, verified independent high-assurance review, and separate Owner approval for Merge/release/Production. Do not mark #74 accepted based on this guide alone.

For an opt-in staging-only, account-authorized MCP content integration, see [Experimental remote content](remote-content.md). This does not yet expose remote Theme authoring or publishing.
