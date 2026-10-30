# Real-binary localization browser acceptance

This opt-in developer fixture exercises an existing Rixa executable on explicitly disposable PostgreSQL databases, private storage and loopback HTTPS. It is not an installer, updater, demo replacement or production profile. It uses fixed test-only credentials from the repository integration fixtures; never point it at customer databases, real credentials or an existing root.

Build/package and validate one exact candidate **before** this journey. Set the executable to that actual package member. The fixture hashes it before/after and never rebuilds or replaces it. Use an already available compatible PostgreSQL test cluster and an exclusive new absolute private directory:

```bash
umask 077
export RIXA_TEST_POSTGRES_DSN='<disposable PostgreSQL admin DSN>'
export RIXA_BROWSER_OBSERVATION=1
export RIXA_BROWSER_BINARY=/absolute/verified-package/rixa
export RIXA_BROWSER_ROOT=/absolute/new-private-browser-fixture

go test ./internal/product -run '^TestLocalizationBrowserFixture$' -count=1 -timeout=20m -v
```

Run the test in a persistent task. It creates separate control/two-site databases, private roots, a dedicated one-hour test certificate and a child product process; it bootstraps and seeds both sites through the supplied binary and normal HTTPS interfaces. It uses distinct `control.localhost`, `a.localhost` and `b.localhost` origins, with no DNS/hosts changes. Its invalid-catalog check constructs a rejected replacement with the actual executable while the serving instance stays available. The new directory must not already exist; inspect/reconcile a failed prior journey rather than rerunning over it.

Wait for `RIXA_BROWSER_ROOT/ready.json`. This protected receipt contains only fixture URLs, opaque test IDs, phase and executable hash. Read it as data; do not publish private configuration, certificates/keys, DSNs, sessions or raw provider output.

Use the existing Playwright installation or the Agent's isolated `browser_run` capability. Load the complete JavaScript from `scripts/lab/localization-browser.js`, supply the parsed receipt as `fixture`, then call:

```javascript
console.log(JSON.stringify(await rixaLocalizationBrowser({
  browser, context, page, fixture, phase: "initial",
  screenshotRoot: "/absolute/new-private-browser-fixture"
})));
```

The caller supplies a fresh isolated Browser context, with a self-signed certificate exception limited to these owned local HTTPS origins. Ordinary TLS validation remains enabled elsewhere. The script accepts only the three declared localhost origins. `screenshotRoot` is optional; when supplied, the directory must already exist and remain private. Record the returned bounded checks and inspect representative screenshots; passing this journey is not universal accessibility or locale conformance.

After the initial browser phase passes, install the **test-only** partial pack into the configured private directory, without editing source or rebuilding the executable:

```bash
install -m 0600 scripts/lab/localization-third.json "$RIXA_BROWSER_ROOT/catalogs/fixture.ar.json"
touch "$RIXA_BROWSER_ROOT/restart.request"
```

The synthetic `ar` pack deliberately includes markup-shaped strings, named placeholders and partial plural forms. It is validation input, not a shipped default language or Arabic translation claim. The product's real catalog parser, immutable instance construction and normal restart load it. The fixture verifies unchanged executable identity, exact pre-restart rows/files/configuration/migration ledgers, original Media/session access and identical static publication bytes. It writes `ready-after-restart.json`.

Read that second receipt and run the same driver with a **new isolated** context and `phase: "restarted"`. Both phases cover actual login and product/Module pages, English/Persian, scoped language cookies, real save/operation lookup, loss of an acknowledgement after one server mutation, input/operation preservation, anonymous private/public reads, foreign Origin/cookie/content denial, logout, keyboard focus and 390/wide screens. The restarted phase also covers post-build language availability on control/site/Module screens, safe server/browser catalog rendering, named parameters, English fallback, plural agreement and separation from stored Persian content language.

After both browser phases pass:

```bash
touch "$RIXA_BROWSER_ROOT/finish.request"
```

Wait for the actual Go task exit. Its 16-minute bound and `t.Cleanup` stop its exact owned child processes and drop its disposable databases; the private certificate key is removed. Retain bounded receipts/screenshots/diagnostics while review needs them, then remove only this proved task-owned directory. The external PostgreSQL cluster belongs to its caller and is never stopped/deleted by this fixture. A timed-out, failed or skipped phase is not acceptance.
