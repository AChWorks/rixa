# Native development server management

The `manage` commands provide one concrete infrastructure-operator path: a verified Rixa archive, private guided configuration, explicit migration and administrator bootstrap, and a persistent systemd service. Use an independently trusted executable that actually contains these commands and follow the guides supplied with its package. Historical packages do not gain management commands from newer documentation.

This prepared-server **development profile** requires supplied PostgreSQL databases and TLS material. Builds exposing `manage web` can present these same operations in a temporary protected browser console, described below. A panel host additionally needs the exact supported panel profile and acceptance evidence; production support, certificate issuance and destructive purge remain separate contracts. The [starter and PostgreSQL prerequisite path](#bootstrap-starter-and-postgresql-prerequisites) below defines a separate clean-host operation for packages that contain it. The [installation and management contract](installation-and-management.md) owns the broader journey; [manual development installation](release-install.md) remains available for other documented runtime profiles.

## Supported inputs and authority

The initial native profile requires:

- Debian 13, Linux amd64, a running systemd system manager and an explicitly authorized root infrastructure operator;
- a trusted management executable under root-owned, non-writable ancestors, such as `/usr/local/bin/rixa`;
- an official supported `gh` verifier under similarly protected paths; the shared [publisher policy](release-verification.md) checks its capabilities and the selected artifact;
- existing PostgreSQL 18 databases, one for control and a different physical database for the initial site, with their own explicit password credentials, migration authority and permission to inspect `pg_control_system()`;
- explicit **loopback TCP PostgreSQL URLs**, including user, password, IP, port, database and only `sslmode=disable`, for example `postgres://user:URL_ENCODED_PASSWORD@127.0.0.1:5432/database?sslmode=disable`;
- distinct control/site HTTPS hostnames, matching the selected listener port, and bounded regular certificate/key files covering both hostnames;
- positively available installation paths and unused `rixa` account/group/service names.

### Optional second site during a new native installation (development source)

The default native installation remains **one site**: Control plus `site-a`, each
with its own physical PostgreSQL 18 database and private account namespace.
A manager built from source containing the optional site-b implementation can
also install **exactly two sites at initial installation**; this is not evidence
that an earlier prerelease binary already supports it.

For the two-site path, the infrastructure operator must supply a **third**
independent PostgreSQL 18 database and role, and a valid runtime TLS
certificate covering three distinct exact hostnames: Control, site-a and
site-b. Native Review and Apply **fail closed** unless all three database
connections authenticate as separate LOGIN roles with distinct passwords,
each role owns only its assigned database among these three, and no role is
SUPERUSER, CREATEDB, CREATEROLE, REPLICATION or BYPASSRLS. On the same PostgreSQL
cluster, effective `CONNECT` (including privileges from `PUBLIC`) and role
membership from any of these three roles to another are both forbidden.
The native two-site profile rejects any inherited or SET-able role
memberships for these three application login roles, including membership
that could permit privilege escalation through unrelated administrative roles.
The infrastructure operator must explicitly revoke default `PUBLIC CONNECT`
on the three databases and establish each isolated database owner/credential.
A limited read-only `pg_control_system()` physical-identity probe must remain
available to those roles for native admission. Rixa never creates roles,
grants privileges, revokes access or silently weakens this preflight.
The existing one-site prerequisite contract remains unchanged. The first site's `site_origin`, `site_dsn`, `site_login` and
`site_password` inputs are unchanged. Add all four optional private draft
fields for site-b:

| Private draft field | Meaning |
| --- | --- |
| `site_b_origin` | The distinct second-site HTTPS origin with the selected listener port |
| `site_b_dsn` | The third independent loopback PostgreSQL 18 URL, using explicit password credentials |
| `site_b_login` | An independent initial site-b administrator login |
| `site_b_password` | The independent initial site-b administrator password |

An incomplete second-site group is invalid. Save the *entire* draft in a
root-owned `0600` regular JSON file outside any web root and use the existing
`rixa manage draft --operation first-install --source /root/private/first-install.json`
followed by Review and explicit token-bound Apply. Do not paste the draft or
its secrets into chat, commit it, or pass credentials as command arguments.
The native `manage web` console presents an optional expandable second-site
section; the default terminal `manage menu` retains its original single-site
field sequence and offers a separate **Configure optional two-site installation**
menu choice; a private `--prefill` containing site-b fields also opens those
optional fields. The web section has an explicit *Clear optional site
from this draft* action; leaving saved password fields blank retains their
existing private values.

Review binds all three physical database identities, confirms one TLS
certificate covers all three hostnames, and authenticates the exact Rixa
release. Apply serializes one installation with separate `site-a` and
`site-b` bootstrap receipts, private Media/publication roots and verified
initial administrator logins. Ordinary Remove still retains **all** data,
databases, private configuration and Media. Restart and status use the same
systemd-owned service and immutable operation journal.

The Native-generated `systemd` unit passively orders itself **after**
`postgresql.service` on Debian hosts where that PostgreSQL meta unit is
already part of the boot transaction. The Debian cluster instances declare
`Before=postgresql.service`, so Rixa does not exhaust its bounded startup
retries while an existing local PostgreSQL cluster is still starting. This is
an ordering constraint only: Rixa does not add PostgreSQL `Wants`/`Requires`,
provision a cluster, take ownership of a supplied database or assume that an
externally supervised PostgreSQL server uses this exact unit name. Operators
must verify external-supervisor readiness separately.

The source-level two-site activation has a bounded 90-second **three-origin**
service-readiness window after `systemd` activation; the default single-site
window remains 30 seconds. Each origin must still pass the same authenticated
TLS/Host and HTTP checks. The normal long-running `rixa run` entry has its
own separately bounded 90-second wait before constructing the application:
it retries only read-only PostgreSQL physical-identity probes that returned
transient unavailability or a bounded probe deadline, spaced 2 seconds apart.
A malformed configuration, invalid TLS, changed physical/tenant isolation or
other unclassified failure still fails immediately. No migrations, account
creation, session writes or serving starts during this wait. The operator
`rixa check` and Native Review retain their existing fail-fast semantics.
Service startup records bounded, credential-free wait/recovery diagnostics.
A failed stage is not automatically retried or considered healthy because a process exists: inspect the retained stage and
its diagnostic, then use the original frozen manager's `resume` action only
when Status positively offers it. A runtime-child diagnostic distinguishes
unavailable physical PostgreSQL identity observations from observed identity
drift without printing the database connection or passwords. These source
changes do not affect previously published binaries.

**Limits:** This initial path cannot add a site to a *running* installation,
cannot provision a third database automatically, and does not grant arbitrary
schema-changing updates. The managed updater remains constrained by its
existing negative read-only pre-stop editorial guard and authenticated,
stopped-service candidate check. The v0.7-to-v0.8 managed schema-changing
migration and candidate-bound AChrix/Editorial compatibility, verified backup
and restoration remain separate Issue #29 acceptance requirements. Treat
source-level capability as unreleased until authenticated artifact, physical
installation acceptance, independent review and authorized release are recorded.

The loopback database restriction makes the reviewed credential source identical under root and the non-login runtime user. Implicit environment/password-file credentials, Unix peer authentication, remote database profiles, service-file options and silent database major upgrades are not supported by this adapter. A missing prerequisite produces a field-specific result; the operator supplies or corrects it before applying.

Supplied TLS material is checked for key pairing, time, hostnames and server usage, then privately staged. Local postflight uses that supplied certificate to verify the exact HTTPS endpoints. This does not establish public DNS resolution, browser CA trust, firewall reachability or automatic certificate renewal.

## Temporary browser access

Use this interface only when the independently trusted manager exposes
`rixa manage web --help`. It runs the same native operations as the terminal and
CLI, with a root-issued short-lived single-use permit bound to one operation,
kind and action set. A product or panel login does not grant this authority.

Prepare a root-private directory and regular `0600` TLS files. The console
certificate must cover `127.0.0.1`; the runtime certificate must cover the control
and site hostnames. Start the trusted manager explicitly:

```bash
sudo /usr/local/bin/rixa manage web --kind install --operation first-install \
  --tls-cert /root/private/console.pem --tls-key /root/private/console.key \
  --runtime-cert /root/private/runtime.pem --runtime-key /root/private/runtime.key \
  --permit-file /root/private/first-install.permit
```

Reach its default loopback TLS listener through an authorized SSH tunnel:

```bash
ssh -N -L 127.0.0.1:9444:127.0.0.1:9444 operator@host
```

Trust the selected console certificate, open the displayed HTTPS URL, and enter
the permit from its private file. Fetch or upload an authenticated release, save
the private draft, review the actual operation and explicitly apply that review.
The browser cannot choose arbitrary commands or local paths. Inspect the same
operation after uncertain acknowledgement; do not repeat an effect because a
tab or tunnel closed. The native executor continues independently.

For a new update, use `--kind update --operation first-update --installation first-install`
with console TLS and a new permit file. Later status/removal uses a fresh permit
with `--actions status,remove`; installation-kind access still requires its
operator-bound runtime TLS flags. Reopen retained work with its exact recorded
manager rather than replacing the frozen image. Ordinary removal retains the
data and prerequisites described below; there is no browser purge operation.

The [browser console guide](https://github.com/AChWorks/rixa/blob/HEAD/docs/management-web.md)
explains the forms, private inputs, localization and aaPanel-assisted native
profile. Read it at the selected package's source version before relying on its
commands. The panel profile uses supplied compatible PostgreSQL 18, native
systemd supervision and direct Rixa TLS on a separate explicit port, with Rixa
panel mapping disabled and existing 80/443 sites preserved. The clean-host
PostgreSQL provisioner below intentionally does not run on panel hosts. A panel
name or PHP/web-root account does not establish those capabilities; exact tested
combinations and delivery evidence belong to
[panel installation work](https://github.com/AChWorks/rixa/issues/30) and the
selected release.

## Guided terminal

Run the same trusted manager for the operation:

```bash
sudo /usr/local/bin/rixa manage menu --operation first-install
```

The menu can fetch an authenticated release through the shared artifact policy, or use exact local archive/bundle inputs. Stable and development selections remain explicit. It asks for origins/listener, certificate paths, the two database connections and initial administrator credentials. Login names use the product's exact lowercase ASCII syntax. Passwords retain their original Unicode contents and follow the same bootstrap policy as direct product operations.

Each field checks its own value while you edit. Related fields may be changed in sequence: enter HTTPS origins without a port, then set the later TLS listener to 443. Completing Configure checks the whole draft, including matching origin/listener ports and separate control/site hostnames and databases. A mismatch returns to the affected field and retains your other entries. Review checks the complete configuration and actual prerequisites again before Apply. The backend controls which actions are currently possible; an uncertain result does not become a generic Retry button.

Use `--plain` for a simple terminal or `--no-color`/`NO_COLOR` to remove color. Direct commands emit JSON and never prompt. `--non-interactive` rejects opening the menu. The operator CLI and terminal menu always use English, independently of the web interface and site content language. Original Unicode input is retained; its display still depends on the terminal's shaping and bidirectional support.

## Direct commands and private input

A draft/prefill JSON contains real credentials. Use a root-owned private regular file, outside web roots and repositories, with mode `0600`. Its fields are:

| Field | Meaning |
| --- | --- |
| `operation_id` | Stable lowercase operation identifier reused for inspection and recovery |
| `archive`, `bundle`, `release`, `source_commit` | Exact artifact paths, selected tag and full source commit |
| `control_origin`, `site_origin`, `listen` | Two distinct HTTPS origins and explicit direct listener |
| `tls_cert`, `tls_key` | Existing certificate/private-key paths |
| `control_dsn`, `site_dsn` | Separate explicit loopback PostgreSQL password URLs |
| `control_login`, `site_login` | Initial exact administrator logins |
| `control_password`, `site_password` | Initial passwords, never command arguments or general logs |
| `language` | Initial web interface language: bundled `en` (default) or `fa`; never changes the English terminal. Additional installed web locales use the [localization guide](localization.md). |

For example, after preparing the private JSON:

```bash
sudo rixa manage draft --source /root/private/first-install.json
sudo rixa manage review --operation first-install
sudo rixa manage apply --operation first-install --review-token <token-from-that-review>
sudo rixa manage status --operation first-install
```

A successful Review returns an opaque `review_token`. Apply requires that exact token under the installation lock. Saving the draft or issuing a new Review invalidates the previous confirmation; Status never supplies a replacement token. If the executing management image, reviewed bytes, TLS material or physical database identities change, review the intended inputs again rather than forcing the previous token.

`manage menu --prefill /root/private/partial.json` can begin with private partial input. The caller's source/prefill file is not automatically deleted; manage that file's credential retention separately.

## Effects, supervision and retained identity

Apply authenticates protected archive bytes before tar parsing or execution. Extraction rejects traversal, links, devices, duplicate names, unsupported sparse/PAX entries and additional gzip members. The adapter checks package completeness, legal notices and actual compiled Go dependency/target metadata; a text `BUILDINFO.json` cannot substitute for compiled component identity. Protected archive verification stays in management storage; extraction uses a private staging directory on the destination release filesystem and activates through an atomic no-replace rename. Separate `/var` and `/opt` filesystems do not require moving an extracted tree between mounts.

The manager freezes its own executable and trusted helper identities separately from the selected serving payload. Standard verifier proxy/CA transport settings are retained in a private environment source. No arbitrary shell or runtime-selected privileged command is accepted.

One fixed systemd executor performs the recorded installation independently of the SSH connection or menu. A queued result is not completion. It establishes the dedicated non-login `rixa` account, private runtime roots, explicit schema migration, durable exact-scope bootstrap, protected configuration preparation and readiness before activating `rixa.service`. Runtime operations run as that account; root-owned management intent is separate from runtime-writable data/configuration.

The service has bounded restart attempts and only the port-binding capability needed for HTTPS. The initial site retains Media and static publication. Its generated development publication policy is conservative: `noindex`, no snippets and wildcard crawler disallow. This is no permission or production-indexing claim.

Completion requires the exact active executable, both local HTTPS administrator logins, authorized pages and explicit session logout/revocation checks. Initial administrator plaintext passwords are then removed from retained management input. Database credentials remain protected because the service still needs them. Ordinary Status observes retained progress and the current owned service/TLS endpoint without creating administrator sessions.

## Interruption and recovery

Keep the operation identifier and retained management files. Do not delete a journal, invent a new operation or edit a protected record to bypass a failure.

```bash
sudo rixa manage status --operation first-install
sudo journalctl -u rixa.service
```

The recorded stage identifies the exact executor unit, named `rixa-install-<operation>-<generation>.service`; inspect its journal and protected `/var/lib/rixa-manager/<operation>.diagnostic` for bounded useful failure context. Do not publish private draft/journal/environment files.

When Status explicitly offers Resume, use the same retained trusted manager:

```bash
sudo rixa manage resume --operation first-install
```

Resume requires the original executor to be positively acknowledged/observed and then quiesced, including its cgroup descendants; the frozen manager/helper/intent and physical targets must still match. A bounded new executor uses effect-specific positive observations or proven-safe migration-ledger re-entry and the existing [bootstrap recovery](bootstrap-operations.md)/[setup recovery](setup-operations.md) contracts. An upgraded manager must not silently take over the old binding; retain the exact protected manager image for that operation.

Some outcomes remain operator-required. An unknown executor start, an unresolved bootstrap authentication outcome, changed ownership/targets, or an unknown HTTP login/logout acknowledgement is not automatically repeated. HTTP postflight uses the normal hosted Identity session lifetime, not the separate short-lived bootstrap-proof session. Known HTTP sessions are explicitly revoked and checked; a lost login response may leave a normal session whose token was never received. The manager does not silently reset passwords or claim that such a session was cleaned up.

## Ordinary removal

Inspect the affected installation, then explicitly confirm retained-data removal:

```bash
sudo rixa manage remove --operation first-install --confirm-remove
```

Removal verifies the static and loaded service definition, drop-ins and any live executable/user before stopping the service. It removes the positively owned runtime executable and service unit, while retaining databases, configuration, Media, published site state, service credentials, the account/group, legal notices and management/recovery evidence. It does not recursively erase directories, remove PostgreSQL or alter an unrelated service. Changed or unknown ownership blocks removal.

Use the separately reviewed update operation below for a supported compatible replacement. There is no unattended updater or destructive purge command. A binary reversal does not undo schema/data changes.


## Compatible native update

Use a trusted manager that actually implements `manage update`. An older installed runtime or its frozen installation manager does not acquire this capability from newer documentation. The updater freezes its own image and verifier separately and preserves the original installation journal, manager and helper. Keep those original files for ownership and recovery evidence.

This development adapter accepts the positively owned native Debian 13/systemd installation above, with its original control and initial site, PostgreSQL 18 physical targets, service authority, TLS, listener, private roots and credentials. It preserves the actual valid runtime configuration, including supported presentation, catalog and resource settings. Changed topology, database targets, principals, roots, TLS or service ownership require explicit operator reconciliation; the updater does not regenerate retained configuration from an old draft or adopt unrelated services.

Prepare a new root-owned `0600` JSON file with only:

| Field | Meaning |
| --- | --- |
| `operation_id` | A new stable update ID; keep it through inspection and recovery |
| `installation_operation` | The original native installation operation ID |
| `archive`, `bundle` | Exact candidate archive and provenance bundle paths |
| `release`, `source_commit` | Exact candidate release tag and full source commit |

No replacement administrator passwords, bootstrap inputs or new database credentials are accepted. Do not edit the original installation journal or replace its frozen manager.

```bash
sudo /usr/local/bin/rixa manage update menu --operation first-update --installation first-install
```

The update menu can fetch an authenticated release or use protected local inputs. It distinguishes the executing manager, original installation, active release and selected candidate. Plain/no-color/noninteractive behavior follows the installation interface. Direct commands never prompt:

```bash
sudo rixa manage update draft --source /root/private/first-update.json
sudo rixa manage update review --operation first-update
sudo rixa manage update apply --operation first-update --review-token <token-from-update-review>
sudo rixa manage update status --operation first-update
```

Review binds the manager/helper, authenticated candidate, current owned runtime/configuration/environment and physical databases. A manager containing the read-only Rixa editorial inspector also checks the retained site's manager-known migration ledger at Review time: pending Rixa editorial schema migration blocks this **schema-compatible-only** update path; inconsistent/unavailable schema evidence requires operator correction. Because this observation is tied to the executing manager's migration plan rather than the selected candidate's exact complete module plans, it is deliberately conservative and **is not proof of candidate compatibility**. A later schema drift is possible even after a successful Review. Apply requires the exact current update review token under the shared lifecycle lock. It queues a fixed independent executor; a queued result is not completion. Inputs or ownership drift require inspection and a new review before execution, rather than reuse of an old confirmation.

The executor authenticates and stages the candidate before downtime. Its bounded declared AChrix component graph must match the actual compiled dependencies and later readiness receipt. The updater binds those candidate identities; it does not require their version values to equal the executing manager's graph. Unknown components, replacement modules and development/pseudo-version component identities are rejected. **While the exact prior service is still running and immediately before stopping it**, a manager build containing the editorial inspector repeats the read-only, PostgreSQL-fenced observation of the retained site's **Rixa-owned editorial ledger**. If that manager can positively identify pending Rixa editorial migrations, the updater declines before stopping anything and confirms the original service and TLS health before releasing its update claim. An unreadable/inconsistent ledger or unverified prior runtime leaves operator reconciliation required; it never grants compatibility. The `prior-uninterrupted` receipt is possible **only on the first recorded stop-stage invocation**, before any stop command. A resumed/uncertain earlier stop never acquires that claim merely from current process health. This is a limited negative early-warning gate: its migration plan belongs to the **executing manager**, not necessarily the authenticated candidate, and it knows nothing about AChrix module transitions. It **does not** establish general candidate schema admission or guarantee every incompatible candidate is caught before downtime. No candidate `check` runs concurrently with the active previous service, because product startup checks may perform Publication filesystem cleanup.

For any candidate not rejected early, the updater disables and quiesces the exact old service, then runs the authenticated candidate's `check` as the non-login runtime account **before any migration**, as before. A failure at this post-stop compatibility gate still uses positively verified restoration of the unchanged prior service. The supported profile requires the candidate already to understand the retained schema and configuration. A candidate needing an unsupported schema transition is rejected; this is not a general schema-changing upgrade mechanism.

A Rixa build **containing the inspection option** can observe each enabled site's Rixa-owned editorial migration ledger without changing it:

```bash
rixa migrate --inspect --config /etc/rixa/rixa.json
```

This diagnostic requires the existing private database environment bindings and physical PostgreSQL identity permissions. The result reports `scope: rixa-editorial-only`, `admission: not-authorized`, individual ledger states (`uninitialized`, `pending`, `current`), current/target versions and exact pending Rixa migration identities. Independent Control/Site database targets are checked, disabled sites are excluded and unknown/future ledger identities fail closed. Inspection runs in a database `READ ONLY` transaction. **It does not inspect AChrix component migrations, establish site readiness, prove database backup/restore or authorize Native Update.** Do not treat it as signed candidate admission or run `migrate` without `--inspect` against retained production data. Earlier published binaries do not acquire this option retroactively; check the exact authenticated artifact's CLI capabilities.

After that precheck, the candidate's own explicit `migrate` and `check` run against the retained stores. The exact candidate service is activated and enabled, and completion verifies loaded process/executable identity, readiness and strict local TLS/login/private-access boundaries. HTTPS postflight waits within one bounded deadline for a positively identified refused TCP connection during listener startup; certificate, hostname, HTTP-status and other transport failures remain immediate failures. There is no rebootstrap, password reset, database major upgrade or automatic database restore. Original accounts, credentials, sessions, site content/revisions, Media, appearance, publication and private language packs remain retained. Actual continued authentication and populated-state compatibility are acceptance requirements for each delivered update profile; operational postflight does not recollect administrator passwords or create new administrator sessions.

### Update interruption and recovery

Inspect the retained operation and the exact executor named `rixa-update-<operation>-<generation>.service`. Bounded private diagnostics remain under `/var/lib/rixa-manager`; never publish these records or private configuration/environment files.

```bash
sudo rixa manage update status --operation first-update
sudo journalctl -u rixa-update-first-update-1.service
```

Only when Status explicitly offers Resume, run the same retained update manager:

```bash
sudo /opt/rixa-manager/<recorded-manager-sha256> manage update resume --operation first-update
```

Resume requires positive executor acknowledgement and a quiescent cgroup, intact frozen images/intent, unchanged physical/configuration bindings and an exact supported old-or-candidate service identity. An earlier `stop` marked `attempted` is resumed only when the old systemd service is **both disabled and positively quiescent**; if it is now running, re-enabled or uncertain, the operation retains its pending claim for manual reconciliation rather than automatically repeating `disable --now` or reporting the predecessor as never interrupted. Timeout or missing acknowledgement does not prove no effect. The shared lifecycle lock and durable pending/active update records serialize new lifecycle work; stale old-manager actions are rechecked against current ownership before any continuation.

Before migration intent, failed compatibility may restore and verify only the exact unchanged prior service. After migration intent, recovery is forward-only with the same authenticated candidate and immutable migration ledgers. The previous service stays disabled while migration/activation is unresolved, including after reboot. There is no automatic old-binary or database rollback. Unknown or changed ownership remains an operator-required result; do not delete journals, invent a new operation or replay an arbitrary stage.

### Status and removal after update

The current manager's ordinary installation Status/Remove follows the active update receipt. You can also inspect/remove through the successful update ID:

```bash
sudo rixa manage update status --operation first-update
sudo rixa manage update remove --operation first-update --confirm-remove
```

Removal checks the current candidate's static/loaded service and executable ownership, stops serving and removes only its positively owned runtime executable/unit. It retains databases, configuration, credentials, accounts, original Media, published files, legal material, prerequisites and original/update recovery evidence. Purge remains a separate unimplemented destructive operation.


<a id="bootstrap-starter-and-postgresql-prerequisite-source"></a>

## Bootstrap starter and PostgreSQL prerequisites

Use this path only with an independently trusted executable or authenticated package that contains `bootstrap` and `manage prerequisites`, following its supplied guides and host profile. These commands do not by themselves provide an authenticated public one-line entry or establish production support. Published profiles and acceptance evidence belong to [Releases](https://github.com/AChWorks/rixa/releases), [installation work](https://github.com/AChWorks/rixa/issues/29) and their linked CI records; a source build or local fixture is not public delivery evidence.

The [starter/generation trust sequence](release-verification.md#bootstrap-starter-and-generation-trust) first publishes and independently verifies a bootstrap-capable starter, then pins it in a subsequent reviewed immutable generation with the official verifier helper. No invented starter digest or mutable script can substitute for that initial trust. The [authenticated entry guide](bootstrap-entry.md) supplies the generation-specific command and retained-tool boundary. Ordinary product releases retain exactly the archive, checksum and attestation assets.

### Discover and select the right manager

Direct inspection and preparation commands are:

```bash
sudo rixa bootstrap inspect --non-interactive
sudo rixa bootstrap route --operation <retained-operation-id> --non-interactive
sudo rixa bootstrap prepare --channel development --operation <new-install-id>
sudo rixa bootstrap prepare --channel development --update --operation <new-update-id>
```

These direct commands return JSON and never open a menu or launch the selected manager. `inspect` observes protected ownership; `route` returns the fixed menu route for the same retained operation. `prepare` fetches through the shared publisher policy, freezes and reverifies copies, checks the package's actual compiled/source/target/legal identity, and stages a manager under protected content-addressed storage in `/opt/rixa-bootstrap`. Its `prepared` result authorizes no package installation, migration, Apply or Resume. Failed verification may leave bounded private evidence; do not treat a partial folder as an authenticated candidate.

Stable is the default and never falls back to a development prerelease. Choose `--channel development` intentionally, or one exact `--release <tag>` without `--channel`. An optional `--output` names a new absolute private download folder; existing folders are never adopted, overwritten or cleared.

Interactive use starts with `sudo rixa bootstrap --channel development`. It acquires the controlling terminal even when starter input came from a download pipe. Plain/no-color preferences survive the selected-manager handoff, and all operator text remains English regardless of web/site locale. The menu exposes actual discovery outcomes, including retained work and an explicit new-update choice. It opens the chosen manager's normal review/apply interface; it never automatically applies, resumes, reinstalls or resets an operation.

The router validates the original installation, pending/active updates and separate PostgreSQL records. Frozen work uses the exact protected image recorded for that operation. Preserve it even when a newer manager has been downloaded. Unknown material, duplicate operation IDs across types, changed images or an unavailable reviewed manager require inspection. An unexecuted draft still needs its own current review. Do not remove a journal, replace an old manager or invent a new operation ID to force a clean installation.

### Clean PostgreSQL profile and bounded effects

The prerequisite adapter targets only an authorized root infrastructure operator on a clean dedicated **Debian 13 / Linux amd64 / systemd / PostgreSQL 18** host. Trusted base APT/dpkg tools and the Debian archive keyring must exist; a one-line generation also needs its documented HTTPS/CA/hash/archive base. Distinct HTTPS hostnames and valid TLS certificate/key material remain supplied inputs. There is no certificate issuance/renewal, DNS/firewall change, listener takeover, reverse-proxy or broader panel profile in this operation.

Choose either supplied compatible PostgreSQL databases through the existing native input path, or explicitly review provisioning on a positively clean dedicated host. The provisioner refuses existing/shared/panel-managed PostgreSQL packages, accounts, clusters, services, occupied target ports and uncertain ownership. Existing incompatible or partially configured package state is an operator/provider condition, not permission to upgrade, adopt or erase it. Stock SSH/resolver/networkd sockets are admitted only when their socket, process/account and native service identity match the reviewed profile; a familiar name or port alone is insufficient. The operation changes no network service or firewall policy.

The reviewed plan identifies exact new Debian/PGDG package versions and their dependency effects. It authenticates scoped signed APT metadata and the pinned official PGDG key, rejects upgrades/removals and unversioned major-tracking package choices, installs common tooling first, and suppresses implicit `main` creation before the version-specific server. The fixed executor and read-only package guard bind package effects to the reviewed plan and actual APT locking/executor context; callers cannot supply an arbitrary privileged command.

It then creates one operation-derived named cluster on the explicitly selected unused loopback port, plus separate least-privilege control/site roles and databases. The public plan supplies the actual cluster name, port and package versions; the owning operation binds its generated role/database identities. Configuration/data ownership, enabled service, physical database identity, effective configuration/HBA/ident/password settings and actual password authentication must verify. The profile binds the generated `postgresql.auto.conf` with no settings and requires positive denial of a wrong password; a successful connection under permissive trust rules is insufficient. Administrative provisioning runs with its explicitly bound OS/database authority; the serving Rixa profile continues to use explicit loopback TCP/password DSNs, with no ambient PG credentials or runtime peer-authentication expansion.

The prerequisite's fixed ownership boundary includes:

| Resource | Purpose |
| --- | --- |
| `/var/lib/rixa-manager/postgres` | Private journals, generated credentials, ownership and diagnostics; preserve for recovery and never publish. |
| `/var/cache/rixa-postgres` | Operation-bound authenticated APT planning/download evidence; not an application credential source. |
| `/usr/share/keyrings/rixa-pgdg.asc` and `/etc/apt/sources.list.d/rixa-pgdg.sources` | Reviewed scoped PGDG trust/source installed during Apply. |
| `/etc/postgresql-common/createcluster.d/rixa.conf` | Owned suppression of implicit `main` creation. |
| `/etc/postgresql/18/<plan-cluster>` and `/var/lib/rixa-postgres/<plan-cluster>` | Exact named cluster configuration/data, bound to the owning operation. |

The plan's actual cluster name and package versions are authoritative. Ordinary Rixa removal preserves these prerequisite resources.

### Prerequisite draft, review, apply and private handoff

The prerequisite draft contains exactly:

```json
{
  "operation_id": "first-postgres",
  "port": 55432,
  "dedicated_host": true
}
```

The port is an example, not a promise that it is free. Store the JSON in a same-operator private regular file outside repositories and web roots. `dedicated_host: true` is an explicit clean-host choice; the terminal requires the corresponding dedication confirmation. It does not grant missing infrastructure authority or bypass the host assessment.

```bash
sudo rixa manage prerequisites draft --source /root/private/first-postgres.json
sudo rixa manage prerequisites review --operation first-postgres
sudo rixa manage prerequisites apply --operation first-postgres --review-token <exact-review-token>
sudo rixa manage prerequisites status --operation first-postgres
```

The guided equivalent is `manage prerequisites menu`, with the existing plain/no-color/noninteractive rules. Saving preserves generated secrets privately and invalidates an earlier review. Review writes bounded planning evidence/cache, authenticates and binds the exact package/host/tool baseline, and returns the token and public effect plan; it does not install packages or create a cluster, roles or databases. Apply requires that exact unexpired review token under the shared native lifecycle lock and rechecks its binding. A queued fixed executor is not completion.

An `installed` prerequisite is reverified before its generated credentials may privately prefill a new native installation:

```bash
sudo rixa bootstrap prepare --channel development \
  --operation first-install --postgres first-postgres
```

The bootstrap menu offers the same explicit completed-prerequisite choice. This handoff writes the two DSNs only into a protected native prefill; public JSON/terminal output contains no credential values and there is no credential-printing verb. Native configuration, its own Review/Apply and administration/readiness checks still follow. Keep caller input/prefill files private and manage their retention separately.

### Prerequisite interruption, unsupported state and removal

Keep the same operation and its protected records under `/var/lib/rixa-manager/postgres`, including diagnostics. Inspect `manage prerequisites status --operation <id>` and the exact executor identified by its stage. Status observes current retained state; it does not silently repair packages, recreate databases or reset passwords.

Only when the backend explicitly offers Resume, use `manage prerequisites resume --operation <id>` with that operation's exact frozen manager. Positive start acknowledgement, a quiescent executor/cgroup, intact reviewed intent/images, package inventory and owned cluster/database observations are required. Timeout is not proof of no effect. Partial dpkg configuration, partial initialization, unmarked database ownership or drifted authentication may remain `reconciliation-required` for operator action. Preserve diagnostics and inspect actual state rather than blindly repeating Apply, creating a replacement operation or using a generic package-repair command.

Ordinary Rixa removal retains PostgreSQL packages, cluster, roles/databases, credentials and prerequisite/recovery records, together with the normal Rixa configuration/data/Media retention. This adapter offers no PostgreSQL removal, destructive purge, password reset, automatic major upgrade or unattended recovery. Use the selected release's supplied guides and linked acceptance evidence for its supported journeys; source/unit/PTY results alone do not establish deployment support.
