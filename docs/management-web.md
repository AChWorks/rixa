# Temporary native management console

Use this development adapter only in a build that exposes **rixa manage web --help**.
It presents the same native installation/update operations as the CLI and terminal.
Native ownership records, review tokens, executors and recovery gates remain
authoritative. The [native operator guide](server-management.md) defines those
operations and their supplied PostgreSQL, TLS and verifier requirements. Use the
guide at the selected package's source version; historical binaries do not gain
commands from newer documentation.

## aaPanel-assisted native profile

This profile runs Rixa as a native systemd service alongside aaPanel. An explicitly
authorized root infrastructure operator prepares the host and opens the temporary
console through SSH. aaPanel continues to own its web stack and existing sites;
Rixa has its own direct TLS listener. Use the selected release's linked evidence
in [panel installation work](https://github.com/AChWorks/rixa/issues/30) and
[the browser implementation](https://github.com/AChWorks/rixa/pull/46) to check
whether your exact panel/web-stack combination has completed acceptance. This
guide alone is not compatibility evidence.

| Capability | Required arrangement |
| --- | --- |
| Host and authority | Debian 13, Linux amd64, systemd and an authorized root operator with SSH access. A website/PHP account alone is insufficient. |
| Rixa supervision | Native `rixa.service` is the sole supervisor. Do not also register the process in Go Project or another panel supervisor. |
| PostgreSQL | Supplied compatible PostgreSQL 18, separate control/site databases and owners, explicit loopback TCP password connections and the native guide's migration/physical-identity authority. |
| Private files | Trusted manager/verifier under root-owned non-writable paths; protected TLS, permits, database secrets, data and recovery records outside panel web roots. |
| Runtime ingress | Distinct control/site HTTPS hostnames on an explicitly available matching port, such as `9443`, with supplied matching certificates. Rixa panel mapping is disabled; existing 80/443 sites are preserved. |
| Browser management | A separate loopback TLS console, normally `127.0.0.1:9444`, reached only through the operator's SSH tunnel and fresh operation-scoped permit. |

The clean-host PostgreSQL prerequisite operation intentionally refuses a panel
host. Have the hosting administrator supply compatible databases through normal
supported PostgreSQL administration before using this console. Do not hide panel
state, replace a shared database service or upgrade its major version to force
that assessment to pass.

Prepare and use the profile in this order:

1. Inspect the panel version, web stack, listeners, existing sites, PostgreSQL and
   ownership. Retain the unrelated sites' configuration, certificates, content
   and response baseline so later Rixa effects can be checked against it.
2. Authenticate the selected Rixa package using its
   [publisher verification guide](release-verification.md), then prepare the
   trusted manager and verifier in an appropriate private location. Uploading or
   extracting a package does not establish trust or start a persistent service.
   Keep archives, secrets, permits and management records out of `/www/wwwroot`
   and every other public web root.
3. Supply the two databases and protected TLS files. The infrastructure operator
   separately establishes authorized DNS, certificate trust and reachability of
   the chosen runtime port. The console does not issue certificates, alter a
   firewall, configure panel mapping or infer client authority from forwarded
   headers.
4. [Open scoped access](#open-scoped-access), authenticate the package in the
   console, and complete Save, Review and explicit Apply. For a runtime on 9443,
   both HTTPS origins include `:9443` and the listener uses that same port.
   Console login, control login and site login are separate authorities.
5. Inspect the same operation until native completion and working administrator
   URLs are verified. Compare the unrelated-site baseline after installation,
   update and ordinary removal. Keep the exact recorded managers and private
   recovery evidence; the CLI remains available when browser access is closed.

Go Project execution, reverse-proxy ingress, constrained shared hosting,
production and other control panels require their own supported profiles and
real evidence. Missing mandatory capabilities require provider action rather
than a privilege bypass.

## Optional two-site first installation

An install-kind console built from source with the native optional-site feature
shows **site-b (optional)** in a collapsed section. One-site inputs remain the
default. Expand the section only when preparing a second independently managed
site; supply its distinct HTTPS origin, a third PostgreSQL 18 database URL,
site-b administrator login and password. A supplied runtime certificate must
cover Control, site-a and site-b. Review and Apply continue to use the single
native lifecycle journal and executor, not a second installation mechanism.

Saved second-site secrets remain private; blank password fields retain prior
values. To remove a previously saved optional site **before Apply**, select
*Clear optional site from this draft* and Save; merely collapsing the section
does not discard credentials or change the reviewed installation. Read the
[operator guide](server-management.md#optional-second-site-during-a-new-native-installation-development-source)
for detailed constraints. Adding sites after installation and arbitrary
schema-changing updates are not provided by this path.

## Open scoped access

Use a trusted manager under root-owned, non-writable paths. Supply private regular
0600 console certificate/key files covering 127.0.0.1; the browser must trust
the selected certificate. Installation additionally binds runtime certificate
and key through root CLI inputs. The browser cannot choose local filesystem paths.

    sudo rixa manage web --kind install --operation first-install \
      --tls-cert /root/private/console.pem --tls-key /root/private/console.key \
      --runtime-cert /root/private/runtime.pem --runtime-key /root/private/runtime.key \
      --permit-file /root/private/first-install.permit

For a new update, use **--kind update --operation next-update --installation first-install**
and the protected console TLS flags. Update collects no replacement runtime
configuration or administrator passwords.

The listener defaults to loopback TLS 127.0.0.1:9444. Reach it through an
authorized SSH tunnel, for example:

    ssh -N -L 127.0.0.1:9444:127.0.0.1:9444 operator@host

Open the displayed HTTPS URL. Do not expose the console through public mapping,
wildcard listening or forwarded-header trust.

Read the permit file locally and enter it in the permit form. It is single-use,
at most 15 minutes, and bound to one operation, kind and action allowlist.
It is never placed in URLs or general CLI output. Product passwords and control/
site cookies cannot authorize infrastructure work. After expiry, logout or
completion, issue fresh access to the same retained operation when needed.
Never replace a frozen manager to regain access.

## Configure, review and inspect

Fetch an authenticated stable or explicit development release, or upload its
archive and provenance bundle with exact tag/source. No silent stable-to-
development fallback occurs. Uploads have fixed private destinations; browser
filenames and arbitrary URLs/commands are not accepted.

An authenticated staged package is separate from the saved native draft. A changed
selection remains visibly unsaved and blocks Review/Apply until Save binds that
exact package, including after reload or language navigation. Editing package
request fields clears prior confirmation; authenticate the request or explicitly
discard only those request edits before saving. Discarding request edits neither
undoes a staged selection nor restores an old Apply confirmation.

Installation asks for origins/listener, separate existing loopback-password
PostgreSQL 18 connections and initial administrator inputs. Saved secrets are
not returned in HTML/JSON. Leave an already saved secret empty to retain it.
Console language is separate from the initial product language.

Save private inputs, review the actual native operation, then explicitly apply
that exact review. Editing invalidates the browser confirmation; native Apply
independently rechecks its token/evidence. Queued execution is not completion.
Inspect progress; only the backend may offer safe Resume.

An uncertain response blocks further mutation. Inspect the SAME operation.
A missing result is not proof that nothing happened. After successful inspection, deliberate retry is available only while the native
backend still offers that action. It reuses the exact retained submitted payload,
review token and operation identity. Nothing automatically replays a mutation.
A lost artifact acknowledgement can be positively matched by its bounded staging
attempt identity during inspection, without repeating the download/upload. This
correlation grants no native authority and does not alter the saved draft.
Read-only inspection failure is not a new unknown mutation. Abandoning the
tab may lose unsaved input. No draft, permit or CSRF token enters WebStorage.

Serving and native execution continue independently of the browser/console.
Staged inputs remain private for those executors. Closing the console revokes
access and removes only its unchanged permit file. Retained artifact cleanup
requires positive completion/ownership inspection, not deleting directories
to make failures disappear.

An original-installation permit does not silently acquire effects on a different
update operation exposed by native convenience routing. Use a fresh matching
update permit and the exact retained manager.

## Later status and removal

A fresh status/removal permit uses **--actions status,remove**. Removal requires
typing REMOVE. It stops serving and removes only owned executable/service
material; databases, configuration, credentials, Media, publication, accounts,
prerequisites and recovery evidence remain. Purge is outside this adapter.

The console defaults English/LTR, bundles Persian/RTL and consumes optional
private catalog data through **--catalog-root**. Its product domain is
**rixa-manager**. The public AChrix catalog contract owns validation/fallback and
locale metadata. Installing valid data-only languages before reconstructing a
console needs no executable rebuild. CLI/TUI always remains English.

A supported panel combination requires a real owned host journey: strict TLS,
session/Origin/privacy boundaries, shared CLI/web operation truth and recovery,
populated signed update/removal retention, required CI and independent review.
Use the selected release's linked acceptance evidence for those results.
Fake-facade/browser checks do not establish a panel delivery profile.

The [source-only browser driver](../scripts/lab/manage-web-browser.md) reproduces
fake-native TLS interaction, recovery, locale and privacy checks. A fresh review
always clears its prior Apply checkbox and summary; confirm the newly displayed
review before applying. Its private fixture receipt verifies teardown and does
not establish a supported host or panel profile.

Installation Resume and Remove require the native strict `InstallationEffects`
seam. It binds the original operation while holding the native effect lock;
CLI parent-to-update convenience routing does not grant browser authority. A
missing seam fails closed. An installed state may still require cleanup or be
unhealthy. Matching recovery access stays available until a positive completion
result without pending Resume; a fresh status/removal permit remains independent.

A localized **Technical diagnostic (English)** section preserves bounded native
cause and repair instructions, including field-specific details. These are
escaped text with English/LTR isolation, not translated by message matching.
Known saved/submitted credentials, connection strings and current request tokens
are masked transiently, including their common encoded forms. The bound private
draft and unchanged private permit file must remain readable for this protection;
if their provenance cannot be verified, the details are visibly unavailable.
The console retains its hashed permit/session authority model.

The Rixa-owned native JSON endpoints retain the existing operation URLs and
use the same temporary session authenticator and exact capability/target
authorizer as the Admin page. The released Admin Shell still renders the page
and catalogs; its ten-second presentation budget does not enclose native
verification. Native requests have a two-minute context, an eight-second body
read bound and five seconds to write an acknowledgement after that context
ends. The earlier permit expiry, client cancellation and owning console cancellation
always propagate. Live session/authority is checked again after body decoding.
The browser waits at most 130 seconds for native acknowledgement (190 seconds
for the separate three-minute artifact fetch/upload path), including CSRF
acquisition. A timeout/cancellation shows bounded localized guidance alongside
retained native state when available. It never proves that no effect occurred,
replays a mutation, or extends operation authority; inspect the same operation
before an explicit retry. Native publisher rechecks and supervised execution
remain unchanged.
