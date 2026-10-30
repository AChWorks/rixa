# Selected-site saved-content browser proof

This explicit developer fixture serves an **already built** Rixa executable with unique disposable PostgreSQL databases, private files and loopback HTTPS. It exercises the control administrator's selected-site draft workflow. It never changes a deployed installation, rebuilds the supplied executable, starts/stops PostgreSQL or enables selected-site publication.

Use an already available test PostgreSQL cluster, an independently verified coherent candidate executable and a new private directory whose parent already exists:

```bash
umask 077
export RIXA_TEST_POSTGRES_DSN='<disposable PostgreSQL admin DSN>'
export RIXA_SELECTED_SITE_OBSERVATION=1
export RIXA_SELECTED_SITE_BINARY=/absolute/verified-candidate/rixa
export RIXA_SELECTED_SITE_ROOT=/absolute/new-private-selected-site-fixture
go test ./internal/product -run '^TestSelectedSiteBrowserFixture$' -count=1 -timeout=25m -v
```

Run this in a persistent task. A pre-existing root is refused; reconcile earlier fixture state and unknown effects rather than rerunning over it. The fixture's 20-minute bound and existing cleanup helpers stop only its exact child product process and drop only its unique databases. The external PostgreSQL service belongs to the caller.

The fixture creates independent control/two-site databases and Media/Public roots, a short-lived dedicated test certificate and fixed test-only administrators. It seeds nonempty private Media and coherent static output through normal HTTPS, then adds enough real source records for **102 contents per site and 56 revisions of a selected item**. All seeded editorial/appearance request bodies, including the existing shared seeding helper’s generated operation IDs, are fsynced privately before requests. Media admission is bound to exact upload bytes by digest/size; authentication endpoints and headers are excluded. It installs the existing synthetic partial `ar` test catalog after hashing the already built executable and before constructing this instance; no new default language is shipped.

Wait for `RIXA_SELECTED_SITE_ROOT/ready.json`. The receipt contains only URLs, opaque fixture IDs/counts and the supplied executable hash. Bind that hash and the separately verified source/package identity in the owning acceptance record. Do not publish configuration, private logs, TLS keys, sessions, DSNs or request payload journals.

Load the complete `scripts/lab/selected-site-browser.js` into the Agent's `browser_run` or another compatible Playwright runner, then call:

```javascript
console.log(JSON.stringify(await rixaSelectedSiteBrowser({
  browser, context, page, fixture,
  privateRoot: "/absolute/new-private-selected-site-fixture"
})));
```

The caller supplies a fresh isolated Browser context with a self-signed certificate exception limited to these owned `control.localhost`, `a.localhost` and `b.localhost` origins. No DNS/hosts changes are needed. The driver requires Node dynamic imports for `node:fs/promises`, which the Agent Browser runtime supports. It refuses a noncanonical/nonprivate root and an existing request journal.

Before each selected create/save reaches the product, the driver fsyncs its exact JSON body, operation ID, selected site/path and timestamp to `selected-site-attempts.jsonl` (0600). It records no authentication headers/tokens. The journal remains private because it contains fixture draft text. Deliberately suppressed acknowledgements are never resolved by fresh-ID replay: one scenario inspects an actually committed save; another proves lookup404 keeps the immutable attempt pending, then explicitly retries the **same serialized payload and operation ID**. A changed-payload reuse is separately expected to conflict.

The real browser journey covers:

- control-only navigation and full create/open/save workflows for both independent sites;
- three finite content pages and three history pages, with no omissions/duplicates;
- selected ID/origin visibility, no body-supplied selection and no selected publication controls;
- canonical root-relative source with preview links resolving against the selected public origin, without a document base;
- concurrent stale-head rejection while local input/CAS stay in the original DOM, plus separate latest-source inspection;
- dirty/pending navigation cancellation, frozen unknown payloads and exact operation inspection/retry;
- revoked-session recovery through a separate sign-in tab while the original edited DOM stays intact;
- actual English/Persian/installed-locale rendering, shared catalog label agreement, keyboard focus and 390px/wide screenshots;
- private preview denial, foreign selected content/Origin/site-cookie rejection, separate control/site sessions and no draft/token WebStorage.

After the driver returns its complete successful result, create an empty completion marker:

```bash
touch "$RIXA_SELECTED_SITE_ROOT/finish.request"
```

Wait for the actual Go task exit. The fixture additionally verifies unchanged executable/configuration, exact private-original/static files, immutable ledgers/accounts and every journaled operation's absence from the other site and lack of duplicate commits. It requires the driver's complete journal marker; stopping after a failed/partial Browser journey does not turn it into acceptance. A skipped opt-in test is not browser evidence.

Keep bounded private journals/screenshots and useful diagnostics while review needs them, then release only proved disposable fixture resources. Passing this representative journey is not universal accessibility, localization, concurrency or fleet-capacity conformance.
