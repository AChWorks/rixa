# Genuine public verification fixture

`cli-v2.103.3.bundle.json` is the public Sigstore/SLSA bundle for the official [GitHub CLI v2.103.3](https://github.com/cli/cli/releases/tag/v2.103.3) distribution. It contains public certificate, signature and transparency data, not credentials or a private key. It is factual verification metadata; no GitHub CLI source or executable is included here.

The fixture archive is `gh_2.103.3_linux_amd64.tar.gz`, SHA-256 `4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d`, source `63837fb778d6a39f02be0ce76ff89f2bc8d74bdc`. The signing workflow is `cli/cli/.github/workflows/deployment.yml`, source ref `refs/heads/trunk`; its invocation is [40384165451/attempts/1](https://github.com/cli/cli/actions/runs/40384165451/attempts/1).

The release-package CI reuses its checksum-pinned helper download as this archive and runs `TestGenuineHelperTransportAndForeignPolicy` with `RIXA_ATTESTATION_PROOF_DIR` set to a private fixture directory. Ordinary runtime tests skip this explicit external-helper proof when no fixture is supplied; a skip is not verification success. The test proves genuine cryptographic transport under the isolated helper environment and rejection by the separate fixed Rixa policy. It is not a positive Rixa release attestation or a production policy override.

The upstream workflow signs several architectures together. Rixa deliberately attests only its single selected archive. If the helper pin changes, update the reused fixture archive/bundle/source expectations together, or retain this frozen corpus and fetch its matching archive separately. Do not fabricate certificates or turn the upstream publisher into an accepted Rixa identity. Trusted-root metadata may still require network access.
