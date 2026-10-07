#!/bin/bash
# I0 spike (docs/install/00-installers.md §8): run jarvisd under launchd on macOS, on alternate
# ports next to a legacy stack, and check /health, Metal and mDNS.
#
# Run as the login user (NOT under sudo); the daemon mode calls sudo itself for the two
# launchctl steps, so the password prompt is the only interaction.
#
#   macos-launchd.sh up     daemon|agent|shell   start jarvisd under that supervisor
#   macos-launchd.sh check                       /health, who launched it, mDNS browse
#   macos-launchd.sh metal                       superuser + app client, install a 0.5B GGUF, chat, grep Metal
#   macos-launchd.sh down   daemon|agent|shell   stop it and remove the plist
#   macos-launchd.sh purge                       remove the throwaway home
#
# Env: JARVISD_BIN (default ~/jarvisd-i0/jarvisd), I0_HOME (default ~/.jarvisd-i0).
# Ports are the legacy ones + 10000 (17700…, MQTT 11884/19883) so a legacy stack can keep running.
set -euo pipefail

BIN=${JARVISD_BIN:-$HOME/jarvisd-i0/jarvisd}
JHOME=${I0_HOME:-$HOME/.jarvisd-i0}
LABEL=net.jarvisautomation.jarvisd-i0
RUN_USER=$(id -un)
UID_N=$(id -u)
CFG=17700 AUTH=17701 LLM=17704

[ "$RUN_USER" = root ] && { echo "run as the login user, not root (the script calls sudo itself)"; exit 2; }

plist_path() {
  case "$1" in
    daemon) echo "/Library/LaunchDaemons/$LABEL.plist" ;;
    agent) echo "$HOME/Library/LaunchAgents/$LABEL.plist" ;;
    *) echo "unknown mode $1" >&2; exit 2 ;;
  esac
}

admin_token() {
  mkdir -p "$JHOME/logs"; chmod 700 "$JHOME"
  [ -f "$JHOME/i0-admin-token" ] || { (umask 077; openssl rand -hex 24 > "$JHOME/i0-admin-token"); }
  cat "$JHOME/i0-admin-token"
}

render_plist() { # $1 = mode
  local user_key=""
  [ "$1" = daemon ] && user_key="<key>UserName</key><string>$RUN_USER</string>"
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  $user_key
  <key>ProgramArguments</key><array><string>$BIN</string><string>serve</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>JARVIS_HOME</key><string>$JHOME</string>
    <key>JARVIS_PORT_CONFIG</key><string>17700</string>
    <key>JARVIS_PORT_AUTH</key><string>17701</string>
    <key>JARVIS_PORT_LOGS</key><string>17702</string>
    <key>JARVIS_PORT_COMMAND_CENTER</key><string>17703</string>
    <key>JARVIS_PORT_LLM</key><string>17704</string>
    <key>JARVIS_PORT_WHISPER</key><string>17706</string>
    <key>JARVIS_PORT_TTS</key><string>17707</string>
    <key>JARVIS_PORT_NOTIFICATIONS</key><string>17712</string>
    <key>JARVIS_PORT_RECIPES</key><string>17030</string>
    <key>JARVIS_PORT_OCR</key><string>17031</string>
    <key>JARVIS_PORT_ADMIN</key><string>17710</string>
    <key>JARVIS_MQTT_ADDR</key><string>:11884</string>
    <key>JARVIS_MQTT_WS_ADDR</key><string>:19883</string>
    <key>JARVIS_AUTH_ADMIN_TOKEN</key><string>$(admin_token)</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
  <key>ProcessType</key><string>Interactive</string>
  <key>Umask</key><integer>63</integer>
  <key>SoftResourceLimits</key><dict><key>NumberOfFiles</key><integer>65536</integer></dict>
  <key>StandardOutPath</key><string>$JHOME/logs/jarvisd.log</string>
  <key>StandardErrorPath</key><string>$JHOME/logs/jarvisd.log</string>
</dict></plist>
EOF
}

json() { plutil -extract "$2" raw -o - "$1" 2>/dev/null || true; } # $1 file, $2 key path

wait_health() {
  for n in $(seq 1 60); do
    curl -fsS "http://127.0.0.1:$CFG/health" >/dev/null 2>&1 && { echo "health OK after ${n}s"; return 0; }
    sleep 1
  done
  echo "health FAILED"; tail -40 "$JHOME/logs/jarvisd.log" || true; return 1
}

