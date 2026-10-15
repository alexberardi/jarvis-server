#!/usr/bin/env bash
# Builds the two Debian/Ubuntu packages (01-no-cli-plan item 2.4) from binaries already built:
#
#   scripts/build-debs.sh VERSION ARCH JARVISD_BIN AGENT_BIN OUT_DIR
#
#   VERSION  the release tag, e.g. v0.1.0-rc1 (the package version is 0.1.0~rc1, so a
#            prerelease sorts before its release)
#   ARCH     amd64 or arm64 (Debian's names, which are also Go's for these two)
#
# It writes OUT_DIR/jarvis-automation-server_<tag without v>_<arch>.deb (jarvisd) and
# OUT_DIR/jarvis-automation-agent_<tag without v>_<arch>.deb. The configs are
# scripts/packaging/deb/{server,agent}.yaml. nfpm is pinned (NFPM_VERSION) and run through
# `go run`, so nothing needs installing; NFPM=/path/to/nfpm uses another one.
set -euo pipefail

NFPM_VERSION=v2.48.0

[ $# -eq 5 ] || {
  echo "usage: $0 VERSION ARCH JARVISD_BIN AGENT_BIN OUT_DIR" >&2
  exit 2
}
VERSION=$1 ARCH=$2
case "$VERSION" in v[0-9]*) ;; *) echo "VERSION must look like v1.2.3, got '$VERSION'" >&2; exit 2 ;; esac
case "$ARCH" in amd64 | arm64) ;; *) echo "ARCH must be amd64 or arm64, got '$ARCH'" >&2; exit 2 ;; esac
abs() { (cd "$(dirname "$1")" && echo "$PWD/$(basename "$1")"); }
JARVISD_BIN=$(abs "$3")
AGENT_BIN=$(abs "$4")
mkdir -p "$5"
OUT=$(cd "$5" && pwd)
for b in "$JARVISD_BIN" "$AGENT_BIN"; do
  [ -x "$b" ] || { echo "not executable: $b" >&2; exit 2; }
done

cd "$(dirname "$0")/.."
read -r -a nfpm <<<"${NFPM:-go run github.com/goreleaser/nfpm/v2/cmd/nfpm@$NFPM_VERSION}"

# v0.1.0-rc1 → 0.1.0~rc1: Debian sorts "~" before anything, so 0.1.0~rc1 < 0.1.0.
DEB_VERSION=${VERSION#v}
DEB_VERSION=${DEB_VERSION/-/\~}
export DEB_VERSION DEB_ARCH=$ARCH JARVISD_BIN AGENT_BIN
export DEB_MAINTAINER=${DEB_MAINTAINER:-Jarvis Automation <noreply@jarvisautomation.io>}
DEB_GEN=$(mktemp -d)
export DEB_GEN
trap 'rm -rf "$DEB_GEN"' EXIT

# A native package's changelog: one entry pointing at the release notes. Its date is the
# commit's (SOURCE_DATE_EPOCH when set), so the same commit builds the same package.
EPOCH=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}
DATE=$(LC_ALL=C date -u -d "@$EPOCH" '+%a, %d %b %Y %H:%M:%S +0000')
for pkg in jarvis-automation-server jarvis-automation-agent; do
  mkdir -p "$DEB_GEN/$pkg"
  printf '%s (%s) stable; urgency=medium\n\n  * Release %s. The notes:\n    https://github.com/alexberardi/jarvis-server/releases/tag/%s\n\n -- %s  %s\n' \
    "$pkg" "$DEB_VERSION" "$VERSION" "$VERSION" "$DEB_MAINTAINER" "$DATE" | gzip -9n >"$DEB_GEN/$pkg/changelog.gz"
done

# nfpm expands variables in some fields only, not in contents' paths: fill the configs in here.
fill() {
  local s
  s=$(<"$1")
  for v in DEB_VERSION DEB_ARCH DEB_MAINTAINER DEB_GEN JARVISD_BIN AGENT_BIN; do
    s=${s//"\${$v}"/${!v}}
  done
# shellcheck disable=SC2016 # literal: expanded by the script it writes / the text it checks
  case "$s" in *'${'*) echo "$1: a variable is left unfilled" >&2; return 1 ;; esac
  printf '%s\n' "$s"
}

export SOURCE_DATE_EPOCH=$EPOCH
for p in server agent; do
  fill "scripts/packaging/deb/$p.yaml" >"$DEB_GEN/$p.yaml"
  target="$OUT/jarvis-automation-${p}_${VERSION#v}_${ARCH}.deb"
  "${nfpm[@]}" package --packager deb --config "$DEB_GEN/$p.yaml" --target "$target"
done
ls -l "$OUT"/jarvis-automation-*_"${VERSION#v}"_"$ARCH".deb
