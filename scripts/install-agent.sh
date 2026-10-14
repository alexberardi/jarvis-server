#!/bin/sh
# Install the Jarvis Automation desktop agent for your account on Linux: Fedora, Bazzite, Arch
# and the rest (01-no-cli-plan Q8; Debian and Ubuntu get a .deb that does the same).
#
#   curl -fsSLo install-agent.sh https://github.com/alexberardi/jarvis-server/releases/latest/download/install-agent.sh && sh install-agent.sh
#
# Run it as yourself, not with sudo: the agent is per user. It downloads the release's agent
# archive, checks it against SHA256SUMS (and SHA256SUMS.minisig when minisign is installed),
# then runs `jarvis-agent install`, which keeps a self-updating copy in ~/.local/lib/jarvis-agent,
# starts it at login, adds "Jarvis Automation Agent" to the app menu, registers browser control
# and the jarvis-agent:// link, and starts it. Its first-run page finishes the setup.
#
# Options (after `sh install-agent.sh`, or after `sh -s --` when piped):
#   --version vX.Y.Z  install that release (default: the release this script came from, or
#                     the latest for a copy from the repository)
#   --base-url URL    fetch the release files from URL or a local directory instead of GitHub
#   --from-dir DIR    the same, for a directory (also JARVIS_AGENT_RELEASE_BASE)
#   --force           reinstall even when this version is installed
#   --no-start        set everything up but don't start the agent now
#   --files-only      write the files only: no systemd, nothing started (tests)
#   --uninstall       undo the install (your sign-in and settings are kept)
#   --purge           with --uninstall: also delete the sign-in, settings and caches
set -eu

REPO=alexberardi/jarvis-server
BASE=${JARVIS_AGENT_RELEASE_BASE:-}
# The project's minisign key, exactly jarvisd's own trust root (internal/update/key.go
# ProjectPublicKey; a unit test keeps them equal). Not overridable.
PUBKEY=RWRyW6ICtU+iyX4p4RnS24ju0gRsWpxvv6B8pI9G+ZS01q8t8oupAQ8L
# The release this copy was published with (release.yml writes the tag here); empty in the
# repository (the latest). --version and --base-url win.
RELEASE_VERSION=""
VERSION="" FORCE=0 UNINSTALL=0 PURGE=0 INSTALL_FLAGS=""

say() { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case $1 in
    --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION=$2; shift ;;
    --version=*) VERSION=${1#*=} ;;
    --base-url | --from-dir) [ $# -ge 2 ] || die "$1 needs a value"; BASE=$2; shift ;;
    --base-url=* | --from-dir=*) BASE=${1#*=} ;;
    --force) FORCE=1 ;;
    --no-start) INSTALL_FLAGS="$INSTALL_FLAGS --no-start" ;;
    --files-only) INSTALL_FLAGS="$INSTALL_FLAGS --files-only" ;;
    --uninstall) UNINSTALL=1 ;;
    --purge) PURGE=1 ;;
    -h | --help) sed -n '2,24p' "$0" 2>/dev/null || true; exit 0 ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
  shift
done
BASE=${BASE%/}
[ -n "$VERSION" ] || [ -n "$BASE" ] || VERSION=$RELEASE_VERSION

case $(uname -s) in
  Linux) ;;
  Darwin) die "on a Mac, install Jarvis Automation Agent from its .dmg (the release page)" ;;
  *) die "unsupported OS $(uname -s); on Windows use the agent's installer from the release page" ;;
esac
case $(uname -m) in x86_64 | amd64) ARCH=amd64 ;; aarch64 | arm64) ARCH=arm64 ;; *) die "unsupported CPU $(uname -m)" ;; esac
[ "$(id -u)" -ne 0 ] || die "run this as yourself, not as root: the agent is installed for your account"

AGENT=$HOME/.local/lib/jarvis-agent/jarvis-agent

if [ $UNINSTALL = 1 ]; then
  [ -x "$AGENT" ] || die "the agent is not installed for $(id -un) ($AGENT)"
  flags=""
  [ $PURGE = 1 ] && flags="--purge"
  case $INSTALL_FLAGS in *--files-only*) flags="$flags --files-only" ;; esac
  # shellcheck disable=SC2086 # flags are words
  "$AGENT" uninstall $flags
  say "Jarvis Automation Agent removed."
  exit 0
fi

