#!/bin/sh
# Install the Go voice node (jarvis-agent --profile pi) on a Raspberry Pi with the ReSpeaker
# 2-mics HAT v2.0, next to or instead of the Python node (go-pi-node.md §6, §7 phase 4.3).
#
#   sudo sh install-pi.sh [--activate]
#
# It downloads the release's linux-arm64 agent archive, checks it against SHA256SUMS (and
# SHA256SUMS.minisig when minisign is installed), puts the agent in /opt/jarvis-agent (the node
# user's, so the agent updates itself there), and does
# the system setup the Python node's install.sh does for the HAT: the overlay, config.txt, ALSA
# (dsnoopmic and output), the mixer baseline, groups and linger, sudoers, journald, swappiness,
# swap, the /etc/jarvis-node marker and the jarvis-agent.service unit. Every step can run again.
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
    -h | --help) sed -n '2,28p' "$0" 2>/dev/null || true; exit 0 ;;
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
ExecStart=$OPT/jarvis-agent run --profile pi --voice --voice-input dsnoopmic --voice-output output
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
install_agent
install_overlay
configure_boot_config
configure_alsa
mixer_baseline
setup_user
install_sudoers
configure_journald
configure_swappiness
configure_swap
write_marker
install_unit
if [ $ACTION = activate ]; then activate; else say "Installed $UNIT, disabled: --activate makes it the node that runs."; fi
[ $NEEDS_REBOOT = 0 ] || say "The HAT's boot settings changed: reboot (sudo reboot) for them to take effect."
