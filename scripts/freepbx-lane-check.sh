#!/usr/bin/env bash
# scripts/freepbx-lane without a hypervisor: its dev-box and KVM guards, and the plans --dry-run
# prints for build and weekly. A stand-in hostname on PATH says which box this is; the lane's
# directory is a scratch one.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
LANE=$ROOT/scripts/freepbx-lane
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin"
printf '#!/bin/sh\necho devbox\n' >"$T/bin/hostname"
chmod +x "$T/bin/hostname"
export PATH="$T/bin:$PATH" JARVIS_FREEPBX_DIR="$T/lane"
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
# --no-update: the build's release, offline.
plan=$(expect 0 'siplane drift' weekly --no-update --dry-run)
grep -q 'qemu-img snapshot -a provisioned' <<<"$plan" || err "weekly --no-update does not revert to provisioned"
grep -q 'restrict=on' <<<"$plan" || err "weekly --no-update is not cut off from the network"
lacks 'upgradeall|provision\.sh' "$plan"

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
