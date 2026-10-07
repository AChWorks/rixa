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
expected_list="$(printf '%s\n' \
  "$base" \
  "$base/.env.example" \
  "$base/BUILDINFO.json" \
  "$base/INSTALL.md" \
  "$base/LICENSE" \
  "$base/config" \
  "$base/config/rixa.example.json" \
  "$base/rixa" | LC_ALL=C sort)"

if [[ "$actual_list" != "$expected_list" ]]; then
  echo "release package contains unexpected or missing entries" >&2
  diff -u <(printf '%s\n' "$expected_list") <(printf '%s\n' "$actual_list") || true
  exit 1
fi

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

version_output="$("$root/rixa" version)"
grep -Fq "\"rixa\":\"$version\"" <<<"$version_output"
grep -Fq "\"achrix\":\"$achrix_version\"" <<<"$version_output"

grep -Fq "\"rixa\": \"$version\"" "$root/BUILDINFO.json"
grep -Fq "\"achrix\": \"$achrix_version\"" "$root/BUILDINFO.json"
grep -Fq "\"source_commit\": \"$source_commit\"" "$root/BUILDINFO.json"
grep -Fq "\"target\": \"linux-amd64\"" "$root/BUILDINFO.json"

echo "release package verified: $base"