cmd_up() {
  local mode=$1
  mkdir -p "$JHOME/logs"; admin_token >/dev/null
  case "$mode" in
    shell)
      ( set -a; eval "$(render_plist shell | sed -n 's|.*<key>\(JARVIS_[A-Z_]*\)</key><string>\(.*\)</string>|\1=\2|p')"
        nohup "$BIN" serve >>"$JHOME/logs/jarvisd.log" 2>&1 & echo $! > "$JHOME/i0-shell.pid" ) ;;
    agent)
      local p; p=$(plist_path agent); mkdir -p "$(dirname "$p")"
      (umask 077; render_plist agent > "$p"); plutil -lint "$p"
      launchctl bootstrap "gui/$UID_N" "$p" ;;
    daemon)
      local p tmp; p=$(plist_path daemon); tmp=$(mktemp)
      render_plist daemon > "$tmp"; plutil -lint "$tmp"
      sudo install -m 0600 -o root -g wheel "$tmp" "$p"; rm -f "$tmp"
      sudo launchctl bootstrap system "$p" ;;
  esac
  wait_health
}

cmd_down() {
  local mode=$1
  case "$mode" in
    shell) [ -f "$JHOME/i0-shell.pid" ] && kill "$(cat "$JHOME/i0-shell.pid")" && rm -f "$JHOME/i0-shell.pid" ;;
    agent) launchctl bootout "gui/$UID_N/$LABEL" || true; rm -f "$(plist_path agent)" ;;
    daemon) sudo launchctl bootout "system/$LABEL" || true; sudo rm -f "$(plist_path daemon)" ;;
  esac
  sleep 2
  # Any engine child still alive? (argv match on our home, no pgrep -f)
  ps -axo pid=,args= | awk -v h="$JHOME" 'index($0, h) && !/awk/ {print "still running:", $0}'
}

cmd_check() {
  echo "== health"; curl -fsS "http://127.0.0.1:$CFG/health"; echo
  echo "== process"
  local pid; pid=$(lsof -nP -t -iTCP:$CFG -sTCP:LISTEN | head -1)
  ps -o pid=,ppid=,user=,args= -p "$pid"
  echo "parent: $(ps -o comm= -p "$(ps -o ppid= -p "$pid" | tr -d ' ')")"
  launchctl print "system/$LABEL" 2>/dev/null | grep -E '^\s*(state|pid|domain|username|program) =' || true
  launchctl print "gui/$UID_N/$LABEL" 2>/dev/null | grep -E '^\s*(state|pid|domain|program) =' || true
  echo "== mdns log lines"; grep -i mdns "$JHOME/logs/jarvisd.log" | tail -5 || true
  echo "== dns-sd -B (5 s)"
  dns-sd -B _jarvis-config._tcp > "$JHOME/i0-browse.txt" 2>&1 & local b=$!; sleep 5; kill $b 2>/dev/null || true
  cat "$JHOME/i0-browse.txt"
  local inst; inst="Jarvis ($(hostname -s))"
  echo "== dns-sd -L \"$inst\" (4 s)"
  dns-sd -L "$inst" _jarvis-config._tcp local > "$JHOME/i0-lookup.txt" 2>&1 & b=$!; sleep 4; kill $b 2>/dev/null || true
  cat "$JHOME/i0-lookup.txt"
}

