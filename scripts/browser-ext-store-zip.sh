#!/usr/bin/env bash
# Build the store uploads of Jarvis Browser Control, manifest.json at the zip root:
#
#   scripts/browser-ext-store-zip.sh chrome [OUT]   # -> dist/jarvis-browser-chrome-<version>.zip
#   scripts/browser-ext-store-zip.sh firefox [OUT]  # -> dist/jarvis-browser-firefox-<version>.zip
#
# chrome: the Chromium build `jarvis-agent browser pack` writes, minus the manifest's public
# "key". That key pins the *unpacked* build's id; the Chrome Web Store assigns the listing its
# own id and won't take a key it didn't issue. The id it assigns goes into ChromeExtensionIDs
# (internal/agent/browser/protocol.go); see docs/agent/store-listing.md.
# firefox: the Firefox build as is, for a *listed* version uploaded in AMO's Developer Hub
# (scripts/browser-ext-sign.sh stays the unlisted route).
#
# Deterministic: same source, same bytes (sorted entries, fixed timestamps, no extra fields).
# Needs Go, Node and zip.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

node_() {
  if command -v node >/dev/null 2>&1; then node "$@"
  elif command -v mise >/dev/null 2>&1; then mise exec node@lts -- node "$@"
  else echo "browser-ext-store-zip: Node is required" >&2; exit 1; fi
}
command -v zip >/dev/null 2>&1 || { echo "browser-ext-store-zip: zip is required" >&2; exit 1; }

STORE=${1:-}
case "$STORE" in
  chrome) PACK=chromium ;;
  firefox) PACK=firefox ;;
  *) echo "usage: scripts/browser-ext-store-zip.sh chrome|firefox [OUT.zip]" >&2; exit 2 ;;
esac

SRC="$TMP/$PACK"
(cd "$ROOT" && go run ./cmd/jarvis-agent browser pack --target "$PACK" --out "$SRC" >/dev/null)

# chrome: drop "key". Either way, check the build is the store's and rewrite the manifest as
# 2-space JSON (what pack writes, so firefox's is unchanged).
version=$(node_ -e '
  const fs = require("fs");
  const [p, store] = process.argv.slice(1);
  const m = JSON.parse(fs.readFileSync(p, "utf8"));
  if (store === "chrome") {
    delete m.key;
    if (m.browser_specific_settings) throw new Error("chrome build kept Firefox settings");
  } else if (m.browser_specific_settings?.gecko?.id !== "browser@jarvis.agent" || "key" in m) {
    throw new Error("firefox build lost its gecko id or kept the Chrome key");
  }
  fs.writeFileSync(p, JSON.stringify(m, null, 2) + "\n");
  process.stdout.write(m.version);
' "$SRC/manifest.json" "$STORE")

OUT=${2:-"$ROOT/dist/jarvis-browser-$STORE-$version.zip"}
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac
mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"

# Fixed mtimes and a fixed zone make the archive reproducible; -X drops uid/gid extra fields,
# -D leaves out directory entries.
find "$SRC" -type f -exec chmod 0644 {} + -exec env TZ=UTC touch -h -t 198001010000 {} +
(cd "$SRC" && find . -type f | sed 's|^\./||' | LC_ALL=C sort | TZ=UTC zip -q -X -D -9 "$OUT" -@)

unzip -Z1 "$OUT" | grep -qx manifest.json || { echo "browser-ext-store-zip: manifest.json not at the zip root" >&2; exit 1; }
if unzip -p "$OUT" manifest.json | grep -q '"key"'; then
  echo "browser-ext-store-zip: manifest still has a key" >&2; exit 1
fi
echo "browser-ext-store-zip: $OUT (version $version)"
unzip -Z1 "$OUT" | sed 's/^/  /'
