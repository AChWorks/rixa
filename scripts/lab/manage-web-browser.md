# Source-only management console browser verification

This driver exercises the actual served management console with Chromium and
the existing `TestConsoleBrowserPreview` fake-native TLS fixture. It performs
no native installation, update, panel, PostgreSQL, service or host-network
effects. Tiny upload bytes pass only the fixture's fake private verification
seam; they are not publisher-authentication proof.

Use the already available browser runtime. Do not install a browser, alter
network policy or point the driver at an operator's real console. The driver
requires the preview schema, exact fake operation and loopback HTTPS origin.
Use an isolated browser context and scope `ignoreHTTPSerrors` to this self-signed
fixture. Strict CA/TLS, ingress/Host/Origin enforcement, admission bounds and
real native effects need their separate existing tests and profile evidence.

## Start one new fixture per run

Create an owned 0700 evidence directory and select a new absolute marker path
inside it. Existing marker, control, receipt and stop paths are rejected.
Run the fixture as the ordinary development worker, never root:

```sh
RIXA_MANAGEWEB_PREVIEW=/absolute/private/proof/run1.json \
  go test -race ./internal/manageweb -run '^TestConsoleBrowserPreview$' -count=1 -v
```

Use a persistent job if the execution tool is short lived. Wait for the marker;
never print it, since it contains the one-use permit. The fixture lives at most
three minutes and consumes only four private test commands:
`inspection-unavailable`, `restore-inspection`, `installed`, and `expire`.
The control file is a test-only local file, with no HTTP endpoint or executor.
No new generic browser framework or repository dependency is required.

## Run the driver

In a browser execution capability with pre-created `context` and `page`,
set the two operator-controlled absolute paths below. Use a new marker and
isolated context for each of `journey`, `expiry` and `cleanup`. The caller owns private
checkpoint I/O; the browser sees neither a local path selector nor control API.

```js
const fs = await import("node:fs/promises");
const marker = "/absolute/private/proof/run1.json";
const sourceRoot = "/absolute/rixa-checkout";
const mode = "journey"; // repeat with "expiry" or "cleanup" and a new fixture
const fixture = JSON.parse(await fs.readFile(marker, "utf8"));
const source = await fs.readFile(sourceRoot + "/scripts/lab/manage-web-browser.js", "utf8");
const run = new Function(source + "; return rixaManageWebBrowser;")();
const checkpoint = async value => {
  await fs.appendFile(marker + ".browser.jsonl", JSON.stringify(value) + "\n", {mode:0o600});
};
const control = async command => {
  await fs.writeFile(marker + ".control.tmp", command, {mode:0o600, flag:"wx"});
  await fs.rename(marker + ".control.tmp", marker + ".control");
  for (let n = 0; n < 100; n++) {
    const receipt = JSON.parse(await fs.readFile(marker + ".receipt", "utf8"));
    if (receipt.command === command) return;
    await new Promise(resolve => setTimeout(resolve, 25));
  }
  throw new Error("fixture control not acknowledged");
};
try {
  const result = await run({context, page, fixture, checkpoint, control, mode,
    screenshotRoot: "/absolute/private/proof"});
  console.log(JSON.stringify(result)); // allowlisted summary only
} catch (error) {
  await checkpoint({phase:"failed", status:"failed", error:error.message});
  throw error;
} finally {
  await fs.writeFile(marker + ".stop", "stop", {mode:0o600, flag:"wx"});
}
```

The journey deliberately drops the first configure and Apply responses only
**after** `route.fetch()` confirms a fake server commit. It retains request
bodies privately in memory, compares exact configure replay, and never replays
Apply after inspection observes the same operation applying. It also checks
read-only failure, dirty navigation cancellation, saved-input/secret handling,
review redaction, invalid-CSRF denial, a required unchecked confirmation for
each fresh review, actual completion revocation, no WebStorage, English/Persian/
installed-locale metadata and escaping, 390/wide overflow and keyboard focus.
The separate expiry run advances only the fake console's expiry, then proves
no configure/Apply request is sent and unsaved input remains.

Screenshots are taken before secret entry or after acknowledged secret clearing.
Checkpoint summaries contain phase names, counters and verdicts, never submitted
bodies, permit/session/CSRF/review tokens or credential values.

## Verify teardown and classify evidence

After the browser finishes, inspect the actual persistent job status, not just
a browser return or log EOF. The final private `.receipt` must have
`phase:"stopped"`, `private_state_removed:true` and `permit_removed:true`.
It records allowlisted fake calls, final state, secret-presence flags and whether
the exact fake review token reached Apply. The marker/control/stop files are
removed; the redacted receipt/checkpoints/screenshots remain for inspection.

Retain failed attempts as failures. An Apply click without the required checkbox
does not submit the HTML form and cannot prove lost-ack recovery. A full pass
must include the later Apply/inspection/completion phases. The driver is bounded
regression and browser evidence, not universal WCAG compliance, real publisher
verification, strict TLS trust, native recovery or aaPanel support.

For `cleanup`, start the preview with `RIXA_MANAGEWEB_PREVIEW_KIND=update-cleanup`.
It binds a fresh `status,resume` permit to `fixture-update`, returns retained
`installed/completion-cleanup-required` state, and performs only one fake typed
update Resume before positive completion. The browser checks live access before
Resume and revoked access afterward using real in-page fetch with the same
explicit same-origin referrer policy as the application. A direct API probe
without Origin/Referer would be rejected independently of session validity and
is insufficient revocation evidence.

The journey includes an intentionally unsafe fake diagnostic containing supplied
credential canaries and markup. Both server HTML and live browser text must retain
the useful repair path, mask the actual credentials and render markup as text.
English technical detail stays LTR beneath a catalog-owned localized label.
Install Resume/Remove require the native `InstallationEffects` seam; do not
substitute the CLI convenience methods when testing an earlier source stack.

## Staged-artifact confirmation regression

For each of the exact fake profiles `artifact-install` and `artifact-update`,
start a new preview with `RIXA_MANAGEWEB_PREVIEW_KIND` equal to that profile,
and pass the same value as the driver's `mode`. The marker records the profile;
the driver rejects a mismatch. Use a separate private screenshot directory for
each attempt and preserve failed receipts.

These profiles keep the same private fake facade and three-artifact-attempt
bound. They stage fake upload A, exercise a failed request without replay, then
drop fake fetch B's acknowledgement after its server commit. Exact observational
Status recognizes B and unlocks Save without another fetch. The regression checks
that request edits, failure/late Status, unsaved B, reload and Persian navigation
cannot restore A's review/confirmation; Save plus a new review binds B. Explicit
discard affects only unsubmitted request edits. It then makes one fake Apply and
observes completion revocation. The displayed digest must fit narrow/wide layouts.

These fixtures authenticate tiny bytes only through their deliberately fake
verification seam. They do not prove real publisher authentication, networking,
native installation/update/recovery or panel support.

## Absolute form request budget regression

Start a fresh preview with `RIXA_MANAGEWEB_PREVIEW_KIND=request-budget-install`
or `request-budget-update` and use the same driver mode. Each exact fake profile
starts with a saved draft and delays only its fake Apply facade for 17 seconds.
The normal Chromium form submission must receive its acknowledgement beyond the
previous 15-second fallback, without inspection, replay or a real native effect.
The driver also runs the actual served pure URL policy against relative paths,
absolute URLs and all five real DOM form actions; foreign origins, credentials,
queries and fragments fail before a mutation. Existing 130/190-second budgets are
unchanged. Check the final fixture receipt for one Apply attempt, one Apply,
exact review forwarding and normal private-state/permit teardown.
