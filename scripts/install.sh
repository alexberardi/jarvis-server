#!/bin/sh
# Install or upgrade jarvisd, the single-binary Jarvis server, on Linux (systemd) or macOS.
#
#   curl -fsSLo install.sh https://github.com/alexberardi/jarvis-server/releases/latest/download/install.sh && sh install.sh
#
# Download, then run: piped (`curl … | sh`), a failed download (a 404) leaves sh an empty script
# and the pipeline exits 0 having done nothing (A10b R1). releases/latest is the newest full
# release; while only release candidates exist it 404s, so install an rc from its own URL
# (releases/download/vX.Y.Z-rcN/install.sh): a published script installs its own release.
#
# Options (after `sh install.sh`, or after `sh -s --` when piped):
#   --version vX.Y.Z  install that release (default: the release this script came from, or
#                     the latest for a copy from the repository)
#   --user            Linux: a systemd --user service for your account (~/.local/bin, ~/.jarvisd)
#   --yes             answer yes: apply the firewall fix without asking
#   --stop-legacy     stop the legacy stack: Docker containers (restart policy off), macOS
#                     LaunchAgents and its admin (off at login); data kept, nothing removed
#   --force           reinstall even when this version is installed
#   --uninstall       remove the service, firewall rules and binary (data is kept)
#   --purge           with --uninstall: also delete the data, after a typed confirmation
#   --base-url URL    fetch the release files from URL instead of GitHub (also JARVISD_RELEASE_BASE)
#
# The script is thin on purpose (00-installers ID3): it downloads, verifies and places the
# binary; `jarvisd service install` registers the service and `jarvisd doctor --fix` opens the
# firewall. It writes no secrets: jarvisd makes its own on first start.
set -eu

REPO=alexberardi/jarvis-server
BASE=${JARVISD_RELEASE_BASE:-}
# The project's minisign key, exactly jarvisd's own trust root (internal/update/key.go
# ProjectPublicKey; a unit test keeps them equal). Not overridable: a different key here would
# only install a release that jarvisd then can't upgrade from.
PUBKEY=RWRyW6ICtU+iyX4p4RnS24ju0gRsWpxvv6B8pI9G+ZS01q8t8oupAQ8L
# The release this copy was published with: release.yml writes the tag here, so a script
# fetched from a release's URL installs that release (a prerelease too: GitHub's
# releases/latest never points at one) and the admin's install command needs no flags.
# Empty in the repository (the latest). --version and --base-url win.
RELEASE_VERSION=""
VERSION="" USER_MODE=0 YES=0 STOP_LEGACY=0 FORCE=0 UNINSTALL=0 PURGE=0

say() { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case $1 in
    --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION=$2; shift ;;
    --version=*) VERSION=${1#*=} ;;
    --base-url) [ $# -ge 2 ] || die "--base-url needs a value"; BASE=$2; shift ;;
    --base-url=*) BASE=${1#*=} ;;
    --user) USER_MODE=1 ;;
    --yes|-y) YES=1 ;;
    --stop-legacy) STOP_LEGACY=1 ;;
    --force) FORCE=1 ;;
    --uninstall) UNINSTALL=1 ;;
    --purge) PURGE=1 ;;
    -h|--help) sed -n '2,20p' "$0" 2>/dev/null || true; exit 0 ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
  shift
done
BASE=${BASE%/}
[ -n "$VERSION" ] || [ -n "$BASE" ] || VERSION=$RELEASE_VERSION

case $(uname -s) in Linux) OS=linux ;; Darwin) OS=darwin ;; *) die "unsupported OS $(uname -s); see install.ps1 for Windows" ;; esac
case $(uname -m) in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) die "unsupported CPU $(uname -m)" ;; esac
[ "$OS-$ARCH" = darwin-amd64 ] && die "Intel Macs are not supported (jarvisd ships for Apple silicon)"
# Under sudo, PATH is sudo's secure_path, which on macOS leaves out /usr/local/bin (Docker
# Desktop's docker CLI) and /opt/homebrew/bin (minisign): without them the legacy containers
# look like "another program" and the signature check is skipped.
if [ "$OS" = darwin ]; then
  for d in /usr/local/bin /opt/homebrew/bin; do
    case :$PATH: in *:$d:*) ;; *) [ -d $d ] && PATH=$d:$PATH ;; esac
  done
  export PATH
