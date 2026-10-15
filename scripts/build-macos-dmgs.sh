#!/usr/bin/env bash
# Builds the two drag-to-Applications disk images (01-no-cli-plan item 2.2), on a Mac, from the
# apps scripts/build-macos-apps.sh made:
#
#   scripts/build-macos-dmgs.sh <tag> <apps-dir> <out-dir>
#
#   <out-dir>/Jarvis-Automation-Server-<tag without v>.dmg   volume "Jarvis Automation Server"
#   <out-dir>/Jarvis-Automation-Agent-<tag without v>.dmg    volume "Jarvis Automation Agent"
#
# (Hyphens, not spaces: GitHub turns spaces in release asset names into dots.) Each holds the
# app, an Applications link and a background with an arrow (scripts/packaging/macos). dmgbuild,
# pinned with hashes in a throwaway venv, writes the window layout without driving Finder and
# runs hdiutil; the result is lzfse-compressed (ULFO, macOS 10.11+).
#
# SIGN_IDENTITY (a Developer ID Application identity; SIGN_KEYCHAIN its keychain) signs each
# dmg, which notarization then covers; "-" or unset leaves them unsigned (CI, dry runs).
# Notarizing and stapling are release.yml's (sign-macos). DMGBUILD_VENV: where the venv goes.
set -euo pipefail

die() { echo "build-macos-dmgs: $*" >&2; exit 1; }
[ $# -eq 3 ] || die "usage: $0 <tag> <apps-dir> <out-dir>"
VERSION=$1 APPS=$2 OUT=$3
[ "$(uname -s)" = Darwin ] || die "needs macOS (hdiutil)"
case "$VERSION" in v[0-9]*) ;; *) die "the tag must look like v1.2.3, got '$VERSION'" ;; esac

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PKG=$ROOT/scripts/packaging/macos
ID=${SIGN_IDENTITY:--}
VENV=${DMGBUILD_VENV:-"$HOME/.cache/jarvis-dmgbuild"}

if [ ! -x "$VENV/bin/dmgbuild" ]; then
  python3 -m venv "$VENV"
  "$VENV/bin/python" -m pip install -q --disable-pip-version-check --require-hashes --only-binary :all: \
    -r "$PKG/dmg-requirements.txt"
fi

mkdir -p "$OUT"
for kind in Server Agent; do
  app="$APPS/Jarvis Automation $kind.app"
  dmg="$OUT/Jarvis-Automation-$kind-${VERSION#v}.dmg"
  [ -d "$app" ] || die "no $app"
  rm -f "$dmg"
  "$VENV/bin/dmgbuild" -s "$PKG/dmg-settings.py" -D app="$app" -D background="$PKG/background.png" \
    "Jarvis Automation $kind" "$dmg"
  if [ "$ID" != - ]; then
    args=(--force --sign "$ID" --timestamp --identifier "net.jarvisautomation.$(echo "$kind" | tr '[:upper:]' '[:lower:]').dmg")
    if [ -n "${SIGN_KEYCHAIN:-}" ]; then args+=(--keychain "$SIGN_KEYCHAIN"); fi
    codesign "${args[@]}" "$dmg"
    codesign --verify --strict --verbose=2 "$dmg"
  fi
  hdiutil verify -quiet "$dmg"
  echo "built $dmg"
done
