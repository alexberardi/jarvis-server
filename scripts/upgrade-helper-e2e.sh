#!/usr/bin/env bash
# The admin Update button through the privileged upgrade helper (ID11) on macOS (the root
# LaunchDaemon net.jarvisautomation.jarvisd-updater) and Windows (the LocalSystem service
# jarvisd-updater), end to end, from fake signed releases. For the CI `upgrade` job on
# macos-14 and windows-latest (Git Bash); runners have passwordless sudo / are administrators.
#
#   scripts/upgrade-helper-e2e.sh
#
# The releases trust a throwaway key (test builds only, -X …extraTrustedKey) and are signed by
# scripts/testsign. Steps:
#   1. install v0.0.1 as a service from the protected directory (/usr/local/bin,
#      %ProgramFiles%\jarvisd); `service status` names the helper;
#   2. the admin button → v0.0.2: jarvisd stages it, wakes the helper, which re-verifies,
#      swaps and restarts jarvisd; the new version passes its gate;
#   3. the service account plants hostile staged upgrades; the helper refuses each one and the
#      binary stays v0.0.2: an older release (genuinely signed), a newer release's signed
#      checksum list over another archive, and (macOS) an archive symlinked out of the home;
#   4. the admin button → v0.0.3, a build that crashes after counting its start: the helper
#      swaps it in, the crash loop requests a rollback, the helper restores v0.0.2 and the
#      restarted v0.0.2 finishes (outcome rolled_back);
#   5. the admin button → v0.0.4, healthy again;
# then uninstalls (the helper goes too).
set -euo pipefail

GO=${GO:-go}
cd "$(dirname "$0")/.."

case "$(uname -s)" in
Darwin) OS=darwin ;;
MINGW* | MSYS* | CYGWIN*) OS=windows ;;
*)
  echo "macOS or Windows only (Linux: scripts/upgrade-e2e.sh)" >&2
  exit 2
  ;;
esac
platform="$OS-$($GO env GOARCH)"
WR=.helper-e2e # relative to the repo: native Windows tools get plain relative paths
rm -rf "$WR"
mkdir -p "$WR"
W="$PWD/$WR"
API_PORT=${API_PORT:-38781}
API="http://127.0.0.1:$API_PORT"
ADMIN=http://127.0.0.1:7710
if [ "$OS" = windows ]; then
  EXE=jarvisd.exe
  BINDIR="/c/Program Files/jarvisd"
  BIN="$BINDIR/$EXE"
  NATIVE_BIN='C:\Program Files\jarvisd\jarvisd.exe'
  HOME_DIR=/c/ProgramData/jarvisd
  HELPER_LOG="$BINDIR/jarvisd-updater.log"
  EXT=zip
  PY=python
  R() { "$@"; }
else
  EXE=jarvisd
  # Not /usr/local/bin: on GitHub's macOS images Homebrew made it writable by the runner
  # user, so jarvisd would swap itself. A real Mac's is root-owned, like this directory.
  BINDIR=/opt/jarvisd-e2e/bin
  BIN="$BINDIR/jarvisd"
  NATIVE_BIN=$BIN
  HOME_DIR="$HOME/.jarvisd"
  HELPER_LOG=/Library/Logs/jarvisd-updater.log
  EXT=tar.gz
  PY=python3
  R() { sudo "$@"; }
fi
J() { R "$BIN" "$@"; }
SRV_PID=""
cleanup() {
  set +e
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
  J service uninstall >/dev/null 2>&1
}
trap cleanup EXIT

