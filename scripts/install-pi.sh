#!/bin/sh
# Install the Go voice node (jarvis-agent --profile pi) on a Raspberry Pi with the ReSpeaker
# 2-mics HAT v2.0, next to or instead of the Python node (go-pi-node.md §6, §7 phase 4.3).
#
#   sudo sh install-pi.sh [--activate]
#
# It downloads the release's linux-arm64 agent archive, checks it against SHA256SUMS (and
# SHA256SUMS.minisig when minisign is installed), puts the agent in /opt/jarvis-agent (the node
# user's, so the agent updates itself there) and the openWakeWord models in
# /usr/local/share/jarvis-agent (pinned by SHA-256, so the node never downloads them), and does
# the system setup the Python node's install.sh does: the packages below, the overlay,
# config.txt, ALSA (dsnoopmic and output), PulseAudio instead of PipeWire with the HAT as a fixed
# sink, the mixer baseline, groups and linger, sudoers, journald, swappiness, swap, Wi-Fi
# power-save off and the NetworkManager profiles, hostapd/dnsmasq masked, the network watchdog,
# the /etc/jarvis-node marker and the jarvis-agent.service unit. Every step can run again.
#
# Only one node may own the HAT. The unit says Conflicts=jarvis-node.service (starting one stops
# the other) and is installed disabled; --activate makes the Go node the one that runs at boot.
# A change to config.txt or the overlay takes effect after a reboot.
#
# Options:
#   --version vX.Y.Z  install that release (default: the release this script came from, or
#                     the latest for a copy from the repository)
#   --base-url URL    fetch the release files from URL or a local directory instead of GitHub
#   --from-dir DIR    the same, for a directory (also JARVIS_AGENT_RELEASE_BASE)
#   --force           reinstall the agent even when this version is installed
#   --user NAME       the account the node runs as (default pi; it must exist)
#   --activate        stop and disable jarvis-node, enable and start jarvis-agent
#   --deactivate      the reverse (only that: nothing is installed or removed)
#   --root DIR        write every file under DIR and run no system command, logging each one to
#                     DIR/install-pi-commands.log instead (tests; also JARVIS_PI_ROOT)
set -eu

REPO=alexberardi/jarvis-server
BASE=${JARVIS_AGENT_RELEASE_BASE:-}
# The project's minisign key, exactly jarvisd's own trust root (internal/update/key.go
# ProjectPublicKey; a unit test keeps them equal). Not overridable.
PUBKEY=RWRyW6ICtU+iyX4p4RnS24ju0gRsWpxvv6B8pI9G+ZS01q8t8oupAQ8L
# The release this copy was published with (release.yml writes the tag here); empty in the
# repository (the latest). --version and --base-url win.
RELEASE_VERSION=""
ROOT=${JARVIS_PI_ROOT:-}
VERSION="" FORCE=0 NODE_USER=pi ACTION=install
ARCH=arm64
OPT=/opt/jarvis-agent
SHARE=/usr/local/share/jarvis-agent
UNIT=jarvis-agent.service
PY_UNIT=jarvis-node.service
CARD=seeed2micvoicec
OVERLAY=respeaker-2mic-v2_0-overlay
NEEDS_REBOOT=0
# The wake-word models (the unit's --wake-models), exactly internal/agent/voice/models.go's
# WakeModels and owwRelease (a unit test keeps them equal): name and SHA-256.
MODELS=$SHARE/openwakeword
OWW_RELEASE=https://github.com/dscripka/openWakeWord/releases/download/v0.5.1
WAKE_MODELS="melspectrogram.onnx ba2b0e0f8b7b875369a2c89cb13360ff53bac436f2895cced9f479fa65eb176f
embedding_model.onnx 70d164290c1d095d1d4ee149bc5e00543250a7316b59f31d056cff7bd3075c1f
hey_jarvis_v0.1.onnx 94a13cfe60075b132f6a472e7e462e8123ee70861bc3fb58434a73712ee0d2cb"
WIFI=wlan0

say() { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case $1 in
    --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION=$2; shift ;;
    --version=*) VERSION=${1#*=} ;;
    --base-url | --from-dir) [ $# -ge 2 ] || die "$1 needs a value"; BASE=$2; shift ;;
    --base-url=* | --from-dir=*) BASE=${1#*=} ;;
    --force) FORCE=1 ;;
    --user) [ $# -ge 2 ] || die "--user needs a value"; NODE_USER=$2; shift ;;
    --user=*) NODE_USER=${1#*=} ;;
    --activate) ACTION=activate ;;
    --deactivate) ACTION=deactivate ;;
    --root) [ $# -ge 2 ] || die "--root needs a value"; ROOT=$2; shift ;;
    --root=*) ROOT=${1#*=} ;;
    -h | --help) sed -n '2,31p' "$0" 2>/dev/null || true; exit 0 ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
  shift
done
BASE=${BASE%/}
ROOT=${ROOT%/}
[ -n "$VERSION" ] || [ -n "$BASE" ] || VERSION=$RELEASE_VERSION

# P PATH: where PATH is written (under --root when set).
P() { printf '%s%s' "$ROOT" "$1"; }
# run CMD...: a command that changes the live system; under --root it is only logged.
run() {
  if [ -n "$ROOT" ]; then printf '%s\n' "$*" >>"$ROOT/install-pi-commands.log"; else "$@"; fi
}
# as_node CMD...: CMD as the node's user (under --root, as whoever runs it).
as_node() {
  if [ -n "$ROOT" ]; then "$@"; else runuser -u "$NODE_USER" -- "$@"; fi
}
# put PATH MODE: write stdin to PATH (under --root) with MODE, through a temp file and a rename.
put() {
  mkdir -p "$(dirname "$(P "$1")")"
  cat >"$(P "$1").tmp"
  chmod "$2" "$(P "$1").tmp"
  mv -f "$(P "$1").tmp" "$(P "$1")"
}
# update PATH MODE: put, and a reboot note when the content changed (PulseAudio and udev read
# these at start).
update() {
  old=$(cat "$(P "$1")" 2>/dev/null || true)
  put "$1" "$2"
  [ "$(cat "$(P "$1")")" = "$old" ] || NEEDS_REBOOT=1
}

