#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 <version> <source-commit> [output-dir]" >&2
  exit 2
}

[[ $# -ge 2 && $# -le 3 ]] || usage

version="$1"
source_commit="$2"
output_dir="${3:-dist}"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
  echo "invalid release version: $version" >&2
  exit 2
fi
if [[ ! "$source_commit" =~ ^[0-9a-f]{40}$ ]]; then
  echo "source commit must be a full lowercase Git SHA" >&2
  exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
cd "$repo_root"

test ! -f go.work
if grep -Eq '^[[:space:]]*replace[[:space:]]' go.mod; then
  echo "release packaging refuses go.mod replace directives" >&2
  exit 1
fi
if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "release packaging requires a clean tracked working tree" >&2
  exit 1
fi
if [[ "$(git rev-parse HEAD)" != "$source_commit" ]]; then
  echo "source commit does not match checked-out HEAD" >&2
  exit 1
fi

expected_achrix="$(awk '/^[[:space:]]*sourceVersion:/ {print $2; exit}' achworks.yaml)"
actual_achrix="$(GOWORK=off go list -m -f '{{.Version}}' github.com/AChWorks/achrix)"
if [[ -z "$expected_achrix" || "$actual_achrix" != "$expected_achrix" ]]; then
  echo "AChrix dependency identity mismatch: descriptor=$expected_achrix module=$actual_achrix" >&2
  exit 1
fi
if [[ "$(go env GOSUMDB)" == "off" ]]; then
  echo "release packaging requires checksum-backed Go module verification" >&2
  exit 1
fi

go_version="$(go env GOVERSION)"
base="rixa-${version}-linux-amd64"
mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"
archive="$output_dir/${base}.tar.gz"
checksum="${archive}.sha256"
rm -f "$archive" "$checksum"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
root="$tmp/$base"
mkdir -p "$root/config" "$root/licenses"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off \
  go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X github.com/AChWorks/rixa/internal/product.Version=$version" \
  -o "$root/rixa" ./cmd/rixa

install -m 0644 .env.example "$root/.env.example"
install -m 0644 config/rixa.example.json "$root/config/rixa.example.json"
install -m 0644 LICENSE "$root/LICENSE"
install -m 0644 docs/release-install.md "$root/INSTALL.md"

cat > "$root/BUILDINFO.json" <<EOF
{
  "rixa": "$version",
  "achrix": "$actual_achrix",
  "source_commit": "$source_commit",
  "go": "$go_version",
  "target": "linux-amd64"
}
EOF
chmod 0644 "$root/BUILDINFO.json"

used_modules="$tmp/used-modules"
GOWORK=off go list -deps -f '{{with .Module}}{{if and .Path .Version .Dir}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}{{end}}{{end}}' ./cmd/rixa | LC_ALL=C sort -u > "$used_modules"

module_manifest="$tmp/third-party-modules.unsorted"
: > "$module_manifest"
while IFS="$(printf '\\t')" read -r module module_version module_dir; do
  [[ -n "$module" && -n "$module_version" && -n "$module_dir" ]] || continue
  [[ "$module" != "github.com/AChWorks/rixa" ]] || continue

  digest="$(printf '%s@%s' "$module" "$module_version" | sha256sum | awk '{print substr($1,1,16)}')"
  license_id="dep-$digest"
  license_dir="$root/licenses/$license_id"
  mkdir -p "$license_dir"

  found=0
  while IFS= read -r -d '' license_file; do
    install -m 0644 "$license_file" "$license_dir/$(basename "$license_file")"
    found=1
  done < <(find "$module_dir" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'COPYING*' -o -iname 'NOTICE*' \) -print0)

  if [[ "$found" -ne 1 ]]; then
    echo "dependency has no discoverable root license/notice file: $module $module_version" >&2
    exit 1
  fi

  printf '%s\t%s\t%s\n' "$module" "$module_version" "$license_id" >> "$module_manifest"
done < "$used_modules"

if [[ ! -s "$module_manifest" ]]; then
  echo "third-party module inventory is unexpectedly empty" >&2
  exit 1
fi
LC_ALL=C sort "$module_manifest" > "$root/THIRD_PARTY_MODULES.txt"
chmod 0644 "$root/THIRD_PARTY_MODULES.txt"
chmod 0755 "$root/rixa" "$root" "$root/config" "$root/licenses"
find "$root/licenses" -mindepth 1 -maxdepth 1 -type d -exec chmod 0755 {} +

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "release packaging modified tracked source files" >&2
  git status --short >&2
  exit 1
fi

LC_ALL=C tar \
  --sort=name \
  --mtime='@0' \
  --owner=0 --group=0 --numeric-owner \
  --format=gnu \
  -cf - -C "$tmp" "$base" | gzip -n > "$archive"

(
  cd "$output_dir"
  sha256sum "${base}.tar.gz" > "${base}.tar.gz.sha256"
)

echo "package=$archive"
echo "checksum=$checksum"
echo "rixa=$version"
echo "achrix=$actual_achrix"
echo "source_commit=$source_commit"
\t' read -r module module_version module_dir; do
  [[ -n "$module" && -n "$module_version" && -n "$module_dir" ]] || continue
  [[ "$module" != "github.com/AChWorks/rixa" ]] || continue

  digest="$(printf '%s@%s' "$module" "$module_version" | sha256sum | awk '{print substr($1,1,16)}')"
  license_id="dep-$digest"
  license_dir="$root/licenses/$license_id"
  mkdir -p "$license_dir"

  found=0
  while IFS= read -r -d '' license_file; do
    install -m 0644 "$license_file" "$license_dir/$(basename "$license_file")"
    found=1
  done < <(find "$module_dir" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'COPYING*' -o -iname 'NOTICE*' \) -print0)

  if [[ "$found" -ne 1 ]]; then
    echo "dependency has no discoverable root license/notice file: $module $module_version" >&2
    exit 1
  fi

  printf '%s\t%s\t%s\n' "$module" "$module_version" "$license_id" >> "$module_manifest"
done < <(GOWORK=off go list -m -f '{{if and .Dir .Version}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}{{end}}' all)

if [[ ! -s "$module_manifest" ]]; then
  echo "third-party module inventory is unexpectedly empty" >&2
  exit 1
fi
LC_ALL=C sort "$module_manifest" > "$root/THIRD_PARTY_MODULES.txt"
chmod 0644 "$root/THIRD_PARTY_MODULES.txt"
chmod 0755 "$root/rixa" "$root" "$root/config" "$root/licenses"
find "$root/licenses" -mindepth 1 -maxdepth 1 -type d -exec chmod 0755 {} +

LC_ALL=C tar \
  --sort=name \
  --mtime='@0' \
  --owner=0 --group=0 --numeric-owner \
  --format=gnu \
  -cf - -C "$tmp" "$base" | gzip -n > "$archive"

(
  cd "$output_dir"
  sha256sum "${base}.tar.gz" > "${base}.tar.gz.sha256"
)

echo "package=$archive"
echo "checksum=$checksum"
echo "rixa=$version"
echo "achrix=$actual_achrix"
echo "source_commit=$source_commit"
