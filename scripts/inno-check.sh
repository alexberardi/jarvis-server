#!/usr/bin/env bash
# The two Windows installers (01-no-cli-plan item 2.3) end to end, silently, on a throwaway
# Windows: the CI runner (an administrator, Git Bash). It installs, upgrades and uninstalls the
# real jarvisd service and registers the agent in the runner's own profile, so it refuses to run
# anywhere else.
#
#   scripts/inno-check.sh DIST OLD NEW SELF_UPDATED_DIR
#
# DIST holds "Jarvis Automation {Server,Agent} Setup <OLD and NEW without v>.exe"
# (scripts/build-windows-installers.sh). SELF_UPDATED_DIR holds jarvisd.exe and jarvis-agent.exe
# built as a version newer than NEW: they stand in for programs that updated themselves, which no
# installer may downgrade.
#
# Server: a fresh install (service and updater running, /health, the binary where the updater
# expects it, the local-subnet firewall rule, the Start menu link to the admin, the setup link in
# the log, the uninstall entry's version), an upgrade (new binary, .prev kept, same setup link),
# a self-updated binary kept by a re-run of the older installer, uninstall (service, updater,
# program folder, firewall rule and links gone; the data kept), a reinstall onto the kept data,
# and an uninstall with /PURGE (the data gone).
# Agent: a fresh install (the program and its windowless launcher, the Run key, the jarvis-agent://
# handler, Start menu and desktop shortcuts through the launcher, the agent started, the uninstall
# entry's version), an upgrade, a self-updated agent kept, uninstall (all of it gone, the settings
# kept), and a reinstall + uninstall with /PURGE (the settings gone).
set -euo pipefail

[ $# -eq 4 ] || { echo "usage: $0 DIST OLD NEW SELF_UPDATED_DIR" >&2; exit 2; }
if [ -z "${CI:-}" ] && [ -z "${JARVIS_INNO_CHECK_OK:-}" ]; then
  echo "inno-check: installs and uninstalls the real jarvisd service; run it only in CI or on a throwaway Windows (JARVIS_INNO_CHECK_OK=1)" >&2
  exit 2
fi
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) ;; *) echo "inno-check: Windows (Git Bash) only" >&2; exit 2 ;; esac
DIST=$(cd "$1" && pwd)
OLD=$2 NEW=$3 SU_DIR=$(cd "$4" && pwd)
SU=$("$SU_DIR/jarvisd.exe" version | tr -d '\r')
SRV_OLD="$DIST/Jarvis Automation Server Setup ${OLD#v}.exe"
SRV_NEW="$DIST/Jarvis Automation Server Setup ${NEW#v}.exe"
AGT_OLD="$DIST/Jarvis Automation Agent Setup ${OLD#v}.exe"
AGT_NEW="$DIST/Jarvis Automation Agent Setup ${NEW#v}.exe"
PF=$(cygpath -u "$PROGRAMFILES")
PD=$(cygpath -u "$PROGRAMDATA")
LAD=$(cygpath -u "$LOCALAPPDATA")
RAD=$(cygpath -u "$APPDATA")
SRV_DIR="$PF/jarvisd"
DATA="$PD/jarvisd"
AGT_DIR="$LAD/Programs/Jarvis Automation Agent"
START_MENU_ALL="$PD/Microsoft/Windows/Start Menu/Programs"
SRV_KEY='HKLM\Software\Microsoft\Windows\CurrentVersion\Uninstall\net.jarvisautomation.jarvisd_is1'
AGT_KEY='HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\net.jarvisautomation.agent_is1'
RUN_KEY='HKCU\Software\Microsoft\Windows\CurrentVersion\Run'
URL_KEY='HKCU\Software\Classes\jarvis-agent\shell\open\command'
W=$(mktemp -d)