preflight() {
  if [ -n "$ROOT" ]; then
    mkdir -p "$ROOT"
    ROOT=$(cd "$ROOT" && pwd)
    : >"$ROOT/install-pi-commands.log"
    NODE_UID=1000 NODE_HOME=/home/$NODE_USER
    return
  fi
  [ "$(uname -s)" = Linux ] || die "this installs a Raspberry Pi node (Linux); got $(uname -s)"
  [ "$(id -u)" -eq 0 ] || die "run it as root: sudo sh $0"
  case $(uname -m) in aarch64 | arm64) ;; *) die "the Go node needs 64-bit Raspberry Pi OS (arm64); got $(uname -m)" ;; esac
  id -u "$NODE_USER" >/dev/null 2>&1 || die "there is no user $NODE_USER (Pi Imager creates it; or pass --user)"
  NODE_UID=$(id -u "$NODE_USER")
  NODE_HOME=$(getent passwd "$NODE_USER" | cut -d: -f6)
}

# What this install needs from apt that Raspberry Pi OS may lack (N:install.sh:206-245):
# PulseAudio (§8 Q2) with its Bluetooth modules, BlueZ and rfkill, iw, and hostapd/dnsmasq,
# the provisioning AP's fallback when the NetworkManager AP fails (§8 Q7). The overlay
# compiler and the ALSA tools too.
install_packages() {
  missing=""
  for pkg in pulseaudio pulseaudio-module-bluetooth bluez rfkill iw hostapd dnsmasq device-tree-compiler alsa-utils; do
    if [ -n "$ROOT" ] || ! dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null | grep -q "install ok installed"; then
      missing="$missing $pkg"
    fi
  done
  [ -n "$missing" ] || return 0
  say "Installing$missing..."
  # shellcheck disable=SC2086 # missing is words
  { run apt-get update -q && run env DEBIAN_FRONTEND=noninteractive apt-get install -y -q $missing; } >/dev/null ||
    warn "apt-get could not install$missing"
}

# --- The agent ---------------------------------------------------------------------------------

