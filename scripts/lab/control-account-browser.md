# Control account creation — real Chromium and PostgreSQL proof

The opt-in Go fixture TestControlAccountBrowserFixture serves one supplied,
already-built Rixa executable against three unique disposable Control/Site-A/
Site-B PostgreSQL 18 databases and local HTTPS. It does not deploy or modify
an existing installation or start/stop the PostgreSQL service. The browser
runner accepts only the fixture's exact control.localhost, a.localhost and
b.localhost origins, with a scoped TLS exception for this disposable fixture.

The dedicated "Control Account Browser" GitHub workflow runs on relevant
source changes and via manual dispatch. Its real Chromium checks include
Control login and cookie scope, explicitly selected site and disabled site
actions, actual identity create and duplicate conflict, a committed site-B
account with deliberately suppressed HTTP acknowledgement (NO retry),
an independent BrowserContext request observer kept active for a bounded
post-error interval (all three browser mutation attempts counted, including
attempts that would result in 409), exact acknowledged/committed IDs matched
against PostgreSQL Identity rows, unchanged administrator account/credential
row digests, subsequent Identity login on the owning site, exact HTTP 401
wrong-site and HTTP 403 Control selection denials, visible localized
completion/conflict/uncertain outcome messages, en/LTR
and fa/RTL, keyboard behavior at 390 and 1366 px, cleared password fields,
browser errors and absence of sensitive WebStorage.

The Go fixture additionally checks the final exact account counts in three
distinct databases, denies cross-site account copies, checks unchanged
Media/public roots and runtime config, and rejects out-of-scope database
table modifications. Only the two exact newly created account/credential
rows, separately verified login sessions and Audit records are expected;
any additional Identity row or altered administrator record fails acceptance.
The request observer checks absence of a replay within its finite live
observation window; it does not claim to prove an unbounded future.

For an isolated authorized manual laboratory with a disposable PostgreSQL
admin DSN, Node and pinned Playwright 1.61.1/Chromium, set RUNNER_TEMP to a
private new temporary workspace, RIXA_TEST_POSTGRES_DSN to the disposable
cluster, NODE_PATH to the Playwright module directory and
PLAYWRIGHT_BROWSERS_PATH to its pinned Chromium install. Then execute:
  bash scripts/lab/run-control-account-browser.sh

The launcher refuses an existing fixture root or candidate binary, starts
only the task-owned local Rixa process, and counts PASS only after both
actual browser and post-browser server-side validations finish. A skipped
opt-in Go test is NOT browser evidence. The existing HTTP/PostgreSQL
integration tests cover different, narrower acceptance. Do not use
production passwords, databases or service endpoints. Browser CI uses
a deliberately scoped local TLS validation exception; it does not test
production certificate trust or arbitrary wire-level packet loss.
