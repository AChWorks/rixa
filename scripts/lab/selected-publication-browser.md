# Selected-site publication browser acceptance

This opt-in developer-only proof binds an already built Rixa candidate to
real control authentication, three independent PostgreSQL databases, actual
Admin Shell, HTTPS public readers and the site-owned publication workflow.
It reuses the seed helper from selected-site-browser.md, but has a separate
test, switch, browser driver and completion journal. The ordinary test suite
cannot publish through this fixture.

Prepare a new private filesystem root and an isolated disposable PostgreSQL 18
test service. The user must never point this test at a deployed instance,
persistent database or the retired Owner demo. Run in a persistent task with
environment variables:

- RIXA_TEST_POSTGRES_DSN: disposable PostgreSQL administrator URL
- RIXA_SELECTED_SITE_PUBLICATION_OBSERVATION=1
- RIXA_SELECTED_SITE_BINARY: absolute path of the verified candidate binary
- RIXA_SELECTED_SITE_ROOT: absolute new private fixture root (does not exist)

The Go command is:

    go test ./internal/product -run '^TestSelectedSitePublicationBrowserFixture$' -count=1 -timeout=25m -v

The fixture creates unique physical control/site-A/site-B databases and
private Media/Public roots, loopback HTTPS with control.localhost/a.localhost/
b.localhost, and three local test administrators. It seeds real site content,
then writes the data-only ready.json receipt. The Go-owned child process
and unique databases are cleaned by its own test cleanup; it does not manage
the caller-owned PostgreSQL service.

After ready.json appears, load the complete selected-publication-browser.js
into an isolated Playwright browser runner with the explicitly scoped
self-signed certificate exception, and invoke its exposed function
rixaSelectedPublicationBrowser({browser,context,page,fixture,privateRoot}).
Use the exact JSON object from ready.json as fixture and the canonical 0700
fixture directory as privateRoot. No public site or DNS change is needed.

The browser walks the actual control login and CSRF/session flow, verifies
English/LTR narrow-screen/keyboard and Persian/RTL wide-screen appearance,
sets reviewed publication intent without publishing, explicitly confirms a
site-wide Apply, checks anonymous HTTPS public HTML and unchanged site B,
clears intent without auto-withdrawal, then performs actual withdrawal with
a deliberately lost HTTP acknowledgment. The driver first proves a real
HTTP 200 effect, awaits the intercepted route abort, and then observes the
matching Playwright requestfailed event and the Admin Shell's unknown-outcome
presentation (including the retained operation ID). Only then does it inspect
the *same operation identity* and verify that the real public route returns
410 Gone. A successful acknowledgment without a matching failed browser
request cannot satisfy this acceptance gate.

The driver journals each mutation's exact payload before its effect to a
bounded 0600 private log without recording headers/cookies/passwords.
The Go fixture requires the **exact ordered four actions**, one matching
committed-effect receipt, one observed browser request failure, the matching
uncertain-result UI observation, and one terminal completion naming all eight
browser checks. Missing, duplicate, unrecognized, extra fresh-ID and partial
entries are rejected. An exact same-ID retry would need a separate explicitly
declared journey; this current proof performs no automatic retry.

After the driver reports success, create an EMPTY finish.request under the
same fixture root and inspect the actual Go task exit. Go also checks
unchanged binary/config, unmodified private originals, unchanged site-B
database/public files and changed site-A public generation. Missing or partial
browser journal, skipped test or missing marker is not acceptance evidence.

Retain only useful private review evidence until candidate review, then safely
delete positively owned disposable resources. This is nonproduction evidence,
not public certificate trust, universal accessibility or release approval.