install_agent() {
  agent=$(P "$OPT/jarvis-agent")
  # $OPT belongs to the node's user, so the agent replaces itself there (its self-update: stage
  # beside it, rename, keep .prev for a rollback). That user can then change anything in it, so
  # root never runs or writes a file there: the installed agent's version and the new copy are
  # the node user's doing (as_node), and the HAT's overlay source lives in $SHARE.
  mkdir -p "$(P "$OPT")"
  run chown "$NODE_USER:" "$OPT"
  TMP=$(mktemp -d "${TMPDIR:-/tmp}/jarvis-pi-install.XXXXXX")
  trap 'rm -rf "$TMP"' EXIT INT TERM
  fetch SHA256SUMS "$TMP/SHA256SUMS" || die "could not download $(url SHA256SUMS)"
  ASSET=$(awk -v p="jarvis-agent-" -v s="-linux-$ARCH.tar.gz" '{f=$2; sub(/^\*/, "", f)}
    substr(f, 1, length(p)) == p && substr(f, length(f)-length(s)+1) == s {print f; exit}' "$TMP/SHA256SUMS")
  [ -n "$ASSET" ] || die "the release has no Jarvis agent for linux-$ARCH"
  REL=${ASSET#jarvis-agent-}; REL=${REL%"-linux-$ARCH.tar.gz"}
  [ -z "$VERSION" ] || [ "$VERSION" = "$REL" ] || die "asked for $VERSION but the release files are for $REL"
  VERSION=$REL

  CUR=""
  [ -x "$agent" ] && CUR=$(as_node "$agent" version 2>/dev/null || echo unknown)
  if [ "$CUR" = "$VERSION" ] && [ $FORCE = 0 ]; then
    say "jarvis-agent $VERSION is already installed in $OPT."
    return
  fi

  # The signature: required when minisign is installed (a missing or bad one is fatal), else
  # checksums only, with a warning. A downloaded binary can't vouch for itself.
  MINISIGN=""
  for m in "$(command -v minisign 2>/dev/null || true)" /usr/local/bin/minisign /usr/bin/minisign; do
    if [ -n "$m" ] && [ -x "$m" ] && "$m" -v >/dev/null 2>&1; then MINISIGN=$m; break; fi
  done
  if [ -n "$MINISIGN" ]; then
    fetch SHA256SUMS.minisig "$TMP/SHA256SUMS.minisig" 2>/dev/null || die "the release has no SHA256SUMS.minisig; not installing an unsigned release"
    "$MINISIGN" -Vq -P "$PUBKEY" -m "$TMP/SHA256SUMS" -x "$TMP/SHA256SUMS.minisig" >/dev/null || die "SHA256SUMS signature is INVALID ($MINISIGN); not installing"
    say "SHA256SUMS signature verified."
  else
    [ "${JARVIS_REQUIRE_SIGNATURE:-0}" = 1 ] && die "JARVIS_REQUIRE_SIGNATURE=1 but minisign is not installed"
    warn "minisign is not installed, so the release signature is not checked (checksums only); apt install minisign for a verified install."
  fi

  say "Downloading jarvis-agent $VERSION for linux-$ARCH..."
  fetch "$ASSET" "$TMP/$ASSET" || die "could not download $(url "$ASSET")"
  want=$(awk -v f="$ASSET" '$2 == f || $2 == "*" f {print $1; exit}' "$TMP/SHA256SUMS")
  [ "$(sha256sum "$TMP/$ASSET" | awk '{print $1}')" = "$want" ] || die "checksum mismatch for $ASSET (corrupt or tampered download)"
  tar -xzf "$TMP/$ASSET" -C "$TMP"
  NEW=$TMP/jarvis-agent-$VERSION-linux-$ARCH/jarvis-agent
  [ "$("$NEW" version)" = "$VERSION" ] || die "the downloaded agent does not run here"

  # A rename, so a running agent keeps its old file until it restarts.
  # shellcheck disable=SC2016 # $1 and $2 are the inner shell's
  as_node sh -c 'cat >"$1" && chmod 0755 "$1" && mv -f "$1" "$2"' sh "$agent.new" "$agent" <"$NEW" ||
    die "could not write $OPT/jarvis-agent as $NODE_USER"
  [ -z "$CUR" ] || run systemctl try-restart "$UNIT"
  say "jarvis-agent $VERSION installed in $OPT."
}

url() { # url ASSET: where a release file lives
  if [ -n "$BASE" ]; then echo "$BASE/$1"
  elif [ -n "$VERSION" ]; then echo "https://github.com/$REPO/releases/download/$VERSION/$1"
  else echo "https://github.com/$REPO/releases/latest/download/$1"; fi
}
fetch() { # fetch ASSET DEST: a URL, or a file in a local directory
  case $BASE in
    "" | http://* | https://* | file://*)
      command -v curl >/dev/null || die "curl is required"
      curl -fsSL --retry 3 --connect-timeout 30 -o "$2" "$(url "$1")" ;;
    *) cp "$BASE/$1" "$2" ;;
  esac
}

# The wake-word models, so the node never fetches them at run time: from --from-dir or --base-url
# when it has them (an offline install), else from openWakeWord's release, each checked against
# its pinned hash. One that is already in place with the right hash is kept.
install_wake_models() {
  mkdir -p "$(P "$MODELS")"
  while read -r name sum; do
    dest=$(P "$MODELS/$name")
    [ -f "$dest" ] && [ "$(sha256sum "$dest" | awk '{print $1}')" = "$sum" ] && continue
    if [ -n "$BASE" ] && fetch "$name" "$TMP/$name" 2>/dev/null; then
      from=$(url "$name")
    else
      from=$OWW_RELEASE/$name
      command -v curl >/dev/null || die "curl is required"
      curl -fsSL --retry 3 --connect-timeout 30 -o "$TMP/$name" "$from" || die "could not download $from"
    fi
    [ "$(sha256sum "$TMP/$name" | awk '{print $1}')" = "$sum" ] || die "checksum mismatch for the wake model $from (corrupt or tampered download)"
    cp "$TMP/$name" "$dest.new"
    chmod 0644 "$dest.new"
    mv -f "$dest.new" "$dest"
    say "Installed the wake model $name."
  done <<EOF
$WAKE_MODELS
EOF
}

# --- The HAT (N:install.sh configure_audio) -----------------------------------------------------

# The overlay, compiled on the Pi from Seeed's DTS (scripts/packaging/pi/, N:install.sh:912-934).
install_overlay() {
  dts=$SHARE/$OVERLAY.dts
  rm -f "$(P "$OPT/$OVERLAY.dts")" # where an older install-pi.sh kept it
  overlays=/boot/firmware/overlays
  if [ ! -d "$(P $overlays)" ] && [ -d "$(P /boot/overlays)" ]; then overlays=/boot/overlays; fi
  overlay_dts | put "$dts" 0644
  if [ -z "$ROOT" ] && ! command -v dtc >/dev/null 2>&1; then
    warn "dtc is not installed (apt install device-tree-compiler): no HAT overlay, so no audio"
    return
  fi
  if [ -z "$ROOT" ] && [ -f "$overlays/$OVERLAY.dtbo" ] &&
    dtc -q -W no-unit_address_vs_reg -I dts -O dtb "$dts" 2>/dev/null | cmp -s - "$overlays/$OVERLAY.dtbo"; then
    return
  fi
  run dtc -q -W no-unit_address_vs_reg -I dts -O dtb -o "$overlays/$OVERLAY.dtbo" "$dts" ||
    { warn "dtc could not compile $dts: no HAT overlay, so no audio"; return; }
  [ -n "$ROOT" ] || NEEDS_REBOOT=1 # under --root dtc only logs, so there is no .dtbo to compare next time
}

# config.txt: the overlay, SPI (the LEDs), I2C (the Grove port), the on-board audio off
# (N:install.sh:936-984).
configure_boot_config() {
  cfg=/boot/firmware/config.txt
  [ -f "$(P $cfg)" ] || cfg=/boot/config.txt
  if [ ! -f "$(P $cfg)" ]; then
    warn "no config.txt in /boot/firmware or /boot: the HAT overlay, SPI and I2C are not enabled"
    return
  fi
  f=$(P $cfg)
  before=$(cksum <"$f")
  sed -i -e 's/^dtparam=audio=on/dtparam=audio=off/' \
    -e 's/^dtoverlay=hifiberry-dac/#dtoverlay=hifiberry-dac # replaced for ReSpeaker v2.0/' \
    -e 's/^dtoverlay=wm8960-soundcard/#dtoverlay=wm8960-soundcard # replaced for ReSpeaker v2.0/' "$f"
  for line in "dtoverlay=$OVERLAY" dtparam=spi=on dtparam=i2c_arm=on; do
    grep -q "^$line" "$f" || printf '%s\n' "$line" >>"$f"
  done
  [ "$(cksum <"$f")" = "$before" ] || { NEEDS_REBOOT=1; say "Updated $cfg."; }
}

# ALSA: the codec first (N:install.sh:991-993), and the device names the node uses
# (N:install.sh:1019-1064, the same file as the Python node's: playback through PulseAudio,
# capture straight from the HAT through dsnoop).
configure_alsa() {
  echo 'options snd-soc-tlv320aic3x index=0' | put /etc/modprobe.d/alsa-base.conf 0644
  put /etc/asound.conf 0644 <<'ASOUND'
# --- Playback -----------------------------------------------------------
pcm.output {
    type pulse
}

# --- Capture ------------------------------------------------------------
pcm.dsnoopmic_hw {
    type dsnoop
    ipc_key 87654321
    slave {
        pcm "hw:CARD=seeed2micvoicec,DEV=0"
        channels 2
        rate 48000
        format S16_LE
        # ~85 ms of ring buffer, so the wake word never scores stale audio (N:install.sh).
        period_size 1024
        buffer_size 4096
    }
}

pcm.dsnoopmic {
    type plug
    slave.pcm "dsnoopmic_hw"
}

# --- Default split: playback via PA, capture direct --------------------
pcm.!default {
    type asym
    playback.pcm "output"
    capture.pcm "dsnoopmic"
}

ctl.!default {
    type pulse
}
ASOUND
}

# PulseAudio instead of PipeWire, for every user (N:install.sh:365-420; go-pi-node.md §8 Q2):
# PipeWire's bluez5 plugin doesn't register A2DP sinks, and asound.conf's "output" is Pulse's.
# A per-user mask on pulseaudio (a debugging leftover) beats the system-wide enable and makes
# every reply silent, so it goes.
switch_to_pulseaudio() {
  if [ -z "$ROOT" ] && [ ! -f /usr/lib/systemd/user/pulseaudio.socket ]; then
    warn "pulseaudio is not installed: the audio stack stays as it is, and the node's replies have no output"
    return
  fi
  etc_user=/etc/systemd/user
  mkdir -p "$(P $etc_user/sockets.target.wants)" "$(P $etc_user/default.target.wants)"
  for unit in pipewire.socket pipewire.service pipewire-pulse.socket pipewire-pulse.service wireplumber.service; do
    [ "$(readlink "$(P $etc_user/$unit)" 2>/dev/null)" = /dev/null ] && continue
    ln -sf /dev/null "$(P $etc_user/$unit)"
    NEEDS_REBOOT=1
  done
  for link in sockets.target.wants/pulseaudio.socket default.target.wants/pulseaudio.service; do
    [ -L "$(P $etc_user/$link)" ] && continue
    ln -sf "/usr/lib/systemd/user/${link#*/}" "$(P $etc_user/$link)"
    NEEDS_REBOOT=1
  done
  for unit in pulseaudio.socket pulseaudio.service; do
    mask=$(P "$NODE_HOME/.config/systemd/user/$unit")
    if [ "$(readlink "$mask" 2>/dev/null)" = /dev/null ]; then
      rm -f "$mask"
      warn "removed a per-user mask on $unit ($NODE_HOME/.config/systemd/user)"
      NEEDS_REBOOT=1
    fi
  done
}

# PulseAudio's drop-ins (/etc/pulse/default.pa includes default.pa.d; .nofail keeps a missing
# module from stopping Pulse):
#   - no suspend on idle (N:install.sh:425-443): the codec doesn't resume a suspended sink
#     reliably, so a reply blocks and the node stops hearing the wake word;
#   - the HAT as the fixed sink jarvis_output, playback only, at 48 kHz (N:install.sh:445-490):
#     udev-detect leaves the card alone (PULSE_IGNORE), so Pulse never takes the capture side
#     the node's dsnoop needs, whatever starts first; the codec runs both directions from one
#     clock, so 44.1 kHz playback would kill the 48 kHz mic;
#   - no restored mute or volume per app (N:install.sh:492-536): a ducked or muted stream
#     would come back that way.
configure_pulseaudio() {
  update /etc/pulse/default.pa.d/99-jarvis-no-suspend.pa 0644 <<'EOF'
.nofail
unload-module module-suspend-on-idle
EOF
  update /etc/udev/rules.d/89-jarvis-hat-pulse-ignore.rules 0644 <<EOF
SUBSYSTEM=="sound", KERNEL=="card*", ATTR{id}=="$CARD", ENV{PULSE_IGNORE}="1"
EOF
  run udevadm control --reload 2>/dev/null || true
  update /etc/pulse/default.pa.d/97-jarvis-hat-sink.pa 0644 <<EOF
.nofail
load-module module-alsa-sink device=hw:CARD=$CARD,DEV=0 sink_name=jarvis_output rate=48000 tsched=0
set-default-sink jarvis_output
EOF
  update /etc/pulse/default.pa.d/98-jarvis-stream-restore.pa 0644 <<'EOF'
.nofail
unload-module module-stream-restore
load-module module-stream-restore restore_device=false restore_muted=false restore_volume=false
EOF
}

# Bluetooth (go-pi-node.md §7 item 5.8; N:install.sh:283-347): the radio unblocked (Raspberry
# Pi OS ships it soft-blocked, so bluetoothd can't go discoverable and a phone never sees the
# node), and an audio Class of Device (0x240404), without which iOS lists the node as a
# computer and won't send it A2DP. The node's user is in group bluetooth (setup_user), which
# bluetoothd's D-Bus policy lets in.
configure_bluetooth() {
  restart=""
  if [ -n "$ROOT" ] || rfkill list bluetooth 2>/dev/null | grep -q "Soft blocked: yes"; then
    run rfkill unblock bluetooth
    restart=1
  fi
  conf=$(P /etc/bluetooth/main.conf)
  if [ -f "$conf" ] && ! grep -q '^Class = 0x240404$' "$conf"; then
    if grep -q '^#*Class = ' "$conf"; then
      sed -i 's/^#*Class = .*/Class = 0x240404/' "$conf"
    else
      sed -i '/^\[General\]/a Class = 0x240404' "$conf"
    fi
    restart=1
  fi
  [ -z "$restart" ] || run systemctl restart bluetooth
}

card_present() { aplay -l 2>/dev/null | grep -qi "$CARD"; }

# The TLV320AIC3104 mixer baseline, stored with alsactl (N:install.sh:1068-1167): the codec's
# defaults are inaudible. The overlay is loaded live first when it can be, so this applies now
# rather than after the reboot.
mixer_baseline() {
  if [ -z "$ROOT" ] && ! card_present && command -v dtoverlay >/dev/null 2>&1 && dtoverlay "$OVERLAY" 2>/dev/null; then
    for _ in 1 2 3 4 5 6 7 8 9 10; do card_present && break; sleep 0.5; done
  fi
  if [ -z "$ROOT" ] && { ! command -v amixer >/dev/null 2>&1 || ! card_present; }; then
    warn "the $CARD card is not up yet (it is after the reboot): run this again then for the mixer baseline"
    return
  fi
  while IFS='|' read -r ctl val; do
    # shellcheck disable=SC2086 # val is words
    run amixer -q -c "$CARD" sset "$ctl" $val </dev/null 2>/dev/null || warn "amixer: no control '$ctl'"
  done <<'MIXER'
PCM|100% unmute
Line|2 unmute
Line DAC|115 unmute
Left Line Mixer DACL1|on
Right Line Mixer DACR1|on
HP|0 mute
HP DAC|0 mute
HPCOM|0 mute
HPCOM DAC|0 mute
PGA|49%
Left PGA Mixer Line1L|on
Right PGA Mixer Line1R|on
MIXER
  run amixer -q -c "$CARD" cset "name=ADC HPF Cut-off" 1 2>/dev/null || warn "amixer: no control 'ADC HPF Cut-off'"
  run alsactl store 2>/dev/null || warn "alsactl store failed: the mixer baseline won't survive a reboot"
}

# --- The account and the system -----------------------------------------------------------------

# The devices' groups (N:install.sh:1174-1178,1616-1618) and linger, so /run/user/<uid> (Pulse)
# exists at boot before anyone logs in (N:install.sh:1620).
setup_user() {
  for grp in audio spi gpio i2c bluetooth; do
    if [ -n "$ROOT" ] || getent group "$grp" >/dev/null 2>&1; then
      run usermod -aG "$grp" "$NODE_USER" || warn "could not add $NODE_USER to $grp"
    fi
  done
  run loginctl enable-linger "$NODE_USER" || warn "loginctl enable-linger $NODE_USER failed: Pulse may not be up when the node starts"
}

# What the agent may run as root, checked with visudo before it goes in place
# (N:install.sh:1819-1862, cut to what the Go node runs today).
install_sudoers() {
  dest=/etc/sudoers.d/jarvis-agent
  mkdir -p "$(P /etc/sudoers.d)"
  tmp=$(P "$dest").tmp
  cat >"$tmp" <<EOF
# Written by jarvis-server scripts/install-pi.sh; do not edit by hand.
# What the Go node (jarvis-agent.service, User=$NODE_USER) may run as root.
# The HAT button (3 s held) and the admin's reboot.
$NODE_USER ALL=(root) NOPASSWD: /usr/bin/systemctl poweroff, /usr/bin/systemctl reboot
$NODE_USER ALL=(root) NOPASSWD: /sbin/poweroff, /usr/sbin/poweroff, /sbin/reboot, /usr/sbin/reboot
$NODE_USER ALL=(root) NOPASSWD: /usr/bin/systemctl restart jarvis-agent, /usr/bin/systemctl restart jarvis-agent.service
# A crash loop (a self-update's broken release) hits the unit's start limit; the rollback's restart needs it reset.
$NODE_USER ALL=(root) NOPASSWD: /usr/bin/systemctl reset-failed jarvis-agent, /usr/bin/systemctl reset-failed jarvis-agent.service
# Not yet, added with the step that needs them (go-pi-node.md §7):
# TODO(phase 4.4, provisioning AP): NetworkManager, nmcli (and hostapd/dnsmasq only if the NM AP fails).
# TODO(parity 5.6, packages): the apt and post-install wrappers.
# TODO(the mixer self-heal port, N:utils/audio_volume.py): jarvis-alsa-store.
EOF
  chmod 0440 "$tmp"
  if [ -z "$ROOT" ] && ! visudo -cf "$tmp" >/dev/null 2>&1; then
    rm -f "$tmp"
    warn "$dest failed visudo's check; not installed (the button can't power off)"
    return
  fi
  mv -f "$tmp" "$(P $dest)"
}

# journald kept across reboots, capped (N:install.sh:2090-2114).
configure_journald() {
  mkdir -p "$(P /var/log/journal)"
  put /etc/systemd/journald.conf.d/jarvis.conf 0644 <<'EOF'
# Jarvis node: a persistent, size-capped journal, so a wedge or a network drop can still be read
# after the reboot.
[Journal]
Storage=persistent
SystemMaxUse=200M
SystemKeepFree=100M
SystemMaxFileSize=20M
EOF
  run systemd-tmpfiles --create --prefix /var/log/journal || true
  run systemctl restart systemd-journald || true
  run journalctl --flush || true
}

# Keep the working set in RAM; 1, not 0, keeps swap as the OOM safety valve (N:install.sh:885-901).
configure_swappiness() {
  put /etc/sysctl.d/99-jarvis-node.conf 0644 <<'EOF'
# Keep the jarvis node's working set resident in RAM: SD-card swap is slow and causes 7-8 s wakes
# after idle. 1 (not 0) keeps swap as a last-resort OOM safety valve.
vm.swappiness=1
EOF
  run sysctl -q -w vm.swappiness=1 || true
}

# 1 GB of swap when there is none: a Zero 2 W has 512 MB (N:install.sh:140-153).
configure_swap() {
  if [ -z "$ROOT" ] && [ -n "$(swapon --show --noheadings 2>/dev/null)" ]; then return; fi
  if [ ! -f "$(P /swapfile)" ]; then
    run fallocate -l 1G /swapfile
    run chmod 600 /swapfile
    run mkswap /swapfile
  fi
  run swapon /swapfile || warn "swapon /swapfile failed"
  touch "$(P /etc/fstab)"
  grep -q '^/swapfile ' "$(P /etc/fstab)" || echo '/swapfile none swap sw 0 0' >>"$(P /etc/fstab)"
}

# The Pi-profile marker (cmd/jarvis-agent/profile.go piMarker). The Python node keeps
# audio_config.json in it, so it is left alone when it exists.
write_marker() {
  [ -e "$(P /etc/jarvis-node)" ] || mkdir -m 0755 "$(P /etc/jarvis-node)"
}

# The unit (N:setup/jarvis-node.service, N:install.sh:1865-1958 with the Go ExecStart). The
# groups come from the account (setup_user): SupplementaryGroups= fails the unit when one of
# them doesn't exist.
install_unit() {
  put "/etc/systemd/system/$UNIT" 0644 <<EOF
# Written by jarvis-server scripts/install-pi.sh; do not edit by hand.
[Unit]
Description=Jarvis voice node (Go)
After=network-online.target sound.target
Wants=network-online.target
# One node owns the HAT: starting this stops the Python node, and starting it stops this.
Conflicts=$PY_UNIT
StartLimitBurst=10
StartLimitIntervalSec=1800

[Service]
User=$NODE_USER
Group=$NODE_USER
ExecStart=$OPT/jarvis-agent run --profile pi --voice --voice-input dsnoopmic --voice-output output --wake-models $MODELS
WorkingDirectory=$NODE_HOME
Restart=always
RestartSec=5
TimeoutStopSec=10
Environment=HOME=$NODE_HOME
Environment=XDG_RUNTIME_DIR=/run/user/$NODE_UID
SyslogIdentifier=jarvis-agent

[Install]
WantedBy=multi-user.target
EOF
  run systemctl daemon-reload
}

# --- The network --------------------------------------------------------------------------------

# hostapd and dnsmasq are the provisioning AP's fallback, started by the node itself, never by
# systemd (N:install.sh:247-252).
mask_ap_services() {
  run systemctl stop hostapd dnsmasq 2>/dev/null || true
  run systemctl disable hostapd dnsmasq 2>/dev/null || true
  run systemctl mask hostapd dnsmasq 2>/dev/null || true
}

# wlan0's NetworkManager connection, or nothing (nmcli only reads here).
wifi_connection() {
  command -v nmcli >/dev/null 2>&1 || return 0
  nmcli -t -f GENERAL.CONNECTION device show "$WIFI" 2>/dev/null | cut -d: -f2-
}

# Wi-Fi power-save off (N:install.sh:1194-1227): in the active connection, which lasts, and in
# the driver now, since brcmfmac doesn't always get NetworkManager's setting. When wlan0 reports
# no connection, the first active Wi-Fi one (as N:; the reconcile below skips instead). The watchdog
# enforces it every minute after that.
wifi_powersave_off() {
  con=$(wifi_connection)
  [ -n "$con" ] || con=$(nmcli -t -f NAME,TYPE connection show --active 2>/dev/null | awk -F: '$2 == "802-11-wireless" {print $1; exit}')
  if [ -n "$con" ]; then
    run nmcli connection modify "$con" wifi.powersave 2 2>/dev/null || true
    run nmcli device reapply "$WIFI" >/dev/null 2>&1 || true
  elif command -v nmcli >/dev/null 2>&1; then
    warn "$WIFI has no NetworkManager connection: the network watchdog keeps its power-save off"
  fi
  if [ -n "$ROOT" ] || command -v iw >/dev/null 2>&1; then
    run iw dev "$WIFI" set power_save off >/dev/null 2>&1 || true
  fi
}

# One NetworkManager profile per SSID (N:install.sh:2148-2191): two autoconnecting profiles for
# the SSID wlan0 is on (a provisioning one and a netplan one) make NM thrash between them after a
# drop and strand the node (the 2026-06 kitchen incident). The one in use gets autoconnect and
# priority 999 (which the watchdog brings up first), the others stop autoconnecting. Nothing
# changes when wlan0 reports no connection.
reconcile_wifi_profiles() {
  con=$(wifi_connection)
  [ -n "$con" ] || return 0
  ssid=$(nmcli -t -g 802-11-wireless.ssid connection show "$con" 2>/dev/null)
  [ -n "$ssid" ] || return 0
  nmcli -t -f NAME,TYPE connection show 2>/dev/null | awk -F: '$2 == "802-11-wireless" {print $1}' >"$TMP/wifi-profiles"
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    [ "$(nmcli -t -g 802-11-wireless.ssid connection show "$name" 2>/dev/null)" = "$ssid" ] || continue
    if [ "$name" = "$con" ]; then
      run nmcli connection modify "$name" connection.autoconnect yes connection.autoconnect-priority 999 2>/dev/null || true
    else
      run nmcli connection modify "$name" connection.autoconnect no 2>/dev/null || true
      say "The Wi-Fi profile '$name' no longer autoconnects: '$con' is the one for $ssid."
    fi
  done <"$TMP/wifi-profiles"
}

# The network watchdog (N:install.sh:2116-2146): a root timer that keeps wlan0's power-save off
# and brings a node that fell off the LAN back (reapply, then a link bounce, then a reboot, at
# most 3 an hour and only after it was online this boot). The script is N:'s, byte for byte.
install_net_watchdog() {
  net_watchdog | put /usr/local/sbin/jarvis-net-watchdog 0755
  put /etc/systemd/system/jarvis-net-watchdog.service 0644 <<'EOF'
# Written by jarvis-server scripts/install-pi.sh; do not edit by hand.
[Unit]
Description=Jarvis node network self-heal (recovers wlan0 from power-save / association loss)
# Run after the network stack is up; the watchdog tolerates "no network yet"
# on its own (it only escalates after FAIL_* consecutive checks and never
# reboots until it has actually been online this boot).
After=network.target NetworkManager.service
Wants=network.target

[Service]
Type=oneshot
User=root
# One check per invocation; all run-state lives in tmpfs (resets on reboot,
# which is the boot-loop guard). See /usr/local/sbin/jarvis-net-watchdog for the
# escalation ladder and why each threshold is where it is.
ExecStart=/usr/local/sbin/jarvis-net-watchdog
# Tunables (consecutive ~60s checks before each escalation step):
Environment=NETWD_IFACE=wlan0
Environment=NETWD_FAIL_REAPPLY=3
Environment=NETWD_FAIL_BOUNCE=4
Environment=NETWD_FAIL_REBOOT=6
# Don't reboot in the first MIN_UPTIME seconds even if it looks down — gives
# WiFi/NetworkManager time to associate on a cold boot.
Environment=NETWD_MIN_UPTIME=120
# Persistent reboot-rate cap so a flapping link can't reboot-loop the node:
# at most NETWD_REBOOT_MAX reboots per NETWD_REBOOT_WINDOW seconds.
Environment=NETWD_REBOOT_MAX=3
Environment=NETWD_REBOOT_WINDOW=3600
SyslogIdentifier=jarvis-net-watchdog
StandardOutput=journal
StandardError=journal
EOF
  put /etc/systemd/system/jarvis-net-watchdog.timer 0644 <<'EOF'
# Written by jarvis-server scripts/install-pi.sh; do not edit by hand.
[Unit]
Description=Run the Jarvis node network self-heal every 60s

[Timer]
# First check 90s after boot (lets NetworkManager associate), then every 60s.
OnBootSec=90
OnUnitActiveSec=60
AccuracySec=10s
# Liveness watching, not a missed-job catch-up.
Persistent=false
Unit=jarvis-net-watchdog.service

[Install]
WantedBy=timers.target
EOF
  run systemctl daemon-reload
  run systemctl enable --now jarvis-net-watchdog.timer >/dev/null 2>&1 || warn "could not enable jarvis-net-watchdog.timer"
}

# --- Which node runs ----------------------------------------------------------------------------

activate() {
  run systemctl disable --now "$PY_UNIT" 2>/dev/null || true
  run systemctl enable --now "$UNIT"
  say "The Go node runs now and at boot; the Python node is stopped and disabled."
}

deactivate() {
  run systemctl disable --now "$UNIT" || true
  if [ -n "$ROOT" ] || [ -f "/etc/systemd/system/$PY_UNIT" ]; then
    run systemctl enable --now "$PY_UNIT"
    say "The Python node runs now and at boot; the Go node is stopped and disabled."
  else
    say "The Go node is stopped and disabled (there is no Python node to start)."
  fi
}

# The network watchdog, scripts/packaging/pi/jarvis-net-watchdog byte for byte (N:scripts/;
# scripts/install-pi-check.sh compares them).
net_watchdog() {
  cat <<'WATCHDOG'
#!/bin/bash
# jarvis-net-watchdog — cause-agnostic network self-heal for Pi nodes.
#
# Runs as a root oneshot every ~60s (jarvis-net-watchdog.timer). One check
# per invocation; all state lives in a tmpfs file so it resets cleanly on
# reboot (that reset IS the boot-loop guard — see below).
#
# WHY THIS EXISTS (2026-06 kitchen-node incident):
#   A Pi node went unreachable — no SSH, no ping, no mDNS — while the CPU was
#   still running (LEDs on, wake word still fired locally). That is the classic
#   "wlan0 dropped off the network but the box is alive" failure: WiFi power-
#   save after idle, a brcmfmac association loss, or a NetworkManager/netplan
#   profile that didn't enforce power-save on the live interface. install.sh
#   *tries* to disable power-save at setup, but that fix is one-shot and
#   silently no-ops on some stacks (iwconfig absent, netplan-managed
#   connection, wrong profile matched). Nothing then RECOVERED the node, and
#   the app-layer post-install watchdog only ever checked jarvis-node.service —
#   never the network — so a node that was "off the network but running" was
#   invisible to it. This watchdog closes that gap regardless of root cause,
#   and also re-fills the connectivity-watchdog role removed in v0.1.69.
#
# WHAT IT DOES, each run:
#   1. Idempotently force WiFi power-save OFF on the live interface (the
#      enforcement install.sh's config-layer setting can't guarantee).
#   2. Probe LAN reachability (ping the default gateway).
#   3. On sustained loss, escalate: reapply -> bounce -> reboot (last resort).
#
# ESCALATION (counts are consecutive failed checks, ~60s apart):
#   >= FAIL_REAPPLY (3, ~3 min): nmcli device reapply  (re-push NM config)
#   >= FAIL_BOUNCE  (4, ~4 min): ip link down/up + reapply  (re-associate)
#   >= FAIL_REBOOT  (6, ~6 min): reboot — ONLY if we were online earlier this
#                                boot (online==1), uptime >= MIN_UPTIME, AND we
#                                are under the persistent reboot-rate limit.
#                                (fail 5 keeps bouncing, giving DHCP/assoc ~2
#                                min after the bounce before we resort to reboot.)
#   TWO reboot guards:
#     1. online==1 (tmpfs): never reboot on a boot where we never reached the
#        network — a node that simply never gets WiFi keeps retrying forever.
#     2. REBOOT_MAX in REBOOT_WINDOW (PERSISTENT, survives reboot): the tmpfs
#        flag re-arms every boot, so on a flapping link guard #1 alone would
#        reboot every ~10 min indefinitely. This caps reboots to REBOOT_MAX per
#        REBOOT_WINDOW; past that we keep bouncing but stop rebooting.

set -u

IFACE="${NETWD_IFACE:-wlan0}"
FAIL_REAPPLY="${NETWD_FAIL_REAPPLY:-3}"
FAIL_BOUNCE="${NETWD_FAIL_BOUNCE:-4}"
FAIL_REBOOT="${NETWD_FAIL_REBOOT:-6}"
MIN_UPTIME="${NETWD_MIN_UPTIME:-120}"
PING_WAIT="${NETWD_PING_WAIT:-2}"
STATE="${NETWD_STATE:-/run/jarvis-net-watchdog.state}"
BREADCRUMB="${NETWD_BREADCRUMB:-/var/log/jarvis-net-watchdog.json}"
# Persistent reboot-rate limit (survives reboot — this is the loop guard the
# tmpfs state can't provide): at most REBOOT_MAX reboots per REBOOT_WINDOW sec.
REBOOT_LOG="${NETWD_REBOOT_LOG:-/var/lib/jarvis-net-watchdog/reboots}"
REBOOT_MAX="${NETWD_REBOOT_MAX:-3}"
REBOOT_WINDOW="${NETWD_REBOOT_WINDOW:-3600}"
# Paths below are overridable only so the test suite can drive the script on a
# non-Pi host; on a real node they resolve to the kernel's standard locations.
SYSFS_NET="${NETWD_SYSFS_NET:-/sys/class/net}"
UPTIME_FILE="${NETWD_UPTIME_FILE:-/proc/uptime}"

log() { printf '[net-watchdog] %s\n' "$*"; }

# --- Skip cleanly when there's nothing to watch (e.g. ethernet-only, container).
if [ ! -e "${SYSFS_NET}/${IFACE}" ]; then
    log "interface ${IFACE} not present — nothing to watch"
    exit 0
fi

# --- Read prior state (fail count + whether we've ever been online this boot).
fail=0
online=0
if [ -r "$STATE" ]; then
    # shellcheck disable=SC1090
    . "$STATE" 2>/dev/null || true
    fail="${fail:-0}"
    online="${online:-0}"
fi
case "$fail" in *[!0-9]*) fail=0 ;; esac
case "$online" in *[!0-9]*) online=0 ;; esac