fi
[ "$OS" = linux ] && [ ! -d /run/systemd/system ] && die "systemd is not running here; download the release and run \`jarvisd serve\` under your own supervisor"

# Privileges: the system service needs root; --user needs it only for the firewall fix.
SUDO=""
[ "$(id -u)" -eq 0 ] || SUDO=sudo
[ -z "$SUDO" ] || command -v sudo >/dev/null || die "run as root (sudo is not installed)"
if [ $USER_MODE = 1 ]; then
  [ "$OS" = linux ] || die "--user is for Linux; on macOS jarvisd runs as a LaunchDaemon as you"
  [ -z "$SUDO" ] && die "--user installs for your own account; run it without sudo"
  BIN_DIR=$HOME/.local/bin LIB_DIR=$HOME/.local/lib/jarvisd RUN="" SVC="--user"
else
  BIN_DIR=/usr/local/bin LIB_DIR=/usr/local/lib/jarvisd RUN=$SUDO SVC=""
fi
BIN=$BIN_DIR/jarvisd
HOMEFLAG="" # root's commands look for a --user service's home in root's account, so name it
[ $USER_MODE = 1 ] && HOMEFLAG="--home $HOME/.jarvisd"

# A prompt needs a terminal; `curl | sh` has one on /dev/tty, CI has none.
TTY=0
if (: </dev/tty) 2>/dev/null; then TTY=1; fi
ask() { # ask "question": yes unless answered n; --yes says yes, no terminal says no
  [ $YES = 1 ] && return 0
  [ $TTY = 1 ] || return 1
  printf '%s [Y/n] ' "$1" >/dev/tty
  read -r ans </dev/tty || ans=n
  case $ans in [nN]*) return 1 ;; esac
}

