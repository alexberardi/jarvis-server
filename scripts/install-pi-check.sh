#!/usr/bin/env bash
# Golden check of install-pi.sh (go-pi-node.md §7 phase 4.3): it installs into a scratch root
# (--root: every file under it, no system command run, each one logged instead) from a fake
# release, and what it wrote (every path with its mode, the content of each text file, the
# commands it would have run) must equal scripts/testdata/install-pi.golden. A second run must
# change nothing. Then --activate/--deactivate, and the refusals.
#
#   scripts/install-pi-check.sh            # check
#   UPDATE=1 scripts/install-pi-check.sh   # rewrite the golden file after a deliberate change
#   scripts/install-pi-check.sh DIST VERSION [valid|invalid]
#                                          # release mode (release.yml's install-pi job): the
#                                          # stamped DIST/install-pi.sh against that run's files
#
# No Pi or root needed. The agent in the fake release is a shell script; the wake models are the
# real ones (their hashes are pinned), from .models/openwakeword (scripts/fetch-test-models.sh)
# or downloaded from openWakeWord's release. A stand-in nmcli answers the Wi-Fi queries.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT=$HERE/install-pi.sh
GOLDEN=$HERE/testdata/install-pi.golden
DTS=$HERE/packaging/pi/respeaker-2mic-v2_0-overlay.dts
WATCHDOG=$HERE/packaging/pi/jarvis-net-watchdog
WRAPPERS="jarvis-apt-install jarvis-apt-source jarvis-post-install"
MODELS=${JARVIS_SHERPA_MODELS:-$HERE/../.models}/openwakeword
WAKE_MODELS="melspectrogram.onnx embedding_model.onnx hey_jarvis_v0.1.onnx"
V=v9.9.9
fail() { echo "install-pi-check: $*" >&2; exit 1; }

T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT

# nmcli: wlan0 is on jarvis-Home (SSID Home); preconfigured (netplan's) is the same SSID, Office
# another one, and there is a wired connection. Only queries: under --root a change is logged.
# FAKE_WLAN0=none: wlan0 reports no connection, though jarvis-Home is still active.
mkdir -p "$T/bin"
cat >"$T/bin/nmcli" <<'NMCLI'
#!/bin/sh
ssid() { case $1 in jarvis-Home | preconfigured) echo Home ;; Office) echo Office ;; esac; }
case "$*" in
  "-t -f GENERAL.CONNECTION device show wlan0") [ "${FAKE_WLAN0:-}" = none ] && echo GENERAL.CONNECTION: || echo GENERAL.CONNECTION:jarvis-Home ;;
  "-t -f NAME,TYPE connection show --active") printf '%s\n' 'Wired connection 1:802-3-ethernet' jarvis-Home:802-11-wireless ;;
  "-t -f NAME,TYPE connection show") printf '%s\n' preconfigured:802-11-wireless 'Wired connection 1:802-3-ethernet' Office:802-11-wireless jarvis-Home:802-11-wireless ;;
  "-t -g 802-11-wireless.ssid connection show "*) ssid "$6" ;;
  *) echo "nmcli $*: not a query" >&2; exit 1 ;;
esac
NMCLI
chmod 0755 "$T/bin/nmcli"
# ip: the route to an SSH client goes out FAKE_SSH_DEV (default eth0).
cat >"$T/bin/ip" <<'IP'
#!/bin/sh
case "$*" in
  "-o route get "*) echo "$4 dev ${FAKE_SSH_DEV:-eth0} src 192.0.2.20 uid 0" ;;
  *) echo "ip $*: not a query" >&2; exit 1 ;;
esac
IP
chmod 0755 "$T/bin/ip"

# A Pi's boot partition and fstab, before the install.
seed() {
  mkdir -p "$1/boot/firmware/overlays" "$1/etc"
  printf '[all]\ndtparam=audio=on\ndtoverlay=wm8960-soundcard\n' >"$1/boot/firmware/config.txt"
  printf 'proc /proc proc defaults 0 0\n' >"$1/etc/fstab"
  # BlueZ's, as Debian 13 ships it (the Class commented out).
  mkdir -p "$1/etc/bluetooth"
  printf '[General]\n#Name = BlueZ\n#Class = 0x000100\n\n[Policy]\n#AutoEnable=true\n' >"$1/etc/bluetooth/main.conf"
}
R() { env -i HOME="$T" TMPDIR="$T" PATH="$T/bin:$PATH" ${FAKE_MINISIGN:+FAKE_MINISIGN=$FAKE_MINISIGN} ${FAKE_WLAN0:+FAKE_WLAN0=$FAKE_WLAN0} \
  ${SSH_CONNECTION:+"SSH_CONNECTION=$SSH_CONNECTION"} ${FAKE_SSH_DEV:+FAKE_SSH_DEV=$FAKE_SSH_DEV} sh "$SCRIPT" "$@"; }