write_state() {
    # tmpfs file; key=val so it can be sourced back next run.
    printf 'fail=%s\nonline=%s\n' "$1" "$2" > "$STATE" 2>/dev/null || true
}

# --- (1) Enforce power-save OFF on the live interface. iw is the driver-level
#     control install.sh now installs; nmcli reapply alone does not always push
#     powersave to brcmfmac. Cheap + idempotent, so do it every run.
if command -v iw >/dev/null 2>&1; then
    iw dev "$IFACE" set power_save off >/dev/null 2>&1 || true
fi

# --- (2) Probe LAN reachability via the default gateway on this interface.
gw="$(ip route show default 0.0.0.0/0 dev "$IFACE" 2>/dev/null | awk '/default/{print $3; exit}')"
[ -z "$gw" ] && gw="$(ip route show default 2>/dev/null | awk '/default/{print $3; exit}')"

healthy=0
if [ -n "$gw" ] && ping -c1 -W "$PING_WAIT" "$gw" >/dev/null 2>&1; then
    healthy=1
fi

if [ "$healthy" -eq 1 ]; then
    if [ "$fail" -ne 0 ]; then
        log "LAN recovered (gw=${gw}) after ${fail} failed check(s)"
    fi
    write_state 0 1
    exit 0
fi

# --- (3) Unhealthy: increment and escalate.
fail=$((fail + 1))
write_state "$fail" "$online"
log "no LAN on ${IFACE} (gw='${gw:-none}') consecutive_fail=${fail}"

