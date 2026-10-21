#!/usr/bin/env bash
# Build the jarvisd image for one architecture from a release-layout archive, on this machine.
# Usage: scripts/container-build.sh DIST VERSION ARCH TAG
#   DIST/jarvisd-VERSION-linux-ARCH.tar.gz → the local image TAG (e.g. jarvisd:release-amd64).
# Used by container-check.sh and by the container jobs of release.yml and container-dev.yml.
set -euo pipefail

if [ $# -ne 4 ]; then
	echo "usage: $0 DIST VERSION ARCH TAG" >&2
	exit 2
fi
DIST=$1
VERSION=$2
ARCH=$3
TAG=$4
case "$ARCH" in
amd64 | arm64) ;;
*)
	echo "ARCH is amd64 or arm64, got '$ARCH'" >&2
	exit 2
	;;
esac
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ARCHIVE="jarvisd-$VERSION-linux-$ARCH.tar.gz"
[ -f "$DIST/$ARCHIVE" ] || {
	echo "no $DIST/$ARCHIVE" >&2
	exit 2
}

ctx=$(mktemp -d "${TMPDIR:-/tmp}/jarvisd-ctx.XXXXXX")
trap 'rm -rf "$ctx"' EXIT
cp "$DIST/$ARCHIVE" "$ROOT/scripts/packaging/container/entrypoint.sh" "$ctx/"
docker build -t "$TAG" -f "$ROOT/scripts/packaging/container/Dockerfile" \
	--build-arg ARCHIVE="$ARCHIVE" --build-arg VERSION="$VERSION" "$ctx"