cmd_metal() {
  local tok app_key access tmp=$JHOME/i0-tmp.json
  tok=$(cat "$JHOME/i0-admin-token")
  # First superuser (only works on an empty DB), else log in.
  local pw; pw=$(cat "$JHOME/i0-pw" 2>/dev/null || { (umask 077; openssl rand -hex 12 > "$JHOME/i0-pw"); cat "$JHOME/i0-pw"; })
  local body="{\"email\":\"i0@example.com\",\"password\":\"$pw\"}"
  curl -sS -o "$tmp" -H 'Content-Type: application/json' -d "$body" "http://127.0.0.1:$AUTH/auth/setup" || true
  access=$(json "$tmp" access_token)
  if [ -z "$access" ]; then
    curl -sS -o "$tmp" -H 'Content-Type: application/json' -d "$body" "http://127.0.0.1:$AUTH/auth/login"
    access=$(json "$tmp" access_token)
  fi
  [ -n "$access" ] || { echo "no access token:"; cat "$tmp"; return 1; }
  local H="Authorization: Bearer $access"

  echo "== hardware"
  curl -fsS -H "$H" "http://127.0.0.1:$LLM/v1/hardware" -o "$tmp"
  echo "flavour=$(json "$tmp" hardware.flavour) device0=$(json "$tmp" hardware.devices.0.name) total_mb=$(json "$tmp" hardware.devices.0.total_mb)"

  echo "== install (Qwen2.5 0.5B Q4_K_M, assign live)"
  curl -sS -H "$H" -H 'Content-Type: application/json' -o "$tmp" \
    -d '{"repo":"Qwen/Qwen2.5-0.5B-Instruct-GGUF","file":"qwen2.5-0.5b-instruct-q4_k_m.gguf","kind":"llm","id":"qwen2.5-0.5b-i0","context_default":4096,"assign":["live"]}' \
    "http://127.0.0.1:$LLM/v1/models/install"
  cat "$tmp"; echo
  local id; id=$(json "$tmp" install.id)
  local st=""
  for _ in $(seq 1 600); do
    curl -fsS -H "$H" -o "$tmp" "http://127.0.0.1:$LLM/v1/models/installs/$id"
    st=$(json "$tmp" state)
    case "$st" in done|failed|cancelled) break ;; esac
    sleep 2
  done
  echo "install state=$st phase=$(json "$tmp" phase) flavour=$(json "$tmp" engine_flavour) error=$(json "$tmp" error) note=$(json "$tmp" note)"
  [ "$st" = done ] || return 1

  echo "== wait for label live ready"
  for _ in $(seq 1 120); do
    curl -fsS -H "$H" -o "$tmp" "http://127.0.0.1:$LLM/v1/models/labels"
    [ "$(json "$tmp" labels.0.state)" = ready ] && break
    sleep 2
  done
  echo "live state=$(json "$tmp" labels.0.state) endpoint=$(json "$tmp" labels.0.endpoint.base_url) reason=$(json "$tmp" labels.0.reason)"

  echo "== app client + chat through jarvisd"
  curl -sS -H "X-Jarvis-Admin-Token: $tok" -H 'Content-Type: application/json' -o "$tmp" \
    -d '{"app_id":"i0-spike","name":"I0 spike"}' "http://127.0.0.1:$AUTH/admin/app-clients"
  app_key=$(json "$tmp" key)
  [ -n "$app_key" ] && (umask 077; echo "$app_key" > "$JHOME/i0-app-key")
  app_key=$(cat "$JHOME/i0-app-key")
  curl -sS -H "X-Jarvis-App-Id: i0-spike" -H "X-Jarvis-App-Key: $app_key" -H 'Content-Type: application/json' \
    -d '{"model":"live","messages":[{"role":"user","content":"Reply with the single word OK."}],"max_tokens":16,"temperature":0}' \
    "http://127.0.0.1:$LLM/v1/chat/completions"; echo

  echo "== engine processes and their listen sockets"
  curl -fsS -H "$H" -o "$tmp" "http://127.0.0.1:$LLM/v1/hardware"
  local epid; epid=$(json "$tmp" engines.0.pid)
  echo "engine pid=$epid state=$(json "$tmp" engines.0.state) port=$(json "$tmp" engines.0.port)"
  ps -o pid=,ppid=,user=,args= -p "$epid" || true
  lsof -nP -a -p "$epid" -iTCP -sTCP:LISTEN || true
  echo "== engine output: Metal lines"
  local i=0 line
  while line=$(plutil -extract "engines.0.output.$i" raw -o - "$tmp" 2>/dev/null); do
    echo "$line"; i=$((i+1))
  done | grep -iE 'metal|MTL|GPU|offload|using device' | head -25
}

# Local Network privacy probe: make jarvisd itself open a unicast TCP connection to a LAN host
# (the HF-endpoint setting pointed at another box), then put the setting back. A blocked process
# gets "no route to host" / EHOSTUNREACH; an allowed one gets that host's HTTP answer.
cmd_lnp() {
  local target=${1:?LAN URL, e.g. http://10.0.0.122:7700}
  local tmp=$JHOME/i0-tmp.json pw access
  pw=$(cat "$JHOME/i0-pw")
  curl -sS -o "$tmp" -H 'Content-Type: application/json' -d "{\"email\":\"i0@example.com\",\"password\":\"$pw\"}" "http://127.0.0.1:$AUTH/auth/login"
  access=$(json "$tmp" access_token)
  local H="Authorization: Bearer $access"
  curl -sS -X PUT -H "$H" -H 'Content-Type: application/json' -d "{\"value\":\"$target\"}" "http://127.0.0.1:$LLM/settings/llm.hf_endpoint" >/dev/null
  echo "== jarvisd -> $target (expect that host's answer, not 'no route to host')"
  curl -sS -w '\nHTTP %{http_code}\n' -H "$H" "http://127.0.0.1:$LLM/v1/models/hf/i0/probe"
  curl -sS -X PUT -H "$H" -H 'Content-Type: application/json' -d '{"value":""}' "http://127.0.0.1:$LLM/settings/llm.hf_endpoint" >/dev/null
}

case "${1:-}" in
  up) cmd_up "${2:?mode}" ;;
  lnp) cmd_lnp "${2:-}" ;;
  down) cmd_down "${2:?mode}" ;;
  check) cmd_check ;;
  metal) cmd_metal ;;
  purge) rm -rf "$JHOME" ;;
  *) sed -n '2,16p' "$0"; exit 2 ;;
esac
