#!/usr/bin/env bash
# The Pi side of `voicelane --audio pi` (docs/decisions/2026-10-18-e2e-pi-voice-lane.md).
# voicelane copies it into the run's directory on the Pi (RUN, from mktemp) with the node build
# and the sandbox's node identity, and runs it over ssh:
#
#   pi.sh serve RUN CARD -- LISTEN-ARGS...  stop what would hear the test or holds the card,
#                                           run `jarvis-agent LISTEN-ARGS` until ssh's stdin
#                                           closes (or the node exits), then put the Pi back
#   pi.sh play RUN                          the WAV on stdin out of the speaker; prints the
#                                           reply tee's size before it played
#   pi.sh fetch RUN OFFSET                  the reply tee from OFFSET (S16_LE, 16 kHz, mono)
#
# CARD is the HAT's ALSA card index, or "auto" (the seeed card in /proc/asound/cards). The node
# and aplay run with HOME=RUN, so ALSA reads RUN/.asoundrc: the Pi's own ALSA config, the user's
# ~/.asoundrc and any node identity on the Pi are never read or changed. serve's lines for
# voicelane start with "e2e-pi: ".
set -u

cmd=${1:-} run=${2:-}
case $run in /*/jarvis-e2e-*) ;; *) echo "e2e-pi: bad run dir '$run'" >&2; exit 2 ;; esac
say() { echo "e2e-pi: $*"; }

# node_env runs a command with the run's HOME and XDG dirs, in place of the shell that calls it
# (`node_env … &` is then the command's own PID, for kill).
node_env() {
  exec env HOME="$run" XDG_CONFIG_HOME="$run/config" XDG_CACHE_HOME="$run/cache" \
    XDG_DATA_HOME="$run/data" XDG_STATE_HOME="$run/state" "$@"
}

# units SCOPE lists the active services and sockets of SCOPE (system, user) that could hear the
# test (a Jarvis node) or hold the sound card (an audio server).
units() {
  local s=()
  [ "$1" = user ] && s=(--user)
  systemctl "${s[@]}" list-units --type=service,socket --state=active --plain --no-legend \
    'jarvis*' 'pulseaudio*' 'pipewire*' 'wireplumber*' 2>/dev/null | awk '{print $1}'
}
state() { echo "system: $(units system | xargs); user: $(units user | xargs)"; }

STOPPED=()
stop() { # SCOPE UNIT
  if [ "$1" = system ]; then sudo -n systemctl stop "$2"; else systemctl --user stop "$2"; fi ||
    { say "could not stop $1 $2"; exit 1; }
  STOPPED+=("$1:$2")
  say "stopped $1 $2"
}

# free: the card opens for capture and playback (a second of silence), so nothing holds it.
free() {
  arecord -q -t raw -D "plughw:$CARD" -d 1 -f S16_LE -r 16000 -c 1 /dev/null 2>/dev/null &&
    aplay -q -t raw -D "plughw:$CARD" -d 1 -f S16_LE -r 16000 -c 1 /dev/zero 2>/dev/null
}

NODE=
restore() {
  local i s
  # ssh may be gone: what is said then goes nowhere, and must not stop the restore.
  trap - EXIT
  trap '' HUP INT TERM PIPE
  if [ -n "$NODE" ] && kill -0 "$NODE" 2>/dev/null; then
    kill -TERM "$NODE" 2>/dev/null
    for _ in $(seq 1 50); do kill -0 "$NODE" 2>/dev/null || break; sleep 0.1; done
    kill -KILL "$NODE" 2>/dev/null
    say "stopped the test node"
  fi
  [ -n "${CAT:-}" ] && kill "$CAT" 2>/dev/null
  # Back in the order they were: sockets and audio servers before the node that uses them.
  for ((i = ${#STOPPED[@]} - 1; i >= 0; i--)); do
    s=${STOPPED[i]}
    if [ "${s%%:*}" = system ]; then sudo -n systemctl start "${s#*:}"; else systemctl --user start "${s#*:}"; fi &&
      say "restored ${s%%:*} ${s#*:}" || say "could not restore ${s%%:*} ${s#*:}"
  done
  rm -rf "$run"
  say "after: $(state)"
  say "done"
}

serve() {
  exec 3<&0 # ssh's stdin: it closes when voicelane goes, however it goes
  CARD=$1
  shift
  [ "${1:-}" = -- ] && shift
  if [ "$CARD" = auto ]; then
    CARD=$(awk 'tolower($0) ~ /seeed/ && $1 ~ /^[0-9]+$/ {print $1; exit}' "${E2E_PI_CARDS:-/proc/asound/cards}")
    [ -n "$CARD" ] || { say "no seeed card in /proc/asound/cards (--pi-card N)"; rm -rf "$run"; exit 1; }
  fi
  case $CARD in '' | *[!0-9]*) say "bad card '$CARD'"; rm -rf "$run"; exit 1 ;; esac
  trap restore EXIT
  trap 'exit 1' HUP INT TERM PIPE
  say "card $CARD"
  say "before: $(state)"
  # A Jarvis node on the Pi would hear the cases too, and answer through its own server.
  local u
  for u in $(units system | grep '^jarvis'); do stop system "$u"; done
  for u in $(units user | grep '^jarvis'); do stop user "$u"; done
  # An audio server only when the card is busy: sockets first, so nothing starts it again.
  if ! free; then
    for u in $(units user | grep -v '^jarvis' | sort -t. -k2r); do stop user "$u"; done
    for u in $(units system | grep -v '^jarvis' | sort -t. -k2r); do stop system "$u"; done
    sleep 1
    if ! free; then
      say "card $CARD is still busy: $(fuser -v /dev/snd/pcmC"$CARD"D* 2>&1 | xargs)"
      exit 1
    fi
  fi
  # The node's mic and speaker, and the test's own speaker (e2e_play). The test plays into the
  # card's dmix beside the node, never through e2e_node_out: an echo canceller whose reference is
  # the node's own playback can't cancel the test's utterance. e2e_tee writes what the node
  # plays (only that) to RUN/reply.raw, appended across the run.
  local key=$((41560 + $$ % 1000 * 2))
  cat >"$run/.asoundrc" <<EOF
pcm.e2e_dmix { type dmix; ipc_key $key; ipc_perm 0600; slave.pcm "hw:$CARD,0" }
pcm.e2e_dsnoop { type dsnoop; ipc_key $((key + 1)); ipc_perm 0600; slave.pcm "hw:$CARD,0" }
pcm.e2e_mic { type plug; slave.pcm "e2e_dsnoop" }
pcm.e2e_play { type plug; slave.pcm "e2e_dmix" }
pcm.e2e_tee { type file; slave.pcm "e2e_play"; file "|cat >> $run/reply.raw"; format "raw" }
pcm.e2e_node_out { type plug; slave { pcm "e2e_tee"; format S16_LE; rate 16000; channels 1 } }
EOF
  : >"$run/reply.raw"
  mkdir -p "$run/config" "$run/cache" "$run/data" "$run/state"
  node_env "$run/jarvis-agent" "$@" </dev/null &
  NODE=$!
  sleep "${E2E_PI_SETTLE:-3}" # the detector and the devices load
  kill -0 "$NODE" 2>/dev/null || { wait "$NODE"; say "the node exited ($?)"; exit 1; }
  say "ready"
  cat <&3 >/dev/null & # a background job's stdin is /dev/null unless it is named
  CAT=$!
  wait -n "$NODE" "$CAT"
  if ! kill -0 "$NODE" 2>/dev/null; then say "the node exited"; exit 1; fi
  exit 0
}

case $cmd in
  serve) serve "${@:3}" ;;
  play)
    cat >"$run/utterance.wav"
    stat -c %s "$run/reply.raw" 2>/dev/null || echo 0
    (node_env aplay -q -D e2e_play "$run/utterance.wav")
    ;;
  fetch) tail -c +$((${3:-0} + 1)) "$run/reply.raw" 2>/dev/null || true ;;
  *) echo "e2e-pi: serve, play or fetch" >&2; exit 2 ;;
esac
