#!/usr/bin/env bash
# Fetch only the immutable published baseline used by the retained-state proof.
set -euo pipefail

[[ $# -eq 1 ]] || { echo "usage: $0 <private-output-directory>" >&2; exit 2; }
output_dir="$1"
[[ "$output_dir" == /* && ! -L "$output_dir" ]] || { echo "output directory must be absolute and not a symlink" >&2; exit 2; }
mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd -P)"
binary="$output_dir/rixa-v0.1.0"
binary_sha256="f43a446486b7803dc061e379d7f76bd2b172b5d01e06ca06799ee59498f93188"
archive_sha256="9a221924b8737ad1710a5990b274040263e918abcae5a40335f2cca5958857d0"

if [[ -e "$binary" ]]; then
  [[ -f "$binary" && ! -L "$binary" && "$(sha256sum "$binary" | awk '{print $1}')" == "$binary_sha256" ]] ||
    { echo "existing baseline binary identity mismatch" >&2; exit 1; }
  echo "$binary"
  exit 0
fi

tmp="$(mktemp -d "$output_dir/.download.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' \
  --connect-timeout 15 --max-time 90 --max-filesize 8388608 \
  --output "$tmp/baseline.tar.gz" \
  https://github.com/AChWorks/rixa/releases/download/v0.1.0/rixa-v0.1.0-linux-amd64.tar.gz
[[ "$(sha256sum "$tmp/baseline.tar.gz" | awk '{print $1}')" == "$archive_sha256" ]] ||
  { echo "published baseline archive SHA-256 mismatch" >&2; exit 1; }

# Read one exact member, rather than extracting archive-controlled paths.
tar -xOzf "$tmp/baseline.tar.gz" rixa-v0.1.0-linux-amd64/rixa > "$tmp/rixa"
[[ "$(sha256sum "$tmp/rixa" | awk '{print $1}')" == "$binary_sha256" ]] ||
  { echo "published baseline binary SHA-256 mismatch" >&2; exit 1; }
chmod 0700 "$tmp/rixa"
# Do not replace a concurrently created output, even if it appears valid.
ln "$tmp/rixa" "$binary"
echo "$binary"
