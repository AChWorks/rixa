# Contributing

## Recover and implement

Start with [README](README.md), the canonical [product specification](docs/PROJECT-SPEC.md), the relevant current Issue and [architecture](docs/architecture.md). Preserve current Git/GitHub/CI/release truth over historical chat. Read only decision-relevant Foundation public guides; links/access never grant repository mutation authority.

Use the smallest reliable real implementation. Product semantics stay in Rixa; request proven reusable Foundation contract changes in its authorized repository. Avoid runtime source copying, private imports, empty Modules, speculative layers/controllers and unrelated refactoring/dependencies.

Executable work must add only the real manifests, secret/configuration handling, supported runtime setup and meaningful build/test/CI needed by actual behavior. Do not add a placeholder Go program, dummy tests or broad CI solely to make planning appear runnable.

## Branch, review and integration policy

Use focused branches and Pull Requests against the repository's canonical integration branch. Preserve unrelated work, resolve applicable review findings/threads, and never force through protected state or bypass required checks. Live GitHub rulesets own enforced merge methods, approvals, checks, branch protections and bypass policy; re-read them before integration instead of copying their current settings into this guide. Review the full effective diff and exact candidate/current base, and never add invented status checks or buy/change a plan as a workaround.

LOW/MEDIUM work may use explicitly recorded self-review where risk permits; self-review is not independent review. HIGH security/data/tenant/public-export/recovery work requires fresh independent read-only security/correctness review tied to the exact candidate and contract, plus the applicable integration approval. Reviewers do not change code/test/deployment state unless separately authorized.

## Validation selection

Inspect the affected behavior, failure paths, environment and full diff, then run the minimum sufficient meaningful evidence. Runtime/security/persistence/concurrency changes require relevant executable proof; documentation-only edits need inspection/link/schema/example checks where affected.

Use `git diff --check <base> <candidate>` for a committed change (or the appropriate working/staged diff). Check changed relative links/anchors, real commands and schema-valid discovery metadata. Validate `achworks.yaml` against the current authoritative [Koinon descriptor schema](https://github.com/AChWorks/koinon/blob/main/schemas/descriptor.schema.json), and record the exact schema commit identity used in that PR/validation evidence rather than hard-coding it in this durable guide.

Reuse unaffected evidence with its identity/assumptions, never relabel it as fresh. Do not duplicate broad local/CI suites or add a matrix for short tests. Expand only for actual changes, failures, unresolved interaction or required gates. Use real supported PostgreSQL for database proof; skips are not success. Product CI is introduced with the first runtime slice and binds checks to actual candidate/base.

User-facing changes prove the real browser/served path, including permissions, errors, privacy, accessibility and RTL/LTR when applicable. Performance claims identify workload/hardware and distinguish idle capacity, static traffic and management work.

## State, secrets and delivery

Use explicit bounded migrations and immutable ledgers; startup/requests do not install schema. Preserve data when disabling features. Unknown mutation acknowledgements require inspection/reconciliation, not blind replay. Code rollback does not reverse data/external effects.

Keep tokens/passwords/signing keys, private datasets and credentials out of Git, Issues and public manifests. Use protected operator/runtime sources and least privilege. Tests operate only on clearly owned disposable targets. Do not restart/mutate live infrastructure or destructively restore user data under a bootstrap task.

Done requires current acceptance reconciled against actual evidence; merged is not automatically released/deployed. Issues own live contracts/state; PRs/Git/CI own implementation/review/validation; Releases and supported deployment systems own actual delivery. Production, paid/visibility decisions and live destructive effects retain their separate gates.

Durable product/architecture/usage documentation describes stable behavior and points to the authoritative owner of mutable truth instead of duplicating release tags, commit SHAs, external schema pins or other rotating evidence. Exact AChrix dependency identity belongs in machine/runtime state such as `achworks.yaml`, the product `go.mod`/`go.sum` once runtime exists, and the active Issue/PR/CI evidence that proves the selected release. Historical migration/benchmark/release evidence may retain exact identities when reproducibility is its purpose.

First-party content is [MPL-2.0](LICENSE) unless explicitly scoped otherwise; preserve third-party notices and do not invent an Apache SDK designation or claim signed contribution consent from a template.
