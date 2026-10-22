#!/usr/bin/env bash
# CI check of the macOS disk images (01-no-cli-plan items 2.2 and 2.5), on a macos-14 runner:
#
#   scripts/macos-dmg-check.sh <server.dmg> <agent.dmg> [notarized]
#
# 1. Mount each dmg read-only: the app, the Applications link, the background and the layout.
# 2. The apps' signatures (codesign --verify --deep --strict), identifiers, hardened runtime,
#    the agent's entitlements and Info.plist keys. With "notarized": spctl accepts each app and
#    dmg as Notarized Developer ID and stapler validates their tickets (a dry run without the
#    Apple secrets is ad-hoc signed and skips this).
# 3. The server app the way it installs (copied out of the dmg, as a drag to Applications does):
#    its dialogs build (--check-ui); --non-interactive runs the same flow as a double-click with
#    sudo for the password dialog: install-from-app → the setup link, /health, the LaunchDaemon
#    named after the app; a second open only opens the link; then Uninstall with the data.
# 4. The agent app: `install` and `uninstall` with a scratch HOME (files only, twice, nothing
#    left), then for real through SMAppService (its in-bundle LaunchAgent loads in gui/<uid>, and
#    uninstall takes it away).
#
# It installs and removes the real system service and a login item: never run it on a machine
# someone uses (it refuses unless CI or JARVIS_INSTALL_CHECK_OK is set). macOS bash 3.2.
set -euo pipefail