say() { printf '\n=== %s\n' "$*"; }
fail() {
  echo "FAIL: $*" >&2
  echo "--- helper log" >&2
  R tail -50 "$HELPER_LOG" >&2 || true
  echo "--- jarvisd log" >&2
  tail -80 "$HOME_DIR/logs/jarvisd.log" >&2 || true
  exit 1
}
sum() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
json() { "$PY" -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

say "throwaway signing key, releases v0.0.1 .. v0.0.4 (v0.0.3 crashes on purpose)"
pub=$($GO run ./scripts/testsign keygen "$WR/test.key")
build() { # tag [extra -X flags]
  local tag=$1
  shift
  mkdir -p "$WR/build/$tag"
  CGO_ENABLED=0 $GO build -trimpath -o "$WR/build/$tag/$EXE" -ldflags "-X main.version=$tag \
    -X github.com/alexberardi/jarvis-server/internal/update.extraTrustedKey=$pub $*" ./cmd/jarvisd
}
publish() { # tag: archive + SHA256SUMS + signature + the release JSON, like release.yml
  local tag=$1 name="jarvisd-$1-$platform" dl="$WR/srv/dl/$1" f assets=""
  mkdir -p "$WR/stage/$name" "$dl" "$WR/srv/repos/alexberardi/jarvis-server/releases/tags"
  cp "$WR/build/$tag/$EXE" "$WR/stage/$name/"
  if [ "$EXT" = zip ]; then
    (cd "$WR/stage" && 7z a -tzip -bd "../srv/dl/$tag/$name.zip" "$name" >/dev/null)
  else
    tar -C "$WR/stage" -czf "$dl/$name.tar.gz" "$name"
  fi
  (cd "$dl" && sum "$name.$EXT" >SHA256SUMS)
  $GO run ./scripts/testsign sign "$WR/test.key" "$dl/SHA256SUMS" "jarvisd $tag SHA256SUMS"
  for f in "$name.$EXT" SHA256SUMS SHA256SUMS.minisig; do
    assets="$assets${assets:+,}{\"name\":\"$f\",\"browser_download_url\":\"$API/dl/$tag/$f\",\"size\":$(wc -c <"$dl/$f" | tr -d ' ')}"
  done
  printf '{"tag_name":"%s","draft":false,"prerelease":false,"assets":[%s]}\n' "$tag" "$assets" \
    >"$WR/srv/repos/alexberardi/jarvis-server/releases/tags/$tag"
}
build v0.0.1
build v0.0.2
build v0.0.3 "-X main.crashForTest=1"
build v0.0.4
for t in v0.0.1 v0.0.2 v0.0.3 v0.0.4; do publish "$t"; done
"$PY" -m http.server "$API_PORT" --bind 127.0.0.1 --directory "$WR/srv" >/dev/null 2>&1 &
SRV_PID=$!

say "1. install v0.0.1 (service + upgrade helper)"
R mkdir -p "$BINDIR"
R cp "$WR/build/v0.0.1/$EXE" "$BIN"
if [ "$OS" = darwin ]; then
  sudo chown -R root:wheel /opt/jarvisd-e2e
  sudo chmod -R 755 /opt/jarvisd-e2e
  # The point of the test: the account jarvisd runs as can't write its binary's directory.
  if touch "$BINDIR/.w" 2>/dev/null; then fail "$BINDIR is writable by $(id -un)"; fi
fi
J service install --no-start
printf 'JARVIS_UPDATE_API=%s\nJARVIS_UPGRADE_GATE_TIMEOUT=30s\nJARVIS_MDNS=0\nJARVIS_NO_BROWSER=1\n' "$API" >>"$HOME_DIR/jarvisd.env"
J service start
J service status --wait 120s
J service status --json | tee "$W/status.json"
helper=$(json 'd.get("upgrade_helper","")' <"$W/status.json")
case "$helper" in *jarvisd-updater*) ;; *) fail "service status doesn't report the helper: $helper" ;; esac
if [ "$OS" = darwin ]; then
  test -f /Library/LaunchDaemons/net.jarvisautomation.jarvisd-updater.plist || fail "no helper plist"
  sudo launchctl print system/net.jarvisautomation.jarvisd-updater >/dev/null || fail "helper not loaded"
  [ "$(stat -f %Su "$HOME_DIR/updates/requests")" = "$(id -un)" ] || fail "request queue not owned by the service account"
else
  sc.exe qc jarvisd-updater
  sc.exe sdshow jarvisd-updater
  sc.exe qc jarvisd-updater | grep -q LocalSystem || fail "helper not LocalSystem"
  sc.exe qc jarvisd-updater | grep -q DEMAND_START || fail "helper not on demand"
fi

running_version() { curl -fsS --max-time 3 "$ADMIN/health" | json 'd["version"]'; }
wait_version() { # tag [seconds]
  for _ in $(seq 1 "${2:-180}"); do
    [ "$(running_version 2>/dev/null)" = "$1" ] && return 0
    sleep 1
  done
  fail "jarvisd $1 never answered (running: $(running_version 2>/dev/null || echo none))"
}
outcome() { R cat "$HOME_DIR/updates/last-upgrade.json" | json "d[\"$1\"]"; }
wait_outcome() { # outcome [seconds]
  for _ in $(seq 1 "${2:-180}"); do
    if ! R test -e "$HOME_DIR/updates/upgrade.json" && [ "$(outcome outcome 2>/dev/null)" = "$1" ]; then return 0; fi
    sleep 1
  done
  fail "no $1 outcome (marker: $(R cat "$HOME_DIR/updates/upgrade.json" 2>/dev/null || echo none), last: $(R cat "$HOME_DIR/updates/last-upgrade.json" 2>/dev/null || echo none))"
}
bin_version() { R "$1" version; }
wait_version v0.0.1

say "admin setup"
token=$(R cat "$HOME_DIR/setup-token")
access=$(curl -fsS -X POST "$ADMIN/api/auth/setup" -H 'Content-Type: application/json' -H "X-Jarvis-Setup-Token: $token" \
  -d '{"email":"root@example.com","password":"password1"}' | json 'd["access_token"]')
auth=(-H "Authorization: Bearer $access" -H 'Content-Type: application/json')
curl -fsS -X PUT "$ADMIN/api/update/settings" "${auth[@]}" -d '{"enabled":true}' >/dev/null
curl -fsS "$ADMIN/api/system/info" "${auth[@]}" | json 'd["supervisor"], d["capabilities"]' | tee "$W/caps"
grep -q "'self_update': True" "$W/caps" || fail "self_update not offered"
[ "$(curl -fsS "$ADMIN/api/update" "${auth[@]}" | json 'd["can_apply"]')" = True ] || fail "can_apply is false"
apply() { # tag
  curl -fsS -X POST "$ADMIN/api/update/apply" "${auth[@]}" -d "{\"version\":\"$1\"}"
  echo
}

