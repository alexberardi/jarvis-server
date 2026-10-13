#!/usr/bin/env bash
# CI check of `jarvis-agent install` / `uninstall` (installers plan item 1.4) on this runner's
# OS: install twice, check what it made, uninstall, and check nothing is left.
#
#   scripts/agent-install-check.sh path/to/jarvis-agent
#
# Linux and macOS use a scratch HOME and --files-only (no systemd/launchd, nothing started).
# Windows has no scratch HKCU, so it registers for real in the runner's own profile (a
# throwaway VM) with --no-start, and checks the registry and shortcuts with reg/PowerShell.
# Never run it on a machine someone uses.
set -euo pipefail
BIN=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
fail() { echo "agent-install-check: $*" >&2; exit 1; }
[ -n "${CI:-}" ] || [ -n "${JARVIS_INSTALL_CHECK_OK:-}" ] || fail "CI only (set JARVIS_INSTALL_CHECK_OK=1 on a throwaway machine)"

case $(uname -s) in
  Linux|Darwin)
    H=$(mktemp -d)
    mkdir -p "$H/.mozilla" "$H/Desktop" "$H/Downloads"
    cp "$BIN" "$H/Downloads/jarvis-agent"
    snap() { (cd "$H" && find . | LC_ALL=C sort); }
    before=$(snap)
    R() { env -i HOME="$H" PATH=/usr/bin:/bin:/usr/sbin:/sbin "$@"; }
    R "$H/Downloads/jarvis-agent" install --files-only --desktop-shortcut
    once=$(cd "$H" && find . -type f -o -type l | LC_ALL=C sort | xargs cat 2>/dev/null | cksum)
    R "$H/Downloads/jarvis-agent" install --files-only --desktop-shortcut
    twice=$(cd "$H" && find . -type f -o -type l | LC_ALL=C sort | xargs cat 2>/dev/null | cksum)
    [ "$once" = "$twice" ] || fail "a second install changed files"
    if [ "$(uname -s)" = Linux ]; then
      ID=net.jarvisautomation.agent.desktop
      for f in ".local/share/applications/$ID" ".config/autostart/$ID" "Desktop/$ID"; do
        desktop-file-validate "$H/$f" || fail "$f is not a valid desktop entry"
      done
      systemd-analyze verify --user --man=no "$H/.config/systemd/user/jarvis-agent.service" || fail "the unit doesn't verify"
      [ -L "$H/.config/systemd/user/graphical-session.target.wants/jarvis-agent.service" ] || fail "unit not enabled"
      grep -qx "x-scheme-handler/jarvis-agent=$ID;" "$H/.config/mimeapps.list" || fail "no URL handler default"
      [ -x "$H/.local/lib/jarvis-agent/jarvis-agent" ] || fail "no per-user copy"
      U="$H/.local/lib/jarvis-agent/jarvis-agent"
    else
      plutil -lint "$H/Library/LaunchAgents/net.jarvisautomation.agent.plist" || fail "bad LaunchAgent plist"
      U="$H/Downloads/jarvis-agent"
    fi
    R "$U" start 'jarvis-agent://start?evil=1' && fail "start accepted a URL with a query"
    R "$U" uninstall --files-only
    after=$(snap)
    if [ "$before" != "$after" ]; then
      diff <(echo "$before") <(echo "$after") >&2 || true
      fail "uninstall left something"
    fi
    rm -rf "$H"
    ;;
  MINGW*|MSYS*|CYGWIN*)
    q() { reg query "HKCU\\$1" "${@:2}" >/dev/null 2>&1; }
    lnk() { powershell.exe -NoProfile -Command "Test-Path -LiteralPath (Join-Path ([Environment]::GetFolderPath('$1')) 'Jarvis Automation Agent.lnk')" | tr -d '\r'; }
    "$BIN" install --no-start
    "$BIN" install --no-start
    q 'Software\Microsoft\Windows\CurrentVersion\Run' //v 'Jarvis Automation Agent' || fail "no Run value"
    q 'Software\Classes\jarvis-agent' //v 'URL Protocol' || fail "no URL scheme"
    reg query 'HKCU\Software\Classes\jarvis-agent\shell\open\command' //ve | grep -q 'start "%1"' || fail "URL handler isn't start"
    [ "$(lnk Programs)" = True ] || fail "no Start menu shortcut"
    [ "$(lnk Desktop)" = True ] || fail "no desktop shortcut"
    "$BIN" start 'jarvis-agent://start?evil=1' && fail "start accepted a URL with a query"
    "$BIN" uninstall
    q 'Software\Microsoft\Windows\CurrentVersion\Run' //v 'Jarvis Automation Agent' && fail "Run value left"
    q 'Software\Classes\jarvis-agent' && fail "URL scheme left"
    [ "$(lnk Programs)" = False ] || fail "Start menu shortcut left"
    [ "$(lnk Desktop)" = False ] || fail "desktop shortcut left"
    ;;
  *) fail "unsupported OS $(uname -s)" ;;
esac
echo "agent-install-check: ok"
