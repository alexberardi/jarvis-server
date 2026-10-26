#!/usr/bin/env bash
# Build the jarvisd image from a release archive and check that it works (PLAN P4 test plan 5).
# Usage: scripts/container-check.sh DIST VERSION [ARCH]
#   DIST/jarvisd-VERSION-linux-ARCH.tar.gz is the archive, packed as the release packs it. ARCH is
#   amd64 or arm64, by default this machine's: the image is run, so it must be the machine's.
# Runs the image as jarvisd:check, container jc, on host ports 37700/37710/31884 and a temp data
# dir; exits 1 naming the first check that fails. Needs docker and curl.
set -euo pipefail

if [ $# -lt 2 ] || [ $# -gt 3 ]; then
	echo "usage: $0 DIST VERSION [ARCH]" >&2
	exit 2
fi
DIST=$1
VERSION=$2
ARCH=${3:-}
if [ -z "$ARCH" ]; then
	case "$(uname -m)" in
	x86_64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*)
		echo "unsupported machine $(uname -m); pass ARCH (amd64 or arm64)" >&2
		exit 2
		;;
	esac
fi
case "$ARCH" in
amd64 | arm64) ;;
*)
	echo "usage: $0 DIST VERSION [ARCH]: ARCH is amd64 or arm64, got '$ARCH'" >&2
	exit 2
	;;
esac
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ARCHIVE="jarvisd-$VERSION-linux-$ARCH.tar.gz"
IMAGE=jarvisd:check
NAME=jc
API=http://localhost:37700

[ -f "$DIST/$ARCHIVE" ] || {
	echo "no $DIST/$ARCHIVE" >&2
	exit 2
}

data=$(mktemp -d "${TMPDIR:-/tmp}/jarvisd-data.XXXXXX")
built=""

cleanup() {
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	if [ -n "$built" ]; then
		# The data dir's files belong to uid 99: remove them from inside.
		docker run --rm --user 0 --entrypoint sh -v "$data:/data" "$IMAGE" \
			-c 'rm -rf /data/* /data/.[!.]*' >/dev/null 2>&1 || true
		docker rmi -f "$IMAGE" >/dev/null 2>&1 || true
	fi
	rmdir "$data" 2>/dev/null || true
}
trap cleanup EXIT

ok() { echo "ok:   $*"; }
fail() {
	echo "FAIL: $*" >&2
	if docker inspect "$NAME" >/dev/null 2>&1; then
		echo "--- docker logs $NAME (last 30 lines)" >&2
		docker logs "$NAME" 2>&1 | tail -30 >&2
	fi
	exit 1
}

run() {
	docker run -d --name "$NAME" -p 37700:7700 -p 37710:7710 -p 31884:1884 \
		-v "$data:/data" -e JARVIS_MDNS=0 "$IMAGE" >/dev/null || fail "docker run"
}

# wait_health: /health answers within 60 s.
wait_health() {
	local i
	for i in $(seq 60); do
		if curl -fsS "$API/health" >/dev/null 2>&1; then
			ok "$1: /health answered after ${i}s"
			return
		fi
		sleep 1
	done
	fail "$1: /health did not answer within 60 s"
}

# wait_healthy: docker's own HEALTHCHECK (curl inside the image) reports healthy.
wait_healthy() {
	local i s=""
	for i in $(seq 90); do
		s=$(docker inspect -f '{{.State.Health.Status}}' "$NAME" 2>/dev/null || true)
		if [ "$s" = healthy ]; then
			ok "$1: container healthy after ${i}s"
			return
		fi
		sleep 1
	done
	fail "$1: container not healthy within 90 s (status: $s)"
}

# --- build
"$ROOT/scripts/container-build.sh" "$DIST" "$VERSION" "$ARCH" "$IMAGE" >/dev/null ||
	fail "docker build"
built=1
ok "built $IMAGE from $ARCHIVE"
arch=$(docker image inspect -f '{{.Architecture}}' "$IMAGE") || fail "docker image inspect"
[ "$arch" = "$ARCH" ] || fail "image architecture is '$arch', want '$ARCH'"
ok "image architecture is $ARCH"

# --- first run
run
wait_health "first run"
wait_healthy "first run"

# Listening TCP ports inside the container: state 0A (LISTEN), not on loopback.
ports=$(docker exec "$NAME" cat /proc/net/tcp /proc/net/tcp6 | awk '
	$4 == "0A" {
		split($2, a, ":")
		# 127.0.0.0/8 (also v4-mapped) ends in 7F little-endian; ::1 is the v6 one.
		if (a[1] ~ /7F$/ || a[1] == "00000000000000000000000001000000") next
		print a[2]
	}' | while read -r h; do echo $((16#$h)); done | sort -n -u | tr '\n' ' ')
[ "$ports" = "1884 7700 7710 " ] || fail "listening ports are '${ports% }', want '1884 7700 7710'"
ok "listening on exactly 1884 7700 7710"

services=$(curl -fsS "$API/services") || fail "GET /services"
cc=$(grep -o '{[^{}]*"name":"jarvis-command-center"[^{}]*}' <<<"$services" || true)
[ -n "$cc" ] || fail "/services has no jarvis-command-center"
grep -q '"url":"[^"]*/cc"' <<<"$cc" || fail "jarvis-command-center's url has no /cc: $cc"
ok "/services: jarvis-command-center url carries /cc"

uid=$(docker exec "$NAME" sh -c '
	for p in /proc/[0-9]*; do
		[ "$(cat "$p/comm" 2>/dev/null)" = jarvisd ] && awk "/^Uid:/ {print \$2}" "$p/status"
	done' | sort -u | tr '\n' ' ')
[ "$uid" = "99 " ] || fail "jarvisd runs as uid '${uid% }', want 99"
owner=$(docker exec "$NAME" stat -c %u /data/jarvis.db) || fail "no /data/jarvis.db"
[ "$owner" = 99 ] || fail "/data/jarvis.db belongs to uid $owner, want 99"
ok "jarvisd runs as uid 99; /data/jarvis.db is uid 99"

# Not `docker logs | grep -q`: grep -q exits at the match, docker logs can die of SIGPIPE, and
# pipefail then fails the check with the token right there in the log.
logs=$(docker logs "$NAME" 2>&1)
grep -q "Setup token:" <<<"$logs" || fail "docker logs has no 'Setup token:'"
ok "docker logs shows the setup token"

code=$(curl -sS -o /dev/null -w '%{http_code}' http://localhost:37710/) || fail "GET :37710/"
[ "$code" = 200 ] || fail "GET :37710/ answered $code, want 200"
ok "admin :37710/ answers 200"

# --- the data survives the container
docker exec "$NAME" touch /data/keep || fail "write /data/keep"
engines=no
if docker exec "$NAME" test -d /data/engines; then engines=yes; fi
docker rm -f "$NAME" >/dev/null || fail "docker rm"
run
wait_health "second run"
wait_healthy "second run"
for f in jarvis.db setup-token keep; do
	docker exec "$NAME" test -f "/data/$f" || fail "/data/$f is gone after docker rm + run"
done
if [ "$engines" = yes ]; then
	docker exec "$NAME" test -d /data/engines || fail "/data/engines is gone after docker rm + run"
fi
ok "second run on the same data: jarvis.db, setup-token, keep still there (engines/ before: $engines)"

echo "all checks passed"
