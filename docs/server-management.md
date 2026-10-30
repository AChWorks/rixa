# Native development server management

The `manage` commands provide one concrete infrastructure-operator path: a verified Rixa archive, private guided configuration, explicit migration and administrator bootstrap, and a persistent systemd service. Use a trusted executable that actually contains these commands. Historical packages do not gain management commands from newer documentation.

This is a **development profile**, separate from production support, automatic PostgreSQL provisioning, certificate issuance, destructive purge and panel/web setup. The [installation and management contract](installation-and-management.md) describes that broader journey; [manual development installation](release-install.md) remains available for other documented runtime profiles.

## Supported inputs and authority

The initial native profile requires:

- Debian 13, Linux amd64, a running systemd system manager and an explicitly authorized root infrastructure operator;
- a trusted management executable under root-owned, non-writable ancestors, such as `/usr/local/bin/rixa`;
- an official supported `gh` verifier under similarly protected paths; the shared [publisher policy](release-verification.md) checks its capabilities and the selected artifact;
- existing PostgreSQL 18 databases, one for control and a different physical database for the initial site, with their own explicit password credentials, migration authority and permission to inspect `pg_control_system()`;
- explicit **loopback TCP PostgreSQL URLs**, including user, password, IP, port, database and only `sslmode=disable`, for example `postgres://user:URL_ENCODED_PASSWORD@127.0.0.1:5432/database?sslmode=disable`;
- distinct control/site HTTPS hostnames, matching the selected listener port, and bounded regular certificate/key files covering both hostnames;
- positively available installation paths and unused `rixa` account/group/service names.

The loopback database restriction makes the reviewed credential source identical under root and the non-login runtime user. Implicit environment/password-file credentials, Unix peer authentication, remote database profiles, service-file options and silent database major upgrades are not supported by this adapter. A missing prerequisite produces a field-specific result; the operator supplies or corrects it before applying.

Supplied TLS material is checked for key pairing, time, hostnames and server usage, then privately staged. Local postflight uses that supplied certificate to verify the exact HTTPS endpoints. This does not establish public DNS resolution, browser CA trust, firewall reachability or automatic certificate renewal.

## Guided terminal

Run the same trusted manager for the operation:

```bash
sudo /usr/local/bin/rixa manage menu --operation first-install
```

The menu can fetch an authenticated release through the shared artifact policy, or use exact local archive/bundle inputs. Stable and development selections remain explicit. It asks for origins/listener, certificate paths, the two database connections and initial administrator credentials. Login names use the product's exact lowercase ASCII syntax. Passwords retain their original Unicode contents and follow the same bootstrap policy as direct product operations.

Review the intended effects before Apply. Valid entered values remain available when a field is corrected or the operator navigates back. The backend controls which actions are currently possible; an uncertain result does not become a generic Retry button.

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

Review binds the manager/helper, authenticated candidate, current owned runtime/configuration/environment and physical databases. Apply requires the exact current update review token under the shared lifecycle lock. It queues a fixed independent executor; a queued result is not completion. Inputs or ownership drift require inspection and a new review before execution, rather than reuse of an old confirmation.

The executor authenticates and stages the candidate before downtime. Its bounded declared AChrix component graph must match the actual compiled dependencies and later readiness receipt. The updater binds those candidate identities; it does not require their version values to equal the executing manager's graph. Unknown components, replacement modules and development/pseudo-version component identities are rejected. It disables and quiesces the exact old service, then runs the authenticated candidate's `check` as the non-login runtime account **before any migration**. The supported profile requires the candidate already to understand the retained schema and configuration. A candidate needing an unsupported schema transition is rejected; this is not a general schema-changing upgrade mechanism.

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

Resume requires positive executor acknowledgement and a quiescent cgroup, intact frozen images/intent, unchanged physical/configuration bindings and an exact supported old-or-candidate service identity. Timeout or missing acknowledgement does not prove no effect. The shared lifecycle lock and durable pending/active update records serialize new lifecycle work; stale old-manager actions are rechecked against current ownership before any continuation.

Before migration intent, failed compatibility may restore and verify only the exact unchanged prior service. After migration intent, recovery is forward-only with the same authenticated candidate and immutable migration ledgers. The previous service stays disabled while migration/activation is unresolved, including after reboot. There is no automatic old-binary or database rollback. Unknown or changed ownership remains an operator-required result; do not delete journals, invent a new operation or replay an arbitrary stage.

### Status and removal after update

The current manager's ordinary installation Status/Remove follows the active update receipt. You can also inspect/remove through the successful update ID:

```bash
sudo rixa manage update status --operation first-update
sudo rixa manage update remove --operation first-update --confirm-remove
```

Removal checks the current candidate's static/loaded service and executable ownership, stops serving and removes only its positively owned runtime executable/unit. It retains databases, configuration, credentials, accounts, original Media, published files, legal material, prerequisites and original/update recovery evidence. Purge remains a separate unimplemented destructive operation.
