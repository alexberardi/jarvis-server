#!/usr/bin/env bash
# Checks that the Swift flags scripts/lib/ios.sh gives xcodebuild let macro plugins run inside a
# Seatbelt sandbox (the loop runner's fence), where macOS refuses the nested sandbox-exec that
# swift-frontend puts each out-of-process plugin under:
#
#   scripts/ios-nested-sandbox-check.sh
#
# It type-checks an @Observable class with the toolchain's ObservationMacros loaded through
# swift-plugin-server (out of process, as Xcode loads expo-dev-menu's PreviewsMacros), first
# without the flags (must fail as the iOS build did: 'sandbox_apply: Operation not permitted',
# 'external macro implementation type … could not be found'), then with them (must pass).
# Needs only the command-line tools. Run it unsandboxed and it wraps itself in sandbox-exec.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
[ "$(uname -s)" = Darwin ] || { echo "ios-nested-sandbox-check: macOS only" >&2; exit 2; }
if [ -z "${NESTED_CHECK_FENCED:-}" ] && sandbox-exec -p '(version 1)(allow default)' /usr/bin/true 2>/dev/null; then
  # Not fenced yet: fence this run as the loop runner does.
  NESTED_CHECK_FENCED=1 exec sandbox-exec -p '(version 1)(allow default)' "$0" "$@"
fi

say() { echo "ios-nested-sandbox-check: $*"; }
die() { say "$*" >&2; exit 1; }
# shellcheck source=lib/ios.sh
. "$ROOT/scripts/lib/ios.sh"
[ -n "${IOS_NESTED_SWIFT_FLAGS:-}" ] || die "scripts/lib/ios.sh gives xcodebuild no IOS_NESTED_SWIFT_FLAGS"

work=$(mktemp -d "${TMPDIR:-/tmp}/ios-nested.XXXXXX")
trap 'rm -rf "$work"' EXIT
export TMPDIR=$work # xcrun and swiftc write their caches here, not in the user's /var/folders
plugins=$(dirname "$(xcrun --find swiftc 2>/dev/null)")/../lib/swift/host/plugins
server=$(xcrun --find swift-plugin-server 2>/dev/null) || die "no swift-plugin-server in this toolchain"
[ -f "$plugins/libObservationMacros.dylib" ] || die "no ObservationMacros plugin in $plugins"
cp "$plugins/libObservationMacros.dylib" "$work/"
printf 'import Observation\n@Observable final class M { var x = 0 }\n' >"$work/o.swift"

typecheck() { # FLAGS…: swiftc with ObservationMacros out of process; output in $work/swiftc.log
  # shellcheck disable=SC2068 # word-split the flags as xcodebuild does OTHER_SWIFT_FLAGS
  xcrun swiftc -module-cache-path "$work/mc" -parse-as-library -typecheck $@ \
    -Xfrontend -load-resolved-plugin -Xfrontend "$work/libObservationMacros.dylib#$server#ObservationMacros" \
    "$work/o.swift" >"$work/swiftc.log" 2>&1
}

if typecheck; then
  die "swiftc passed without the flags: this is not a nested sandbox, so the check proves nothing"
fi
grep -q 'could not be found' "$work/swiftc.log" || { cat "$work/swiftc.log" >&2; die "swiftc failed for another reason"; }
say "without the flags: fails as expected ($(grep -m1 -o 'sandbox_apply: .*' "$work/swiftc.log" || echo 'macro not found'))"
typecheck "$IOS_NESTED_SWIFT_FLAGS" || { cat "$work/swiftc.log" >&2; die "swiftc failed with '$IOS_NESTED_SWIFT_FLAGS'"; }
say "with '$IOS_NESTED_SWIFT_FLAGS': ok"
