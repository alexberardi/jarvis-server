#!/usr/bin/env bash
# Builds the two macOS app bundles (01-no-cli-plan item 2.2), on a Mac:
#
#   scripts/build-macos-apps.sh <tag> <jarvisd> <jarvis-agent> <jarvis-server-app> <out-dir>
#
#   <out-dir>/Jarvis Automation Server.app   the installer: jarvis-server-app (the launcher) and
#                                            jarvisd beside it in Contents/MacOS
#   <out-dir>/Jarvis Automation Agent.app    the desktop agent, with its LaunchAgent for
#                                            SMAppService in Contents/Library/LaunchAgents
#
# SIGN_IDENTITY is the codesign identity: "-" (the default) signs ad-hoc, for CI and dry runs;
# a "Developer ID Application" identity (release.yml's sign-macos) adds a secure timestamp.
# Both use the hardened runtime and the same entitlements, so CI runs what a release ships.
# SIGN_KEYCHAIN names the identity's keychain. Identifiers: net.jarvisautomation.server (the
# launcher, the bundle id), net.jarvisautomation.agent (TCC grants key on it and the team: never
# change it), net.jarvisautomation.jarvisd (the nested server, as in the release archive).
#
# jarvisd is the release's own: signed with a Developer ID it is used as given (sign-macos has
# already signed and notarized it, so the app carries the binary the archive does and the
# firewall's allow entry stays the same); ad-hoc, it is signed here as sign-macos would.
# macOS ships bash 3.2: no associative arrays or mapfile here.
set -euo pipefail

die() { echo "build-macos-apps: $*" >&2; exit 1; }
[ $# -eq 5 ] || die "usage: $0 <tag> <jarvisd> <jarvis-agent> <jarvis-server-app> <out-dir>"
VERSION=$1 JARVISD=$2 AGENT=$3 LAUNCHER=$4 OUT=$5
[ "$(uname -s)" = Darwin ] || die "needs macOS (codesign, sips, iconutil)"
case "$VERSION" in v[0-9]*) ;; *) die "the tag must look like v1.2.3, got '$VERSION'" ;; esac
for f in "$JARVISD" "$AGENT" "$LAUNCHER"; do [ -f "$f" ] || die "no such file: $f"; done

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PKG=$ROOT/scripts/packaging/macos
ID=${SIGN_IDENTITY:--}
# CFBundleVersion and CFBundleShortVersionString take integers and dots only: 0.1.0-rc1 → 0.1.0.
BUNDLE_VERSION=${VERSION#v}
BUNDLE_VERSION=${BUNDLE_VERSION%%-*}
SERVER="$OUT/Jarvis Automation Server.app"
AGENT_APP="$OUT/Jarvis Automation Agent.app"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# sign <path> <identifier> [entitlements]
sign() {
  local args=(--force --sign "$ID" --identifier "$2" --options runtime)
  if [ "$ID" = - ]; then args+=(--timestamp=none); else args+=(--timestamp); fi
  if [ -n "${SIGN_KEYCHAIN:-}" ]; then args+=(--keychain "$SIGN_KEYCHAIN"); fi
  if [ -n "${3:-}" ]; then args+=(--entitlements "$3"); fi
  codesign "${args[@]}" "$1"
}

# icns <out.icns>: the app icon, from the shield the menu bar and the .debs use (128 px; the
# larger sizes are scaled up from it).
icns() {
  local set="$TMP/AppIcon.iconset" s
  rm -rf "$set" && mkdir -p "$set"
  for s in 16 32 128 256; do
    sips -z "$s" "$s" "$ROOT/internal/agent/tray/icon.png" --out "$set/icon_${s}x${s}.png" >/dev/null
    sips -z $((s * 2)) $((s * 2)) "$ROOT/internal/agent/tray/icon.png" --out "$set/icon_${s}x${s}@2x.png" >/dev/null
  done
  iconutil -c icns "$set" -o "$1"
}

plist() { # plist <template> <out>
  sed "s/__VERSION__/$BUNDLE_VERSION/g" "$1" >"$2"
  plutil -lint "$2" >/dev/null
}

mkdir -p "$OUT"
rm -rf "$SERVER" "$AGENT_APP"

# The server app.
mkdir -p "$SERVER/Contents/MacOS" "$SERVER/Contents/Resources"
plist "$PKG/server/Info.plist" "$SERVER/Contents/Info.plist"
install -m 0755 "$LAUNCHER" "$SERVER/Contents/MacOS/jarvis-server-app"
install -m 0755 "$JARVISD" "$SERVER/Contents/MacOS/jarvisd"
if [ "$ID" = - ]; then
  sign "$SERVER/Contents/MacOS/jarvisd" net.jarvisautomation.jarvisd "$ROOT/scripts/macos/jarvisd.entitlements"
else
  codesign --verify --strict "$SERVER/Contents/MacOS/jarvisd" || die "jarvisd isn't signed (sign-macos signs it first)"
  codesign -dvv "$SERVER/Contents/MacOS/jarvisd" 2>&1 | grep -qx "Identifier=net.jarvisautomation.jarvisd" ||
    die "jarvisd isn't signed as net.jarvisautomation.jarvisd"
fi
icns "$SERVER/Contents/Resources/AppIcon.icns"
sign "$SERVER" net.jarvisautomation.server

# The agent app: its executable is jarvis-agent itself (opening the app is `jarvis-agent start`).
mkdir -p "$AGENT_APP/Contents/MacOS" "$AGENT_APP/Contents/Resources" "$AGENT_APP/Contents/Library/LaunchAgents"
plist "$ROOT/internal/agent/deploy/macos/Info.plist" "$AGENT_APP/Contents/Info.plist"
install -m 0755 "$AGENT" "$AGENT_APP/Contents/MacOS/jarvis-agent"
install -m 0644 "$ROOT/internal/agent/deploy/macos/LaunchAgent.plist" \
  "$AGENT_APP/Contents/Library/LaunchAgents/net.jarvisautomation.agent.plist"
icns "$AGENT_APP/Contents/Resources/AppIcon.icns"
sign "$AGENT_APP" net.jarvisautomation.agent "$PKG/agent.entitlements"

for app in "$SERVER" "$AGENT_APP"; do
  codesign --verify --deep --strict --verbose=2 "$app"
done
how="a Developer ID"; [ "$ID" = - ] && how=ad-hoc
echo "built $SERVER and $AGENT_APP ($VERSION, signed $how)"