say() { printf '\n=== %s\n' "$*"; }
fail() {
  echo "inno-check: FAIL: $*" >&2
  for f in "$W"/*.log; do [ -e "$f" ] && { echo "--- $f" >&2; tail -60 "$f" >&2; }; done
  tail -60 "$DATA/logs/jarvisd.log" >&2 2>/dev/null || true
  exit 1
}
# MSYS would turn /VERYSILENT, /v and the like into paths.
nc() { MSYS2_ARG_CONV_EXCL='*' "$@"; }
gone() { for p in "$@"; do [ ! -e "$p" ] || fail "still there: $p"; done; }
present() { for p in "$@"; do [ -e "$p" ] || fail "missing: $p"; done; }
reg_value() { nc reg query "$1" /v "$2" 2>/dev/null | tr -d '\r' | sed -n "s/^ *$2 *REG_[A-Z_]* *//p"; }
reg_default() { nc reg query "$1" /ve 2>/dev/null | tr -d '\r' | sed -n 's/^ *(Default) *REG_[A-Z_]* *//p'; }
key_exists() { nc reg query "$1" >/dev/null 2>&1; }
version_of() { "$1" version | tr -d '\r'; }
running_version() {
  local v=""
  for _ in $(seq 120); do
    v=$(curl -fsS http://127.0.0.1:7710/health 2>/dev/null | python -c 'import json,sys; print(json.load(sys.stdin).get("version",""))' 2>/dev/null || true)
    [ -n "$v" ] && break
    sleep 0.5
  done
  echo "$v"
}
# install SETUP NAME [ARGS...]: a silent install; its log is $W/NAME.log.
install() {
  local setup=$1 name=$2
  shift 2
  nc "$setup" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- "/LOG=$(cygpath -w "$W/$name.log")" "$@" ||
    fail "$(basename "$setup") exited with $?"
}
# uninstall DIR KEY NAME [ARGS...]: a silent uninstall. Inno's uninstaller hands over to a copy
# of itself and exits at once, so wait for its uninstall entry and program folder to go.
uninstall() {
  local dir=$1 key=$2 name=$3
  shift 3
  nc "$dir/unins000.exe" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART "/LOG=$(cygpath -w "$W/$name.log")" "$@" ||
    fail "the uninstaller exited with $?"
  for _ in $(seq 240); do
    if ! key_exists "$key" && [ ! -e "$dir" ]; then return 0; fi
    sleep 0.5
  done
  fail "the uninstall didn't finish: $(key_exists "$key" && echo "entry $key") $([ -e "$dir" ] && ls "$dir")"
}
setup_link() { tr -d '\r' <"$W/$1.log" | sed -n 's/.*Setup link: //p' | tail -1; }
lnk_target() {
  powershell.exe -NoProfile -Command "(New-Object -ComObject WScript.Shell).CreateShortcut('$(cygpath -w "$1")').TargetPath" | tr -d '\r'
}
agent_running() { tasklist.exe //FI "IMAGENAME eq jarvis-agent.exe" //NH 2>/dev/null | tr -d '\r' | grep -qi '^jarvis-agent.exe'; }
wait_agent() {
  for _ in $(seq 60); do agent_running && return 0; sleep 0.5; done
  fail "no jarvis-agent process"
}
wait_no_agent() {
  for _ in $(seq 60); do agent_running || return 0; sleep 0.5; done
  fail "jarvis-agent is still running"
}

#### Server ####

say "server 1. fresh install of $OLD"
install "$SRV_OLD" server-1
first_link=$(setup_link server-1)
echo "setup link: $first_link"
case "$first_link" in http://*:7710/setup#token=*) ;; *) fail "no setup link in the log: '$first_link'" ;; esac
[ "$(version_of "$SRV_DIR/jarvisd.exe")" = "$OLD" ] || fail "the binary isn't $OLD"
[ "$(running_version)" = "$OLD" ] || fail "jarvisd $OLD isn't answering"
sc.exe query jarvisd | grep -q RUNNING || fail "the jarvisd service isn't running"
sc.exe qc jarvisd-updater >/dev/null || fail "no updater service"
nc netsh advfirewall firewall show rule name=jarvisd verbose | tr -d '\r' | tee "$W/fw.txt"
grep -qi 'RemoteIP: *LocalSubnet' "$W/fw.txt" || fail "the firewall rule isn't limited to the local subnet"
grep -qi 'Profiles: *Private' "$W/fw.txt" || fail "the firewall rule isn't for private networks"
grep -qx 'URL=http://localhost:7710/' <(tr -d '\r' <"$START_MENU_ALL/Jarvis Automation Server.url") ||
  fail "the Start menu entry doesn't open the admin"
gone "$(cygpath -u "$PUBLIC")/Desktop/Jarvis Automation Server.url" # the desktop link is unticked by default
[ "$(reg_value "$SRV_KEY" DisplayVersion)" = "${OLD#v}" ] || fail "DisplayVersion is '$(reg_value "$SRV_KEY" DisplayVersion)'"
[ "$(reg_value "$SRV_KEY" DisplayName)" = "Jarvis Automation Server" ] || fail "DisplayName"

say "server 2. upgrade to $NEW: data kept"
install "$SRV_NEW" server-2
[ "$(version_of "$SRV_DIR/jarvisd.exe")" = "$NEW" ] || fail "the binary isn't $NEW"
[ "$(version_of "$SRV_DIR/jarvisd.prev.exe")" = "$OLD" ] || fail "jarvisd.prev.exe isn't $OLD"
[ "$(running_version)" = "$NEW" ] || fail "jarvisd $NEW isn't answering"
[ "$(setup_link server-2)" = "$first_link" ] || fail "the upgrade has another setup link: $(setup_link server-2)"
[ "$(reg_value "$SRV_KEY" DisplayVersion)" = "${NEW#v}" ] || fail "DisplayVersion is '$(reg_value "$SRV_KEY" DisplayVersion)'"

say "server 3. a self-updated $SU survives the $NEW installer"
"$SRV_DIR/jarvisd.exe" service stop
cp "$SU_DIR/jarvisd.exe" "$SRV_DIR/jarvisd.exe"
"$SRV_DIR/jarvisd.exe" service start
[ "$(running_version)" = "$SU" ] || fail "the stand-in $SU isn't answering"
install "$SRV_NEW" server-3
grep -q "keeping jarvisd $SU" "$W/server-3.log" || fail "the installer didn't say it kept $SU"
[ "$(version_of "$SRV_DIR/jarvisd.exe")" = "$SU" ] || fail "the $NEW installer replaced $SU"
[ "$(running_version)" = "$SU" ] || fail "jarvisd isn't $SU any more"
[ "$(reg_value "$SRV_KEY" DisplayVersion)" = "${SU#v}" ] || fail "DisplayVersion is '$(reg_value "$SRV_KEY" DisplayVersion)', not the running ${SU#v}"

say "server 4. uninstall: the data stays"
uninstall "$SRV_DIR" "$SRV_KEY" server-4
if sc.exe query jarvisd >/dev/null 2>&1; then fail "the jarvisd service is still there"; fi
if sc.exe query jarvisd-updater >/dev/null 2>&1; then fail "the updater service is still there"; fi
if nc netsh advfirewall firewall show rule name=jarvisd >/dev/null 2>&1; then fail "the firewall rule is still there"; fi
gone "$SRV_DIR" "$START_MENU_ALL/Jarvis Automation Server.url"
present "$DATA/jarvis.db"

say "server 5. reinstall onto the kept data, then uninstall with /PURGE"
install "$SRV_NEW" server-5
[ "$(running_version)" = "$NEW" ] || fail "jarvisd $NEW isn't answering"
[ "$(setup_link server-5)" = "$first_link" ] || fail "the reinstall didn't pick up the kept data: $(setup_link server-5)"
uninstall "$SRV_DIR" "$SRV_KEY" server-6 /PURGE
gone "$DATA" "$SRV_DIR"

#### Agent ####

AGENT="$AGT_DIR/jarvis-agent.exe"
LAUNCHER="$AGT_DIR/jarvis-agentw.exe"
check_registered() {
  [ "$(reg_value "$RUN_KEY" 'Jarvis Automation Agent')" = "\"$(cygpath -w "$LAUNCHER")\" run" ] ||
    fail "the Run key is '$(reg_value "$RUN_KEY" 'Jarvis Automation Agent')'"
  [ "$(reg_default "$URL_KEY")" = "\"$(cygpath -w "$LAUNCHER")\" start \"%1\"" ] ||
    fail "the jarvis-agent:// handler is '$(reg_default "$URL_KEY")'"
  for f in Programs Desktop; do
    local d
    d=$(cygpath -u "$(powershell.exe -NoProfile -Command "[Environment]::GetFolderPath('$f')" | tr -d '\r')")
    [ "$(lnk_target "$d/Jarvis Automation Agent.lnk")" = "$(cygpath -w "$LAUNCHER")" ] ||
      fail "the $f shortcut targets '$(lnk_target "$d/Jarvis Automation Agent.lnk")'"
  done
}

say "agent 1. fresh install of $OLD"
install "$AGT_OLD" agent-1
[ "$(version_of "$AGENT")" = "$OLD" ] || fail "the agent isn't $OLD"
present "$LAUNCHER"
check_registered
[ "$(reg_value "$AGT_KEY" DisplayVersion)" = "${OLD#v}" ] || fail "DisplayVersion is '$(reg_value "$AGT_KEY" DisplayVersion)'"
# The silent install starts it as a login does: not set up, it waits in its status icon.
wait_agent
# The launcher passes its arguments on (it shows nothing, so check what the agent did).
nc "$LAUNCHER" config set chimes false
for _ in $(seq 60); do grep -qs '"chimes": *false' "$RAD/jarvis-agent/agent.json" && break; sleep 0.5; done
grep -qs '"chimes": *false' "$RAD/jarvis-agent/agent.json" || fail "the launcher didn't run the agent's config set"

say "agent 2. upgrade to $NEW"
install "$AGT_NEW" agent-2
[ "$(version_of "$AGENT")" = "$NEW" ] || fail "the agent isn't $NEW"
[ "$(version_of "$AGT_DIR/jarvis-agent.prev.exe")" = "$OLD" ] || fail "jarvis-agent.prev.exe isn't $OLD"
check_registered
[ "$(reg_value "$AGT_KEY" DisplayVersion)" = "${NEW#v}" ] || fail "DisplayVersion is '$(reg_value "$AGT_KEY" DisplayVersion)'"
wait_agent

say "agent 3. a self-updated $SU survives the $NEW installer"
"$AGENT" stop
wait_no_agent
cp "$SU_DIR/jarvis-agent.exe" "$AGENT"
install "$AGT_NEW" agent-3
[ "$(version_of "$AGENT")" = "$SU" ] || fail "the $NEW installer replaced $SU"
[ "$(reg_value "$AGT_KEY" DisplayVersion)" = "${SU#v}" ] || fail "DisplayVersion is '$(reg_value "$AGT_KEY" DisplayVersion)', not ${SU#v}"

say "agent 4. uninstall: the settings stay"
uninstall "$AGT_DIR" "$AGT_KEY" agent-4
wait_no_agent
if key_exists "$URL_KEY"; then fail "the jarvis-agent:// handler is still there"; fi
[ -z "$(reg_value "$RUN_KEY" 'Jarvis Automation Agent')" ] || fail "the Run key is still there"
for f in Programs Desktop; do
  d=$(cygpath -u "$(powershell.exe -NoProfile -Command "[Environment]::GetFolderPath('$f')" | tr -d '\r')")
  gone "$d/Jarvis Automation Agent.lnk"
done
present "$RAD/jarvis-agent/agent.json"

say "agent 5. reinstall, then uninstall with /PURGE"
install "$AGT_NEW" agent-5
check_registered
uninstall "$AGT_DIR" "$AGT_KEY" agent-6 /PURGE
gone "$RAD/jarvis-agent" "$LAD/jarvis-agent"

say "PASS"
