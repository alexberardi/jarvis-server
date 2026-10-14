#!/usr/bin/env bash
# CI check of install-agent.sh (01-no-cli-plan Q8) against a release directory the workflow
# just built (SHA256SUMS, the jarvis-agent archives, maybe SHA256SUMS.minisig):
#
#   scripts/install-agent-check.sh DIST VERSION [signature]
#
# signature: "valid" (minisign is on PATH and the list carries the project's signature: it
# must be verified), "invalid" (minisign is on PATH and the list is signed by another key: it
# must be refused), or empty (no minisign: checksums only).
#
# A scratch HOME and --files-only, as scripts/agent-install-check.sh: no systemd, nothing
# started. Never point it at a HOME someone uses.
set -euo pipefail
DIST=$(cd "$1" && pwd)
VERSION=$2
SIG=${3:-}
SCRIPT=$(cd "$(dirname "$0")" && pwd)/install-agent.sh
fail() { echo "install-agent-check: $*" >&2; exit 1; }

H=$(mktemp -d)
R() { env -i HOME="$H" PATH="$PATH" "$@"; }
AGENT="$H/.local/lib/jarvis-agent/jarvis-agent"

if [ "$SIG" = invalid ]; then
  if R sh "$SCRIPT" --from-dir "$DIST" --files-only >"$H/out" 2>&1; then cat "$H/out"; fail "installed a release signed by another key"; fi
  cat "$H/out"
  grep -q "signature is INVALID" "$H/out" || fail "refused for another reason"
  [ ! -e "$AGENT" ] || fail "the refused install left an agent"
  echo "install-agent-check: refused the foreign signature"
  exit 0
fi

echo "--- install"
R sh "$SCRIPT" --from-dir "$DIST" --files-only | tee "$H/out"
[ "$("$AGENT" version)" = "$VERSION" ] || fail "the per-user copy is not $VERSION"
grep -q "is installed for" "$H/out" || fail "no closing line"
case $SIG in
  valid) grep -q "signature verified" "$H/out" || fail "the signature was not checked" ;;
  *) ;;
esac
[ -e "$H/.local/share/applications/net.jarvisautomation.agent.desktop" ] || fail "no app menu entry"

echo "--- re-run: same version, no download"
R sh "$SCRIPT" --from-dir "$DIST" --files-only | tee "$H/out"
grep -q "already installed" "$H/out" || fail "a re-run downloaded again"

echo "--- a tampered archive is refused"
T=$(mktemp -d)
cp "$DIST"/SHA256SUMS* "$T/"
for a in "$DIST"/jarvis-agent-*-linux-*.tar.gz; do
  cp "$a" "$T/"
  printf 'x' >>"$T/$(basename "$a")"
done
if R sh "$SCRIPT" --from-dir "$T" --files-only --force >"$H/out" 2>&1; then cat "$H/out"; fail "installed a tampered archive"; fi
grep -q "checksum mismatch" "$H/out" || { cat "$H/out"; fail "refused for another reason"; }

echo "--- uninstall"
R sh "$SCRIPT" --uninstall --files-only
[ ! -e "$AGENT" ] || fail "uninstall left the per-user copy"
[ ! -e "$H/.local/share/applications/net.jarvisautomation.agent.desktop" ] || fail "uninstall left the menu entry"

echo "--- refusals"
R sh "$SCRIPT" --nope >/dev/null 2>&1 && fail "accepted an unknown option"
echo "install-agent-check: PASS"