TMP=$(mktemp -d "${TMPDIR:-/tmp}/jarvis-agent-install.XXXXXX")
trap 'rm -rf "$TMP"' EXIT INT TERM
url() { # url ASSET: where a release file lives
  if [ -n "$BASE" ]; then echo "$BASE/$1"
  elif [ -n "$VERSION" ]; then echo "https://github.com/$REPO/releases/download/$VERSION/$1"
  else echo "https://github.com/$REPO/releases/latest/download/$1"; fi
}
fetch() { # fetch ASSET DEST: a URL, or a file in a local directory
  case $BASE in
    "" | http://* | https://* | file://*)
      command -v curl >/dev/null || die "curl is required"
      curl -fsSL --retry 3 --connect-timeout 30 -o "$2" "$(url "$1")" ;;
    *) cp "$BASE/$1" "$2" ;;
  esac
}
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'; }

# SHA256SUMS lists jarvisd's and the agent's archives; the agent's name carries the version.
fetch SHA256SUMS "$TMP/SHA256SUMS" || die "could not download $(url SHA256SUMS)"
ASSET=$(awk -v p="jarvis-agent-" -v s="-linux-$ARCH.tar.gz" '{f=$2; sub(/^\*/, "", f)}
  substr(f, 1, length(p)) == p && substr(f, length(f)-length(s)+1) == s {print f; exit}' "$TMP/SHA256SUMS")
[ -n "$ASSET" ] || die "the release has no Jarvis Automation Agent for linux-$ARCH"
REL=${ASSET#jarvis-agent-}; REL=${REL%"-linux-$ARCH.tar.gz"}
[ -z "$VERSION" ] || [ "$VERSION" = "$REL" ] || die "asked for $VERSION but the release files are for $REL"
VERSION=$REL

CUR=""
[ -x "$AGENT" ] && CUR=$("$AGENT" version 2>/dev/null || echo unknown)
if [ "$CUR" = "$VERSION" ] && [ $FORCE = 0 ]; then
  say "Jarvis Automation Agent $VERSION is already installed; checking its setup."
  # shellcheck disable=SC2086 # flags are words
  exec "$AGENT" install $INSTALL_FLAGS
fi

# The signature: required when minisign is installed (a missing or bad one is fatal), else
# checksums only, with a warning. A downloaded binary can't vouch for itself.
MINISIGN=""
for m in "$(command -v minisign 2>/dev/null || true)" /usr/local/bin/minisign /usr/bin/minisign; do
  if [ -n "$m" ] && [ -x "$m" ] && "$m" -v >/dev/null 2>&1; then MINISIGN=$m; break; fi
done
if [ -n "$MINISIGN" ]; then
  fetch SHA256SUMS.minisig "$TMP/SHA256SUMS.minisig" 2>/dev/null || die "the release has no SHA256SUMS.minisig; not installing an unsigned release"
  "$MINISIGN" -Vq -P "$PUBKEY" -m "$TMP/SHA256SUMS" -x "$TMP/SHA256SUMS.minisig" >/dev/null || die "SHA256SUMS signature is INVALID ($MINISIGN); not installing"
  say "SHA256SUMS signature verified."
else
  [ "${JARVIS_REQUIRE_SIGNATURE:-0}" = 1 ] && die "JARVIS_REQUIRE_SIGNATURE=1 but minisign is not installed"
  warn "minisign is not installed, so the release signature is not checked (checksums only); install minisign for a verified install."
fi

say "Downloading Jarvis Automation Agent $VERSION for linux-$ARCH..."
fetch "$ASSET" "$TMP/$ASSET" || die "could not download $(url "$ASSET")"
want=$(awk -v f="$ASSET" '$2 == f || $2 == "*" f {print $1; exit}' "$TMP/SHA256SUMS")
[ "$(sha256 "$TMP/$ASSET")" = "$want" ] || die "checksum mismatch for $ASSET (corrupt or tampered download)"
tar -xzf "$TMP/$ASSET" -C "$TMP"
NEW=$TMP/jarvis-agent-$VERSION-linux-$ARCH/jarvis-agent
[ "$("$NEW" version)" = "$VERSION" ] || die "the downloaded agent does not run here"

# `install` copies itself to ~/.local/lib/jarvis-agent (the copy that updates itself) and runs
# from there, so the download can go.
# A running agent is stopped first: its binary is the one being replaced.
case $INSTALL_FLAGS in
  *--files-only*) ;;
  *) if [ -n "$CUR" ]; then "$AGENT" stop >/dev/null 2>&1 || true; fi ;;
esac
# shellcheck disable=SC2086 # flags are words
"$NEW" install $INSTALL_FLAGS
say "Jarvis Automation Agent $VERSION is installed for $(id -un)."
