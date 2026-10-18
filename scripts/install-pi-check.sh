#!/usr/bin/env bash
# Golden check of install-pi.sh (go-pi-node.md §7 phase 4.3): it installs into a scratch root
# (--root: every file under it, no system command run, each one logged instead) from a fake
# release, and what it wrote (every path with its mode, the content of each text file, the
# commands it would have run) must equal scripts/testdata/install-pi.golden. A second run must
# change nothing. Then --activate/--deactivate, and the refusals.
#
#   scripts/install-pi-check.sh            # check
#   UPDATE=1 scripts/install-pi-check.sh   # rewrite the golden file after a deliberate change
#
# No Pi, root or network needed. The agent in the fake release is a shell script.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT=$HERE/install-pi.sh
GOLDEN=$HERE/testdata/install-pi.golden
DTS=$HERE/packaging/pi/respeaker-2mic-v2_0-overlay.dts
V=v9.9.9
fail() { echo "install-pi-check: $*" >&2; exit 1; }

T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT

# A release: the arm64 agent, and an amd64 one listed first (the script must pick arm64).
mkdir -p "$T/stage/jarvis-agent-$V-linux-arm64" "$T/dist"
# shellcheck disable=SC2016 # $1 is the fake agent's own argument
printf '#!/bin/sh\n[ "$1" = version ] && echo %s\n' "$V" >"$T/stage/jarvis-agent-$V-linux-arm64/jarvis-agent"
chmod 0755 "$T/stage/jarvis-agent-$V-linux-arm64/jarvis-agent"
tar -C "$T/stage" -czf "$T/dist/jarvis-agent-$V-linux-arm64.tar.gz" "jarvis-agent-$V-linux-arm64"
(cd "$T/dist" && { echo "$(printf '0%.0s' $(seq 64))  jarvis-agent-$V-linux-amd64.tar.gz"; sha256sum -- *.tar.gz; } >SHA256SUMS)

# minisign: a stand-in that isn't usable (-v fails), so the run is checksums only and the same
# on every machine. FAKE_MINISIGN=invalid makes it usable and every signature bad.
mkdir -p "$T/bin"
cat >"$T/bin/minisign" <<'EOF'
#!/bin/sh
[ "${FAKE_MINISIGN:-}" = invalid ] || exit 1
[ "$1" = -v ] && exit 0
exit 1
EOF
chmod 0755 "$T/bin/minisign"
for m in /usr/local/bin/minisign /usr/bin/minisign; do
  [ ! -x "$m" ] || fail "$m is installed: the check needs a machine without minisign in the fixed paths"
done

# A Pi's boot partition and fstab, before the install.
seed() {
  mkdir -p "$1/boot/firmware/overlays" "$1/etc"
  printf '[all]\ndtparam=audio=on\ndtoverlay=wm8960-soundcard\n' >"$1/boot/firmware/config.txt"
  printf 'proc /proc proc defaults 0 0\n' >"$1/etc/fstab"
}
R() { env -i HOME="$T" TMPDIR="$T" PATH="$T/bin:$PATH" ${FAKE_MINISIGN:+FAKE_MINISIGN=$FAKE_MINISIGN} sh "$SCRIPT" "$@"; }

# snapshot ROOT: what the install wrote, as text.
snapshot() {
  echo "## paths"
  (cd "$1" && find . -mindepth 1 ! -name install-pi-commands.log -printf '%y %m %P\n' | sort -k3)
  echo "## commands"
  cat "$1/install-pi-commands.log"
  (cd "$1" && find . -type f ! -name install-pi-commands.log ! -path './opt/*' -printf '%P\n' | sort) |
    while read -r f; do
      echo "## $f"
      cat "$1/$f"
    done
}