# Release mode (release.yml's install-pi job, on arm64): the stamped DIST/install-pi.sh, as
# published, against that run's own files. It must be listed in the signed SHA256SUMS, pinned to
# VERSION (with no --from-dir it fetches that tag's files), and install the real agent from DIST.
# valid: minisign is installed and the signature must verify; invalid: it must be refused (a
# dry run's throwaway key); neither: checksums only, minisign must not be installed.
if [ $# -gt 0 ]; then
  [ $# -ge 2 ] || fail "usage: $0 [DIST VERSION [valid|invalid]]"
  DIST=$(cd "$1" && pwd) V=$2 SIG=${3:-}
  SCRIPT=$DIST/install-pi.sh
  grep -qx "RELEASE_VERSION=\"$V\"" "$SCRIPT" || fail "$SCRIPT is not stamped with $V"
  grep -x "[0-9a-f]*  install-pi.sh" "$DIST/SHA256SUMS" >"$T/sum" || fail "install-pi.sh is not in SHA256SUMS"
  (cd "$DIST" && sha256sum -c --quiet "$T/sum") || fail "install-pi.sh differs from its SHA256SUMS line"

  echo "--- pinned to $V"
  mkdir -p "$T/nocurl"
  printf '#!/bin/sh\nexit 22\n' >"$T/nocurl/curl"
  chmod 0755 "$T/nocurl/curl"
  seed "$T/pin"
  if env -i HOME="$T" TMPDIR="$T" PATH="$T/nocurl:$T/bin:$PATH" sh "$SCRIPT" --root "$T/pin" >"$T/out" 2>&1; then
    cat "$T/out"; fail "installed with every download failing"
  fi
  grep -qF "could not download https://github.com/alexberardi/jarvis-server/releases/download/$V/SHA256SUMS" "$T/out" ||
    { cat "$T/out"; fail "the stamped script does not fetch release $V"; }

  echo "--- install from $DIST (${SIG:-checksums only})"
  case $SIG in
    "") for m in /usr/local/bin/minisign /usr/bin/minisign; do [ ! -x "$m" ] || fail "$m is installed (pass valid or invalid)"; done ;;
    valid | invalid) command -v minisign >/dev/null || fail "minisign is not installed" ;;
    *) fail "unknown signature mode $SIG" ;;
  esac
  ROOT=$T/root
  seed "$ROOT"
  if [ "$SIG" = invalid ]; then
    if R --root "$ROOT" --from-dir "$DIST" >"$T/out" 2>&1; then cat "$T/out"; fail "installed with a signature from another key"; fi
    grep -q "signature is INVALID" "$T/out" || { cat "$T/out"; fail "refused for another reason than the signature"; }
    [ ! -e "$ROOT/opt/jarvis-agent/jarvis-agent" ] || fail "the refused install left an agent"
    echo "install-pi-check: PASS (release $V, signature refused)"
    exit 0
  fi
  R --root "$ROOT" --from-dir "$DIST" | tee "$T/out"
  grep -q "installed in /opt/jarvis-agent" "$T/out" || fail "no agent installed"
  [ "$SIG" != valid ] || grep -q "SHA256SUMS signature verified" "$T/out" || fail "the signature was not checked"
  [ "$("$ROOT/opt/jarvis-agent/jarvis-agent" version)" = "$V" ] || fail "the agent in /opt is not $V"
  [ -f "$ROOT/etc/systemd/system/jarvis-agent.service" ] || fail "no jarvis-agent.service"
  echo "install-pi-check: PASS (release $V)"
  exit 0
fi

