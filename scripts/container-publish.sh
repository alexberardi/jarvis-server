#!/usr/bin/env bash
# Push the two single-arch jarvisd images and join them into multi-arch tags.
# Usage: scripts/container-publish.sh DIR BASE TAG...
#   DIR holds jarvisd-image-amd64.tar.gz and jarvisd-image-arm64.tar.gz (docker save of
#   jarvisd:release-<arch>). Pushes IMAGE:BASE-amd64 and IMAGE:BASE-arm64, then each IMAGE:TAG
#   as an index of both. IMAGE is $JARVISD_IMAGE (default ghcr.io/alexberardi/jarvisd).
#   Needs a docker login to the registry. Tested by scripts/container-publish-test.sh.
set -euo pipefail

if [ $# -lt 3 ]; then
	echo "usage: $0 DIR BASE TAG..." >&2
	exit 2
fi
DIR=$1
BASE=$2
shift 2
IMAGE=${JARVISD_IMAGE:-ghcr.io/alexberardi/jarvisd}

for arch in amd64 arm64; do
	[ -f "$DIR/jarvisd-image-$arch.tar.gz" ] || {
		echo "no $DIR/jarvisd-image-$arch.tar.gz" >&2
		exit 2
	}
done
for arch in amd64 arm64; do
	gunzip -c "$DIR/jarvisd-image-$arch.tar.gz" | docker load
	docker tag "jarvisd:release-$arch" "$IMAGE:$BASE-$arch"
	docker push "$IMAGE:$BASE-$arch"
done
tags=()
for t in "$@"; do
	tags+=(-t "$IMAGE:$t")
done
docker buildx imagetools create "${tags[@]}" "$IMAGE:$BASE-amd64" "$IMAGE:$BASE-arm64"
docker buildx imagetools inspect "$IMAGE:$1"
