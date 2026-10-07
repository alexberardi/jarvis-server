#!/bin/sh
# Install or upgrade jarvisd, the single-binary Jarvis server, on Linux (systemd) or macOS.
#
#   curl -fsSL https://github.com/alexberardi/jarvis-server/releases/latest/download/install.sh | sh
#
# Options (after `sh -s --` when piped):
#   --version vX.Y.Z  install that release (default: the latest)
#   --user            Linux: a systemd --user service for your account (~/.local/bin, ~/.jarvisd)
#   --yes             answer yes: apply the firewall fix without asking
#   --stop-legacy     stop the legacy Docker stack if it holds jarvisd's ports (docker stop +
#                     restart policy off; its data is kept, nothing is removed)
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
# The project's minisign key (key id 725ba202b54fa2c9), shared with jarvis-node-setup.
PUBKEY=${JARVISD_MINISIGN_PUBKEY:-RWRyW6ICtU+iyX4p4RnS24ju0gRsWpxvv6B8pI9G+ZS01q8t8oupAQ8L}
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

case $(uname -s) in Linux) OS=linux ;; Darwin) OS=darwin ;; *) die "unsupported OS $(uname -s); see install.ps1 for Windows" ;; esac
case $(uname -m) in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) die "unsupported CPU $(uname -m)" ;; esac
[ "$OS-$ARCH" = darwin-amd64 ] && die "Intel Macs are not supported (jarvisd ships for Apple silicon)"
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

if [ $UNINSTALL = 1 ]; then
  [ -x "$BIN" ] || die "jarvisd is not installed in $BIN_DIR"
  flags="$SVC"
  [ $PURGE = 1 ] && flags="$flags --purge"
  [ $PURGE = 1 ] && [ $YES = 1 ] && flags="$flags --yes"
  # shellcheck disable=SC2086 # flags are words
  if [ $TTY = 1 ]; then $RUN "$BIN" service uninstall $flags </dev/tty; else $RUN "$BIN" service uninstall $flags; fi \
    || { [ $PURGE = 1 ] && die "uninstall stopped; nothing more was removed"; warn "the service was not removed cleanly"; }
  $RUN rm -f "$BIN" && $RUN rm -rf "$LIB_DIR"
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

# Signature: pluggable. Verified when minisign is installed and the release is signed;
# otherwise the TLS-anchored checksum alone (JARVISD_REQUIRE_SIGNATURE=1 refuses that).
# `minisign -v` must run: a version-manager shim with no version selected (mise, asdf) is on
# PATH but fails every call, which would read as an INVALID signature.
if command -v minisign >/dev/null && ! minisign -v >/dev/null 2>&1; then
  warn "minisign is on PATH but doesn't run (\`minisign -v\` failed); treating it as not installed"
  MINISIGN_OK=0
else
  MINISIGN_OK=1
fi
if [ $MINISIGN_OK = 1 ] && command -v minisign >/dev/null && fetch SHA256SUMS.minisig "$TMP/SHA256SUMS.minisig" 2>/dev/null; then
  minisign -Vq -P "$PUBKEY" -m "$TMP/SHA256SUMS" -x "$TMP/SHA256SUMS.minisig" >/dev/null || die "SHA256SUMS signature is INVALID; not installing"
  say "SHA256SUMS signature verified."
else
  [ "${JARVISD_REQUIRE_SIGNATURE:-0}" = 1 ] && die "no verifiable SHA256SUMS signature (minisign missing or release unsigned)"
  warn "SHA256SUMS signature not checked (minisign not installed, or the release is unsigned); verifying checksums only"
fi
say "Downloading jarvisd $VERSION for $OS-$ARCH..."
fetch "$ASSET" "$TMP/$ASSET" || die "could not download $(url "$ASSET")"
want=$(awk -v f="$ASSET" '$2 == f || $2 == "*" f {print $1; exit}' "$TMP/SHA256SUMS")
[ "$(sha256 "$TMP/$ASSET")" = "$want" ] || die "checksum mismatch for $ASSET (corrupt or tampered download)"
tar -xzf "$TMP/$ASSET" -C "$TMP"
NEW=$TMP/jarvisd-$VERSION-$OS-$ARCH/jarvisd
[ "$("$NEW" version)" = "$VERSION" ] || die "the downloaded jarvisd does not run here"

