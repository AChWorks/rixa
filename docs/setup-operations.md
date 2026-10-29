# Shared setup assessment and configuration preparation

Rixa's `setup` commands assess supplied runtime prerequisites and prepare one private configuration file. They own the configuration-preparation step used by [native management](server-management.md) and future web setup surfaces. They do not install PostgreSQL, obtain certificates, migrate schemas, create administrators, register a persistent service, update an executable or open ingress. The [installation and management experience](installation-and-management.md) describes the complete intended journey; server and panel delivery need their own evidence.

## Required inputs

Prepare a candidate JSON using the package's `config/rixa.example.json`. Keep database passwords and DSNs in the protected environment sources named by `database_env`, never in configuration or command arguments. Supply actual compatible PostgreSQL targets, exclusive private storage roots and a valid certificate/private key for the selected runtime profile. A missing prerequisite is reported for operator/provider action; the presence of root, a panel or a package manager is not a readiness test.

Choose an existing private directory for the installed configuration. The directory also owns durable setup records and the shared operation lock; keep it outside public web roots and do not replace or delete its metadata to force a retry. Configuration preparation creates a new file without overwriting an existing target. Updating existing configuration is outside this operation.

Build or unpack one coherent Rixa binary and use that same binary for assessment, apply and recovery. The operation binds the actual executing binary, exact candidate bytes and target. Editing the candidate or replacing the binary changes that binding. The hash identifies the selected local candidate; it does not authenticate a release publisher. Verify downloaded artifacts separately using the guide shipped with that release.

## Commands

Use one non-secret operation identifier for one exact setup, and retain it for inspection and re-entry:

```bash
rixa setup assess --source /private/candidate.json \
  --target /private/installation/rixa.json --operation initial-setup

rixa setup apply --source /private/candidate.json \
  --target /private/installation/rixa.json --operation initial-setup

rixa setup status --target /private/installation/rixa.json \
  --operation initial-setup
```

Assessment is read-only. Apply takes the installation lock, refreshes evidence and authority, checks the bound AChrix plan, records the attempt durably before the effect and verifies the result afterward. The only executable adapter in this slice creates the protected configuration. No shell command or privilege escalation is accepted from a setup caller.

Commands are noninteractive and return structured JSON. Assessment/apply return a nonzero exit for blocked, unsupported or reconciliation-required plans; inspect the JSON reason and requirement hints. The status command reports retained state without re-running prerequisite probes, so a successful status read is not proof that an operation succeeded. Safe requirement identifiers, categories, reason codes and operation identity are shared with future TUI/web callers; presentation and translation belong to those surfaces. Ordinary invalid input does not discard an existing operation or overwrite valid configuration. Correct the indicated prerequisite and reuse the original exact request when appropriate.

A ready configuration assessment describes only these declared preparation requirements. It does not prove migrations, administrator login, service persistence, public DNS, browser trust in a certificate chain or a complete deployment. After configuration preparation, follow [explicit migration, administrator bootstrap and check](release-install.md#initialize-and-verify) before serving.

## Interruption and retained state

A timeout or lost response is not evidence that creation failed. Inspect the same operation first. Setup keeps an attempted effect distinct from a verified result; fresh exact target evidence can reconcile successful creation. An absent or conflicting result must satisfy the supported fencing/reconciliation rules before any effect is proposed. Previously verified configuration that disappears or changes is a recovery condition, not permission to recreate it.

Conflicting calls serialize through the shared private parent lock across operation identifiers. The supported file effect is synchronous under that lock; no detached child or remote mutation may arrive later. Invalid or unknown journal state, unsafe permissions, links, binding changes and unrecognized target material fail closed. Do not remove retained state, invent a new operation ID or edit a journal to bypass that boundary.

The private state retains the validated candidate configuration and operation/binding/progress metadata so valid inputs survive interruption. It never resolves secret environment references into persisted DSNs or passwords; public results exclude the candidate and private paths. Preserve private files for diagnosis and retain the actual secret source separately. Reverting a binary does not roll back configuration, database changes or other external effects.

## Runtime ingress profiles

The default development profile retains loopback serving. Explicit direct TLS uses Rixa's own TLS listener and exact configured HTTPS authorities; enabled origin ports agree with the listener. The runtime validates certificate names/time/server usage and protected private-key files, then serves the validated pair. It starts and checks the composed runtime before binding ingress; bind failure performs bounded shutdown and preserves the existing listener owner.

This is a runtime capability, not a production or aaPanel support promise. No forwarded header establishes trusted HTTPS or client identity. A panel proxy requires a separately reviewed routing profile; uploading files alone cannot start a persistent Go service. See [architecture](architecture.md#installation-and-hosting-boundary) for ownership and [release installation](release-install.md) for package limits.
