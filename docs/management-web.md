# Temporary native management console

Use this development adapter only in a build that exposes **rixa manage web --help**.
It presents the same native installation/update operations as the CLI and terminal.
Native ownership records, review tokens, executors and recovery gates remain
authoritative. This guide does not establish tested aaPanel, Go Project,
reverse-proxy, shared-hosting or production support.

The first panel profile is **aaPanel-assisted native development installation**:
an explicitly authorized root host operator, Debian 13 / Linux amd64 / systemd,
compatible existing PostgreSQL 18, private inputs and Rixa direct TLS on an
explicit separate port. Panel mapping is disabled for Rixa; preserve existing
80/443 sites. Do not register two supervisors for one Rixa process. A constrained
website/PHP account needs its provider's help rather than escalation.

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

    ssh -L 9444:127.0.0.1:9444 operator@host

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

Actual support still requires a fresh owned aaPanel host journey, strict TLS/
session/Origin/privacy proof, concurrent CLI/web recovery and populated signed
update/removal evidence, required CI and independent review. Fake-facade/browser
evidence is not a substitute for those delivery gates.
