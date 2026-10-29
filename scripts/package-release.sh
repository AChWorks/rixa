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

components_json="$(./scripts/verify-dependencies.sh --json)"
actual_achrix="$(GOWORK=off go list -m -f '{{.Version}}' github.com/AChWorks/achrix)"

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

version_output="$("$root/rixa" version)"
if ! grep -Fq "\"achrix_components\":$components_json" <<<"$version_output"; then
  echo "compiled AChrix component identities differ from selected dependencies" >&2
  exit 1
fi

install -m 0644 .env.example "$root/.env.example"
install -m 0644 config/rixa.example.json "$root/config/rixa.example.json"
install -m 0644 LICENSE "$root/LICENSE"
# Keep operator guides beside the binary; links to unbundled source documents
# follow this exact packaged commit rather than a later default-branch version.
python3 - "$root" "$source_commit" <<'PY'
import pathlib
import posixpath
import re
import sys
from urllib.parse import urlsplit

output = pathlib.Path(sys.argv[1])
commit = sys.argv[2]
exports = {
    "docs/release-install.md": "INSTALL.md",
    "docs/release-verification.md": "VERIFY.md",
    "docs/server-management.md": "MANAGE.md",
}
aliases = dict(exports, **{"LICENSE": "LICENSE"})
current = "https://github.com/AChWorks/rixa/blob/HEAD/"
pinned = "https://github.com/AChWorks/rixa/blob/" + commit + "/"
for source, destination in exports.items():
    def rewrite(match):
        target = match.group(1)
        if target.startswith("#"):
            return match.group(0)
        if target.startswith(current):
            relative = target[len(current):]
            parts = urlsplit(relative)
            path = posixpath.normpath(parts.path)
        else:
            parts = urlsplit(target)
            if parts.scheme or parts.netloc:
                return match.group(0)
            path = posixpath.normpath(posixpath.join(posixpath.dirname(source), parts.path))
        entry = pathlib.Path(path)
        if path.startswith(("/", "../")) or path == ".." or entry.is_symlink() or not entry.is_file():
            raise SystemExit("invalid packaged guide source link: " + source + " -> " + target)
        address = aliases.get(path, pinned + path)
        if parts.query:
            address += "?" + parts.query
        if parts.fragment:
            address += "#" + parts.fragment
        return "](" + address + ")"
    text = re.sub(r"\]\(([^)\s]+)\)", rewrite, pathlib.Path(source).read_text(encoding="utf-8"))
    (output / destination).write_text(text, encoding="utf-8")
    (output / destination).chmod(0o644)
PY

cat > "$root/BUILDINFO.json" <<EOF
{
  "rixa": "$version",
  "achrix": "$actual_achrix",
  "achrix_components": $components_json,
  "source_commit": "$source_commit",
  "go": "$go_version",
  "target": "linux-amd64"
}
EOF
chmod 0644 "$root/BUILDINFO.json"

used_modules="$tmp/used-modules"
GOWORK=off go list -deps -f '{{with .Module}}{{if and .Path .Version .Dir}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}' ./cmd/rixa | LC_ALL=C sort -u > "$used_modules"

module_manifest="$tmp/third-party-modules.unsorted"
: > "$module_manifest"
while IFS='|' read -r module module_version module_dir; do
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
