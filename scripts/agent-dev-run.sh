#!/usr/bin/env bash
# Build, enroll (first time only) and run the Jarvis desktop agent, on Linux or macOS.
#
#   scripts/agent-dev-run.sh --server http://192.0.2.10:7700 --email you@example.com   # first time
#   scripts/agent-dev-run.sh                       # afterwards: build and run in the foreground
#   scripts/agent-dev-run.sh --install             # start at login instead (user session only)
#   scripts/agent-dev-run.sh --uninstall           # stop starting at login (keeps the enrollment)
#   scripts/agent-dev-run.sh --status              # what the agent can do right now, as the agent
#   scripts/agent-dev-run.sh --install --browser   # ... and set up browser control without asking
#
# Options:
#   --server URL      jarvisd's address (only needed to enroll)
#   --email ADDR      your Jarvis account; the password is prompted for, never echoed
#   --household ID    only if your account belongs to several households
#   --install         install as a systemd user unit (Linux) or LaunchAgent (macOS) and start it
#   --uninstall       remove that unit/agent
#   --status          print the agent's capabilities (on macOS run as Jarvis Agent via launchd:
#                     from a terminal, macOS would report the terminal's permissions instead)
#   --browser         set up browser control (Firefox / Chrome-family extension + native host)
#                     without asking; --no-browser: don't offer it. Default: ask when interactive
#                     (on first enrollment, and on --install for the installed binary).
#   -- ARGS           extra `jarvis-agent run` flags, e.g. -- --allow-open-any, or -- --voice
#                     (the "hey jarvis" pipeline; foreground runs only, --install doesn't pass flags)
#
# macOS: the build is "~/Applications/Jarvis Agent.app", signed with your Developer ID (or
# Apple Development) identity when the keychain has one, so the Accessibility grant survives
# rebuilds; with no identity it is ad-hoc signed and every rebuild needs re-granting. The agent
# runs under launchd even in the foreground, so macOS attributes permissions to Jarvis Agent,
# not to your terminal. Ctrl-C stops it.
#
# Set JARVIS_AGENT_SIGN_IDENTITY to pick a signing identity ("-" forces ad-hoc).
set -euo pipefail

SERVER='' EMAIL='' HOUSEHOLD='' MODE=run BROWSER=ask
while [ $# -gt 0 ]; do
  case $1 in
    --server) SERVER=$2; shift 2 ;;
    --email) EMAIL=$2; shift 2 ;;
    --household) HOUSEHOLD=$2; shift 2 ;;
    --install) MODE=install; shift ;;
    --uninstall) MODE=uninstall; shift ;;
    --status) MODE=status; shift ;;
    --browser) BROWSER=yes; shift ;;
    --no-browser) BROWSER=no; shift ;;
    --) shift; break ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) echo "agent-dev-run: unknown argument $1 (run flags go after --)" >&2; exit 2 ;;
  esac
done

say() { echo "agent-dev-run: $*" >&2; }
die() { say "$*"; exit 1; }

ROOT=$(cd "$(dirname "$0")/.." && pwd)
OS=$(uname -s)
LABEL=io.jarvis.agent
DEV_LABEL=io.jarvis.agent.dev
APP="$HOME/Applications/Jarvis Agent.app"
UNIT="$HOME/.config/systemd/user/jarvis-agent.service"
AGENT_PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG_DIR="$HOME/Library/Logs"

case $OS in Linux|Darwin) ;; *) die "Linux and macOS only (Windows: see docs/agent/DESIGN.md)" ;; esac

