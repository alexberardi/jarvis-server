#!/usr/bin/env bash
# `jarvisd install-from-app` (01-no-cli-plan 1.10) end to end, the way each GUI installer calls
# it, on a CI runner (passwordless sudo / an elevated Windows runner). Never run it on a dev box:
# it installs, replaces and uninstalls the real system service.
#
#   Linux    the .deb's postinst (scripts/packaging/deb/postinst) with the binary where the
#            package puts it, /usr/lib/jarvisd/jarvisd; ufw on, so the firewall fix has work
#   macOS    sudo "<…>/Jarvis Server.app/Contents/MacOS/jarvisd" install-from-app --run-as <user>
#   Windows  the binary already in %ProgramFiles%\jarvisd (Inno's [Files]), run with --url-file
#
# Steps on each: a fresh install prints the setup link (token) and leaves the service healthy;
# a re-run changes nothing and prints the same link; a newer installer replaces the binary
# (jarvisd.prev kept, databases snapshotted) and restarts on it; an older installer keeps the
# newer binary; then uninstall.
set -euo pipefail

GO=${GO:-go}
cd "$(dirname "$0")/.."

case "$(uname -s)" in
Linux) OS=linux ;;
Darwin) OS=darwin ;;
MINGW* | MSYS* | CYGWIN*) OS=windows ;;
*)
  echo "unsupported OS" >&2
  exit 2
  ;;
esac
W=$(mktemp -d)
ADMIN=http://127.0.0.1:7710
if [ "$OS" = windows ]; then
  EXE=jarvisd.exe
  BIN="/c/Program Files/jarvisd/jarvisd.exe"
  PREV="/c/Program Files/jarvisd/jarvisd.prev.exe"
  HOME_DIR=/c/ProgramData/jarvisd
  PY=python
  R() { "$@"; }
else
  EXE=jarvisd
  BIN=/usr/local/bin/jarvisd
  PREV=/usr/local/bin/jarvisd.prev
  HOME_DIR=/var/lib/jarvisd
  [ "$OS" = darwin ] && HOME_DIR="$HOME/.jarvisd"
  PY=python3
  R() { sudo "$@"; }
fi
cleanup() {
  set +e
  R "$BIN" service uninstall >/dev/null 2>&1
  R rm -f "$BIN" "$PREV"
}
trap cleanup EXIT

say() { printf '\n=== %s\n' "$*"; }
fail() {
  echo "FAIL: $*" >&2
  [ "$OS" = linux ] && sudo journalctl -u jarvisd --no-pager | tail -80 >&2
  [ "$OS" != linux ] && tail -80 "$HOME_DIR/logs/jarvisd.log" >&2
  exit 1
}
json() { "$PY" -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

say "builds v0.0.1-app and v0.0.2-app"
for v in v0.0.1-app v0.0.2-app; do
  mkdir -p "$W/$v"
  CGO_ENABLED=0 $GO build -trimpath -o "$W/$v/$EXE" -ldflags "-X main.version=$v" ./cmd/jarvisd
done

# installer <version>: run the installer's call for that build; the URL lands in $W/url.
installer() {
  local v=$1
  case "$OS" in
  linux)
    sudo install -D -m 755 "$W/$v/jarvisd" /usr/lib/jarvisd/jarvisd
    # shellcheck disable=SC2024 # the output is ours to read, not root's
    sudo sh scripts/packaging/deb/postinst configure >"$W/url" 2>"$W/log" || { cat "$W/log"; fail "postinst ($v)"; }
    ;;
  darwin)
    local app="$W/apps/$v/Jarvis Server.app/Contents/MacOS"
    mkdir -p "$app"
    cp "$W/$v/jarvisd" "$app/jarvisd"
    # shellcheck disable=SC2024
    sudo "$app/jarvisd" install-from-app --run-as "$(id -un)" >"$W/url" 2>"$W/log" || { cat "$W/log"; fail "install-from-app ($v)"; }
    ;;
  windows)
    if [ ! -e "$BIN" ]; then
      # Inno Setup's [Files] put the first one in place.
      mkdir -p "$(dirname "$BIN")"
      cp "$W/$v/$EXE" "$BIN"
      "$BIN" install-from-app --url-file "$(cygpath -w "$W/url-file")" >"$W/url" 2>"$W/log" || { cat "$W/log"; fail "install-from-app ($v)"; }
    else
      # Later ones run from a download, the general path.
      "$W/$v/$EXE" install-from-app --url-file "$(cygpath -w "$W/url-file")" >"$W/url" 2>"$W/log" || { cat "$W/log"; fail "install-from-app ($v)"; }
    fi
    tr -d '\r' <"$W/url-file" | diff - <(tr -d '\r' <"$W/url") || fail "--url-file differs from stdout"
    ;;
  esac
  cat "$W/log"
  echo "URL: $(cat "$W/url")"
  [ "$(wc -l <"$W/url" | tr -d ' ')" = 1 ] || fail "stdout is not one URL line"
}
running_version() { curl -fsS --max-time 3 "$ADMIN/health" | json 'd["version"]'; }
bin_version() { R "$1" version | tr -d '\r'; }