say "2. the admin button: v0.0.2 through the helper"
apply v0.0.2
wait_version v0.0.2
wait_outcome succeeded
[ "$(bin_version "$BIN")" = v0.0.2 ] || fail "binary is not v0.0.2"
PREV="$BINDIR/jarvisd.prev"
[ "$OS" = windows ] && PREV="$BINDIR/jarvisd.prev.exe"
[ "$(bin_version "$PREV")" = v0.0.1 ] || fail "jarvisd.prev is not v0.0.1"
R grep -q "installed v0.0.2 (was v0.0.1)" "$HELPER_LOG" || fail "the helper didn't do the swap"
if [ "$OS" = darwin ]; then
  [ "$(stat -f %Su "$BIN")" = root ] || fail "binary not root-owned"
  [ "$(stat -f %Su "$HOME_DIR/updates/last-upgrade.json")" = "$(id -un)" ] || fail "result not owned by the service account"
  [ "$(stat -f %Su "$HOME_DIR/updates")" = "$(id -un)" ] || fail "updates dir not owned by the service account"
fi

say "3. hostile staged upgrades: the helper refuses each"
# Planted as the account jarvisd runs as (macOS: the runner user; Windows: the administrator
# stands in for NT SERVICE\jarvisd, which may modify the home).
plant() { # to-tag files-from-tag archive-from-tag
  local to=$1 st="$HOME_DIR/updates/staged" asset="jarvisd-$1-$platform.$EXT"
  rm -rf "$st"
  mkdir -p "$st"
  cp "$WR/srv/dl/$2/SHA256SUMS" "$WR/srv/dl/$2/SHA256SUMS.minisig" "$st/"
  cp "$WR/srv/dl/$3/jarvisd-$3-$platform.$EXT" "$st/$asset"
  ASSET="$asset" TO="$to" EXE_PATH="$NATIVE_BIN" "$PY" -c '
import json, os
print(json.dumps({"state": "staged", "from_version": "v0.0.2", "to_version": os.environ["TO"],
  "exe": os.environ["EXE_PATH"], "asset": os.environ["ASSET"], "attempts": 0,
  "staged_at": "2026-10-07T00:00:00Z", "by": "e2e"}))' >"$W/marker.json"
  cp "$W/marker.json" "$HOME_DIR/updates/upgrade.json"
}
wake() {
  if [ "$OS" = darwin ]; then
    touch "$HOME_DIR/updates/requests/e2e-$RANDOM" # as the service account
  else
    sc.exe start jarvisd-updater >/dev/null || true
  fi
}
expect_refused() { # reason-substring
  wake
  wait_outcome failed 90
  local reason
  reason=$(outcome reason)
  echo "refused: $reason"
  case "$reason" in *"$1"*) ;; *) fail "refused for another reason: $reason (want $1)" ;; esac
  [ "$(bin_version "$BIN")" = v0.0.2 ] || fail "the binary changed"
  [ "$(running_version)" = v0.0.2 ] || fail "jarvisd is not v0.0.2"
}
# An older release with its genuine signature.
plant v0.0.1 v0.0.1 v0.0.1
expect_refused "not newer"
# A newer release's genuine signed checksum list over another (signed, older) archive.
plant v0.0.4 v0.0.4 v0.0.1
expect_refused "doesn't match"
if [ "$OS" = darwin ]; then
  # The archive as a symlink out of the home (to the root-owned binary itself).
  plant v0.0.4 v0.0.4 v0.0.4
  rm "$HOME_DIR/updates/staged/jarvisd-v0.0.4-$platform.$EXT"
  ln -s "$BIN" "$HOME_DIR/updates/staged/jarvisd-v0.0.4-$platform.$EXT"
  expect_refused "escapes"
fi

say "4. the admin button: v0.0.3 crashes, the helper rolls it back"
apply v0.0.3
wait_outcome rolled_back 420
wait_version v0.0.2 60
[ "$(bin_version "$BIN")" = v0.0.2 ] || fail "binary not restored"
R grep -q "restored the previous binary" "$HELPER_LOG" || fail "the helper didn't restore the binary"
echo "reason: $(outcome reason)"

say "5. the admin button: v0.0.4"
apply v0.0.4
wait_version v0.0.4
wait_outcome succeeded
[ "$(bin_version "$PREV")" = v0.0.2 ] || fail "jarvisd.prev is not v0.0.2"

say "helper log"
R cat "$HELPER_LOG"

say "uninstall removes the helper too"
J service uninstall
if [ "$OS" = darwin ]; then
  test ! -e /Library/LaunchDaemons/net.jarvisautomation.jarvisd-updater.plist || fail "helper plist kept"
  if sudo launchctl print system/net.jarvisautomation.jarvisd-updater >/dev/null 2>&1; then fail "helper still loaded"; fi
else
  if sc.exe query jarvisd-updater >/dev/null 2>&1; then fail "helper service kept"; fi
fi
say "PASS ($OS)"
