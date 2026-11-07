#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
set -euo pipefail
[[ -n "${RUNNER_TEMP:-}" && -n "${RIXA_TEST_POSTGRES_DSN:-}" && -n "${NODE_PATH:-}" ]] || { echo "Missing isolated PostgreSQL/Playwright runner prerequisites" >&2; exit 2; }
umask 077
root="$RUNNER_TEMP/rixa-theme-browser-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
log="$RUNNER_TEMP/rixa-theme-browser-fixture.log"
[[ ! -e "$root" && ! -e "$log" ]] || { echo "Refusing existing Theme fixture state" >&2; exit 2; }
RIXA_THEME_BROWSER_ROOT="$root" RIXA_THEME_BROWSER_OBSERVATION=1 \
  go test ./internal/product -run '^TestCodeFirstThemeChromiumFixture$' -count=1 -timeout=10m -v >"$log" 2>&1 &
pid=$!
cleanup() {
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT
for _ in $(seq 1 180); do
  [[ -f "$root/ready.json" ]] && break
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "Theme fixture terminated before readiness" >&2
    tail -n 100 "$log" >&2
    wait "$pid" || true
    exit 1
  fi
  sleep 1
done
if [[ ! -f "$root/ready.json" ]]; then
  echo "Theme fixture timed out" >&2
  tail -n 100 "$log" >&2
  exit 1
fi
if ! node scripts/lab/theme-browser.cjs "$root"; then
  echo "Real Chromium Theme workflow failed" >&2
  tail -n 100 "$log" >&2
  exit 1
fi
touch "$root/finish.request"
if ! wait "$pid"; then
  tail -n 100 "$log" >&2
  exit 1
fi
trap - EXIT
echo RIXA_THEME_BROWSER_COMPLETE
grep -E 'RIXA_THEME_BROWSER|PASS|FAIL' "$log" | tail -n 12