fail() { echo "macos-dmg-check: FAIL: $*" >&2; exit 1; }
say() { printf '\n=== %s\n' "$*"; }
[ $# -ge 2 ] || fail "usage: $0 <server.dmg> <agent.dmg> [notarized]"
[ -n "${CI:-}" ] || [ -n "${JARVIS_INSTALL_CHECK_OK:-}" ] || fail "CI only (it installs a system service)"
[ "$(uname -s)" = Darwin ] || fail "macOS only"
SERVER_DMG=$1 AGENT_DMG=$2 NOTARIZED=${3:-}
W=$(mktemp -d)
M="$W/mnt"
mkdir -p "$M"
SERVER_APP_NAME="Jarvis Automation Server.app"
AGENT_APP_NAME="Jarvis Automation Agent.app"
ADMIN=http://127.0.0.1:7700

cleanup() {
  set +e
  for d in "$M"/*; do [ -d "$d" ] && hdiutil detach -quiet -force "$d"; done
  if [ -x /usr/local/bin/jarvisd ]; then
    sudo /usr/local/bin/jarvisd service uninstall >/dev/null 2>&1
    sudo rm -f /usr/local/bin/jarvisd
  fi
}
trap cleanup EXIT

plistval() { /usr/libexec/PlistBuddy -c "Print :$2" "$1/Contents/Info.plist"; }

say "mount and look at each dmg"
for kind in server agent; do
  if [ $kind = server ]; then dmg=$SERVER_DMG app=$SERVER_APP_NAME; else dmg=$AGENT_DMG app=$AGENT_APP_NAME; fi
  hdiutil imageinfo "$dmg" | grep -q "Format: ULFO" || fail "$dmg isn't lzfse-compressed (ULFO)"
  hdiutil attach -nobrowse -readonly -noautoopen -mountpoint "$M/$kind" "$dmg" >/dev/null
  [ -d "$M/$kind/$app" ] || fail "$dmg has no $app"
  [ "$(readlink "$M/$kind/Applications")" = /Applications ] || fail "$dmg has no Applications link"
  # dmgbuild combines background.png and background@2x.png into one HiDPI TIFF.
  [ -s "$M/$kind/.background.tiff" ] || fail "$dmg has no background (.background.tiff)"
  [ -s "$M/$kind/.DS_Store" ] || fail "$dmg has no window layout (.DS_Store)"
  # Nothing else in the window: the app, the link (and the hidden layout files).
  for f in "$M/$kind"/* "$M/$kind"/.[!.]*; do
    [ -e "$f" ] || [ -L "$f" ] || continue
    case "$(basename "$f")" in
      "$app" | Applications | .background.tiff | .DS_Store | .VolumeIcon.icns | .fseventsd | .Trashes) ;;
      *) fail "$dmg has more in it: $(basename "$f")" ;;
    esac
  done
done
SERVER_IN_DMG="$M/server/$SERVER_APP_NAME"
AGENT_IN_DMG="$M/agent/$AGENT_APP_NAME"

say "signatures and Info.plist"
for app in "$SERVER_IN_DMG" "$AGENT_IN_DMG"; do
  codesign --verify --deep --strict --verbose=2 "$app"
done
sig() { codesign -dvv "$1" 2>&1; }
sig "$SERVER_IN_DMG" | grep -qx "Identifier=net.jarvisautomation.server" || fail "server app identifier"
sig "$SERVER_IN_DMG/Contents/MacOS/jarvisd" | grep -qx "Identifier=net.jarvisautomation.jarvisd" || fail "nested jarvisd identifier"
sig "$AGENT_IN_DMG" | grep -qx "Identifier=net.jarvisautomation.agent" || fail "agent app identifier (TCC grants key on it)"
for code in "$SERVER_IN_DMG" "$SERVER_IN_DMG/Contents/MacOS/jarvisd" "$AGENT_IN_DMG"; do
  # (runtime) when signed with a Developer ID, (adhoc,runtime) in a dry run.
  sig "$code" | grep -Eq "flags=0x[0-9a-f]+\(([a-z-]+,)*runtime(,[a-z-]+)*\)" || { sig "$code"; fail "no hardened runtime on $code"; }
done
ents=$(codesign -d --entitlements - --xml "$AGENT_IN_DMG" 2>/dev/null)
for e in com.apple.security.automation.apple-events com.apple.security.device.audio-input com.apple.security.cs.disable-library-validation; do
  echo "$ents" | grep -q "<key>$e</key>" || fail "the agent lacks the entitlement $e"
done
[ "$(plistval "$SERVER_IN_DMG" CFBundleIdentifier)" = net.jarvisautomation.server ] || fail "server bundle id"
[ "$(plistval "$AGENT_IN_DMG" CFBundleIdentifier)" = net.jarvisautomation.agent ] || fail "agent bundle id"
[ "$(plistval "$AGENT_IN_DMG" LSUIElement)" = true ] || fail "the agent shows a Dock icon (LSUIElement)"
[ "$(plistval "$AGENT_IN_DMG" CFBundleURLTypes:0:CFBundleURLSchemes:0)" = jarvis-agent ] || fail "no jarvis-agent: URL scheme"
for k in NSMicrophoneUsageDescription NSAppleEventsUsageDescription NSAccessibilityUsageDescription; do
  plistval "$AGENT_IN_DMG" "$k" >/dev/null || fail "the agent has no $k"
done
for app in "$SERVER_IN_DMG" "$AGENT_IN_DMG"; do
  plistval "$app" NSLocalNetworkUsageDescription >/dev/null || fail "$app has no NSLocalNetworkUsageDescription"
  [ "$(plistval "$app" NSBonjourServices:0)" = _jarvis-config._tcp ] || fail "$app has no NSBonjourServices _jarvis-config._tcp"
done
[ -f "$AGENT_IN_DMG/Contents/Library/LaunchAgents/net.jarvisautomation.agent.plist" ] || fail "no in-bundle LaunchAgent"
for app in "$SERVER_IN_DMG" "$AGENT_IN_DMG"; do
  [ -f "$app/Contents/Resources/AppIcon.icns" ] || fail "no app icon in $app"
done

if [ "$NOTARIZED" = notarized ]; then
  say "Gatekeeper and the stapled tickets"
  for app in "$SERVER_IN_DMG" "$AGENT_IN_DMG"; do
    v=$(spctl --assess --type execute --verbose=4 "$app" 2>&1) || { echo "$v"; fail "Gatekeeper refuses $app"; }
    echo "$v"
    echo "$v" | grep -q "source=Notarized Developer ID" || fail "$app isn't notarized"
    xcrun stapler validate "$app" || fail "$app has no stapled ticket"
  done
  for dmg in "$SERVER_DMG" "$AGENT_DMG"; do
    v=$(spctl --assess --type open --context context:primary-signature --verbose=4 "$dmg" 2>&1) || { echo "$v"; fail "Gatekeeper refuses $dmg"; }
    echo "$v"
    echo "$v" | grep -q "source=Notarized Developer ID" || fail "$dmg isn't notarized"
    xcrun stapler validate "$dmg" || fail "$dmg has no stapled ticket"
  done
else
  echo "(not notarized: an ad-hoc dry run; spctl and stapler are checked on a signed release)"
fi

say "the server app: install the way a double-click does"
mkdir -p "$W/Applications"
ditto "$SERVER_IN_DMG" "$W/Applications/$SERVER_APP_NAME"
SAPP="$W/Applications/$SERVER_APP_NAME/Contents/MacOS/jarvis-server-app"
codesign --verify --deep --strict "$W/Applications/$SERVER_APP_NAME"
"$SAPP" --check-ui
[ ! -e /usr/local/bin/jarvisd ] || fail "a jarvisd is already installed on this runner"
url=$("$SAPP" --non-interactive) || fail "the server app's install failed"
echo "opened: $url"
case "$url" in http://*/setup#token=*) ;; *) fail "the install didn't open the setup link ($url)" ;; esac
curl -fsS --retry 30 --retry-delay 1 --retry-all-errors "$ADMIN/health" >/dev/null || fail "/health doesn't answer"
want=$("$W/Applications/$SERVER_APP_NAME/Contents/MacOS/jarvisd" version)
[ "$(/usr/local/bin/jarvisd version)" = "$want" ] || fail "/usr/local/bin/jarvisd isn't the app's"
cmp -s /usr/local/bin/jarvisd "$W/Applications/$SERVER_APP_NAME/Contents/MacOS/jarvisd" || fail "the installed jarvisd differs from the app's"
plist=/Library/LaunchDaemons/net.jarvisautomation.jarvisd.plist
[ "$(/usr/libexec/PlistBuddy -c 'Print :UserName' "$plist")" = "$(id -un)" ] || fail "the LaunchDaemon doesn't run as the installing user"
[ "$(/usr/libexec/PlistBuddy -c 'Print :AssociatedBundleIdentifiers' "$plist")" = net.jarvisautomation.server ] ||
  fail "the LaunchDaemon isn't associated with the app"
codesign --verify --strict /usr/local/bin/jarvisd || fail "the installed jarvisd's signature broke in the copy"
# A second open asks nothing (the server runs, unprivileged status reads it as running) and opens
# the same link.
again=$("$SAPP" --non-interactive 2>"$W/again.err") || { cat "$W/again.err"; fail "the second open failed"; }
cat "$W/again.err"
if grep -q '^\[' "$W/again.err"; then fail "the second open showed a dialog"; fi
[ "$again" = "$url" ] || fail "the second open opened $again, not the setup link $url"

say "the server app: Uninstall, deleting the data"
"$SAPP" --non-interactive --uninstall --purge
[ ! -e "$plist" ] || fail "the LaunchDaemon is still there"
[ ! -e /usr/local/bin/jarvisd ] || fail "/usr/local/bin/jarvisd is still there"
[ ! -e "$HOME/.jarvisd" ] || fail "the data (~/.jarvisd) is still there"
if launchctl print system/net.jarvisautomation.jarvisd >/dev/null 2>&1; then fail "jarvisd is still loaded"; fi
if curl -fsS --max-time 2 "$ADMIN/health" >/dev/null 2>&1; then fail "jarvisd still answers"; fi

say "the agent app: install / uninstall, files only, scratch HOME"
H="$W/home"
mkdir -p "$H/Applications" "$H/Library"
ditto "$AGENT_IN_DMG" "$H/Applications/$AGENT_APP_NAME"
AAPP="$H/Applications/$AGENT_APP_NAME/Contents/MacOS/jarvis-agent"
R() { env -i HOME="$H" PATH=/usr/bin:/bin:/usr/sbin:/sbin "$@"; }
snap() { (cd "$H" && find . ! -type d ! -path "./Applications/*" | LC_ALL=C sort); }
before=$(snap)
plan=$(R "$AAPP" install --dry-run)
echo "$plan"
echo "$plan" | grep -q '^login-item \[activate\] path=".*/Applications/Jarvis Automation Agent.app" name="net.jarvisautomation.agent.plist"' ||
  fail "install from the app doesn't use SMAppService"
R "$AAPP" install --files-only
R "$AAPP" install --files-only
R "$AAPP" uninstall --files-only
after=$(snap)
[ "$before" = "$after" ] || { diff <(echo "$before") <(echo "$after") >&2 || true; fail "uninstall left (or removed) files"; }

say "the agent app: the login item through SMAppService"
R "$AAPP" install --no-start --no-browser
job="gui/$(id -u)/net.jarvisautomation.agent"
launchctl print "$job" >/dev/null 2>&1 || fail "SMAppService didn't load the agent's LaunchAgent ($job)"
info=$(launchctl print "$job")
echo "$info" | grep -E '^\s*(program|path|state|bundle id|parent bundle identifier|parent bundle version) ' || true
# SMAppService names the program relative to the bundle (BundleProgram) or by its full path.
echo "$info" | grep -q "Contents/MacOS/jarvis-agent" || { echo "$info"; fail "the login item doesn't run the app's binary"; }
echo "$info" | grep -q "parent bundle identifier = net.jarvisautomation.agent" || fail "the login item isn't the app's own"
R "$AAPP" uninstall
for _ in $(seq 50); do launchctl print "$job" >/dev/null 2>&1 || break; sleep 0.2; done
if launchctl print "$job" >/dev/null 2>&1; then fail "uninstall left the login item loaded"; fi
echo "macos-dmg-check: ok"
