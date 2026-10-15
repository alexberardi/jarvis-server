#!/usr/bin/env bash
# The two .debs (01-no-cli-plan item 2.4) end to end, as root, on a throwaway Ubuntu with systemd:
# the CI runner (`sudo scripts/deb-check.sh …`) or a container that boots systemd. It installs,
# upgrades, removes and purges the real system service, so it refuses to run anywhere else.
#
#   scripts/deb-check.sh DIST OLD NEW SELF_UPDATED_JARVISD
#
# DIST holds jarvis-automation-{server,agent}_<OLD and NEW without v>_<arch>.deb (scripts/build-debs.sh).
# SELF_UPDATED_JARVISD is a jarvisd built as a version newer than NEW: it stands in for a binary
# jarvisd's own updater installed, which no package may downgrade.
#
# Server: install (setup link printed, service active, /health answers, binary in
# /usr/local/bin and not dpkg's, the firewall opened and tagged when ufw is active), a reinstall
# that restarts nothing, an upgrade (new binary, .prev kept), a self-updated binary kept through
# a reinstall and a downgrade, the menu entry's link, remove (service and firewall rules gone,
# data and account kept), reinstall onto kept data, purge (data, account, any stray tagged rule).
# Agent: install (menu entry, URL handler in the MIME cache, icon), a test user's first launch
# with a scratch HOME and a private D-Bus (the per-user copy, its menu entry and login item, the
# setup page opened through xdg-open), remove (system pieces gone, the user's copy kept), and the
# documented per-user `uninstall --purge`.
set -euo pipefail

