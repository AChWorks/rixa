# Interface languages and installed catalogs

Rixa uses the released AChrix `admin/localize` support. English/LTR is the default web interface; Persian/RTL is bundled and selectable. The CLI and full-screen terminal always use English. Translation does not rewrite content, change permissions, translate machine IDs, or choose a calendar, currency or date format.

## Select a web interface language

The protected configuration's `language` field chooses the instance default (`en` when omitted). The administration language screen lists available locales. Selection is a presentation preference for that exact hostname, retained across administration navigation. Control and site origins remain independent. Unsupported selections fail before the requested operation.

Appearance's **Site language** is separate: its current content/publication policy supports English and Persian. Installing another interface language does not expand that contract or modify stored Appearance rows. Public labels and direction follow content language; administration follows interface language. Existing static generations retain their bytes until explicitly republished.

## Install another interface language after building

Bundled English/Persian require no external files. To install data-only translations in a compatible executable:

1. Prepare a dedicated private directory readable by the runtime, separate from Media, public publication, uploads, application source and credentials. Use an absolute canonical path; do not grant site users write access.
2. Set `catalog_root` in the protected runtime JSON to that directory. This is operator configuration, not a web upload field.
3. Add UTF-8 JSON files following the [AChrix catalog contract](https://github.com/AChWorks/achrix/blob/HEAD/docs/web/localization.md). Each file owns one domain/locale. The product domain is `rixa`; shared screens use Admin, Identity and Media domains. Preserve existing English keys and named parameters. Partial packs use English fallback. Never repeat bundled domain/locale/key identities.
4. Validate using the trusted selected executable: `rixa check --config /absolute/rixa.json`. This also requires valid runtime prerequisites and starts no ingress. Keep the running instance available when validation fails.
5. Restart or reconstruct through the supported operator path, then select the installed language. Data-only language installation needs no source edit, dependency update or binary rebuild.

Catalogs are validated immutable in-memory snapshots loaded at construction. There is no per-label disk/database access, automatic watcher, translation editor or live mutation of a serving snapshot. Existing instances keep their prior snapshot. Restarting is an operational action; it does not update a binary or replace a native operation's frozen manager. Never overwrite that manager to install translations.

Locale metadata supplies tag, native name and direction. Invalid JSON, conflicting metadata, duplicate identities, missing English baselines, incorrect parameters and bounded-input violations fail construction. Preserve diagnostic source/message context in authorized operator output, without printing private interpolation values. Correct or remove the bad pack and validate again. Recovery restores the prior configuration/catalog files; binary reversal is not database rollback.

## Maintain translations

Product resources are under `internal/product/locales/`. Production Go, JavaScript and templates reference stable domain/key identities using request localization and the shared browser API. Persian user content and Unicode test fixtures remain valid. Translations are plain text escaped by normal templates/DOM text nodes; they cannot supply executable HTML, JavaScript or templates. Translate complete messages with named placeholders rather than sentence fragments.

The [upstream guide](https://github.com/AChWorks/achrix/blob/HEAD/docs/web/localization.md) owns syntax, fallback, plurals and bounds. Rixa owns composition, selection, private roots and reconstruction. It adds no second translation engine or mandatory Core service. Additional UI languages do not establish universal font shaping, accessibility or locale-formatting conformance.