uptime_s="$(cut -d. -f1 "$UPTIME_FILE" 2>/dev/null || echo 0)"
case "$uptime_s" in *[!0-9]*) uptime_s=0 ;; esac

if [ "$fail" -ge "$FAIL_REBOOT" ] && [ "$online" -eq 1 ] && [ "$uptime_s" -ge "$MIN_UPTIME" ]; then
    # Persistent cross-reboot rate limit. The tmpfs `online` flag re-arms on
    # every boot, so without this a node on a flapping link reboots forever.
    # Count reboots recorded within REBOOT_WINDOW; if we're at the cap, do NOT
    # reboot — fall through and keep bouncing instead.
    now_epoch="$(date +%s 2>/dev/null || echo 0)"
    case "$now_epoch" in *[!0-9]*) now_epoch=0 ;; esac
    recent=0
    if [ -r "$REBOOT_LOG" ]; then
        tmpf="${REBOOT_LOG}.$$"
        : > "$tmpf" 2>/dev/null || true
        while read -r ts; do
            case "$ts" in '' | *[!0-9]*) continue ;; esac
            # now_epoch==0 means the clock is unreadable — treat every prior
            # reboot as "recent" (fail safe toward NOT rebooting).
            if [ "$now_epoch" -eq 0 ] || [ $((now_epoch - ts)) -lt "$REBOOT_WINDOW" ]; then
                printf '%s\n' "$ts" >> "$tmpf" 2>/dev/null || true
                recent=$((recent + 1))
            fi
        done < "$REBOOT_LOG"
        mv "$tmpf" "$REBOOT_LOG" 2>/dev/null || rm -f "$tmpf" 2>/dev/null || true
    fi

    if [ "$recent" -ge "$REBOOT_MAX" ]; then
        log "reboot rate-limited: ${recent} reboot(s) in the last ${REBOOT_WINDOW}s — NOT rebooting; continuing bounce/reapply"
    else
        log "LAN down for ${fail} checks, online this boot — rebooting (last resort; ${recent} prior in window)"
        mkdir -p "$(dirname "$REBOOT_LOG")" 2>/dev/null || true
        printf '%s\n' "$now_epoch" >> "$REBOOT_LOG" 2>/dev/null || true
        now="$(date -Iseconds 2>/dev/null || date 2>/dev/null || echo unknown)"
        mkdir -p "$(dirname "$BREADCRUMB")" 2>/dev/null || true
        cat > "$BREADCRUMB" 2>/dev/null <<JSON || true
{"action":"reboot","reason":"lan_down_after_online","iface":"${IFACE}","gateway":"${gw:-none}","consecutive_fail":${fail},"uptime_s":${uptime_s},"reboots_in_window":${recent},"timestamp":"${now}"}
JSON
        sync 2>/dev/null || true
        systemctl reboot 2>/dev/null || reboot 2>/dev/null || true
        exit 0
    fi
