#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
set -euo pipefail

# This is a CI/private helper preparation step, not the public Rixa installer.
# The trust pin is verified before any archive parsing or helper execution.
if [[ $# != 1 || "$1" != /* || "$1" == / || "$1" == *$'\n'* ]]; then
  echo "usage: $0 <new-absolute-private-directory>" >&2
  exit 2
fi
helper_dir="$1"
version="2.103.3"
sha256="4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d"
url="https://github.com/cli/cli/releases/download/v${version}/gh_${version}_linux_amd64.tar.gz"

# Refuse to adopt/overwrite an existing directory or an unexpected tool.
mkdir -m 0700 "$helper_dir"
archive="${helper_dir}/gh.tar.gz"
curl --proto '=https' --proto-redir '=https' --tlsv1.2 \
  --fail --silent --show-error --location \
  --connect-timeout 15 --max-time 120 "$url" -o "$archive"
printf '%s  %s\n' "$sha256" "$archive" | sha256sum -c - >&2
python3 -B - "$archive" "$helper_dir/gh" "$version" <<'PY'
import os
from pathlib import Path
import sys
import tarfile

archive, output, version = sys.argv[1:]
expected = f"gh_{version}_linux_amd64/bin/gh"
with tarfile.open(archive, "r:gz") as source:
    matches = [entry for entry in source if entry.name == expected]
    if len(matches) != 1 or not matches[0].isfile() or not 0 < matches[0].size <= 128 << 20:
        raise SystemExit("verified helper archive has an unexpected binary entry")
    with source.extractfile(matches[0]) as member, open(output, "xb") as target:
        data = member.read((128 << 20) + 1)
        if len(data) != matches[0].size:
            raise SystemExit("helper binary size is inconsistent")
        target.write(data)
os.chmod(output, 0o755)
PY
helper="$helper_dir/gh"
"$helper" --version >&2
help="$("$helper" attestation verify --help)"
for capability in --bundle --repo --source-ref --source-digest --signer-digest --signer-workflow --predicate-type --cert-oidc-issuer --deny-self-hosted-runners --hostname --format; do
  grep -Fq -- "$capability" <<< "$help" || {
    echo "pinned verifier does not support required capability: $capability" >&2
    exit 1
  }
done
printf '%s\n' "$helper"
