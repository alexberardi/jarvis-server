#!/usr/bin/env bash
# Test scripts/container-publish.sh with a fake docker that logs its arguments.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir -p "$t/bin" "$t/img"
cat > "$t/bin/docker" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >> "$LOG"
if [ "$1" = load ]; then
	cat > /dev/null
fi
exit 0
FAKE
chmod +x "$t/bin/docker"
PATH="$t/bin:$PATH"
export LOG=$t/log JARVISD_IMAGE=example/jarvisd
for arch in amd64 arm64; do
	echo x | gzip > "$t/img/jarvisd-image-$arch.tar.gz"
done
fail=0
want() {
	printf '%s\n' load "tag jarvisd:release-amd64 example/jarvisd:$1-amd64" "push example/jarvisd:$1-amd64" \
		load "tag jarvisd:release-arm64 example/jarvisd:$1-arm64" "push example/jarvisd:$1-arm64" "$2" \
		"buildx imagetools inspect example/jarvisd:$1"
}
: > "$LOG"
if ! "$ROOT/scripts/container-publish.sh" "$t/img" v1.2.3 v1.2.3 latest > /dev/null ||
	[ "$(cat "$LOG")" != "$(want v1.2.3 "buildx imagetools create -t example/jarvisd:v1.2.3 -t example/jarvisd:latest example/jarvisd:v1.2.3-amd64 example/jarvisd:v1.2.3-arm64")" ]; then
	echo "FAIL: release"; fail=1
fi
: > "$LOG"
if ! "$ROOT/scripts/container-publish.sh" "$t/img" dev dev > /dev/null ||
	[ "$(cat "$LOG")" != "$(want dev "buildx imagetools create -t example/jarvisd:dev example/jarvisd:dev-amd64 example/jarvisd:dev-arm64")" ]; then
	echo "FAIL: dev"; fail=1
fi
: > "$LOG"
rm "$t/img/jarvisd-image-arm64.tar.gz"
"$ROOT/scripts/container-publish.sh" "$t/img" dev dev > /dev/null 2>&1
if [ $? -ne 2 ] || [ -s "$LOG" ]; then echo "FAIL: missing arm64"; fail=1; fi
: > "$LOG"
"$ROOT/scripts/container-publish.sh" "$t/img" dev > /dev/null 2>&1
if [ $? -ne 2 ]; then echo "FAIL: usage"; fail=1; fi
exit "$fail"
