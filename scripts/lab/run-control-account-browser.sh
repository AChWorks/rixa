#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
set -euo pipefail

if [[ -z "${RUNNER_TEMP:-}" || -z "${RIXA_TEST_POSTGRES_DSN:-}" || -z "${NODE_PATH:-}" ]]; then
  echo "Missing isolated runner temporary root, disposable PostgreSQL DSN or pinned Playwright path" >&2
  exit 2
fi
umask 077
root="$RUNNER_TEMP/rixa-control-account-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
binary="$RUNNER_TEMP/rixa-control-account-binary"
if [[ -e "$root" || -e "$binary" ]]; then
  echo "Refusing to reuse fixture root or candidate binary" >&2
  exit 2
fi
go build -trimpath -o "$binary" ./cmd/rixa
fixture_log="$RUNNER_TEMP/rixa-control-account-fixture.log"
RIXA_CONTROL_ACCOUNT_BINARY="$binary" \
RIXA_CONTROL_ACCOUNT_ROOT="$root" \
RIXA_CONTROL_ACCOUNT_OBSERVATION=1 \
go test ./internal/product -run '^TestControlAccountBrowserFixture$' -count=1 -timeout=13m -v >"$fixture_log" 2>&1 &
fixture_pid=$!
cleanup() {
  if kill -0 "$fixture_pid" 2>/dev/null; then
    kill "$fixture_pid" 2>/dev/null || true
    wait "$fixture_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT

for _ in $(seq 1 180); do
  if [[ -f "$root/ready.json" ]]; then break; fi
  if ! kill -0 "$fixture_pid" 2>/dev/null; then
    echo "Rixa browser fixture exited before readiness" >&2
    cat "$fixture_log" >&2
    wait "$fixture_pid" || true
    exit 1
  fi
  sleep 1
done
if [[ ! -f "$root/ready.json" ]]; then
  echo "Rixa browser fixture did not become ready within bounded time" >&2
  tail -n 40 "$fixture_log" >&2
  exit 1
fi
if ! node scripts/lab/control-account-browser.cjs "$root"; then
  echo "Chromium account browser acceptance failed; fixture retained in temporary runner root" >&2
  tail -n 50 "$fixture_log" >&2
  exit 1
fi
touch "$root/finish.request"
if ! wait "$fixture_pid"; then
  echo "Rixa PostgreSQL/file/identity post-browser verification failed" >&2
  tail -n 80 "$fixture_log" >&2
  exit 1
fi
trap - EXIT
echo "RIXA_CONTROL_ACCOUNT_BROWSER_COMPLETE"
grep -E 'RIXA_CONTROL_ACCOUNT_BROWSER|PASS|FAIL' "$fixture_log" | tail -n 10
