#!/usr/bin/env bash
# Have Mozilla sign the Firefox build of Jarvis Browser Control as an *unlisted* add-on (not on
# addons.mozilla.org's listing; signed so release Firefox will install it), and put the .xpi
# where the agent embeds it: internal/agent/browser/signed/jarvis-browser.xpi. Commit that file;
# `jarvis-agent browser install` then offers it to Firefox.
#
#   AMO_JWT_ISSUER=user:123:456 AMO_JWT_SECRET=... scripts/browser-ext-sign.sh
#
# Credentials: addons.mozilla.org -> Tools -> Manage API Keys (https://addons.mozilla.org/developers/addon/api/key/).
# They are read from the environment only (never put them on the command line or in the repo).
# AMO refuses a version it has already signed: bump "version" in
# internal/agent/browser/extension/manifest.json first. The extension id (browser@jarvis.agent)
# is permanent once signed.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
: "${AMO_JWT_ISSUER:?set AMO_JWT_ISSUER (AMO API key, "JWT issuer")}"
: "${AMO_JWT_SECRET:?set AMO_JWT_SECRET (AMO API secret, "JWT secret")}"
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT

(cd "$ROOT" && go run ./cmd/jarvis-agent browser pack --target firefox --out "$OUT/src")
"$ROOT/scripts/browser-ext-lint.sh"

npx_() {
  if command -v npx >/dev/null 2>&1; then npx "$@"; else mise exec node@lts -- npx "$@"; fi
}
# web-ext reads the credentials from these variables, keeping them out of the process list.
WEB_EXT_API_KEY=$AMO_JWT_ISSUER WEB_EXT_API_SECRET=$AMO_JWT_SECRET \
  npx_ -y web-ext@8 sign --channel=unlisted --source-dir "$OUT/src" --artifacts-dir "$OUT/signed" --no-config-discovery

xpi=$(find "$OUT/signed" -name '*.xpi' | head -n 1)
[ -n "$xpi" ] || { echo "browser-ext-sign: web-ext produced no .xpi" >&2; exit 1; }
cp "$xpi" "$ROOT/internal/agent/browser/signed/jarvis-browser.xpi"
echo "browser-ext-sign: signed -> internal/agent/browser/signed/jarvis-browser.xpi (commit it; rebuild the agent)"