# The legacy Docker stack (ID7), by the same rule as `jarvisd doctor` (internal/doctor
# LegacyContainer): a running container named jarvis-*, or in the Compose project "jarvis"
# (~/.jarvis/compose: llama-server, llama-server-bg, llm-proxy-worker, go2rtc), a "jarvis-*"
# project (a source checkout's per-service projects, the dockerized node), or any project
# whose files are in ~/.jarvis/compose. Nothing else is touched.
DOCKER=""
docker_init() { # set DOCKER here, not in the $(legacy_containers) subshell
  [ -z "$DOCKER" ] || return 0
  command -v docker >/dev/null || return 0
  DOCKER=docker; docker ps >/dev/null 2>&1 || DOCKER="$SUDO docker"
}
legacy_containers() {
  fmt='{{.Names}};{{.Label "com.docker.compose.project"}};{{.Label "com.docker.compose.project.working_dir"}}'
  [ -n "$DOCKER" ] || return 0
  $DOCKER ps --format "$fmt" 2>/dev/null | awk -F';' '
    { wd = $3; sub(/\/$/, "", wd) }
    $1 ~ /^jarvis-/ || $2 == "jarvis" || $2 ~ /^jarvis-/ || wd ~ /\/\.jarvis\/compose$/ { printf "%s ", $1 }'
}
# The legacy admin (finding 2): a systemd *user* unit, jarvis-admin.service on 7711, whose
# reconcile runs `docker compose up -d` and would start the stack again. It belongs to the
# account that installed it: this user, or under sudo $SUDO_USER (reached through its user
# manager with `systemctl --user -M user@`). Not reachable when that account has no running
# user manager (no session, no linger); then it isn't running either, and the command to run
# as that account is printed. On macOS it is a LaunchAgent, com.jarvis.admin (LEGACY_AGENTS).
admin_unit() { # admin_unit ARGS...: systemctl --user for the legacy admin's account
  if [ "$(id -u)" -ne 0 ]; then
    [ -n "${XDG_RUNTIME_DIR:-}" ] || XDG_RUNTIME_DIR=/run/user/$(id -u)
    XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR systemctl --user "$@"
  elif [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
    systemctl --user -M "$SUDO_USER@" "$@"
  else
    return 2
  fi
}
stop_legacy_admin() {
  [ "$OS" = linux ] || return 0
  who=$(id -un)
  [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && who=$SUDO_USER
  if admin_unit list-unit-files jarvis-admin.service --no-legend 2>/dev/null | grep -q '^jarvis-admin\.service'; then
    say "Stopping the legacy admin (systemd user unit jarvis-admin.service of $who) and turning it off at login"
    admin_unit disable --now jarvis-admin.service >/dev/null 2>&1 \
      || warn "could not stop jarvis-admin.service; as $who run: systemctl --user disable --now jarvis-admin.service"
    return 0
  fi
  # Not listed although its file (where the admin's installer writes it) is there: the user
  # manager wasn't reachable.
  home=$HOME
  [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && home=$(getent passwd "$SUDO_USER" | cut -d: -f6)
  if [ -f "$home/.config/systemd/user/jarvis-admin.service" ]; then
    warn "the legacy admin's unit is in $home/.config/systemd/user but $who's user manager isn't reachable from here; as $who run: systemctl --user disable --now jarvis-admin.service"
  fi
}
# The legacy native services (macOS): user LaunchAgents of the account that installed them,
# exactly internal/doctor LegacyAgents (a test keeps them equal): the GPU services that each
# service's deploy-launchd.sh, and through it the legacy admin's native mode, install
# (llm-proxy 7704/7705, whisper 7706, TTS 7707, OCR 7031 and its worker), and the legacy admin
# itself, whose reconcile starts the Docker stack again. They live in the GUI domain of this
# user, or under sudo of $SUDO_UID, which root reaches as gui/<uid>. Other agents
# (com.jarvis.osx-api, io.jarvis.host-agent, ...) are never touched. On Linux the legacy
# tooling runs no service units but the admin's (above).
LEGACY_AGENTS="com.jarvis.llm-proxy com.jarvis.whisper-api com.jarvis.tts com.jarvis.ocr.service com.jarvis.ocr.worker com.jarvis.admin"
AGENT_UID=""
if [ "$(id -u)" -ne 0 ]; then AGENT_UID=$(id -u); elif [ "${SUDO_UID:-0}" != 0 ]; then AGENT_UID=$SUDO_UID; fi
legacy_agents() { # the loaded ones
  [ "$OS" = darwin ] && [ -n "$AGENT_UID" ] || return 0
  for l in $LEGACY_AGENTS; do
    if launchctl print "gui/$AGENT_UID/$l" >/dev/null 2>&1; then printf '%s ' "$l"; fi
  done
}
stop_legacy_agents() { # stop_legacy_agents LABELS: off at login (disable), then stopped (bootout)
  [ -n "$1" ] || return 0
  who=$(id -un)
  [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && who=$SUDO_USER
  say "Stopping the legacy native services (LaunchAgents of $who) and turning them off at login: $1"
  back=""
  for l in $1; do
    d=gui/$AGENT_UID/$l
    plist=$(launchctl print "$d" 2>/dev/null | sed -n 's/^[[:space:]]*path = //p' | head -n 1)
    [ -n "$plist" ] || plist=/Users/$who/Library/LaunchAgents/$l.plist
    launchctl disable "$d" || die "could not disable $l; as $who run: launchctl disable $d && launchctl bootout $d"
    launchctl bootout "$d" 2>/dev/null || true # "Boot-out failed: 5" for one already exiting
    if launchctl print "$d" >/dev/null 2>&1; then die "could not stop $l; as $who run: launchctl bootout $d"; fi
    back="$back
  launchctl enable $d && launchctl bootstrap gui/$AGENT_UID $plist"
  done
  say "To bring them back later (after \`sudo jarvisd service stop\`), as $who:$back"
}
stop_legacy() { # stop_legacy NAMES AGENTS: restart policy off + stop (never down/rm), the agents, the admin
  if [ -n "$1" ]; then
    say "Stopping the legacy stack: $1"
    # shellcheck disable=SC2086 # names are words
    { $DOCKER update --restart=no $1 >/dev/null && $DOCKER stop $1 >/dev/null; } || die "could not stop the legacy stack ($1)"
  fi
  stop_legacy_agents "$2"
  stop_legacy_admin
}

if [ $UNINSTALL = 1 ]; then
  [ -x "$BIN" ] || die "jarvisd is not installed in $BIN_DIR"
  flags="$SVC"
  [ $PURGE = 1 ] && flags="$flags --purge"
  [ $PURGE = 1 ] && [ $YES = 1 ] && flags="$flags --yes"
  # shellcheck disable=SC2086 # flags are words
  if [ $TTY = 1 ]; then $RUN "$BIN" service uninstall $flags </dev/tty; else $RUN "$BIN" service uninstall $flags; fi \
    || { [ $PURGE = 1 ] && die "uninstall stopped; nothing more was removed"; warn "the service was not removed cleanly"; }
  # jarvisd.prev / .rolledback: `jarvisd upgrade` keeps the previous binary, and a rollback
  # the newer one, next to the executable.
  $RUN rm -f "$BIN" "$BIN.prev" "$BIN.rolledback" && $RUN rm -rf "$LIB_DIR"
  say "jarvisd removed."
  exit 0
fi

command -v curl >/dev/null || die "curl is required"
TMP=$(mktemp -d "${TMPDIR:-/var/tmp}/jarvisd-install.XXXXXX")
trap 'rm -rf "$TMP"' EXIT INT TERM
url() { # url ASSET: where a release file lives
  if [ -n "$BASE" ]; then echo "$BASE/$1"
  elif [ -n "$VERSION" ]; then echo "https://github.com/$REPO/releases/download/$VERSION/$1"
  else echo "https://github.com/$REPO/releases/latest/download/$1"; fi
}
fetch() { curl -fsSL --retry 3 --connect-timeout 30 -o "$2" "$(url "$1")"; }
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'; }

# SHA256SUMS names the archive, which carries the version: no API call for "latest".
fetch SHA256SUMS "$TMP/SHA256SUMS" || die "could not download $(url SHA256SUMS)"
ASSET=$(awk -v s="-$OS-$ARCH.tar.gz" '{f=$2; sub(/^\*/, "", f)} substr(f, length(f)-length(s)+1) == s {print f; exit}' "$TMP/SHA256SUMS")
[ -n "$ASSET" ] || die "the release has no jarvisd for $OS-$ARCH"
REL=${ASSET#jarvisd-}; REL=${REL%"-$OS-$ARCH.tar.gz"}
[ -z "$VERSION" ] || [ "$VERSION" = "$REL" ] || die "asked for $VERSION but the release files are for $REL"
VERSION=$REL

# The installed version is checked before anything else is downloaded (A10 F21): a re-run
# of the same version fetches only SHA256SUMS.
CUR="" REUSE=0
[ -x "$BIN" ] && CUR=$("$BIN" version 2>/dev/null || echo unknown)
# An upgrade swaps the binary but the service definition is still the one the old version
# wrote: a Mac upgraded from rc4 had no updater LaunchDaemon, a Linux unit an older
# ExecStartPre. The new binary says whether its definition is stale (`service status --json`,
# definition_stale) and `service install` rewrites it, keeping the binary path, home, account,
# data and firewall rules.
refresh_service() {
  # shellcheck disable=SC2086
  "$BIN" service status $SVC --json 2>/dev/null | grep -q '"definition_stale": true' || return 0
  say "Updating the jarvisd service definition for $VERSION (an older version wrote it)..."
  # shellcheck disable=SC2086
  $RUN "$BIN" service install $SVC || die "updating the service definition failed; run: ${RUN:+sudo }jarvisd service install${SVC:+ $SVC}"
}
if [ -n "$CUR" ]; then
  # jarvisd is already here, so stopping the legacy stack can't leave the box with neither.
  if [ $STOP_LEGACY = 1 ]; then docker_init; stop_legacy "$(legacy_containers)" "$(legacy_agents)"; fi
  if [ "$CUR" = "$VERSION" ] && [ $FORCE = 0 ] && "$BIN" service status $SVC >/dev/null 2>&1; then
    say "jarvisd $VERSION is already installed and running."
    refresh_service
    # shellcheck disable=SC2086
    $RUN "$BIN" setup-link $HOMEFLAG 2>/dev/null || true
    exit 0
  fi
  # A jarvisd with its own upgrade takes over from here: it checks the signature itself with
  # the key built into the installed binary (mandatory, no minisign needed), checks free disk,
  # snapshots the database, swaps, waits for the health gate and rolls back on failure. Then
  # the new binary's service definition (above).
  if [ "$CUR" != "$VERSION" ] && "$BIN" help 2>/dev/null | grep -q '^  upgrade'; then
    say "Upgrading jarvisd $CUR -> $VERSION with \`jarvisd upgrade\`..."
    # shellcheck disable=SC2086 # RUN is sudo or nothing
    $RUN env JARVISD_RELEASE_BASE="$BASE" "$BIN" upgrade --version "$VERSION" || exit 1
    [ "$("$BIN" version 2>/dev/null)" = "$VERSION" ] || die "jarvisd upgrade finished but $BIN is not $VERSION"
    refresh_service
    exit 0
  fi
  # Installed but not running: reinstall the service on the binary already here.
  if [ "$CUR" = "$VERSION" ] && [ $FORCE = 0 ]; then
    say "jarvisd $VERSION is installed but not running; reinstalling its service (nothing to download)."
    REUSE=1
  fi
fi

# Signature, for a fresh install (or a jarvisd too old to upgrade itself). Releases are
# signed, and every later upgrade is verified by jarvisd itself; here the downloaded binary
# can't vouch for itself, so the check needs the minisign tool: with minisign installed the
# signature is required (missing or invalid is fatal); without it the TLS-anchored checksum
# alone, with a warning (JARVISD_REQUIRE_SIGNATURE=1 refuses that).
# `minisign -v` must run: a version-manager shim with no version selected (mise, asdf) is on
# PATH but fails every call, which would read as an INVALID signature.
# Under sudo, PATH is sudo's secure_path, which leaves out Homebrew (on macOS added back
# above) and MacPorts, so their bin dirs are looked in too, and then the invoking user's own
# PATH (mise, nix, ~/bin) as their login shell sets it.
MINISIGN="" tried=" "
minisign_try() { # minisign_try PATH: use it when it runs
  [ -n "$1" ] && [ -x "$1" ] || return 1
  case $tried in *" $1 "*) return 1 ;; esac
  tried="$tried$1 "
  if "$1" -v >/dev/null 2>&1; then MINISIGN=$1; return 0; fi
  warn "$1 doesn't run (\`minisign -v\` failed); not using it"
  return 1
}
find_minisign() {
  for m in "$(command -v minisign 2>/dev/null || true)" /opt/homebrew/bin/minisign /usr/local/bin/minisign /opt/local/bin/minisign; do
    minisign_try "$m" && return 0
  done
  [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] || return 0
  if [ "$OS" = darwin ]; then ush=$(dscl . -read "/Users/$SUDO_USER" UserShell 2>/dev/null | awk '{print $2}')
  else ush=$(getent passwd "$SUDO_USER" | cut -d: -f7); fi
  [ -x "${ush:-}" ] || ush=/bin/sh
  m=$(sudo -H -u "$SUDO_USER" "$ush" -lc 'command -v minisign' </dev/null 2>/dev/null | tail -n 1) || m=""
  case $m in /*) minisign_try "$m" || true ;; esac
}
NEW=$BIN
if [ $REUSE = 0 ]; then
  find_minisign
  if [ -n "$MINISIGN" ]; then
    fetch SHA256SUMS.minisig "$TMP/SHA256SUMS.minisig" 2>/dev/null || die "the release has no SHA256SUMS.minisig; not installing an unsigned release"
    "$MINISIGN" -Vq -P "$PUBKEY" -m "$TMP/SHA256SUMS" -x "$TMP/SHA256SUMS.minisig" >/dev/null || die "SHA256SUMS signature is INVALID ($MINISIGN); not installing"
    say "SHA256SUMS signature verified."
  else
    [ "${JARVISD_REQUIRE_SIGNATURE:-0}" = 1 ] && die "JARVISD_REQUIRE_SIGNATURE=1 but minisign is not installed"
    warn "minisign is not installed, so the release signature is not checked (checksums only); install minisign for a verified first install. Upgrades are verified by jarvisd itself."
  fi
  say "Downloading jarvisd $VERSION for $OS-$ARCH..."
  fetch "$ASSET" "$TMP/$ASSET" || die "could not download $(url "$ASSET")"
  want=$(awk -v f="$ASSET" '$2 == f || $2 == "*" f {print $1; exit}' "$TMP/SHA256SUMS")
  [ "$(sha256 "$TMP/$ASSET")" = "$want" ] || die "checksum mismatch for $ASSET (corrupt or tampered download)"
  tar -xzf "$TMP/$ASSET" -C "$TMP"
  NEW=$TMP/jarvisd-$VERSION-$OS-$ARCH/jarvisd
  [ "$("$NEW" version)" = "$VERSION" ] || die "the downloaded jarvisd does not run here"
fi

if [ -z "$CUR" ]; then
  # A fresh install next to the legacy stack (ID7): jarvisd needs its ports, and its GPUs
  # (the legacy llama-servers hold whole cards), so --stop-legacy stops the stack even when
  # its ports are free, and only then looks at what still holds them.
  ports_held() { "$NEW" doctor --json 2>/dev/null | grep -q '"name": "ports"'; }
  holders() { # "  COMMAND (pid N) on PORT" for each listener on a jarvisd port (all of them as root)
    jp='p = port + 0; if ((p >= 7700 && p <= 7712) || p == 7030 || p == 7031 || p == 1884 || p == 9883)'
    if command -v lsof >/dev/null; then
      lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null | awk "NR > 1 { port = \$9; sub(/.*:/, \"\", port); $jp printf \"  %s (pid %s) on %s\\n\", \$1, \$2, port }"
    elif command -v ss >/dev/null; then
      ss -ltnpH 2>/dev/null | awk "{ port = \$4; sub(/.*:/, \"\", port); $jp printf \"  %s on %s\\n\", \$6, port }"
    fi | sort -u
  }
  other_die() {
    h=$(holders || true)
    die "another program holds jarvisd's ports (7700-7712, 7030-7031, 1884, 9883); stop it first${h:+:
$h}
  (\`sudo lsof -nP -iTCP -sTCP:LISTEN\` or \`sudo ss -ltnp\` names it; a source checkout's \`./jarvis\` CLI runs services as background processes: \`./jarvis stop\`)"
  }
  if [ $STOP_LEGACY = 1 ]; then
    docker_init
    legacy=$(legacy_containers) agents=$(legacy_agents)
    stop_legacy "$legacy" "$agents"
    if [ -n "$legacy$agents" ]; then # a stopped server can take a moment to let go of its ports
      i=0; while [ $i -lt 15 ] && ports_held; do sleep 1; i=$((i + 1)); done
    fi
    if ports_held; then other_die; fi
  elif ports_held; then
    docker_init
    legacy=$(legacy_containers) agents=$(legacy_agents)
    [ -n "$legacy$agents" ] || other_die
    die "the legacy Jarvis stack is running and holds jarvisd's ports:${legacy:+
  Docker containers: $legacy}${agents:+
  LaunchAgents: $agents}
  Re-run with --stop-legacy to stop it (containers: docker stop + restart policy off; LaunchAgents: stopped
  and off at login; its admin's user unit off; its data is kept). It prints how to go back${legacy:+;
  for the containers: jarvisd service stop && docker start $legacy}"
  fi
fi

if [ $REUSE = 0 ]; then
  say "Installing $BIN..."
  $RUN mkdir -p "$BIN_DIR" "$LIB_DIR"
  [ -n "$CUR" ] && $RUN cp -p "$BIN" "$LIB_DIR/jarvisd.prev"
  $RUN cp "$NEW" "$BIN_DIR/.jarvisd.new" && $RUN chmod 755 "$BIN_DIR/.jarvisd.new" && $RUN mv -f "$BIN_DIR/.jarvisd.new" "$BIN"
fi
# Reinstalling the service rewrites its definition and restarts it on the new binary, then
# waits for /health. An upgrade that doesn't come up goes back to the previous binary.
# shellcheck disable=SC2086
if ! $RUN "$BIN" service install $SVC || ! "$BIN" service status $SVC --wait 90s >/dev/null; then
  if [ -n "$CUR" ] && [ $REUSE = 0 ]; then
    warn "jarvisd $VERSION did not come up; going back to $CUR"
    $RUN mv -f "$LIB_DIR/jarvisd.prev" "$BIN"
    $RUN "$BIN" service install $SVC || true
  fi
  die "the jarvisd service is not healthy; see \`jarvisd service status\` and its log"
fi

# Firewall (ID5): ask, default yes; without a terminal only with --yes. Private LANs only.
# shellcheck disable=SC2086
report=$($RUN "$BIN" doctor --json $HOMEFLAG 2>/dev/null || true)
if printf '%s' "$report" | grep -q '"fix_cmds"'; then
  lans=$(printf '%s' "$report" | sed -n 's/.*"name": "firewall \([0-9./]*\)".*/\1/p' | tr '\n' ' ' | sed 's/ $//')
  if ask "Allow nodes and phones on ${lans:-your LAN} to reach jarvisd through the host firewall?"; then
    # shellcheck disable=SC2086
    $SUDO "$BIN" doctor --fix $HOMEFLAG >/dev/null || warn "the firewall fix failed; run \`sudo jarvisd doctor --fix\` to see why"
  else
    warn "firewall left as is; nodes and phones can't reach jarvisd until you run: sudo jarvisd doctor --fix"
  fi
fi

say ""
# shellcheck disable=SC2086
$RUN "$BIN" doctor $HOMEFLAG || true
say ""
say "jarvisd $VERSION is running."
# shellcheck disable=SC2086
$RUN "$BIN" setup-link $HOMEFLAG || true
case "$OS$SVC" in
  linux--user) say "Logs: journalctl --user -u jarvisd -f" ;;
  linux) say "Logs: journalctl -u jarvisd -f" ;;
  darwin) say "Logs: tail -f ~/.jarvisd/logs/jarvisd.log" ;;
esac
# Under `curl | sh` there is no install.sh on disk to re-run (A10b): name the URL instead.
if [ -f "$0" ]; then uninst="sh $0 --uninstall${SVC:+ $SVC}"
else uninst="curl -fsSL $(url install.sh) | sh -s -- --uninstall${SVC:+ $SVC}"; fi
say "Manage: jarvisd service status${SVC:+ $SVC} | ${RUN:+sudo }jarvisd service restart${SVC:+ $SVC} | $uninst"