# A release: the arm64 agent, and an amd64 one listed first (the script must pick arm64).
mkdir -p "$T/stage/jarvis-agent-$V-linux-arm64" "$T/dist"
# shellcheck disable=SC2016 # $1 is the fake agent's own argument
printf '#!/bin/sh\n[ "$1" = version ] && echo %s\n' "$V" >"$T/stage/jarvis-agent-$V-linux-arm64/jarvis-agent"
chmod 0755 "$T/stage/jarvis-agent-$V-linux-arm64/jarvis-agent"
tar -C "$T/stage" -czf "$T/dist/jarvis-agent-$V-linux-arm64.tar.gz" "jarvis-agent-$V-linux-arm64"
# The wake models ship with the release files.
for m in $WAKE_MODELS; do
  if [ -f "$MODELS/$m" ]; then cp "$MODELS/$m" "$T/dist/"; else
    curl -fsSL --retry 3 -o "$T/dist/$m" "https://github.com/dscripka/openWakeWord/releases/download/v0.5.1/$m" ||
      fail "could not download the wake model $m (or run scripts/fetch-test-models.sh)"
  fi
done
(cd "$T/dist" && { echo "$(printf '0%.0s' $(seq 64))  jarvis-agent-$V-linux-amd64.tar.gz"; sha256sum -- *.tar.gz; } >SHA256SUMS)

# minisign: a stand-in that isn't usable (-v fails), so the run is checksums only and the same
# on every machine. FAKE_MINISIGN=invalid makes it usable and every signature bad.
cat >"$T/bin/minisign" <<'MINISIGN'
#!/bin/sh
[ "${FAKE_MINISIGN:-}" = invalid ] || exit 1
[ "$1" = -v ] && exit 0
exit 1
MINISIGN
chmod 0755 "$T/bin/minisign"
for m in /usr/local/bin/minisign /usr/bin/minisign; do
  [ ! -x "$m" ] || fail "$m is installed: the check needs a machine without minisign in the fixed paths"
done

