#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: $0 <archive> <checksum> <version> <source-commit> <achrix-version>" >&2
  exit 2
fi

archive="$1"
checksum="$2"
version="$3"
source_commit="$4"
achrix_version="$5"
base="rixa-${version}-linux-amd64"

[[ -f "$archive" && -f "$checksum" ]] || {
  echo "release archive/checksum missing" >&2
  exit 1
}
[[ "$(basename "$archive")" == "${base}.tar.gz" ]] || {
  echo "unexpected archive name" >&2
  exit 1
}
[[ "$(basename "$checksum")" == "${base}.tar.gz.sha256" ]] || {
  echo "unexpected checksum name" >&2
  exit 1
}

(
  cd "$(dirname "$archive")"
  sha256sum -c "$(basename "$checksum")"
)

actual_list="$(tar -tzf "$archive" | sed 's#/$##' | LC_ALL=C sort -u)"
for required in \
  "$base" \
  "$base/.env.example" \
  "$base/BUILDINFO.json" \
  "$base/INSTALL.md" \
  "$base/LICENSE" \
  "$base/THIRD_PARTY_MODULES.txt" \
  "$base/config" \
  "$base/config/rixa.example.json" \
  "$base/licenses" \
  "$base/rixa"; do
  grep -Fxq "$required" <<<"$actual_list" || {
    echo "required release entry missing: $required" >&2
    exit 1
  }
done

while IFS= read -r entry; do
  case "$entry" in
    "$base"|"$base/.env.example"|"$base/BUILDINFO.json"|"$base/INSTALL.md"|"$base/LICENSE"|"$base/THIRD_PARTY_MODULES.txt"|"$base/config"|"$base/config/rixa.example.json"|"$base/licenses"|"$base/rixa")
      ;;
    "$base/licenses/"*)
      ;;
    *)
      echo "unexpected release entry: $entry" >&2
      exit 1
      ;;
  esac
done <<<"$actual_list"

if ! tar -tvzf "$archive" | awk 'substr($1,1,1) != "-" && substr($1,1,1) != "d" { exit 1 }'; then
  echo "release package contains a non-regular/non-directory entry" >&2
  exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
tar -xzf "$archive" --no-same-owner --no-same-permissions -C "$tmp"
root="$tmp/$base"

[[ -x "$root/rixa" ]] || {
  echo "packaged binary is not executable" >&2
  exit 1
}
unsafe="$(find "$root" \( -type l -o -type b -o -type c -o -type p -o -type s \) -print -quit)"
[[ -z "$unsafe" ]] || {
  echo "unsafe packaged filesystem entry: $unsafe" >&2
  exit 1
}
[[ -z "$(find "$root/licenses" -mindepth 2 -type d -print -quit)" ]] || {
  echo "unexpected nested third-party license directory" >&2
  exit 1
}

version_output="$("$root/rixa" version)"
grep -Fq "\"rixa\":\"$version\"" <<<"$version_output"
grep -Fq "\"achrix\":\"$achrix_version\"" <<<"$version_output"

grep -Fq "\"rixa\": \"$version\"" "$root/BUILDINFO.json"
grep -Fq "\"achrix\": \"$achrix_version\"" "$root/BUILDINFO.json"
grep -Fq "\"source_commit\": \"$source_commit\"" "$root/BUILDINFO.json"
grep -Fq "\"target\": \"linux-amd64\"" "$root/BUILDINFO.json"

manifest_count=0
while IFS=$'\t' read -r module module_version license_id extra; do
  [[ -n "$module" && -n "$module_version" && "$license_id" =~ ^dep-[0-9a-f]{16}$ && -z "${extra:-}" ]] || {
    echo "invalid third-party module inventory entry" >&2
    exit 1
  }
  license_dir="$root/licenses/$license_id"
  [[ -d "$license_dir" ]] || {
    echo "missing license directory for $module $module_version" >&2
    exit 1
  }
  found=0
  while IFS= read -r license_file; do
    found=1
    name="$(basename "$license_file")"
    case "${name^^}" in
      LICENSE*|COPYING*|NOTICE*) ;;
      *)
        echo "unexpected third-party license filename: $name" >&2
        exit 1
        ;;
    esac
  done < <(find "$license_dir" -maxdepth 1 -type f -print)
  [[ "$found" -eq 1 ]] || {
    echo "empty third-party license directory for $module $module_version" >&2
    exit 1
  }
  manifest_count=$((manifest_count + 1))
done < "$root/THIRD_PARTY_MODULES.txt"

license_dir_count="$(find "$root/licenses" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
[[ "$manifest_count" -gt 0 && "$manifest_count" -eq "$license_dir_count" ]] || {
  echo "third-party module/license inventory mismatch" >&2
  exit 1
}

echo "release package verified: $base"
