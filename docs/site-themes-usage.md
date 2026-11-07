# Code-first site themes: human and AI usage

**Availability:** This is the **accepted future workflow for [Rixa #74](https://github.com/AChWorks/rixa/issues/74)**, not a working operator guide. In the baseline Rixa source inspected for this design, custom editable Header/Body/Footer HTML, `site.css`, `site.js`, theme bundle import, and remote Theme API/MCP tools **do not exist**. Do not attempt to paste these examples into Appearance's `header_tagline`, `footer_text`, or the ordinary content editor: those fields are intentionally restricted. A later release must replace the prospective UI/API details below with its actual documented, tested invocation and exact version.

For architectural and security requirements, see [site-themes.md](site-themes.md). For existing site content editing, see [selected-site-content.md](selected-site-content.md); for existing publication and recovery, see [static-publication.md](static-publication.md).

## Quick choice: which workflow do you need?

| Who / goal | Accepted workflow | Current status |
| --- | --- | --- |
| Human designer: build an entire site | Edit Theme code from trusted Site Appearance, Preview, Publish | Planned in #74, not shipped |
| Human content editor: write a post/page | Use existing safe content editor and explicit publication | Available now; HTML deliberately restricted |
| AI code assistant: prepare a site design | Generate the six Theme source files locally, give the owner a reviewable bundle | Can prepare source text now; no live Rixa import |
| Connected AI or script: maintain a permitted site's Theme | Authorized HTTP/MCP to the same site-scoped Theme service, explicit draft/publish rights and inspectable receipts | **Not shipped**; requires #74 + [#49](https://github.com/AChWorks/rixa/issues/49) |
| Visitor: read the custom design | Static published HTML/CSS/JS on public Origin, no public DB/API needed | Target after code-theme release |

## First-use requirements

- Select the correct **site**; a Control account does not automatically confer that site's Theme permission.
- Use a Rixa build and installed runtime whose published guide explicitly lists `Code-first site themes`; **verify the exact selected release**.
- Provide a separate authenticated **management Origin** and unprivileged **public Origin**, plus isolated Preview Origin, confirmed working TLS, correctly scoped sessions and CSP. If that isolation is not supported in the chosen profile, **do not enable custom JS**.
- Have the site's dedicated `theme.edit` permission for source edits; `theme.publish` for public activation (names in the contract are prospective). Neither follows from ordinary `content.edit`.
- Use the existing Rixa content, Media and public-image process for real content and authorized image publication. An image's private upload URL is **not** a publishable asset URL.

## Human journey (future UI contract)

1. Open the **site's authenticated management Origin**, select its site, and go to **Appearance > Custom Code**. Confirm current site ID, effective published generation and Theme draft revision.
2. Start from the built-in Theme/default skeleton or import a deliberately compatible portable Theme bundle. The six editor tabs are **Head / Header / Body / Footer / CSS / JavaScript**. Each shows ordinary validation errors with slot/line and retains unsaved input.
3. Add or update the site's approved **public** settings and referenced Theme assets. The template data browser must expose field names, types, sample values and escaping rules, **not** raw SQL, secrets or other sites' data.
4. **Save Draft**. Verify the returned exact revision/operation receipt. Save is **not** Publish, and a conflict must not silently overwrite someone else's work.
5. **Preview** that exact saved Theme revision against a selected home/page/post. It opens the isolated, no-credential Preview Origin; check mobile/desktop, RTL/LTR, browser console, actual asset loads, images, links and generated page meaning.
6. **Publish** only after explicit review and with `theme.publish`. Confirm the acknowledged public generation, active Theme revision, and a real public URL in a new unauthenticated browser session. The published public site must not carry an Admin cookie or allow privileged API access.
7. For a bad change, choose a known prior Theme revision and **Republish** it. Confirm the new generation. The unpublished/newer draft may remain for debugging; do not delete history as a rollback shortcut.

The eventual UI should make a failed preview/publish and a lost response distinguishable, provide **Inspect Operation** with the original operation ID, and never say a publish succeeded until the durable public generation is acknowledged. These controls are acceptance requirements, **not current UI screenshots**.

## Authoring reference: portable Theme sources

Each slot may be edited independently. The future **required portable export/import** must preserve the same six files without bundling Rixa secrets or Go source:

```text
my-site-theme/
  head.html
  header.html
  body.html
  footer.html
  site.css
  site.js
  [optional approved public assets and format/version metadata]
```

These are **example files**, not files automatically read by an installed Rixa. A future import must validate the package and never execute uploaded JavaScript in the Admin origin.

### `head.html` (additive public Head)

```html
<meta name="theme-color" content="#14283e">
```

Do **not** write replacement canonical URLs, `robots` directives, fabricated JSON-LD, authentication cookies or a blanket CSP relaxation into this slot. Rixa's publisher retains those responsibilities.

### `header.html`

```html
<header class="site-header">
  <a class="brand" href="{{.Site.HomeURL}}">{{.Site.Title}}</a>
  <button class="menu-toggle" type="button"
          aria-expanded="false" aria-controls="main-nav">Menu</button>
  <nav id="main-nav" aria-label="Main navigation">
    <a href="/">Home</a>
    <a href="/about/">About</a>
  </nav>
</header>
```

The `{{.Site.*}}` expressions are **proposed** public read-only bindings. Replace example links with routes that actually exist on your site. Later, a verified navigation/settings feature may provide data-driven menu items without changing the template engine.

### `body.html` (one template for home, posts and pages)

```html
{{if .Content}}
<main id="main" class="page-layout">
  <article>
    <h1>{{.Content.Title}}</h1>
    <div class="entry">{{.Content.BodyHTML}}</div>
  </article>
</main>
{{else}}
<main id="main" class="page-layout">
  <h1>{{.Site.Title}}</h1>
  <ul class="post-list">
    {{range .Home.Items}}
    <li><a href="{{.URL}}">{{.Title}}</a></li>
    {{end}}
  </ul>
</main>
{{end}}
```

Rixa must provide only the selected published/safely previewable content and must emit the permitted, canonicalized `BodyHTML` as a trusted HTML fragment. **Do not** pass unsanitized arbitrary HTML from a post, API caller or database string into that trusted fragment. Normal fields are context-escaped automatically by `html/template`. The publisher must reject missing required visible content rather than serving an apparently valid empty page.

### `footer.html`

```html
<footer class="site-footer">
  <p>&copy; {{.Site.Title}}</p>
  <a href="/contact/">Contact</a>
</footer>
```

### `site.css`

```css
:root { --brand: #14283e; --text: #192333; }
* { box-sizing: border-box; }
body { margin: 0; color: var(--text); font: 1rem/1.6 system-ui, sans-serif; }
.site-header, .page-layout, .site-footer {
  width: min(100% - 2rem, 72rem); margin-inline: auto;
}
.site-header { display: flex; align-items: center; gap: 1.5rem; padding-block: 1rem; }
.brand { font-weight: 700; color: var(--brand); }
.site-header nav { display: flex; gap: 1rem; }
.menu-toggle { display: none; }
@media (max-width: 48rem) {
  .site-header { flex-wrap: wrap; }
  .menu-toggle { display: inline-block; }
  .site-header nav { display: none; width: 100%; }
  .site-header nav[data-open="true"] { display: flex; flex-direction: column; }
}
:dir(rtl) .site-header { text-align: start; }
```

### `site.js` (public browser interaction only)

```js
const toggle = document.querySelector(".menu-toggle");
const navigation = document.querySelector("#main-nav");
if (toggle && navigation) {
  toggle.addEventListener("click", () => {
    const open = toggle.getAttribute("aria-expanded") !== "true";
    toggle.setAttribute("aria-expanded", String(open));
    navigation.dataset.open = String(open);
  });
}
```

This JS must execute on the **public or isolated Preview Origin only**. It has no server, database or admin-session privileges. External dependencies, analytics, fonts or embeds require a deliberately approved host/CSP policy and should be validated in the final target profile. A Theme owner is responsible for third-party code they choose to serve to visitors.

## AI and automation: two distinct modes

### Mode A: offline AI as a Theme author — available as a collaboration pattern now

1. The site owner supplies the AI the public Theme binding reference, allowed asset types, desired pages, brand rules and the currently selected Theme source/revision (when available). **Never** supply passwords, private tokens, DB DSNs, private Media originals or admin cookies.
2. AI produces the six named files as text and, if appropriate, a diff against the previous Theme bundle. Keep dynamic Rixa data in declared bindings; do not hardcode a content table query or invent hidden fields.
3. The owner reviews the code, links and third-party scripts. Once Theme import/editor **actually ships**, an authorized human imports/saves it as a draft, previews and explicitly publishes.
4. AI can suggest changes after inspecting a public page, but inspection is not a guarantee that a private draft or protected site is reachable.

### Mode B: connected AI / HTTP client / MCP client — **not currently available**

After both #74 and #49's applicable supported client/authorization paths are delivered, a client must use actual Rixa user authorization and declared site scope through the normal supported adapter, not database writes or SSH deployment. The future operation contract is:

| Logical product operation (not a live tool name) | Minimum input/authority | Expected outcome |
| --- | --- | --- |
| Read Bundle/History | site + authenticated `theme.read` | Allowed site/revision and public binding/asset inventory |
| Save Draft | site + stable operation ID + expected Theme revision + bounded changed source slots + `theme.edit` | New committed immutable draft revision / explicit conflict |
| Preview | site + exact draft revision + optional authorized content selection + `theme.preview` | Scoped preview receipt/link, isolated from Admin |
| Publish | site + stable operation ID + exact Theme/source revision + expected public generation + `theme.publish` | Active coherent generation receipt / explicit unknown or conflict |
| Inspect Operation | actual caller/site + operation ID + scoped permission | Exact saved result/pending/unknown without exposing another user/site |
| Republish Revision | site + historical Theme revision + current public-generation precondition + `theme.publish` | New public generation using that prior Theme revision |

These are **semantic service contracts**, not URLs, CLI flags, released Go methods, MCP tool names or payload schemas. The published release must supply tested actual HTTP method/path, OpenAPI/authentication and MCP tool/resource descriptions, typed request/response/error schemas, version/compatibility policy and exact usage examples. Neither #74 nor #49 grants an AI implicit authorization to push to a site. Default safe operational posture is **draft access only**; live publication requires explicitly granted authority and a separate mutation.

A client dealing with timeout or missing acknowledgement must retain **the same** operation ID, inspect the actual operation under the same user/site, and reconcile before retrying. A fresh operation ID can duplicate a completed effect. Permission revocation, site removal or channel restrictions take effect according to the independently tested common-policy freshness contract. Do not assume Gateway user mapping from an email string or copied Control credential.

### Machine integration acceptance checklist

Before documenting any client as working, supply reproducible evidence for: authorized human UI Save/Preview/Publish; authenticated HTTP flow using the same site/revision and operation semantics; direct supported MCP client; Gateway-mediated human and AI path where claimed; two site users with different grants; negative cross-site/forged-actor/current-role/expired or revoked grant; optimistic concurrency and lost acknowledgement; audit with actual user/client/channel/operation correlation; real public output on correct TLS/Origin. Source compilation, schema generation or mock Gateway tests alone do **not** prove this.

## Troubleshooting and recovery expectations

| Symptom | Check and correct |
| --- | --- |
| `Custom Code` absent in Appearance | Selected installed release may predate #74; verify exact binary/release, not just GitHub `main`. |
| Code appears as text or HTML is rejected in Tagline/Footer/post editor | These are **existing plain-text/sanitized content fields**, not Theme code slots. |
| JS does not run on a published site | Check exact published generation, asset URL, developer console, CSP violations, allowed hosts and whether the site/public origin is isolated; do **not** globally disable CSP. |
| Draft saved but public output unchanged | This is expected until explicit Publish/Apply and a matching generation receipt. |
| Preview contains private data or sends privileged cookies | Stop publishing; this violates the management/preview isolation contract and requires correction before acceptance. |
| Concurrent change reports conflict | Fetch authoritative head revision, compare work and save deliberately; never overwrite by ignoring the precondition. |
| Publish response timed out | Inspect the **original** operation ID and active generation; do not submit an unrelated new Publish. |
| Old design needed | Republish a previously retained immutable Theme revision, then verify the newly active public generation. |
| AI reports it updated the site but no result is visible | Demand a real authenticated operation receipt, current site/revision/generation and independently fetched public result; a chat claim or generated code file is not deployment proof. |

**Release criterion:** When implemented, convert this page from prospective guidance to a verified version-scoped how-to with real tested routes, exact request samples and error examples. Keep historical limitations explicit for older releases.
