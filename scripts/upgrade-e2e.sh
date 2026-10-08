#!/usr/bin/env bash
# Self-update end to end (AD5, ID10) against a real service manager, from fake signed releases.
#
#   scripts/upgrade-e2e.sh system   # systemd system unit (needs sudo; the CI `upgrade` job)
#   scripts/upgrade-e2e.sh user     # systemd --user unit, throwaway home and ports (a dev box)
#
# It builds four releases trusting a throwaway minisign key (test builds only, via -ldflags),
# signs their SHA256SUMS with the minisign CLI ($MINISIGN, default `minisign`), serves them as
# a fake GitHub on 127.0.0.1, then:
#   1. installs v0.0.1 as a service;
#   2. `jarvisd upgrade --version v0.0.2`         → healthy v0.0.2, jarvisd.prev = v0.0.1;
#   3. `jarvisd upgrade --version v0.0.3` (a build that crashes after counting its start)
#                                                 → exits non-zero, rolled back to v0.0.2;
#   4. the admin button: setup → enable updates → POST /api/update/apply {v0.0.4} → v0.0.4
#      (system mode: the service account can't write /usr/local/bin, so the unit's
#      ExecStartPre=+ helper swaps it in);
#   5. `jarvisd upgrade --rollback`               → v0.0.2 again;
# and uninstalls.
set -euo pipefail

MODE=${1:-system}
MINISIGN=${MINISIGN:-minisign}
GO=${GO:-go}
cd "$(dirname "$0")/.."

W=$(mktemp -d)
SRV_PID=""
# `service install --user` turns lingering on; put it back as it was.
LINGER=$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null || echo unknown)
cleanup() {
  set +e
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
  if [ "$MODE" = system ]; then
    sudo /usr/local/bin/jarvisd service uninstall >/dev/null 2>&1
  else
    "$W/bin/jarvisd" service uninstall --user >/dev/null 2>&1
    [ "$LINGER" = no ] && loginctl disable-linger "$(id -un)" 2>/dev/null
    rm -rf "$W"
  fi
}
trap cleanup EXIT

