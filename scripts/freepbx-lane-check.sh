#!/usr/bin/env bash
# scripts/freepbx-lane without a hypervisor: its dev-box and KVM guards, and the plans --dry-run
# prints for build and weekly. A stand-in hostname on PATH says which box this is; the lane's
# directory is a scratch one.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
LANE=$ROOT/scripts/freepbx-lane
# In the checkout, not TMPDIR: a noexec tmp silently skips the stand-in and the lane's
# dev-box guard then refuses with the real hostname, failing every plan check at once.
T=$(mktemp -d "$ROOT/.lane-check.XXXXXX")
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin"
printf '#!/bin/sh\necho devbox\n' >"$T/bin/hostname"
chmod +x "$T/bin/hostname"
export PATH="$T/bin:$PATH" JARVIS_FREEPBX_DIR="$T/lane"
[ "$(hostname)" = devbox ] ||
  { echo "freepbx-lane-check: the stand-in hostname did not take effect (noexec tmp?): hostname says $(hostname)" >&2; exit 1; }
fail=0
err() { echo "freepbx-lane-check: $*" >&2; fail=1; }

# expect CODE PATTERN ARGS...: the lane exits CODE and its output matches PATTERN (grep -E).
expect() {
  local code=$1 pat=$2 out rc=0
  shift 2
  out=$("$LANE" "$@" 2>&1) || rc=$?
  [ "$rc" = "$code" ] || err "freepbx-lane $*: exit $rc, want $code; output: $out"
  grep -qE -- "$pat" <<<"$out" || err "freepbx-lane $*: output lacks /$pat/: $out"
  printf '%s\n' "$out"
}
# lacks PATTERN OUTPUT
lacks() { if grep -qE -- "$1" <<<"$2"; then err "output has /$1/ and should not"; fi; }

bash -n "$LANE" || err "bash -n scripts/freepbx-lane"
expect 2 'freepbx-lane build --dev-box NAME' >/dev/null

# Only the dev box, named by build and remembered next to the disk.
expect 1 '--dev-box NAME is required' build --dry-run >/dev/null
expect 1 'this is devbox, not the dev box otherbox: refusing' build --dev-box otherbox --dry-run >/dev/null
expect 1 'no VM built here' weekly --dry-run >/dev/null
mkdir -p "$T/lane"
echo otherbox >"$T/lane/dev-box"
expect 1 'not the dev box otherbox the lane was built on: refusing' weekly --dry-run >/dev/null
expect 1 'refusing' ssh true >/dev/null
rm -r "$T/lane"

# No KVM: a real build stops before it creates anything.
FREEPBX_KVM=$T/no-kvm expect 1 'needs KVM' build --dev-box devbox >/dev/null
[ ! -e "$T/lane/disk.qcow2" ] || err "build without KVM created a disk"

# The build plan: Debian 12 cloud image checked, the installer, both snapshots, provisioning.
plan=$(expect 0 'qemu-img snapshot -c provisioned' build --dev-box devbox --dry-run)
for want in 'debian-12-genericcloud-amd64\.qcow2' 'sha512sum' 'qemu-system-x86_64 .*-enable-kvm' \
  'hostfwd=tcp:127\.0\.0\.1:2247-:22' 'sng_freepbx_debian_install\.sh' 'qemu-img snapshot -c installed' \
  'provision\.sh'; do
  grep -qE -- "$want" <<<"$plan" || err "build plan lacks /$want/"
done
lacks 'hostfwd=tcp:0\.0\.0\.0|hostfwd=tcp::' "$plan"
# Every forward is on loopback: SSH, HTTP, SIP over UDP and TCP, and each RTP port at its own number.
loopback_only() {
  local fwds
  fwds=$(grep -oE 'hostfwd=[^, ]*' <<<"$1" || true)
  for want in 'hostfwd=tcp:127\.0\.0\.1:2247-:22' 'hostfwd=tcp:127\.0\.0\.1:8447-:80' 'hostfwd=udp:127\.0\.0\.1:5260-:5060' \
    'hostfwd=tcp:127\.0\.0\.1:5260-:5060' 'hostfwd=udp:127\.0\.0\.1:30800-:30800' 'hostfwd=udp:127\.0\.0\.1:30819-:30819'; do
    grep -qE -- "^$want\$" <<<"$fwds" || err "$2: no forward /$want/"
  done
  lacks 'hostfwd=udp:127\.0\.0\.1:30820-' "$fwds"
  [ "$(grep -cvE '^hostfwd=(tcp|udp):127\.0\.0\.1:[0-9]+-:[0-9]+$' <<<"$fwds")" = 0 ] ||
    err "$2: a forward not on 127.0.0.1: $(grep -vE '^hostfwd=(tcp|udp):127\.0\.0\.1:' <<<"$fwds" | head -3)"
}
loopback_only "$plan" "build plan"
lacks 'sha256sum -c' "$plan"
# A pinned installer checksum is checked before the installer runs as root.
sum=$(printf '%064d' 0)
plan=$(FREEPBX_INSTALLER_SHA256=$sum expect 0 'sha256sum -c' build --dev-box devbox --dry-run)
grep -qE -- "$sum  sng_freepbx_debian_install\.sh' \| sha256sum -c --quiet && sudo bash" <<<"$plan" ||
  err "build plan does not check the installer's sha256 before running it"