fi

# Preferred wifi profile = the highest autoconnect-priority 802-11-wireless
# connection (install.sh pins the known-good one to 999). Forcing it up
# recovers — WITHOUT a reboot — from a duplicate-same-SSID profile race where
# NM latched onto a profile that can't authenticate (the 2026-06 root cause).
pref="$(nmcli -t -f NAME,TYPE connection show 2>/dev/null \
    | awk -F: '$2=="802-11-wireless"{print $1}' \
    | while IFS= read -r n; do
        printf '%s\t%s\n' "$(nmcli -t -g connection.autoconnect-priority connection show "$n" 2>/dev/null || echo 0)" "$n"
      done | sort -rn | head -1 | cut -f2-)"

if [ "$fail" -ge "$FAIL_BOUNCE" ]; then
    log "bouncing ${IFACE} (link down/up + reapply + force preferred profile)"
    ip link set "$IFACE" down 2>/dev/null || true
    sleep 2
    ip link set "$IFACE" up 2>/dev/null || true
    nmcli device reapply "$IFACE" >/dev/null 2>&1 || true
    if [ -n "$pref" ]; then
        nmcli connection up "$pref" >/dev/null 2>&1 || true
    fi
    exit 0
fi

if [ "$fail" -ge "$FAIL_REAPPLY" ]; then
    log "reapplying NM config + forcing preferred profile on ${IFACE}"
    nmcli device reapply "$IFACE" >/dev/null 2>&1 || true
    if [ -n "$pref" ]; then
        log "forcing up preferred wifi profile: ${pref}"
        nmcli connection up "$pref" >/dev/null 2>&1 || true
    fi
    exit 0