if [ -x "$BIN" ]; then
  CUR=$("$BIN" version 2>/dev/null || echo unknown)
  if [ "$CUR" = "$VERSION" ] && [ $FORCE = 0 ] && "$BIN" service status $SVC >/dev/null 2>&1; then
    say "jarvisd $VERSION is already installed and running."
    # shellcheck disable=SC2086
    $RUN "$BIN" setup-link $HOMEFLAG 2>/dev/null || true
    exit 0
  fi
  # A jarvisd with its own upgrade (snapshot, health gate, rollback, signature check by the
  # running binary's key) takes over from here.
  if [ "$CUR" != "$VERSION" ] && "$BIN" help 2>/dev/null | grep -q '^  upgrade'; then
    say "Upgrading jarvisd $CUR -> $VERSION with \`jarvisd upgrade\`..."
    # shellcheck disable=SC2086 # $SVC and $HOMEFLAG are words (--user, --home DIR)
    exec $RUN env JARVISD_RELEASE_BASE="$BASE" "$BIN" upgrade --version "$VERSION" $SVC $HOMEFLAG
  fi
else
  CUR=""
  # A fresh install next to the legacy stack (ID7): jarvisd needs its ports.
  if "$NEW" doctor --json 2>/dev/null | grep -q '"name": "ports"'; then
    legacy=$(docker ps --format '{{.Names}}' 2>/dev/null || $SUDO docker ps --format '{{.Names}}' 2>/dev/null || true)
    legacy=$(printf '%s\n' "$legacy" | grep '^jarvis-' | tr '\n' ' ' || true)
    [ -n "$legacy" ] || die "another program holds jarvisd's ports (7700-7712, 7030-7031, 1884, 9883); stop it first (\`sudo ss -ltnp\` or \`sudo lsof -iTCP -sTCP:LISTEN\` names it)"
    [ $STOP_LEGACY = 1 ] || die "the legacy Jarvis Docker stack is running ($legacy) and holds jarvisd's ports.
  Re-run with --stop-legacy to stop it (docker stop + restart policy off; its data is kept).
  To go back to it later: jarvisd service stop && docker start $legacy"
    docker=docker; docker ps >/dev/null 2>&1 || docker="$SUDO docker"
    say "Stopping the legacy stack: $legacy"
    # shellcheck disable=SC2086 # names are words
    $docker update --restart=no $legacy >/dev/null && $docker stop $legacy >/dev/null
  fi
fi

say "Installing $BIN..."
$RUN mkdir -p "$BIN_DIR" "$LIB_DIR"
[ -n "$CUR" ] && $RUN cp -p "$BIN" "$LIB_DIR/jarvisd.prev"
$RUN cp "$NEW" "$BIN_DIR/.jarvisd.new" && $RUN chmod 755 "$BIN_DIR/.jarvisd.new" && $RUN mv -f "$BIN_DIR/.jarvisd.new" "$BIN"
# Reinstalling the service rewrites its definition and restarts it on the new binary, then
# waits for /health. An upgrade that doesn't come up goes back to the previous binary.
# shellcheck disable=SC2086
if ! $RUN "$BIN" service install $SVC || ! "$BIN" service status $SVC --wait 90s >/dev/null; then
  if [ -n "$CUR" ]; then
    warn "jarvisd $VERSION did not come up; going back to $CUR"
    $RUN mv -f "$LIB_DIR/jarvisd.prev" "$BIN"
    $RUN "$BIN" service install $SVC || true
  fi
  die "the jarvisd service is not healthy; see \`jarvisd service status\` and its log"
fi

# Firewall (ID5): ask, default yes; without a terminal only with --yes. Private LANs only.
# shellcheck disable=SC2086
report=$($SUDO "$BIN" doctor --json $HOMEFLAG 2>/dev/null || true)
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
$SUDO "$BIN" doctor $HOMEFLAG || true
say ""
say "jarvisd $VERSION is running."
# shellcheck disable=SC2086
$RUN "$BIN" setup-link $HOMEFLAG || true
case "$OS$SVC" in
  linux--user) say "Logs: journalctl --user -u jarvisd -f" ;;
  linux) say "Logs: journalctl -u jarvisd -f" ;;
  darwin) say "Logs: tail -f ~/.jarvisd/logs/jarvisd.log" ;;
esac
say "Manage: jarvisd service status | ${SUDO:+sudo }jarvisd service restart | sh install.sh --uninstall"
