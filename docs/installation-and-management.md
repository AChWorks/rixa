# Installation and management experience

This is the accepted experience contract for supported Rixa installation and management paths. It guides implementation; it does not make a one-line installer, TUI, web setup, updater or panel adapter available. The [release installation guide](release-install.md) owns runnable package instructions, the [native operator guide](server-management.md) owns the implemented development subset, the [architecture](architecture.md) owns technical boundaries, and Issues/PRs/CI/releases own implementation and support evidence.

## Users and scope

Infrastructure administrators operate the installation, its shared product artifact and declared sites. Site administrators manage their authorized site content, Media, appearance and publication. An installation-wide update affects every site using that build; a site administrator receives no implicit permission to update shared code, change the host or remove the installation.

Direct-server and control-panel installations are different deployment profiles over the same product operations. Each profile declares its supported artifact, database, filesystem, persistent process, privilege, ingress/TLS and recovery capabilities. A panel name, writable web root or detected command does not establish those outcomes.

## Shared operations, suitable interfaces

One product operation model owns intended targets, persisted progress, result/error categories and reconciliation. A stable bootstrap entry, interactive full-screen terminal interface, direct CLI and web management consume it rather than each implementing an installer/updater.

AChrix [Installation](https://github.com/AChWorks/achrix/blob/HEAD/installation/README.md) supplies pure requirement/evidence/adapter/progress assessment, immutable plans and rechecking. Rixa supplies trustworthy probes, supported adapters, protected configuration, locks/fencing, durable attempts and actual effects. Reusable Foundation gaps return to their owning repository; product deployment work does not create a competing generic Foundation engine.

Interface consistency means the same task names, scope, state and consequences, with layouts suited to terminal and browser. Direct CLI remains useful when web ingress or the application is unavailable. Provide meaningful help, stable exit/error categories and a plain/non-interactive output path; machine-readable results must not depend on screen control sequences or translated prose.

Host actions cross a separately authorized, narrow deployment boundary. Neither a website login nor an AChrix capability grants root or arbitrary shell access. A web-triggered replacement must continue and retain its result across replacement/restart of the application itself, using the selected supported deployment mechanism rather than a generic root web shell.

## Bootstrap and package

The documented one-line entry remains stable as versions change. It resolves the selected channel and exact compatible release/artifact, verifies authenticity/integrity before privileged payload execution, detects existing installations and then opens the appropriate install or management path. Re-running does not create duplicate installations or administrators.

Keep development/prerelease and stable channels explicit. An unavailable stable selection produces an actionable failure; it never silently selects development. A mutable source entry is not an immutable stable release. A normal operator receives a prebuilt artifact for a validated OS/architecture and does not compile Rixa or choose arbitrary latest component combinations.

Bootstrap trust has two delivery steps. First publish and independently verify a bootstrap-capable starter. A subsequent reviewed immutable generation pins that exact published starter tag/source/archive digest and the official verifier helper's version/archive digest. Authenticate pins before parsing or executing their bytes, then reuse the product's shared release fetch/verification and authenticated package parser for candidate selection. The starter's source cannot contain its own final archive digest. Keep the ordinary three release assets; the entry does not introduce a second provenance or installation engine. The [authenticated entry guide](bootstrap-entry.md) owns the concrete generation command, base tools and retained initializer boundary.

Discovery validates retained installation, update and prerequisite ownership before choosing a path. Existing work uses its recorded exact frozen manager, and a new update is an explicit choice. A newly staged manager opens the relevant reviewed operation; discovery or re-entry never automatically applies, resumes, reinstalls or resets state. The [native guide](server-management.md#bootstrap-starter-and-postgresql-prerequisite-source) distinguishes source commands from delivered one-line support.

The package contains the necessary application and interface material, embedded migrations, build/component identity and license/install information. PostgreSQL, TLS and protected deployment configuration remain declared inputs or supported provisioning outcomes. Replaceable application files stay separate from retained configuration, credentials and site state.

## Guided setup

Ask only for information that cannot safely be discovered or generated. Explain why a choice matters in user terms.

| Stage | User outcome |
| --- | --- |
| Inspect | Identify the host profile, existing installation and usable prerequisites without mutation. |
| Configure | Collect the required domains, deployment/database choice and initial administrator inputs, with protected secret handling. |
| Review | Show the exact target, selected release, changes, retained state and any required operator action before effects. |
| Apply | Execute bounded ordered operations with durable progress and truthful stage results. |
| Verify | Check migrations, bootstrap, persistent service, readiness and the actual administration route; report the working URL and next useful action. |

Keep valid entries when a field fails validation, and return focus to the affected input. Earlier choices remain reviewable; changing them revalidates dependent fields. Do not repeatedly request already-known values or lose ordinary progress on navigation.

Persist configuration and resumable operation state in protected product-owned locations; do not place passwords, DSNs, tokens or private state in public roots, URLs or ordinary reports. A setup endpoint has an installation-specific, bounded authorization mechanism and is closed after successful setup. An uninitialized public page must not grant ownership to its first visitor.

## Terminal layout and feedback

Use a full-screen menu when the terminal supports it. The header gives the selected installation, active Rixa version, service/health state, administration URL and any material warning. Component/build detail belongs in an inspectable detail view rather than crowding the default header.

Group navigation by user task, for example overview, service, install/update, configuration, diagnostics and advanced recovery/removal. Keep a clear current location, back/cancel path and keyboard guidance. Hide irrelevant complexity progressively; do not create an empty submenu for a capability that does not exist.

Use semantic color consistently for selection, information, success, warning and destructive/error states. Text and symbols also communicate the meaning. Support keyboard operation, resizing, small terminals and color-disabled/plain fallback. Do not require terminal features for automated use; verify Persian/mixed-direction presentation on actual supported terminals rather than assuming browser RTL behavior.

Feedback distinguishes input correction, unmet prerequisites, transient failure, unavailable authority and an uncertain prior effect. Keep recoverable errors within the current task. Show what failed and what can be done next, with an optional diagnostic detail view, stable reason and operation reference. Preserve useful logs/context while protecting actual secrets. Do not fabricate progress percentages, bury a blocking failure in a disappearing message or retry an uncertain mutation merely to keep the menu moving.

## Prerequisites and panel setup

Reuse existing compatible prerequisites. Provision or upgrade a dependency only through the selected supported adapter and its current authority. An older version number alone does not justify changing a healthy compatible service. A database major upgrade or panel-managed/shared service change requires its own compatibility and recovery plan; ordinary Rixa update does not silently adopt that ownership.

On constrained hosts, report every missing mandatory outcome and the exact capability the hosting administrator must expose. Preserve existing listeners and panel-managed sites. Reverse-proxy/TLS/client-address trust needs a reviewed profile; generic forwarded headers are not an escape hatch around the existing browser boundary.

The first panel path targets aaPanel capabilities, with other panels supported only after their own evidence. The intended path is upload a prebuilt package, register/start the persistent Go service through the supported panel facility, open protected web setup, supply required inputs, verify and finish. Uploading a package alone does not start a Go service. A panel-specific package or integration may later remove repeated manual steps when its supported mechanism is proven.

## Update and recovery

Offer discovery of suitable updates and a clear administrator-triggered update first. Unattended updates are a later opt-in behavior after compatibility, interruption and recovery paths have their own evidence.

The operation identifies the compatible Rixa/Core/component set and impact, verifies/stages it separately from active code, establishes the needed coherent recovery evidence, serializes conflicting lifecycle work, applies explicit migrations, replaces/activates the artifact and verifies the active identity and critical paths. Status remains observable from CLI and web even after the initiating connection closes.

Preserve configuration, credentials, accounts, site data, original Media, appearance and publication relationships by default. Check session/revocation compatibility explicitly. An update of shared code does not mix component versions per site or dynamically replace running Go packages.

Record intent/attempt before each effect and verification after it. Missing or unknown observations after an attempted effect require reconciliation; a request timeout is not proof that nothing happened. A previously verified resource that drifts is not permission to recreate it. Avoid duplicate accounts, databases, services and migrations on resume.

Automatic rollback is offered only where the current schema/state makes it safe. Reverting a binary is not database rollback. Otherwise retain the failure evidence, isolation and actionable forward-recovery path. Backups needed for recovery must have credible scope, integrity and restore evidence; the current controlled transfer profile does not establish production disaster recovery.

## Uninstall and retained data

Ordinary uninstall removes positively identified product-owned executable/service material and preserves durable site data and useful configuration by default. Show the affected installation/sites, what will be removed, what will remain and the supported reinstall/recovery path.

Destructive purge is a distinct explicit action with its own scope and confirmation. Never infer ownership from directory proximity, and do not remove shared PostgreSQL, web servers, panel configuration or unknown sibling files. Preserve the selected host adapter's trustworthy pre-change shared-state baseline when restoration is needed.

## CMS administration and development quality

Product capabilities evolve alongside installation and operations. Content/revisions, Media selection, appearance, publication and selected-site control flows should become easier to understand and complete without weakening their existing authorization, privacy or site-transfer contracts. A lifecycle project does not replace CMS development with host tooling.

Use a modern minimal hierarchy, readable typography, restrained semantic color and clear primary actions. Keep selected site and action scope visible, preserve drafts and valid form state, and explain empty, denied, loading, conflict and failure states in context. Reuse supported AChrix Admin seams; shared-shell improvements remain with AChrix rather than copying its runtime into Rixa.

Support Persian/English and RTL/LTR through real language/direction metadata, readable mixed-script values and tested keyboard/focus behavior. Target [WCAG 2.2 AA](https://www.w3.org/TR/WCAG22/) for the delivered web flows; conformance is a tested outcome, not a claim created by this contract.

Choose small readable code, explicit ownership and supported extension boundaries. Reuse actual capabilities before adding dependencies or new infrastructure; measure relevant CPU/memory/database/I/O costs when optimization decisions depend on them. Avoid speculative frameworks, empty Modules, universal lifecycle services or abstractions justified only by imagined scale.

Per-site optional features keep code availability, composition, resource activation, authorization and destructive removal separate. Disabling retains data; re-enabling validates compatibility. Hot plugins, arbitrary per-site Foundation versions, bulk operations, Search and WordPress feature parity do not follow from the desired experience.

## Evidence before support

Before advertising an operation for a profile, prove its relevant paths: realistic first install, reuse of compatible prerequisites, actionable constrained-host errors, interrupted execution and resume, persistent restart, authenticated administration, coherent update or ownership-aware removal. User-facing evidence includes input correction without lost valid state, keyboard/back/cancel behavior, readable status/errors, supported narrow/wide and RTL/LTR views and recovery from unavailable web access.

Sequence implementations from an honestly identified compatible runtime and retained-state proof, to common product operations, one concrete server path, then panel/web integration and managed update. A supported profile may implement a subset before the whole experience exists; user documentation must identify that subset truthfully.
