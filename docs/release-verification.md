# Verify a Rixa release before execution

Release authentication checks who built an exact artifact and which source produced it. It is separate from package completeness, runtime compatibility and authority to install or update a host. The verifier does not extract or execute the supplied archive and does not grant host privileges.

## Required evidence

New releases produced by the provenance-enabled release workflow provide three adjacent assets:

- `rixa-<version>-linux-amd64.tar.gz`;
- the archive's `.sha256` integrity file;
- the archive's `.sigstore.json` GitHub attestation bundle.

Use the exact selected release tag and source commit obtained from the trusted GitHub release/tag information. Keep stable and development prereleases distinct. A filename, the latest version label or an adjacent checksum alone does not establish the producer. Existing historical packages without an attestation remain historical manual-development packages; the authentication command rejects missing evidence.

The attestation policy binds the archive digest, Rixa publisher repository and owner identities, release workflow, tag reference, exact source commit, GitHub Actions issuer and hosted-runner identity. A different repository, branch build, source, workflow, runner or artifact must not pass merely because a verifier subprocess exited successfully. Repository/owner identity changes require a reviewed policy change.

## Verification command

With a trusted Rixa executable that contains this command and a compatible trusted official GitHub CLI:

```bash
rixa artifact verify \
  --archive /private/download/rixa-<version>-linux-amd64.tar.gz \
  --bundle /private/download/rixa-<version>-linux-amd64.tar.gz.sigstore.json \
  --release <version> \
  --source-commit <exact-tag-source-commit>
```

An optional `--sha256` supplies an additional expected digest; it never replaces provenance verification. The command returns `rixa.artifact-verification.v1` JSON. Accept success only with exit zero and `status: verified` / `code: ok`; identity fields on a failed result describe the requested policy, not authenticated approval. It returns a nonzero exit when required evidence, a compatible helper or a policy check fails. Follow the specific prerequisite or diagnostic result; there is no signature-bypass option.

The normal CMS runtime does not depend on GitHub CLI. Use the helper's [official installation guidance](https://github.com/cli/cli/blob/trunk/docs/install_linux.md); an operating-system package named `gh` may be too old to provide the required attestation flags. The verifier reports its supported helper requirements. Never obtain the trusted verifier by executing the unverified Rixa payload itself.

A local bundle permits verification without logging into GitHub. Initial refresh of trusted signing metadata may still need network access; this is not a fully air-gapped verification profile. A future one-line bootstrap must independently establish its trusted helper before it executes elevated product code.

## Meaning and limits of success

Success identifies the bytes that were actually checked. It does not make an ordinary download pathname immutable. Installation must stage those exact bytes in protected storage, recheck the recorded digest and bind the intended candidate before replacement or privileged execution. The package's compiled component metadata, licenses, supported platform and schema compatibility need their own checks.

A valid attestation is neither a successful installation nor permission to deploy, migrate, overwrite or delete data. The [installation and management experience](installation-and-management.md) owns those operation/recovery boundaries; [release installation](release-install.md) owns the supported package procedure. Actual clean-server, full-screen terminal and panel delivery require their own executable evidence.

## Publication failure and re-entry

The tag-triggered publisher signs and publishes only on the first workflow attempt. Package-only manual runs and Pull Requests never publish or sign a release. The writer verifies all three assets in a draft before its single final publication transition; existing releases are not overwritten.

An uncertain write acknowledgement is inspected without repeating the write. If the outcome remains unknown, the draft and original run remain evidence for operator reconciliation. A workflow rerun is not a resume mechanism, and deleting/recreating the same tag is not a retry mechanism. Inspect the original run, authoritative tag and release/asset state before choosing recovery; do not infer non-application from a missing read or force an overwrite.
