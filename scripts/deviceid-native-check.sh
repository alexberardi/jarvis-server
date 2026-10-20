#!/usr/bin/env bash
# The desktop agent's device id (internal/agent/deviceid) on this machine's real OS: present,
# the same across two processes, and (with --other-user) different for another OS user.
# Prints only pass/fail, never the id; the files it compares hold a sha256 of it.
#
#   scripts/deviceid-native-check.sh DIR [--other-user]
#
# DIR gets the test binary (deviceid.test[.exe]) and the fingerprint (fp), so a later step can
# run the binary as another user (CI does that on Windows in PowerShell). --other-user runs it
# as `nobody` through sudo (Linux, macOS): CI only, or a machine where that is fine.
set -euo pipefail
fail() { echo "deviceid-native-check: $*" >&2; exit 1; }
[ $# -ge 1 ] || fail "usage: $0 DIR [--other-user]"
DIR=$1
OTHER=${2:-}
mkdir -p "$DIR"
BIN="$DIR/deviceid.test$(go env GOEXE)"
rm -f "$DIR/fp"
CGO_ENABLED=0 go test -c -o "$BIN" ./internal/agent/deviceid
run() { "$@" -test.run '^TestNativeDeviceID$' -test.v; }

for _ in 1 2; do
  JARVIS_HOST_NATIVE=1 JARVIS_DEVICEID_SAME_AS="$DIR/fp" run "$BIN"
done
[ -s "$DIR/fp" ] || fail "no fingerprint written"

if [ "$OTHER" = --other-user ]; then
  case $(uname -s) in
    Linux|Darwin) ;;
    *) fail "--other-user is for Linux and macOS" ;;
  esac
  # A directory `nobody` can read (the runner's temp dir may sit under a private home).
  O=$(mktemp -d /tmp/deviceid.XXXXXX)
  trap 'rm -rf "$O"' EXIT
  cp "$BIN" "$DIR/fp" "$O/"
  chmod 755 "$O" "$O/deviceid.test"
  chmod 644 "$O/fp"
  run sudo -u nobody env -i HOME="$O" JARVIS_HOST_NATIVE=1 JARVIS_DEVICEID_DIFFERS_FROM="$O/fp" "$O/deviceid.test"
fi
echo "deviceid-native-check: ok"
