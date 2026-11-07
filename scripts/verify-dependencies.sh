#!/usr/bin/env bash
set -euo pipefail

mode="${1:---tsv}"
[[ $# -le 1 && ( "$mode" == "--tsv" || "$mode" == "--json" ) ]] || {
  echo "usage: $0 [--tsv|--json]" >&2
  exit 2
}
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$script_dir/.."
export GOWORK=off

[[ ! -f go.work ]] || {
  echo "dependency verification refuses a workspace file" >&2
  exit 1
}
if grep -Eq '^[[:space:]]*replace([[:space:]]|\()' go.mod; then
  echo "dependency verification refuses replacement directives" >&2
  exit 1
fi
[[ "$(go env GOSUMDB)" != "off" ]] || {
  echo "checksum-backed Go module verification is required" >&2
  exit 1
}

# This deliberately reads the descriptor's small canonical plain-scalar mapping,
# not arbitrary YAML. Invalid/duplicate/unsupported entries fail closed below.
expected="$(awk '
  /^      components:$/ { if (found++) exit 1; active=1; next }
  active && /^        [^ ]/ {
    if (NF != 2 || $1 !~ /:$/) exit 1
    sub(/:$/, "", $1); print $1 "\t" $2; next
  }
  active && /^[^ ]|^      [^ ]/ { active=0 }
  END { if (!found) exit 1 }
' achworks.yaml | LC_ALL=C sort)"
# The descriptor separately records Foundation modules present only through
# tagged dependency manifests. They are NOT compiled Rixa capabilities.
transitive="$(awk '
  /^      transitiveModules:$/ { if (found++) exit 1; active=1; next }
  /^      transitiveModules: \{\}$/ { if (found++) exit 1; active=0; next }
  active && /^        [^ ]/ {
    if (NF != 2 || $1 !~ /:$/) exit 1
    sub(/:$/, "", $1); print $1 "\t" $2; next
  }
  active && /^[^ ]|^      [^ ]/ { active=0 }
  END { if (!found) exit 1 }
' achworks.yaml | LC_ALL=C sort)"
expected_paths="$(printf '%s\n' "$expected" | cut -f1)"
required_paths="$(printf '%s\n' \
  github.com/AChWorks/achrix \
  github.com/AChWorks/achrix/admin \
  github.com/AChWorks/achrix/audit \
  github.com/AChWorks/achrix/bridge \
  github.com/AChWorks/achrix/identity \
  github.com/AChWorks/achrix/installation \
  github.com/AChWorks/achrix/media \
  github.com/AChWorks/achrix/multisite \
  github.com/AChWorks/achrix/oauth | LC_ALL=C sort)"
[[ "$expected_paths" == "$required_paths" ]] || {
  echo "descriptor must identify the complete selected AChrix component set" >&2
  exit 1
}
while IFS=$'\t' read -r module version; do
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || {
    echo "invalid released component identity: $module" >&2
    exit 1
  }
done <<<"$(printf '%s\n%s\n' "$expected" "$transitive" | sed '/^[[:space:]]*$/d')"

replaced="$(go list -m -f '{{if .Replace}}{{.Path}}{{end}}' all)"
[[ -z "$replaced" ]] || {
  echo "resolved module graph contains a replacement" >&2
  exit 1
}
actual="$(go list -m -f '{{.Path}} {{.Version}}' all | awk '$1 ~ /^github\.com\/AChWorks\/achrix($|\/)/ {print $1 "\t" $2}' | LC_ALL=C sort)"
expected_graph="$(printf '%s\n%s\n' "$expected" "$transitive" | sed '/^[[:space:]]*$/d' | LC_ALL=C sort)"
[[ "$actual" == "$expected_graph" ]] || {
  echo "resolved AChrix graph differs from declared selected and transitive module versions" >&2
  exit 1
}
core="$(awk '$1 == "github.com/AChWorks/achrix" { print $2 }' <<<"$actual")"
source_version="$(awk '/^      sourceVersion:/ {print $2}' achworks.yaml)"
[[ "$core" == "$source_version" ]] || {
  echo "descriptor Core identity differs from selected component set" >&2
  exit 1
}
go mod verify >/dev/null

if [[ "$mode" == "--json" ]]; then
  awk 'BEGIN { printf "{" } { printf "%s\"%s\":\"%s\"", (NR == 1 ? "" : ","), $1, $2 } END { print "}" }' <<<"$expected"
else
  printf '%s\n' "$expected"
fi
