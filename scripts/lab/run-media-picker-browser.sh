#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
set -euo pipefail
[[ -n "${RUNNER_TEMP:-}" && -n "${RIXA_TEST_POSTGRES_DSN:-}" && -n "${NODE_PATH:-}" ]] || { echo "Missing disposable runner prerequisites" >&2; exit 2; }
umask 077
root="$RUNNER_TEMP/rixa-media-picker-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
binary="$RUNNER_TEMP/rixa-media-picker-binary"
log="$RUNNER_TEMP/rixa-media-picker-fixture.log"
for item in "$root" "$binary" "$log"; do
  [[ ! -e "$item" ]] || { echo "Refusing existing fixture path $item" >&2; exit 2; }
done
go build -trimpath -o "$binary" ./cmd/rixa
RIXA_SELECTED_SITE_BINARY="$binary" RIXA_SELECTED_SITE_ROOT="$root" RIXA_MEDIA_PICKER_OBSERVATION=1 \
  go test ./internal/product -run '^TestMediaPickerBrowserFixture$' -count=1 -timeout=25m -v >"$log" 2>&1 &
fixture_pid=$!
cleanup() {
  if kill -0 "$fixture_pid" 2>/dev/null; then kill "$fixture_pid" 2>/dev/null || true; wait "$fixture_pid" 2>/dev/null || true; fi
}
trap cleanup EXIT
for _ in $(seq 1 240); do
  [[ ! -f "$root/ready.json" ]] || break
  if ! kill -0 "$fixture_pid" 2>/dev/null; then
    echo "Picker fixture exited before readiness" >&2
    tail -n 80 "$log" >&2
    wait "$fixture_pid" || true
    exit 1
  fi
  sleep 1
done
[[ -f "$root/ready.json" ]] || { echo "Picker fixture readiness timeout" >&2; tail -n 80 "$log" >&2; exit 1; }
if ! node scripts/lab/media-picker-browser.cjs "$root"; then
  echo "Picker Chromium acceptance failed" >&2
  tail -n 80 "$log" >&2
  exit 1
fi
touch "$root/finish.request"
if ! wait "$fixture_pid"; then
  echo "Post-browser PostgreSQL/file acceptance failed" >&2
  tail -n 80 "$log" >&2
  exit 1
fi
trap - EXIT
echo RIXA_MEDIA_PICKER_BROWSER_COMPLETE
grep -E 'RIXA_MEDIA_PICKER_BROWSER|PASS|FAIL' "$log" | tail -n 12