# snapshot ROOT: what the install wrote, as text.
snapshot() {
  echo "## paths"
  (cd "$1" && find . -mindepth 1 ! -name install-pi-commands.log \( -type l -printf '%y %m %P -> %l\n' -o -printf '%y %m %P\n' \) | LC_ALL=C sort -k3)
  echo "## commands"
  cat "$1/install-pi-commands.log"
  (cd "$1" && find . -type f ! -name install-pi-commands.log ! -path './opt/*' ! -path './usr/local/share/jarvis-agent/*' ! -path ./usr/local/sbin/jarvis-net-watchdog ! -path './usr/local/sbin/jarvis-apt-*' ! -path ./usr/local/sbin/jarvis-post-install -printf '%P
' | LC_ALL=C sort) |
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
cmp -s "$ROOT/usr/local/share/jarvis-agent/respeaker-2mic-v2_0-overlay.dts" "$DTS" ||
  fail "the DTS in install-pi.sh differs from scripts/packaging/pi/respeaker-2mic-v2_0-overlay.dts"
cmp -s "$ROOT/usr/local/sbin/jarvis-net-watchdog" "$WATCHDOG" ||
  fail "the watchdog in install-pi.sh differs from scripts/packaging/pi/jarvis-net-watchdog"
for w in $WRAPPERS; do
  cmp -s "$ROOT/usr/local/sbin/$w" "$HERE/packaging/pi/$w" ||
    fail "$w in install-pi.sh differs from scripts/packaging/pi/$w"
done
# The sudoers file parses (install-pi.sh runs the same check on a Pi; under --root it can't).
if command -v visudo >/dev/null; then
  visudo -cf "$ROOT/etc/sudoers.d/jarvis-agent" >/dev/null 2>&1 || fail "the sudoers file fails visudo: $(visudo -cf "$ROOT/etc/sudoers.d/jarvis-agent" 2>&1)"
fi
[ "$("$ROOT/opt/jarvis-agent/jarvis-agent" version)" = "$V" ] || fail "the agent in /opt is not $V"
for m in $WAKE_MODELS; do
  cmp -s "$ROOT/usr/local/share/jarvis-agent/openwakeword/$m" "$T/dist/$m" || fail "the wake model $m is not in /usr/local/share/jarvis-agent/openwakeword"
done
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
grep -q "wake model" "$T/out" && fail "a re-run installed the wake models again"
grep -q "reboot" "$T/out" && fail "a re-run changed config.txt"
snapshot "$ROOT" >"$T/got2"
diff -u "$T/got" "$T/got2" || fail "a second run changed what was written"

echo "--- an existing /etc/jarvis-node (the Python node's) is kept, a user's mask on pulseaudio isn't, a main.conf without a Class gets one"
R2=$T/root2
seed "$R2"
printf '[General]\nName = kitchen\n\n[Policy]\n' >"$R2/etc/bluetooth/main.conf"
mkdir -p "$R2/etc/jarvis-node" "$R2/home/pi/.config/systemd/user"
echo '{}' >"$R2/etc/jarvis-node/audio_config.json"
ln -s /dev/null "$R2/home/pi/.config/systemd/user/pulseaudio.service"
R --root "$R2" --from-dir "$T/dist" >/dev/null 2>"$T/err"
[ "$(cat "$R2/etc/jarvis-node/audio_config.json")" = '{}' ] || fail "the Python node's /etc/jarvis-node was touched"
[ ! -L "$R2/home/pi/.config/systemd/user/pulseaudio.service" ] || fail "the per-user mask on pulseaudio.service was kept"
grep -q "removed a per-user mask on pulseaudio.service" "$T/err" || fail "no warning for the removed mask: $(cat "$T/err")"
[ "$(sed -n 2p "$R2/etc/bluetooth/main.conf")" = "Class = 0x240404" ] || fail "no audio Class in [General]: $(cat "$R2/etc/bluetooth/main.conf")"

echo "--- wlan0 reports no connection: power-save off on the active Wi-Fi one, no reconcile (as N:)"
R5=$T/root-nowlan
seed "$R5"
FAKE_WLAN0=none R --root "$R5" --from-dir "$T/dist" >/dev/null
grep -qx "nmcli connection modify jarvis-Home wifi.powersave 2" "$R5/install-pi-commands.log" ||
  fail "power-save was not turned off on the active Wi-Fi connection"
grep -q "autoconnect" "$R5/install-pi-commands.log" && fail "reconciled the Wi-Fi profiles with no connection on wlan0"

echo "--- an older install (root's /opt, the overlay source in it) is moved over"
R4=$T/root4
seed "$R4"
mkdir -p "$R4/opt/jarvis-agent"
echo old >"$R4/opt/jarvis-agent/respeaker-2mic-v2_0-overlay.dts"
R --root "$R4" --from-dir "$T/dist" >/dev/null
[ ! -e "$R4/opt/jarvis-agent/respeaker-2mic-v2_0-overlay.dts" ] || fail "the overlay source was left in the node user's /opt/jarvis-agent"
grep -qx 'chown pi: /opt/jarvis-agent' "$R4/install-pi-commands.log" || fail "/opt/jarvis-agent was not given to the node's user"

echo "--- --activate / --deactivate"
R --root "$ROOT" --from-dir "$T/dist" --activate >/dev/null
tail -2 "$ROOT/install-pi-commands.log" | tr '\n' ';' | grep -qx 'systemctl disable --now jarvis-node.service;systemctl enable --now jarvis-agent.service;' ||
  fail "--activate: $(tail -2 "$ROOT/install-pi-commands.log")"
R --root "$ROOT" --deactivate >/dev/null
tr '\n' ';' <"$ROOT/install-pi-commands.log" | grep -qx 'systemctl disable --now jarvis-agent.service;systemctl enable --now jarvis-node.service;' ||
  fail "--deactivate: $(cat "$ROOT/install-pi-commands.log")"

echo "--- --activate on a node that isn't enrolled, over SSH on wlan0 (#423)"
R6=$T/root-unenrolled
seed "$R6"
SSH="192.0.2.50 50000 192.0.2.20 22"
# S runs it as `sudo sh install-pi.sh` does: env_reset leaves SSH_CONNECTION out of the script's
# environment; only its parents have it. Under --root, /proc is the stand-in's: this shell (the
# script's parent) is sudo, its parent (pid 4242) the SSH login shell.
mkdir -p "$R6/proc/$$" "$R6/proc/4242"
printf 'HOME=/root\0SUDO_USER=pi\0' >"$R6/proc/$$/environ"
printf 'Name:\tsudo\nPPid:\t4242\n' >"$R6/proc/$$/status"
printf 'HOME=/home/pi\0SSH_CONNECTION=%s\0USER=pi\0' "$SSH" >"$R6/proc/4242/environ"
printf 'Name:\tbash\nPPid:\t1\n' >"$R6/proc/4242/status"
S() { SSH_CONNECTION='' R "$@"; }
if FAKE_SSH_DEV=wlan0 S --root "$R6" --from-dir "$T/dist" --activate >"$T/out" 2>&1; then
  cat "$T/out"; fail "--activate under sudo went on over wlan0 with no credentials"
fi
grep -q "not activated: this node isn't enrolled, and this SSH session runs over wlan0" "$T/out" || { cat "$T/out"; fail "refused for another reason"; }
grep -qF "sudo -u pi -H /opt/jarvis-agent/jarvis-agent enroll --server" "$T/out" || { cat "$T/out"; fail "no enroll command"; }
grep -q "enable --now jarvis-agent" "$R6/install-pi-commands.log" && fail "the refused --activate enabled jarvis-agent"
[ -f "$R6/etc/systemd/system/jarvis-agent.service" ] || fail "the refused --activate did not install the unit"
# As root over SSH (or sudo -E), SSH_CONNECTION is the script's own.
if SSH_CONNECTION=$SSH FAKE_SSH_DEV=wlan0 R --root "$R6" --from-dir "$T/dist" --activate >"$T/out" 2>&1; then
  cat "$T/out"; fail "--activate as root went on over wlan0 with no credentials"
fi
FAKE_SSH_DEV=wlan0 S --root "$R6" --from-dir "$T/dist" --activate --setup-mode >"$T/out" 2>&1 ||
  { cat "$T/out"; fail "--activate --setup-mode was refused"; }
grep -q "enable --now jarvis-agent" "$R6/install-pi-commands.log" || fail "--setup-mode did not activate"
FAKE_SSH_DEV=eth0 S --root "$R6" --from-dir "$T/dist" --activate >"$T/out" 2>&1 ||
  { cat "$T/out"; fail "--activate over ethernet was refused"; }
grep -q "warning: this node isn't enrolled: jarvis-agent starts in setup mode" "$T/out" || { cat "$T/out"; fail "no setup-mode warning over ethernet"; }
mkdir -p "$R6/home/pi/.config/jarvis-agent"
echo '{}' >"$R6/home/pi/.config/jarvis-agent/node.json"
FAKE_SSH_DEV=wlan0 S --root "$R6" --from-dir "$T/dist" --activate >"$T/out" 2>&1 ||
  { cat "$T/out"; fail "--activate of an enrolled node over wlan0 was refused"; }
grep -q "isn't enrolled" "$T/out" && { cat "$T/out"; fail "warned on an enrolled node"; }
grep -q "enable --now jarvis-agent" "$R6/install-pi-commands.log" || fail "an enrolled node was not activated"

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
T3=$T/badmodel
mkdir -p "$T3"
cp "$T/dist"/* "$T3/"
printf 'x' >>"$T3/hey_jarvis_v0.1.onnx"
if R --root "$T/root-badmodel" --from-dir "$T3" >"$T/out" 2>&1; then cat "$T/out"; fail "installed a tampered wake model"; fi
grep -q "checksum mismatch for the wake model" "$T/out" || { cat "$T/out"; fail "refused the tampered wake model for another reason"; }
[ ! -e "$T/root-badmodel/usr/local/share/jarvis-agent/openwakeword/hey_jarvis_v0.1.onnx" ] || fail "the refused install left the tampered wake model"
if R --root "$R3" --from-dir "$T/dist" --version v1.0.0 >"$T/out" 2>&1; then fail "installed another version than asked"; fi
R --root "$R3" --nope >/dev/null 2>&1 && fail "accepted an unknown option"

echo "--- the net watchdog leaves wlan0 alone while the provisioning hotspot is up"
W=$T/wd
mkdir -p "$W/bin" "$W/sys/wlan0"
# Stand-ins that log their calls; nmcli lists the hotspot as active.
for c in nmcli ip ping iw; do
  cat >"$W/bin/$c" <<EOF
#!/bin/sh
echo "$c \$*" >>"$W/calls"
[ "$c \$*" = "nmcli -t -f NAME connection show --active" ] && echo jarvis-setup-ap
exit 0
EOF
  chmod 0755 "$W/bin/$c"
done
printf 'fail=5\nonline=1\n' >"$W/state"
env -i PATH="$W/bin:/usr/bin:/bin" NETWD_SYSFS_NET="$W/sys" NETWD_STATE="$W/state" bash "$WATCHDOG" >"$T/out" 2>&1 ||
  { cat "$T/out"; fail "the watchdog failed with the hotspot up"; }
[ "$(cat "$W/state")" = "$(printf 'fail=0\nonline=1')" ] || fail "the watchdog counted the hotspot as a LAN failure: $(cat "$W/state")"
[ "$(cat "$W/calls")" = "nmcli -t -f NAME connection show --active" ] || fail "the watchdog touched wlan0 with the hotspot up: $(cat "$W/calls")"
echo "install-pi-check: PASS"
