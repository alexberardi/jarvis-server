#!/usr/bin/env bash
# Lint the Jarvis Browser Control extension (internal/agent/browser/extension) as each browser
# gets it: `jarvis-agent browser pack` writes the per-browser copy, then web-ext lint checks the
# Firefox one (AMO's own linter; warnings fail) and node checks the script parses.
#
#   scripts/browser-ext-lint.sh            # needs Go and Node (npx); CI runs it
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT

npx_() {
  if command -v npx >/dev/null 2>&1; then npx "$@"
  elif command -v mise >/dev/null 2>&1; then mise exec node@lts -- npx "$@"
  else echo "browser-ext-lint: Node (npx) is required" >&2; exit 1; fi
}

(cd "$ROOT" && go run ./cmd/jarvis-agent browser pack --target firefox --out "$OUT/firefox" >/dev/null)
(cd "$ROOT" && go run ./cmd/jarvis-agent browser pack --target chromium --out "$OUT/chromium" >/dev/null)
node_() {
  if command -v node >/dev/null 2>&1; then node "$@"; else mise exec node@lts -- node "$@"; fi
}
node_ --check "$OUT/firefox/background.js"
npx_ -y web-ext@8 lint --source-dir "$OUT/firefox" --warnings-as-errors --no-config-discovery
echo "browser-ext-lint: ok"