# ax_help explains a missing macOS Accessibility grant, including the stale-entry case: a grant
# made for an earlier (differently signed) build stays switched on in Settings but doesn't
# apply to this one. It only prints; resetting TCC is the user's call.
ax_help() {
  cat >&2 <<'EOF'

agent-dev-run: Accessibility is NOT granted to Jarvis Agent, so window control (list/focus/minimise/close) is off.
  1. System Settings → Privacy & Security → Accessibility: turn on "Jarvis Agent".
  2. If it already looks on, the switch belongs to an earlier, differently signed build. Reset it:
       tccutil reset Accessibility io.jarvis.agent
       tccutil reset AppleEvents io.jarvis.agent
     then rerun scripts/agent-dev-run.sh and turn "Jarvis Agent" on when macOS asks.
  3. Check: scripts/agent-dev-run.sh --status   (expect "accessibility: true" and windows.* capabilities)
EOF
}

# --- uninstall needs no build ---
if [ "$MODE" = uninstall ]; then
  if [ "$OS" = Darwin ]; then
    launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
    rm -f "$AGENT_PLIST"
  else
    systemctl --user disable --now jarvis-agent 2>/dev/null || true
    rm -f "$UNIT"
    systemctl --user daemon-reload
  fi
  say "uninstalled; the enrollment is kept (re-enroll with: jarvis-agent enroll --force ...)"
  say "browser control stays registered; to remove it: jarvis-agent browser uninstall"
  exit 0
fi

# --- status needs no build either ---
if [ "$MODE" = status ]; then
  if [ "$OS" = Linux ]; then
    [ -x "$ROOT/dist/jarvis-agent" ] || die "not built yet: run scripts/agent-dev-run.sh first"
    exec "$ROOT/dist/jarvis-agent" status
  fi
  BIN="$APP/Contents/MacOS/jarvis-agent"
  [ -x "$BIN" ] || die "not built yet: run scripts/agent-dev-run.sh first"
  mkdir -p "$ROOT/dist"
  out="$ROOT/dist/status.log"
  : > "$out"
  sp="$ROOT/dist/$LABEL.status.plist"
  cat > "$sp" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL.status</string>
  <key>ProgramArguments</key><array><string>$BIN</string><string>status</string></array>
  <key>RunAtLoad</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>$out</string>
  <key>StandardErrorPath</key><string>$out</string>
</dict></plist>
EOF
  launchctl bootout "gui/$(id -u)/$LABEL.status" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$sp"
  # status prints "enrollment:" or "jarvisd:" last. Wait for that line (the job may not have
  # started yet when bootstrap returns), up to 30 s, and only then unload the job.
  for _ in $(seq 1 60); do
    grep -qE '^(enrollment|jarvisd):' "$out" 2>/dev/null && break
    sleep 0.5
  done
  launchctl bootout "gui/$(id -u)/$LABEL.status" 2>/dev/null || true
  [ -s "$out" ] || die "the agent printed nothing within 30 s (see: launchctl print gui/$(id -u)/$LABEL.status)"
  cat "$out"
  grep -q '^accessibility: false' "$out" && ax_help
  exit 0
fi

# --- build ---
gocmd() {
  if command -v go >/dev/null 2>&1; then GOTOOLCHAIN=local go "$@"
  elif [ -x /opt/homebrew/bin/go ]; then GOTOOLCHAIN=local /opt/homebrew/bin/go "$@"
  elif [ -x /usr/local/go/bin/go ]; then GOTOOLCHAIN=local /usr/local/go/bin/go "$@"
  elif command -v mise >/dev/null 2>&1; then GOTOOLCHAIN=local mise exec go -- go "$@"
  else die "Go is not installed (https://go.dev/dl/)"; fi
}
VERSION="dev-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
mkdir -p "$ROOT/dist"
# The voice pipeline embeds onnxruntime (internal/voice/sherpa/libs, gitignored): fetch it once.
plat="$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
"$ROOT/scripts/fetch-sherpa-libs.sh" "$plat" >&2 || die "could not fetch the native voice libraries for $plat"
say "building $VERSION"
(cd "$ROOT" && CGO_ENABLED=0 gocmd build -trimpath -ldflags "-X main.version=$VERSION" -o dist/jarvis-agent ./cmd/jarvis-agent)