say() { printf '\n=== %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

arch=$($GO env GOARCH)
platform="linux-$arch"
API_PORT=${API_PORT:-38780}
API="http://127.0.0.1:$API_PORT"

say "throwaway signing key"
"$MINISIGN" -G -W -p "$W/test.pub" -s "$W/test.key" >/dev/null
pub=$(tail -n 1 "$W/test.pub")

build() { # tag [extra -X flags]
  local tag=$1; shift
  mkdir -p "$W/build/$tag"
  CGO_ENABLED=0 $GO build -trimpath -o "$W/build/$tag/jarvisd" -ldflags "-s -w -X main.version=$tag \
    -X github.com/alexberardi/jarvis-server/internal/update.extraTrustedKey=$pub $*" ./cmd/jarvisd
}

publish() { # tag: archive + SHA256SUMS + signature + the release JSON, like release.yml
  local tag=$1 name="jarvisd-$1-$platform" dl="$W/srv/dl/$1"
  mkdir -p "$W/stage/$name" "$dl" "$W/srv/repos/alexberardi/jarvis-server/releases/tags"
  cp "$W/build/$tag/jarvisd" LICENSE "$W/stage/$name/"
  tar -C "$W/stage" -czf "$dl/$name.tar.gz" "$name"
  (cd "$dl" && sha256sum "$name.tar.gz" > SHA256SUMS)
  "$MINISIGN" -S -s "$W/test.key" -m "$dl/SHA256SUMS" -t "jarvisd $tag SHA256SUMS" >/dev/null
  local assets="" f
  for f in "$name.tar.gz" SHA256SUMS SHA256SUMS.minisig; do
    assets="$assets${assets:+,}{\"name\":\"$f\",\"browser_download_url\":\"$API/dl/$tag/$f\",\"size\":$(stat -c %s "$dl/$f")}"
  done
  printf '{"tag_name":"%s","draft":false,"prerelease":false,"assets":[%s]}\n' "$tag" "$assets" \
    > "$W/srv/repos/alexberardi/jarvis-server/releases/tags/$tag"
}

say "building v0.0.1 .. v0.0.4 (v0.0.3 crashes on purpose)"
build v0.0.1
build v0.0.2
build v0.0.3 "-X main.crashForTest=1"
build v0.0.4
for t in v0.0.1 v0.0.2 v0.0.3 v0.0.4; do publish "$t"; done
(cd "$W/srv" && exec python3 -m http.server "$API_PORT" --bind 127.0.0.1 >/dev/null 2>&1) &
SRV_PID=$!

# Per mode: how to run jarvisd as the operator, where things live, which ports.
if [ "$MODE" = system ]; then
  BIN=/usr/local/bin/jarvisd
  HOME_DIR=/var/lib/jarvisd
  ADMIN=http://127.0.0.1:7710
  ENV_FILE=/etc/jarvisd/jarvisd.env
  J() { sudo "$BIN" "$@"; }
  R() { sudo "$@"; }
  sudo install -m 755 "$W/build/v0.0.1/jarvisd" "$BIN"
  say "install v0.0.1 (system unit)"
  J service install --no-start
  printf 'JARVIS_UPDATE_API=%s\nJARVIS_UPGRADE_GATE_TIMEOUT=30s\nJARVIS_MDNS=0\n' "$API" | sudo tee -a "$ENV_FILE" >/dev/null
  J service start
else
  BIN="$W/bin/jarvisd"
  HOME_DIR="$W/home"
  ADMIN=http://127.0.0.1:37710
  ENV_FILE="$HOME_DIR/jarvisd.env"
  J() { "$BIN" "$@" --home "$HOME_DIR"; }
  R() { "$@"; }
  mkdir -p "$W/bin" "$HOME_DIR"
  install -m 755 "$W/build/v0.0.1/jarvisd" "$BIN"
  {
    for l in CONFIG:37700 AUTH:37701 LOGS:37702 COMMAND_CENTER:37703 LLM:37704 WHISPER:37706 TTS:37707 \
      NOTIFICATIONS:37712 RECIPES:37030 OCR:37031 ADMIN:37710; do echo "JARVIS_PORT_${l%%:*}=${l#*:}"; done
    echo "JARVIS_MQTT_ADDR=127.0.0.1:31884"
    echo "JARVIS_MQTT_WS_ADDR=127.0.0.1:39883"
    echo "JARVIS_MDNS=0"
    echo "JARVIS_NO_BROWSER=1"
    echo "JARVIS_UPDATE_API=$API"
    echo "JARVIS_UPGRADE_GATE_TIMEOUT=30s"
  } > "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  say "install v0.0.1 (systemd --user unit)"
  "$BIN" service install --user --bin "$BIN" --home "$HOME_DIR"
fi
UP=(upgrade); [ "$MODE" = user ] && UP=(upgrade --user)

running_version() {
  curl -fsS --max-time 3 "$ADMIN/health" | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])'
}
wait_version() { # tag
  for _ in $(seq 1 120); do
    [ "$(running_version 2>/dev/null)" = "$1" ] && return 0
    sleep 1
  done
  fail "jarvisd $1 never answered (running: $(running_version 2>/dev/null || echo none))"
}
outcome() { R cat "$HOME_DIR/updates/last-upgrade.json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["outcome"])'; }

wait_version v0.0.1

say "2. jarvisd upgrade --version v0.0.2"
J "${UP[@]}" --version v0.0.2
wait_version v0.0.2
[ "$(R "$BIN.prev" version)" = v0.0.1 ] || fail "jarvisd.prev is not v0.0.1"
[ "$(outcome)" = succeeded ] || fail "outcome $(outcome)"
R test ! -e "$HOME_DIR/updates/upgrade.json" || fail "marker left"
[ "$(R sh -c "ls $HOME_DIR/backups/jarvis-v0.0.1-*.db | wc -l")" = 1 ] || fail "no snapshot"

say "3. jarvisd upgrade --version v0.0.3 (crashes: must roll back)"
if J "${UP[@]}" --version v0.0.3; then fail "the broken upgrade reported success"; fi
wait_version v0.0.2
[ "$(outcome)" = rolled_back ] || fail "outcome $(outcome)"
[ "$(R "$BIN" version)" = v0.0.2 ] || fail "binary not restored"
# The crash loop used several of the unit's StartLimitBurst starts; start the count afresh.
if [ "$MODE" = system ]; then sudo systemctl reset-failed jarvisd; else systemctl --user reset-failed jarvisd; fi

say "4. the admin button: POST /api/update/apply v0.0.4"
token=$(R cat "$HOME_DIR/setup-token")
access=$(curl -fsS -X POST "$ADMIN/api/auth/setup" -H 'Content-Type: application/json' -H "X-Jarvis-Setup-Token: $token" \
  -d '{"email":"root@example.com","password":"password1"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
auth=(-H "Authorization: Bearer $access" -H 'Content-Type: application/json')
curl -fsS -X PUT "$ADMIN/api/update/settings" "${auth[@]}" -d '{"enabled":true}' >/dev/null
curl -fsS "$ADMIN/api/system/info" "${auth[@]}" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["supervisor"]=="systemd" and d["restart_supported"], d'
curl -fsS -X POST "$ADMIN/api/update/apply" "${auth[@]}" -d '{"version":"v0.0.4"}'
echo
wait_version v0.0.4
for _ in $(seq 1 60); do [ "$(outcome)" = succeeded ] && break; sleep 1; done
[ "$(outcome)" = succeeded ] || fail "outcome $(outcome)"
curl -fsS "$ADMIN/api/update/apply" "${auth[@]}"
echo
[ "$(R "$BIN.prev" version)" = v0.0.2 ] || fail "jarvisd.prev is not v0.0.2"
if [ "$MODE" = system ]; then
  sudo journalctl -u jarvisd --no-pager | grep -q 'prestart: installed v0.0.4' || fail "the ExecStartPre helper didn't swap"
fi

say "4b. the restart button"
pid_before=$(curl -fsS "$ADMIN/api/system/info" "${auth[@]}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["started_at"])')
curl -fsS -X POST "$ADMIN/api/system/restart" "${auth[@]}" -d '{}'
echo
sleep 3
for _ in $(seq 1 60); do
  now=$(curl -fsS --max-time 3 "$ADMIN/api/system/info" "${auth[@]}" 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["started_at"])' 2>/dev/null || true)
  [ -n "$now" ] && [ "$now" != "$pid_before" ] && break
  sleep 1
done
[ -n "$now" ] && [ "$now" != "$pid_before" ] || fail "restart button didn't restart jarvisd"

say "5. jarvisd upgrade --rollback"
J "${UP[@]}" --rollback
wait_version v0.0.2
[ "$(outcome)" = rolled_back ] || fail "last-upgrade.json doesn't record the manual rollback: $(outcome)"

say "PASS ($MODE)"
