#!/usr/bin/env bash
# Tell whether a ghcr.io image can be pulled without logging in (the package is Public).
# Usage: scripts/ghcr-public-check.sh IMAGE TAG   (IMAGE like ghcr.io/alexberardi/jarvisd)
# Exit 0: public. Exit 1: not public (or no such tag). Exit 2: usage. Needs curl and jq.
set -euo pipefail

if [ $# -ne 2 ] || [ "${1#ghcr.io/}" = "$1" ]; then
	echo "usage: $0 ghcr.io/OWNER/NAME TAG" >&2
	exit 2
fi
IMAGE=$1
TAG=$2
REPO=${IMAGE#ghcr.io/}

token=$(curl -fsS "https://ghcr.io/token?service=ghcr.io&scope=repository:$REPO:pull" 2>/dev/null |
	jq -r '.token // empty' || true)
code=$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${token:-none}" \
	-H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
	"https://ghcr.io/v2/$REPO/manifests/$TAG" || echo 000)
if [ "$code" = 200 ]; then
	echo "public: $IMAGE:$TAG"
	exit 0
fi
echo "not public: $IMAGE:$TAG (HTTP $code)" >&2
exit 1