[ $# -eq 4 ] || { echo "usage: $0 DIST OLD NEW SELF_UPDATED_JARVISD" >&2; exit 2; }
if [ -z "${CI:-}" ] && [ -z "${JARVIS_DEB_CHECK_OK:-}" ]; then
  echo "deb-check: installs and purges the real jarvisd service; run it only in CI or a throwaway container (JARVIS_DEB_CHECK_OK=1)" >&2
  exit 2
fi
[ "$(id -u)" = 0 ] || { echo "deb-check: run it as root" >&2; exit 2; }
DIST=$(cd "$1" && pwd)
OLD=$2 NEW=$3 SU_BIN=$4
SU=$("$SU_BIN" version)
ARCH=$(dpkg --print-architecture)
SRV_OLD="$DIST/jarvis-automation-server_${OLD#v}_$ARCH.deb"
SRV_NEW="$DIST/jarvis-automation-server_${NEW#v}_$ARCH.deb"
AGENT="$DIST/jarvis-automation-agent_${NEW#v}_$ARCH.deb"
BIN=/usr/local/bin/jarvisd
export DEBIAN_FRONTEND=noninteractive
W=$(mktemp -d)

say() { printf '\n=== %s\n' "$*"; }
fail() {
  echo "deb-check: FAIL: $*" >&2
  journalctl -u jarvisd --no-pager 2>/dev/null | tail -60 >&2 || true
  exit 1
}
apt_get() { apt-get -y -q -o Dpkg::Use-Pty=0 "$@" >"$W/apt.log" 2>&1 || { cat "$W/apt.log"; fail "apt-get $*"; }; }
# running_version: what the admin listener's /health says, polled until the service answers.
running_version() {
  local v=""
  for _ in $(seq 120); do
    v=$(curl -fsS http://127.0.0.1:7710/health 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin).get("version",""))' 2>/dev/null || true)
    [ -n "$v" ] && break
    sleep 0.5
  done
  echo "$v"
}
fw_active() { command -v ufw >/dev/null && ufw status | grep -q '^Status: active'; }
fw_tagged() { ufw status | grep -q '# jarvisd'; }
main_pid() { systemctl show -p MainPID --value jarvisd; }

if command -v ufw >/dev/null; then
  ufw --force enable >/dev/null || echo "deb-check: ufw can't be enabled here; the firewall checks are skipped"
fi

say "server: install $OLD"
apt_get install "$SRV_OLD"
cat "$W/apt.log"
grep -q '/setup#token=' "$W/apt.log" || fail "the install printed no setup link"
systemctl is-active --quiet jarvisd || fail "jarvisd isn't active"
curl -fsS -o /dev/null http://127.0.0.1:7700/health || fail "/health doesn't answer"
[ "$(running_version)" = "$OLD" ] || fail "running $(running_version), not $OLD"
[ "$("$BIN" version)" = "$OLD" ] || fail "$BIN isn't $OLD"
if dpkg -S "$BIN" >/dev/null 2>&1; then fail "dpkg owns $BIN: the updater couldn't replace it"; fi
[ "$(stat -c %U /var/lib/jarvisd)" = jarvisd ] || fail "/var/lib/jarvisd isn't jarvisd's"
if fw_active; then fw_tagged || fail "ufw is active and no jarvisd rule was added"; ufw status | grep jarvisd; fi
LINK=$(grep -o 'http://[^ ]*/setup#token=[A-Za-z0-9_-]*' "$W/apt.log" | head -1)

say "server: the menu entry's link (pkexec runs it with a clean environment)"
desktop-file-validate /usr/share/applications/net.jarvisautomation.server.desktop || fail "the server's menu entry doesn't validate"
[ -f /usr/share/icons/hicolor/128x128/apps/net.jarvisautomation.server.png ] || fail "no server icon"
[ "$(env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin "$BIN" setup-link --url)" = "$LINK" ] || fail "setup-link --url isn't the link the install printed"

say "server: reinstall $OLD changes nothing"
pid=$(main_pid)
apt_get install --reinstall "$SRV_OLD"
grep -q 'already installed and running' "$W/apt.log" || { cat "$W/apt.log"; fail "the reinstall did more than check"; }
[ "$(main_pid)" = "$pid" ] || fail "the reinstall restarted jarvisd"

say "server: upgrade to $NEW"
apt_get install "$SRV_NEW"
[ "$("$BIN" version)" = "$NEW" ] || fail "$BIN isn't $NEW after the upgrade"
[ "$(running_version)" = "$NEW" ] || fail "running $(running_version) after the upgrade, not $NEW"
[ "$("$BIN.prev" version)" = "$OLD" ] || fail "no $BIN.prev holding $OLD"

say "server: a self-updated $SU is never downgraded by the package"
systemctl stop jarvisd
install -m 0755 "$SU_BIN" "$BIN"
systemctl start jarvisd
[ "$(running_version)" = "$SU" ] || fail "the stand-in self-update isn't running"
apt_get install --reinstall "$SRV_NEW"
grep -q "keeping jarvisd $SU" "$W/apt.log" || { cat "$W/apt.log"; fail "the reinstall didn't keep $SU"; }
apt_get install --allow-downgrades "$SRV_OLD"
[ "$("$BIN" version)" = "$SU" ] || fail "installing the $OLD package replaced the self-updated $SU"
[ "$(running_version)" = "$SU" ] || fail "running $(running_version) after the downgrade, not $SU"

say "server: remove keeps the data"
apt_get remove jarvis-automation-server
cat "$W/apt.log"
if systemctl is-active --quiet jarvisd; then fail "jarvisd still runs"; fi
[ ! -e /etc/systemd/system/jarvisd.service ] || fail "the unit is still there"
[ ! -e "$BIN" ] && [ ! -e "$BIN.prev" ] && [ ! -e /usr/lib/jarvisd ] || fail "binaries left behind"
[ -f /var/lib/jarvisd/jarvis.db ] && [ -d /etc/jarvisd ] || fail "remove deleted the data"
getent passwd jarvisd >/dev/null || fail "remove deleted the jarvisd account"
if fw_active && fw_tagged; then fail "remove left the jarvisd firewall rules"; fi
grep -q 'apt purge jarvis-automation-server' "$W/apt.log" || fail "remove didn't say how to delete the data"

say "server: reinstall onto the kept data"
apt_get install "$SRV_NEW"
systemctl is-active --quiet jarvisd || fail "jarvisd isn't active after reinstalling"
[ "$("$BIN" version)" = "$NEW" ] || fail "$BIN isn't $NEW"
[ "$(env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin "$BIN" setup-link --url)" = "$LINK" ] || fail "the kept data's setup link changed"

say "server: purge deletes everything"
apt_get remove jarvis-automation-server
if fw_active; then
  # A rule the removal couldn't take away (no jarvisd to run): purge finds it by its tag.
  ufw allow from 10.0.0.0/8 to any port 7700 proto tcp comment jarvisd >/dev/null
fi
apt_get purge jarvis-automation-server
[ ! -e /var/lib/jarvisd ] && [ ! -e /etc/jarvisd ] || fail "purge left the data"
if getent passwd jarvisd >/dev/null || getent group jarvisd >/dev/null; then fail "purge left the jarvisd account"; fi
if fw_active && fw_tagged; then ufw status; fail "purge left a jarvisd firewall rule"; fi
if dpkg -s jarvis-automation-server >/dev/null 2>&1; then fail "dpkg still lists the package"; fi
[ ! -e "$BIN" ] && [ ! -e /usr/lib/jarvisd ] && [ ! -e /usr/share/applications/net.jarvisautomation.server.desktop ] || fail "purge left files"

say "agent: install"
apt_get install "$AGENT"
LIB=/usr/lib/jarvis-agent/jarvis-agent
ID=net.jarvisautomation.agent
[ "$("$LIB" version)" = "$NEW" ] || fail "$LIB isn't $NEW"
desktop-file-validate "/usr/share/applications/$ID.desktop" || fail "the agent's menu entry doesn't validate"
grep -qx "Exec=$LIB start %u" "/usr/share/applications/$ID.desktop" || fail "the menu entry doesn't run start"
[ -f "/usr/share/icons/hicolor/128x128/apps/$ID.png" ] || fail "no agent icon"
grep -q "^x-scheme-handler/jarvis-agent=$ID.desktop" /usr/share/applications/mimeinfo.cache || fail "the URL handler isn't in the MIME cache"
for d in /home/*; do
  [ ! -e "$d/.local/lib/jarvis-agent" ] || fail "the package installed something in $d"
done

say "agent: a person's first launch (scratch HOME, private D-Bus)"
U=jarvisdebcheck
id "$U" >/dev/null 2>&1 || useradd -m -s /bin/sh "$U"
H=$(mktemp -d /var/tmp/deb-check-home.XXXXXX)
mkdir -p "$H/run" "$H/fakebin"
# xdg-open stands in for the browser: it records the setup page's address.
# shellcheck disable=SC2016 # literal: expanded by the script it writes / the text it checks
printf '#!/bin/sh\nprintf "%%s\\n" "$1" > "$HOME/opened"\n' >"$H/fakebin/xdg-open"
chmod 755 "$H/fakebin/xdg-open"
chmod 700 "$H/run"
chown -R "$U:" "$H"
as_user() { runuser -u "$U" -- env -i HOME="$H" PATH="$H/fakebin:/usr/bin:/bin" XDG_RUNTIME_DIR="$H/run" "$@"; }
[ "$(as_user xdg-mime query default x-scheme-handler/jarvis-agent)" = "$ID.desktop" ] || fail "jarvis-agent:// doesn't open the agent"
# WAYLAND_DISPLAY names no socket: it only tells the agent a session is there to open a page in.
setsid runuser -u "$U" -- env -i HOME="$H" PATH="$H/fakebin:/usr/bin:/bin" XDG_RUNTIME_DIR="$H/run" \
  WAYLAND_DISPLAY=wayland-deb-check dbus-run-session -- "$LIB" start >"$W/start.log" 2>&1 &
PG=$!
for _ in $(seq 120); do [ -s "$H/opened" ] && break; sleep 0.5; done
[ -s "$H/opened" ] || { cat "$W/start.log"; kill -- -"$PG" 2>/dev/null || true; fail "the first launch opened no setup page"; }
PAGE=$(cat "$H/opened")
code=$(curl -s -o /dev/null -w '%{http_code}' "$PAGE" || true)
kill -- -"$PG" 2>/dev/null || true
wait "$PG" 2>/dev/null || true
[ "$code" = 200 ] || { cat "$W/start.log"; fail "the setup page answered $code"; }
COPY="$H/.local/lib/jarvis-agent/jarvis-agent"
[ "$(as_user "$COPY" version)" = "$NEW" ] || fail "no per-user copy at $COPY"
[ "$(stat -c %U "$COPY")" = "$U" ] || fail "the per-user copy isn't the user's (it couldn't update itself)"
grep -qx "Exec=$COPY start %u" "$H/.local/share/applications/$ID.desktop" || fail "the user's menu entry doesn't run their copy"
grep -Eqx "x-scheme-handler/jarvis-agent=$ID\.desktop;?" "$H/.config/mimeapps.list" || fail "the user's URL handler isn't set"
[ -f "$H/.config/autostart/$ID.desktop" ] && [ -f "$H/.config/systemd/user/jarvis-agent.service" ] || fail "no login item"

say "agent: remove keeps the person's copy"
apt_get remove jarvis-automation-agent
cat "$W/apt.log"
[ ! -e /usr/lib/jarvis-agent ] && [ ! -e "/usr/share/applications/$ID.desktop" ] && [ ! -e "/usr/share/icons/hicolor/128x128/apps/$ID.png" ] || fail "remove left system files"
if grep -q "jarvis-agent" /usr/share/applications/mimeinfo.cache 2>/dev/null; then fail "the MIME cache still names the agent"; fi
[ -x "$COPY" ] || fail "remove deleted the person's copy"
grep -q 'uninstall --purge' "$W/apt.log" || fail "remove didn't say how to remove a person's copy"

say "agent: the person's own uninstall --purge"
as_user "$COPY" uninstall --purge
[ ! -e "$COPY" ] && [ ! -e "$H/.local/share/applications/$ID.desktop" ] && [ ! -e "$H/.config/autostart/$ID.desktop" ] || fail "uninstall --purge left the copy or its entries"
userdel -r "$U" 2>/dev/null || true
rm -rf "$H" "$W"
echo
echo "deb-check: PASS"