FREEPBX_INSTALLER_SHA256=nothex expect 2 'is not a sha256' build --dev-box devbox --dry-run >/dev/null
[ ! -e "$T/lane" ] || err "build --dry-run wrote $T/lane"

# The weekly plan: from "installed", updated, provisioned afresh, captured, diffed against L1.
mkdir -p "$T/lane"
echo devbox >"$T/lane/dev-box"
plan=$(expect 0 'siplane drift' weekly --dry-run)
for want in 'qemu-img snapshot -a installed' 'fwconsole ma upgradeall' 'provision\.sh' \
  "pjsip show endpoint 100" "pjsip show aor 100" 'freepbx-show-100\.txt'; do
  grep -qE -- "$want" <<<"$plan" || err "weekly plan lacks /$want/"
done
lacks 'restrict=on' "$plan"
# Then the calls (loop #490), after the capture and before the VM goes down: they change SIP
# Settings and the trunk, never extension 100.
for want in 'sandbox up --name freepbx-l2 --voice --sip' 'calls\.sh 5260 30800 30819' 'siplane l2 --sandbox freepbx-l2 --sip 127\.0\.0\.1:5260 --ssh 127\.0\.0\.1:2247'; do
  grep -qE -- "$want" <<<"$plan" || err "weekly plan lacks /$want/"
done
order=$(grep -E '^\+ ' <<<"$plan" | grep -oE 'pjsip show aor 100|calls\.sh [0-9]|siplane l2|sudo poweroff|siplane drift' | tr '\n' '|' || true)
[ "$order" = "pjsip show aor 100|calls.sh 5|siplane l2|sudo poweroff|siplane drift|" ] || err "weekly runs the calls out of order: $order"
plan=$(expect 0 'siplane drift' weekly --no-calls --dry-run)
lacks 'calls\.sh|siplane l2|sandbox up' "$plan"
# --no-update: the build's release; offline only without the calls (restrict=on also drops the
# VM's replies on the forwarded UDP ports).
plan=$(expect 0 'siplane drift' weekly --no-update --no-calls --dry-run)
grep -q 'qemu-img snapshot -a provisioned' <<<"$plan" || err "weekly --no-update does not revert to provisioned"
grep -q 'restrict=on' <<<"$plan" || err "weekly --no-update --no-calls is not cut off from the network"
lacks 'upgradeall|provision\.sh' "$plan"
plan=$(expect 0 'siplane l2' weekly --no-update --dry-run)
lacks 'restrict=on|upgradeall|provision\.sh' "$plan"

# The calls alone: on "provisioned", with the network on, a sandbox's far ends, the ports the
# forwards use (FREEPBX_* moves them all together).
plan=$(expect 0 'siplane l2' calls --dry-run)
for want in 'qemu-img snapshot -a provisioned' 'calls\.sh 5260 30800 30819' 'sudo poweroff'; do
  grep -qE -- "$want" <<<"$plan" || err "calls plan lacks /$want/"
done
lacks 'restrict=on|upgradeall|provision\.sh|siplane drift' "$plan"
loopback_only "$plan" "calls plan"
plan=$(FREEPBX_SIP_PORT=5360 FREEPBX_RTP_START=30900 FREEPBX_RTP_COUNT=4 expect 0 'siplane l2 --sandbox mine --sip 127\.0\.0\.1:5360' calls --sandbox mine --dry-run)
for want in 'hostfwd=udp:127\.0\.0\.1:5360-:5060' 'hostfwd=udp:127\.0\.0\.1:30903-:30903' 'calls\.sh 5360 30900 30903' 'sandbox up --name mine '; do
  grep -qE -- "$want" <<<"$plan" || err "calls plan with other ports lacks /$want/"
done
lacks 'hostfwd=udp:127\.0\.0\.1:30904-' "$plan"
expect 1 'letters, digits' calls --sandbox 'a;b' --dry-run >/dev/null
bash -n "$ROOT/scripts/freepbx-lane.d/calls.sh" || err "bash -n scripts/freepbx-lane.d/calls.sh"

# The timer's units, rendered for this checkout.
expect 0 'systemctl --user enable --now jarvis-freepbx-lane\.timer' timer >/dev/null
svc=$T/lane/systemd/jarvis-freepbx-lane.service
grep -qF "ExecStart=$ROOT/scripts/freepbx-lane weekly" "$svc" || err "rendered service does not run this checkout's lane"
lacks '@CHECKOUT@' "$(cat "$svc")"
grep -q '^OnCalendar=' "$T/lane/systemd/jarvis-freepbx-lane.timer" || err "no OnCalendar in the rendered timer"

# Provisioning mirrors L1: extension 100 and DID 5551230000 → 100.
grep -q '^100,Jarvis,pjsip,' "$ROOT/scripts/freepbx-lane.d/extensions.csv" || err "extensions.csv lacks extension 100"
grep -q '^5551230000,.*from-did-direct,100,1' "$ROOT/scripts/freepbx-lane.d/dids.csv" || err "dids.csv lacks DID 5551230000 → 100"
grep -q '5551230000' "$ROOT/test/sip/asterisk/extensions.conf" || err "L1's inbound DID is no longer 5551230000"

[ "$fail" = 0 ] && echo "freepbx-lane-check: ok"
exit "$fail"
