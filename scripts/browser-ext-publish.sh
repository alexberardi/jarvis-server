#!/usr/bin/env bash
# Publish Jarvis Browser Control to a store, if the store doesn't have this version yet:
#
#   CWS_SERVICE_ACCOUNT_KEY="$(cat key.json)" CWS_PUBLISHER_ID=… scripts/browser-ext-publish.sh chrome
#   AMO_JWT_ISSUER=… AMO_JWT_SECRET=… scripts/browser-ext-publish.sh firefox
#
# chrome: Chrome Web Store API v2. Skips when the published or submitted revision is already at
# (or past) the source version; otherwise uploads the store zip (browser-ext-store-zip.sh),
# waits for the upload to be processed and submits it for review (published when approved).
# CWS_SERVICE_ACCOUNT_KEY is the JSON key of the service account linked in the Developer
# Dashboard; the script trades it for an access token (or set CWS_ACCESS_TOKEN instead).
# firefox: submits the version to AMO's *listed* channel for review and returns without waiting.
# AMO refuses a version it has seen on either channel ("already exists"); that is a skip, so a
# version first signed unlisted (browser-ext-sign.sh) never reaches the listing.
#
# Run by browser-ext-publish.yml on every release tag; docs/agent/store-listing.md.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
CHROME_ITEM=abkbjmelelgelkhneobebgoilejlgbob # ChromeStoreExtensionID (internal/agent/browser/protocol.go)
CWS=${CWS_API:-https://chromewebstore.googleapis.com} # CWS_API: a fake store, for scripts/browser-ext-publish-test.sh

node_() {
  if command -v node >/dev/null 2>&1; then node "$@"; else mise exec node@lts -- node "$@"; fi
}
npx_() {
  if command -v npx >/dev/null 2>&1; then npx "$@"; else mise exec node@lts -- npx "$@"; fi
}
log() { echo "browser-ext-publish: $*" >&2; }
# api: curl that fails on an HTTP error and prints the response body (the store's reason) to stderr.
api() { local out; out=$(curl -sS --fail-with-body "$@") || { echo "$out" >&2; return 1; }; printf '%s' "$out"; }

VERSION=$(node_ -p 'require(process.argv[1]).version' "$ROOT/internal/agent/browser/extension/manifest.json")

# newer A B: A is a strictly higher dotted version than B.
newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n1)" = "$1" ]; }

# cws_token: an access token for the service account (OAuth 2.0 JWT bearer grant, RS256).
cws_token() {
  local assertion
  assertion=$(node_ -e '
    const crypto = require("crypto");
    const k = JSON.parse(process.env.CWS_SERVICE_ACCOUNT_KEY);
    const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
    const now = Math.floor(Date.now() / 1000);
    const body = b64({ alg: "RS256", typ: "JWT" }) + "." + b64({ iss: k.client_email,
      scope: "https://www.googleapis.com/auth/chromewebstore", aud: "https://oauth2.googleapis.com/token",
      iat: now, exp: now + 3600 });
    process.stdout.write(body + "." + crypto.sign("RSA-SHA256", Buffer.from(body), k.private_key).toString("base64url"));
  ')
  api "${CWS_TOKEN_URL:-https://oauth2.googleapis.com/token}" -d grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer \
    --data-urlencode "assertion=$assertion" | node_ -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(0)).access_token)'
}

chrome() {
  : "${CWS_PUBLISHER_ID:?set CWS_PUBLISHER_ID}"
  if [ -z "${CWS_ACCESS_TOKEN:-}" ]; then
    : "${CWS_SERVICE_ACCOUNT_KEY:?set CWS_SERVICE_ACCOUNT_KEY (or CWS_ACCESS_TOKEN)}"
    CWS_ACCESS_TOKEN=$(cws_token)
  fi
  local item="publishers/$CWS_PUBLISHER_ID/items/$CHROME_ITEM" auth="Authorization: Bearer $CWS_ACCESS_TOKEN"
  status() { api -H "$auth" "$CWS/v2/$item:fetchStatus"; }

  local st have
  st=$(status)
  # The highest version published or waiting in review.
  have=$(node_ -e '
    const s = JSON.parse(process.argv[1]);
    const vs = [s.publishedItemRevisionStatus, s.submittedItemRevisionStatus]
      .flatMap(r => (r?.distributionChannels ?? []).map(c => c.crxVersion)).filter(Boolean);
    vs.sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
    process.stdout.write(vs.at(-1) ?? "");
  ' "$st")
  if [ -n "$have" ] && ! newer "$VERSION" "$have"; then
    log "Chrome Web Store already has $have (source $VERSION): nothing to publish"
    return 0
  fi
  log "Chrome Web Store has ${have:-nothing}; publishing $VERSION"

  local zip="$ROOT/dist/jarvis-browser-chrome-$VERSION.zip"
  "$ROOT/scripts/browser-ext-store-zip.sh" chrome "$zip" >/dev/null
  local up state
  up=$(api -X POST -H "$auth" -H "Content-Type: application/zip" --data-binary "@$zip" \
    "$CWS/upload/v2/$item:upload?uploadType=media")
  state=$(node_ -p 'JSON.parse(process.argv[1]).uploadState ?? ""' "$up")
  # The docs name the pending state both IN_PROGRESS and UPLOAD_IN_PROGRESS.
  for _ in $(seq 60); do
    case "$state" in *IN_PROGRESS) ;; *) break ;; esac
    sleep 5
    state=$(node_ -p 'JSON.parse(process.argv[1]).lastAsyncUploadState ?? ""' "$(status)")
  done
  if [ "$state" != SUCCEEDED ]; then
    log "upload of $VERSION ended as '${state:-?}': $up"
    return 1
  fi
  local pub
  pub=$(api -X POST -H "$auth" -H "Content-Type: application/json" -d '{}' "$CWS/v2/$item:publish")
  log "Chrome Web Store: $VERSION submitted ($(node_ -p 'JSON.parse(process.argv[1]).state' "$pub"))"
  # shellcheck disable=SC2016 # a JavaScript template literal
  node_ -e '
    for (const w of JSON.parse(process.argv[1]).warningInfo?.warnings ?? [])
      console.error(`browser-ext-publish: warning: ${w.reason}: ${w.description}`);
  ' "$pub"
}

firefox() {
  : "${AMO_JWT_ISSUER:?set AMO_JWT_ISSUER}" "${AMO_JWT_SECRET:?set AMO_JWT_SECRET}"
  local tmp out
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  (cd "$ROOT" && go run ./cmd/jarvis-agent browser pack --target firefox --out "$tmp/src" >/dev/null)
  "$ROOT/scripts/browser-ext-lint.sh" >/dev/null
  # web-ext reads the credentials from these variables, keeping them out of the process list.
  if out=$(WEB_EXT_API_KEY=$AMO_JWT_ISSUER WEB_EXT_API_SECRET=$AMO_JWT_SECRET \
    npx_ -y web-ext@8 sign --channel=listed --approval-timeout=0 --source-dir "$tmp/src" \
    --artifacts-dir "$tmp/signed" --no-config-discovery 2>&1); then
    log "AMO: $VERSION submitted to the listed channel for review"
  elif grep -q "already exists" <<<"$out"; then
    log "AMO already has $VERSION (listed or unlisted): nothing to publish"
  else
    echo "$out" >&2
    return 1
  fi
}

case "${1:-}" in
  chrome) chrome ;;
  firefox) firefox ;;
  *) echo "usage: scripts/browser-ext-publish.sh chrome|firefox" >&2; exit 2 ;;
esac