if [ "$OS" = Darwin ]; then
  mkdir -p "$APP/Contents/MacOS"
  cp "$ROOT/dist/jarvis-agent" "$APP/Contents/MacOS/jarvis-agent"
  plist=$(<"$ROOT/internal/agent/deploy/macos/Info.plist")
  printf '%s\n' "${plist//__VERSION__/${VERSION#dev-}}" > "$APP/Contents/Info.plist"
  ID=${JARVIS_AGENT_SIGN_IDENTITY:-}
  if [ -z "$ID" ]; then
    ids=$(security find-identity -v -p codesigning 2>/dev/null || true)
    ID=$(printf '%s\n' "$ids" | sed -n 's/.*"\(Developer ID Application:[^"]*\)".*/\1/p' | head -n 1)
    [ -n "$ID" ] || ID=$(printf '%s\n' "$ids" | sed -n 's/.*"\(Apple Development:[^"]*\)".*/\1/p' | head -n 1)
    [ -n "$ID" ] || ID=-
  fi
  if ! codesign --force --timestamp=none --identifier "$LABEL" --sign "$ID" "$APP" 2>"$ROOT/dist/codesign.err"; then
    cat "$ROOT/dist/codesign.err" >&2
    [ "$ID" = - ] && die "ad-hoc signing failed"
    die "signing with \"$ID\" failed (over SSH the keychain may be locked: run this in a local Terminal, or set JARVIS_AGENT_SIGN_IDENTITY=- for ad-hoc)"
  fi
  if [ "$ID" = - ]; then
    say "signed ad-hoc: the Accessibility grant will need re-enabling after every rebuild"
  else
    say "signed as $ID"
  fi
  BIN="$APP/Contents/MacOS/jarvis-agent"
else
  BIN="$ROOT/dist/jarvis-agent"
fi

# offer_browser BIN: set up browser control for the binary at BIN (asks, default yes, unless --browser/--no-browser).
offer_browser() {
  case $BROWSER in
    no) return 0 ;;
    ask)
      if [ ! -t 0 ]; then say "browser control (optional): $1 browser install"; return 0; fi
      printf 'Set up browser control, so Jarvis can use your Firefox / Chrome tabs (list, open, read, pause video)? [Y/n] ' >&2
      # Enter means yes; input that ends without an answer means no.
      read -r ans || ans=n
      case $ans in ''|[yY]|[yY][eE][sS]) ;; *) say "skipped; later: $1 browser install"; return 0 ;; esac ;;
  esac
  "$1" browser install || say "browser setup failed (see above); retry with: $1 browser install"
}

# --- enroll, first time only ---
enroll=(enroll --if-needed)
[ -n "$SERVER" ] && enroll+=(--server "$SERVER")
[ -n "$EMAIL" ] && enroll+=(--email "$EMAIL")
[ -n "$HOUSEHOLD" ] && enroll+=(--household "$HOUSEHOLD")
# Browser control registers the binary browsers will start: for --install that's the installed
# copy, so it's offered after installing. A plain run lets a first enrollment ask; an explicit
# --browser is handled below, enrolled or not.
if [ "$MODE" = install ] || [ "$BROWSER" != ask ]; then enroll+=(--no-browser); fi
"$BIN" "${enroll[@]}" || die "not enrolled; pass --server URL --email ADDR"
if [ "$MODE" = run ] && [ "$BROWSER" = yes ]; then offer_browser "$BIN"; fi

