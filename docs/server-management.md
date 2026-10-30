# Native development server management

The `manage` commands provide one concrete infrastructure-operator path: a verified Rixa archive, private guided configuration, explicit migration and administrator bootstrap, and a persistent systemd service. Use a trusted executable that actually contains these commands. Historical packages do not gain management commands from newer documentation.

This is a **development profile**, separate from production support, automatic PostgreSQL provisioning, certificate issuance, executable updates, destructive purge and panel/web setup. The [installation and management contract](installation-and-management.md) describes that broader journey; [manual development installation](release-install.md) remains available for other documented runtime profiles.

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

There is no automatic executable updater or destructive purge command in this profile. A binary reversal does not undo schema/data changes; those broader lifecycle paths need their own implementation, recovery evidence and applicable approval.