fi

# fail 1..(FAIL_REAPPLY-1): just observe; could be a transient gateway blip.
exit 0
WATCHDOG
}

# The DTS, scripts/packaging/pi/respeaker-2mic-v2_0-overlay.dts byte for byte
# (scripts/install-pi-check.sh compares them).
overlay_dts() {
  cat <<'DTS'
/*
* Copyright (C) 2024 Seeed Studio
* bbear <bbear953308023@gmail.com>
*
* MIT License
*
*/
/dts-v1/;
/plugin/;

/ {
	compatible = "brcm,bcm2835", "brcm,bcm2708", "brcm,bcm2709", "brcm,bcm2711";
	
	fragment@0 {
		target-path="/";
		__overlay__ {
			hardware = "reSpeaker V2.0";
		};
	};
	
	fragment@1 {
		target-path="/";
		__overlay__ {
			
			tlv320aic3104_mclk: tlv320aic3104_mclk {
				#clock-cells = <0>;
				compatible = "fixed-clock";
				clock-frequency = <24576000>;
			};
		};
	};
	
	fragment@2 {
		target = <&i2c1>;
		__overlay__ {
			#address-cells = <1>;
			#size-cells = <0>;
			status = "okay";
			
			tlv320aic3104: tlv320aic3104@18 {
				#sound-dai-cells = <0>;
				compatible = "ti,tlv320aic3104";
				reg = <0x18>;
				ai3x-micbias-vg = <2>;// set MICBIAS to 2.5v
			};
		};
	};
	
	fragment@3 {
		target = <&i2s_clk_consumer>;
		__overlay__ {
			status = "okay";
		};
	};
	
	fragment@4 {
		target = <&sound>;
		slave_overlay: __overlay__ {
			compatible = "simple-audio-card";
			simple-audio-card,format = "i2s";
			simple-audio-card,name = "seeed2micvoicec";
			i2s-controller = <&i2s_clk_consumer>;
			status = "okay";
			
			simple-audio-card,widgets =
			"Headphone", "Headphone Jack",
			"Line", "Line In";
			simple-audio-card,routing =
			"Headphone Jack",       "HPLOUT",
			"Headphone Jack",       "HPROUT",
			"LINE1L",               "Line In",
			"LINE1R",               "Line In";
			
			simple-audio-card,bitclock-master = <&sound_master>;
			simple-audio-card,frame-master = <&sound_master>;
			
			simple-audio-card,cpu {
				sound-dai = <&i2s_clk_consumer>;
			};
			sound_master: simple-audio-card,codec {
				#sound-dai-cells = <0>;
				sound-dai = <&tlv320aic3104>;
				clocks = <&tlv320aic3104_mclk>;
				clock-names = "mclk";
			};
		};
	};
	
	__overrides__ {
	};
};

DTS
}

preflight
case $ACTION in
  deactivate) deactivate; exit 0 ;;
esac
install_packages
install_agent
install_wake_models
install_overlay
configure_boot_config
configure_alsa
switch_to_pulseaudio
configure_pulseaudio
configure_bluetooth
mixer_baseline
setup_user
install_sudoers
configure_journald
configure_swappiness
configure_swap
mask_ap_services
wifi_powersave_off
reconcile_wifi_profiles
install_net_watchdog
write_marker
install_unit
if [ $ACTION = activate ]; then activate; else say "Installed $UNIT, disabled: --activate makes it the node that runs."; fi
[ $NEEDS_REBOOT = 0 ] || say "The HAT's boot settings or the audio stack changed: reboot (sudo reboot) for them to take effect."