say "1. fresh install"
if [ "$OS" = linux ] && command -v ufw >/dev/null; then
  sudo ufw default deny incoming
  sudo ufw --force enable
fi
installer v0.0.1-app
case "$(cat "$W/url")" in http://*:7710/setup#token=*) ;; *) fail "not a setup link: $(cat "$W/url")" ;; esac
first_url=$(cat "$W/url")
[ "$(bin_version "$BIN")" = v0.0.1-app ] || fail "binary at $BIN is not v0.0.1-app"
[ "$(running_version)" = v0.0.1-app ] || fail "jarvisd v0.0.1-app isn't answering"
R "$BIN" service status --json | tee "$W/status.json"
[ "$(json 'd["definition_stale"]' <"$W/status.json")" = False ] || fail "fresh definition is stale"
case "$OS" in
linux)
  if command -v ufw >/dev/null; then
    sudo ufw status | tee "$W/ufw"
    grep -q '# jarvisd' "$W/ufw" || fail "no tagged ufw rule"
  fi
  grep -q 'Or scan this with a phone' "$W/log" || fail "the postinst printed no QR code"
  ;;
darwin)
  sudo launchctl print system/net.jarvisautomation.jarvisd >/dev/null || fail "LaunchDaemon not loaded"
  grep -q "<string>$(id -un)</string>" /Library/LaunchDaemons/net.jarvisautomation.jarvisd.plist || fail "not run as the user"
  ;;
windows)
  sc.exe qc jarvisd
  sc.exe qc jarvisd-updater >/dev/null || fail "no updater service"
  ;;
esac

say "2. a re-run changes nothing"
installer v0.0.1-app
[ "$(cat "$W/url")" = "$first_url" ] || fail "re-run printed another link"
grep -q 'already installed and running' "$W/log" || fail "re-run reinstalled the service"
R test ! -e "$PREV" || fail "re-run kept a jarvisd.prev"

say "3. a newer installer replaces the binary"
installer v0.0.2-app
[ "$(bin_version "$BIN")" = v0.0.2-app ] || fail "binary not replaced"
[ "$(bin_version "$PREV")" = v0.0.1-app ] || fail "jarvisd.prev is not v0.0.1-app"
for _ in $(seq 1 60); do [ "$(running_version 2>/dev/null)" = v0.0.2-app ] && break; sleep 1; done
[ "$(running_version)" = v0.0.2-app ] || fail "jarvisd v0.0.2-app isn't answering"
R sh -c "ls '$HOME_DIR'/backups/jarvis-v0.0.1-app-*.db" || fail "no database snapshot"
[ "$(cat "$W/url")" = "$first_url" ] || fail "the upgrade printed another link"

say "4. an older installer keeps the newer binary"
installer v0.0.1-app
grep -q 'keeping jarvisd v0.0.2-app' "$W/log" || fail "the older installer didn't keep v0.0.2-app"
[ "$(bin_version "$BIN")" = v0.0.2-app ] || fail "the older installer replaced the binary"
[ "$(running_version)" = v0.0.2-app ] || fail "jarvisd isn't v0.0.2-app any more"

say "uninstall"
R "$BIN" service uninstall
say "PASS ($OS)"