ROOT=$T/root
seed "$ROOT"
echo "--- install"
R --root "$ROOT" --from-dir "$T/dist" | tee "$T/out"
grep -q "installed in /opt/jarvis-agent" "$T/out" || fail "no agent installed"
grep -q "reboot" "$T/out" || fail "no reboot note after a config.txt change"
cmp -s "$ROOT/opt/jarvis-agent/respeaker-2mic-v2_0-overlay.dts" "$DTS" ||
  fail "the DTS in install-pi.sh differs from scripts/packaging/pi/respeaker-2mic-v2_0-overlay.dts"
[ "$("$ROOT/opt/jarvis-agent/jarvis-agent" version)" = "$V" ] || fail "the agent in /opt is not $V"
snapshot "$ROOT" >"$T/got"
if [ "${UPDATE:-}" = 1 ]; then
  mkdir -p "$(dirname "$GOLDEN")"
  cp "$T/got" "$GOLDEN"
  echo "install-pi-check: wrote $GOLDEN"
fi
diff -u "$GOLDEN" "$T/got" || fail "what install-pi.sh writes differs from $GOLDEN (UPDATE=1 to accept a deliberate change)"

echo "--- a second run changes nothing"
R --root "$ROOT" --from-dir "$T/dist" | tee "$T/out"
grep -q "already installed" "$T/out" || fail "a re-run downloaded again"
grep -q "reboot" "$T/out" && fail "a re-run changed config.txt"
snapshot "$ROOT" >"$T/got2"
diff -u "$T/got" "$T/got2" || fail "a second run changed what was written"

echo "--- an existing /etc/jarvis-node (the Python node's) is kept"
R2=$T/root2
seed "$R2"
mkdir -p "$R2/etc/jarvis-node"
echo '{}' >"$R2/etc/jarvis-node/audio_config.json"
R --root "$R2" --from-dir "$T/dist" >/dev/null
[ "$(cat "$R2/etc/jarvis-node/audio_config.json")" = '{}' ] || fail "the Python node's /etc/jarvis-node was touched"

echo "--- --activate / --deactivate"
R --root "$ROOT" --from-dir "$T/dist" --activate >/dev/null
tail -2 "$ROOT/install-pi-commands.log" | tr '\n' ';' | grep -qx 'systemctl disable --now jarvis-node.service;systemctl enable --now jarvis-agent.service;' ||
  fail "--activate: $(tail -2 "$ROOT/install-pi-commands.log")"
R --root "$ROOT" --deactivate >/dev/null
tr '\n' ';' <"$ROOT/install-pi-commands.log" | grep -qx 'systemctl disable --now jarvis-agent.service;systemctl enable --now jarvis-node.service;' ||
  fail "--deactivate: $(cat "$ROOT/install-pi-commands.log")"

echo "--- refusals"
R3=$T/root3
T2=$T/tampered
mkdir -p "$T2"
cp "$T/dist"/* "$T2/"
printf 'x' >>"$T2/jarvis-agent-$V-linux-arm64.tar.gz"
if R --root "$R3" --from-dir "$T2" >"$T/out" 2>&1; then cat "$T/out"; fail "installed a tampered archive"; fi
grep -q "checksum mismatch" "$T/out" || { cat "$T/out"; fail "refused the tampered archive for another reason"; }
[ ! -e "$R3/opt/jarvis-agent/jarvis-agent" ] || fail "the refused install left an agent"
echo 'sig' >"$T2/SHA256SUMS.minisig"
if FAKE_MINISIGN=invalid R --root "$R3" --from-dir "$T2" >"$T/out" 2>&1; then cat "$T/out"; fail "installed with a bad signature"; fi
grep -q "signature is INVALID" "$T/out" || { cat "$T/out"; fail "refused the bad signature for another reason"; }
if R --root "$R3" --from-dir "$T/dist" --version v1.0.0 >"$T/out" 2>&1; then fail "installed another version than asked"; fi
R --root "$R3" --nope >/dev/null 2>&1 && fail "accepted an unknown option"
echo "install-pi-check: PASS"