# --- install: start at login, in the user's session ---
if [ "$MODE" = install ]; then
  if [ "$OS" = Darwin ]; then
    mkdir -p "$(dirname "$AGENT_PLIST")" "$LOG_DIR"
    t=$(<"$ROOT/internal/agent/deploy/launchd/io.jarvis.agent.plist")
    t=${t//__AGENT_BIN__/$BIN}
    printf '%s\n' "${t//__LOG_DIR__/$LOG_DIR}" > "$AGENT_PLIST"
    launchctl bootout "gui/$(id -u)/$DEV_LABEL" 2>/dev/null || true
    launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
    launchctl bootstrap "gui/$(id -u)" "$AGENT_PLIST"
    say "installed and started; logs: tail -f $LOG_DIR/jarvis-agent.err.log"
    offer_browser "$BIN"
  else
    mkdir -p "$HOME/.local/bin" "$(dirname "$UNIT")"
    install -m 0755 "$BIN" "$HOME/.local/bin/jarvis-agent"
    t=$(<"$ROOT/internal/agent/deploy/systemd/jarvis-agent.service")
    printf '%s\n' "${t//__AGENT_BIN__/$HOME/.local/bin/jarvis-agent}" > "$UNIT"
    systemctl --user daemon-reload
    systemctl --user enable jarvis-agent >/dev/null
    systemctl --user restart jarvis-agent
    say "installed and started; logs: journalctl --user -u jarvis-agent -f"
    say "it starts with your graphical session; it stops when you log out unless: loginctl enable-linger $USER"
    offer_browser "$HOME/.local/bin/jarvis-agent"
  fi
  exit 0
fi

# --- run in the foreground ---
if [ "$OS" = Linux ]; then
  if systemctl --user is-active --quiet jarvis-agent 2>/dev/null; then
    die "the installed agent is running; stop it first (systemctl --user stop jarvis-agent): two copies would fight over the node's session"
  fi
  exec "$BIN" run "$@"
fi

# macOS: run under launchd so TCC attributes Accessibility to Jarvis Agent, not to this terminal.
if launchctl print "gui/$(id -u)/$LABEL" >/dev/null 2>&1; then
  die "the installed agent is running; run --uninstall first: two copies would fight over the node's session"
fi
xml() { local s=${1//&/&amp;}; s=${s//</&lt;}; printf '%s' "${s//>/&gt;}"; }
args="<string>$(xml "$BIN")</string><string>run</string>"
for a in "$@"; do args+="<string>$(xml "$a")</string>"; done
LOG="$ROOT/dist/jarvis-agent.log"
DEV_PLIST="$ROOT/dist/$DEV_LABEL.plist"
cat > "$DEV_PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$DEV_LABEL</string>
  <key>ProgramArguments</key><array>$args</array>
  <key>RunAtLoad</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>AssociatedBundleIdentifiers</key><string>$LABEL</string>
  <key>StandardOutPath</key><string>$(xml "$LOG")</string>
  <key>StandardErrorPath</key><string>$(xml "$LOG")</string>
</dict></plist>
EOF
: > "$LOG"
launchctl bootout "gui/$(id -u)/$DEV_LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$DEV_PLIST"
tail -n +1 -F "$LOG" 2>/dev/null &
TAIL=$!
# shellcheck disable=SC2329 # called from the traps below
stop() {
  trap - INT TERM EXIT
  launchctl bootout "gui/$(id -u)/$DEV_LABEL" 2>/dev/null || true  # SIGTERM: the agent exits cleanly
  sleep 0.5
  kill "$TAIL" 2>/dev/null || true
}
trap 'stop; exit 0' INT TERM
trap stop EXIT
say "running (Ctrl-C to stop); log: $LOG"
sleep 2
for _ in $(seq 1 10); do grep -q 'jarvis-agent starting' "$LOG" 2>/dev/null && break; sleep 0.5; done
grep -q 'Accessibility not granted' "$LOG" 2>/dev/null && ax_help
while launchctl print "gui/$(id -u)/$DEV_LABEL" 2>/dev/null | grep -q 'state = running'; do sleep 1; done
code=$(launchctl print "gui/$(id -u)/$DEV_LABEL" 2>/dev/null | sed -n 's/.*last exit code = \(.*\)/\1/p' | head -n 1)
say "the agent exited (${code:-unknown})"
exit 1
