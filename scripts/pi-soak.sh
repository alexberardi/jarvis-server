#!/usr/bin/env bash
# The Go Pi node's soak (go-pi-node.md §3.19): the installed jarvis-agent.service as the only
# node on a lab Pi for DURATION, sampled every INTERVAL, with a scripted voice turn per command
# family every TURNS_EVERY. Runs on the Pi, as the node's user (sudo for systemctl and the
# journal), after `sudo sh install-pi.sh` (without --activate).
#
#   scripts/pi-soak.sh run --lab-host NAME [--duration 24h] [--interval 60] [--turns-every 4h]
#                          [--wavs DIR] [--out DIR] [--turn-wait 90]
#   scripts/pi-soak.sh summarize OUT     the summary again from OUT's files
#
# run refuses unless the hostname is NAME (the lab Pi; never the prod kitchen node, and never a
# host whose name says kitchen). It stops jarvis-node.service, starts jarvis-agent.service and
# puts both back as they were on any exit (end, Ctrl-C, TERM, an error).
#
# OUT (default ~/pi-soak-<date>) gets:
#   samples.csv   one row per INTERVAL: the agent's CPU (% of one core, from /proc/PID/stat),
#                 RSS/PSS/anonymous/swap (smaps_rollup, read with sudo; empty when unreadable), the same for its child processes (the
#                 command sidecar), MemAvailable, swap in use, load, SoC temperature, throttling,
#                 the unit's NRestarts
#   turns.log     one TSV row per scripted turn: time, family, pass/FAIL, outcome, transcript, reply
#   journal.log   the unit's journal for the run (taken after the stop, for the capture stats)
#   summary.md    the numbers for go-pi-node.md §3.19's table
#
# A scripted turn is DIR/<family>.wav played out of the HAT's speaker (PLAY, default
# `aplay -q -D output`) for the running node to hear; it passes when the node logs a voice turn
# with outcome=spoken within --turn-wait seconds and, if DIR/<family>.expect exists, the line
# matches that extended regex (case-insensitive).
#
# Tests (scripts/pi-soak-check.sh) point SOAK_PROC, SOAK_SYS at fixtures, put stand-ins for
# sudo, systemctl, journalctl and hostname on PATH, and shorten SOAK_FIRST_TURN (300 s) and
# SOAK_TURN_GAP (20 s).
set -uo pipefail

PROC=${SOAK_PROC:-/proc}
SYS=${SOAK_SYS:-/sys}
AGENT=jarvis-agent.service
PY=jarvis-node.service
PLAY=${PLAY:-aplay -q -D output}
FIRST_TURN=${SOAK_FIRST_TURN:-300} # the first round 5 minutes in, once the sidecar is up
TURN_GAP=${SOAK_TURN_GAP:-20}
PY_BASELINE="Python node, Zero 2 W, idle listening (§3.11): 87.1% of one core, 129 MB anonymous"

say() { echo "pi-soak: $*" >&2; }
die() { say "$*"; exit 1; }

# secs 24h|90m|30s|45 → seconds.
secs() {
  case $1 in
    *h) echo $((${1%h} * 3600)) ;;
    *m) echo $((${1%m} * 60)) ;;
    *s) echo "${1%s}" ;;
    *[!0-9]* | '') return 1 ;;
    *) echo "$1" ;;
  esac
}

# --- Sampling --------------------------------------------------------------------------------

TCK=$(getconf CLK_TCK 2>/dev/null || echo 100)

# ticks PID → utime+stime (fields 14 and 15; the comm in field 2 may hold spaces).
ticks() {
  local s
  s=$(cat "$PROC/$1/stat" 2>/dev/null) || return 1
  s=${s##*) }
  # shellcheck disable=SC2086 # split the fields after the comm
  set -- $s
  echo $((${12} + ${13}))
}

# children PID → every descendant's PID, one line (stat's field 1 is the PID, field 4 after the
# comm the parent's).
children() {
  cat "$PROC"/[0-9]*/stat 2>/dev/null | awk -v root="$1" '
    {s=$0; sub(/.*\) /, "", s); split(s, f, " "); kid[f[2]]=kid[f[2]] " " $1}
    END{q=root; out=""
      while (q != "") {
        n=split(q, a, " "); q=""
        for (i=1; i<=n; i++) if (a[i] in kid) { out=out kid[a[i]]; q=q kid[a[i]] }
      }
      print out}' | xargs -n1 | sort -n | xargs
}

# mem PID → "rss pss anon swap" in kB from smaps_rollup, read with sudo: the agent is non-dumpable
# under its unit, so the file is EACCES to the node's own user. Fails (no output) when unreadable.
mem() {
  local s
  s=$(sudo -n cat "$PROC/$1/smaps_rollup" 2>/dev/null) || return 1
  awk '/^Rss:/{r=$2; seen=1} /^Pss:/{p=$2} /^Anonymous:/{a=$2} /^Swap:/{s=$2}
       END{if (!seen) exit 1; printf "%d %d %d %d\n", r, p, a, s}' <<<"$s"
}

# warn_mem PID → says once per run that PID's memory could not be read.
MEM_WARNED=0
warn_mem() {
  [ "$MEM_WARNED" = 1 ] && return
  MEM_WARNED=1
  say "cannot read $PROC/$1/smaps_rollup with sudo -n: memory columns are left empty while it fails"
}

HEADER=ts,pid,agent_cpu_pct,agent_rss_kb,agent_pss_kb,agent_anon_kb,agent_swap_kb,side_procs,side_cpu_pct,side_rss_kb,side_pss_kb,mem_avail_kb,swap_used_kb,load1,temp_c,throttled,nrestarts

# The previous sample, for the CPU deltas: a new PID (a restart) or a new child set leaves the
# CPU column empty for one row.
LAST_T=0 LAST_PID=0 LAST_A=0 LAST_KIDS='' LAST_K=0

# sample → one CSV row on stdout.
sample() {
  local now pid a k=0 kids n=0 c acpu='' kcpu='' r p an sw sr=0 sp=0 m mp avail swapu load temp thr nr
  now=$(date +%s)
  pid=$(systemctl show -p MainPID --value "$AGENT" 2>/dev/null)
  nr=$(systemctl show -p NRestarts --value "$AGENT" 2>/dev/null)
  read -r r p an sw <<<"0 0 0 0"
  if [ -n "$pid" ] && [ "$pid" != 0 ] && a=$(ticks "$pid"); then
    # An unreadable smaps_rollup leaves the columns empty: never zeros that look like data.
    m=$(mem "$pid") || warn_mem "$pid"
    read -r r p an sw <<<"$m"
    kids=$(children "$pid")
    for c in $kids; do
      if ! m=$(mem "$c"); then
        [ -e "$PROC/$c" ] || continue # it exited since children listed it
        warn_mem "$c"
        sr='' sp=''
      fi
      n=$((n + 1))
      k=$((k + $(ticks "$c" || echo 0)))
      read -r m mp _ <<<"$m"
      [ -z "$sr" ] || sr=$((sr + m)) sp=$((sp + mp))
    done
    if [ "$pid" = "$LAST_PID" ] && [ "$now" -gt "$LAST_T" ]; then
      acpu=$(awk -v d=$((a - LAST_A)) -v t="$TCK" -v s=$((now - LAST_T)) 'BEGIN{printf "%.1f", 100*d/t/s}')
      [ "$kids" = "$LAST_KIDS" ] &&
        kcpu=$(awk -v d=$((k - LAST_K)) -v t="$TCK" -v s=$((now - LAST_T)) 'BEGIN{printf "%.1f", 100*d/t/s}')
    fi
    LAST_PID=$pid LAST_A=$a LAST_K=$k LAST_KIDS=$kids
  else
    pid=0 LAST_PID=0
  fi
  LAST_T=$now
  avail=$(awk '/^MemAvailable:/{print $2}' "$PROC/meminfo")
  swapu=$(awk '/^SwapTotal:/{t=$2} /^SwapFree:/{f=$2} END{print t-f}' "$PROC/meminfo")
  load=$(awk '{print $1}' "$PROC/loadavg")
  temp=$(awk '{printf "%.1f", $1/1000}' "$SYS/class/thermal/thermal_zone0/temp" 2>/dev/null)
  thr=$(vcgencmd get_throttled 2>/dev/null | sed 's/^throttled=//')
  echo "$now,$pid,$acpu,$r,$p,$an,$sw,$n,$kcpu,$sr,$sp,$avail,$swapu,$load,$temp,$thr,$nr"
}

# --- Scripted turns --------------------------------------------------------------------------

# turn FAMILY WAV → one turns.log row; the node must log a spoken voice turn within TURN_WAIT.
turn() {
  local fam=$1 wav=$2 t0 line='' outcome tr reply ok=FAIL deadline expect
  t0=$(date +%s)
  # shellcheck disable=SC2086 # PLAY is a command line
  $PLAY "$wav" || say "could not play $wav"
  deadline=$((t0 + TURN_WAIT))
  while [ "$(date +%s)" -le "$deadline" ]; do
    line=$(sudo -n journalctl -u "$AGENT" --since "@$t0" -o cat --no-pager 2>/dev/null |
      grep -m1 'msg="voice turn"')
    [ -n "$line" ] && break
    sleep 2
  done
  outcome=$(field outcome "$line")
  tr=$(field transcript "$line")
  reply=$(field reply "$line")
  if [ "$outcome" = spoken ]; then
    ok=pass
    expect=${wav%.wav}.expect
    if [ -f "$expect" ] && ! grep -Eqi -f "$expect" <<<"$line"; then ok=FAIL; fi
  fi
  [ -n "$line" ] || outcome=no_turn
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$(date -d "@$t0" '+%F %T' 2>/dev/null || echo "$t0")" \
    "$fam" "$ok" "$outcome" "$tr" "$reply" >>"$OUT/turns.log"
  say "turn $fam: $ok ($outcome) \"$tr\" → \"$reply\""
}

# field KEY LINE → KEY's value in a slog text line (quoted or bare).
field() {
  local v
  v=$(grep -o " $1=\"\([^\"\\\\]\|\\\\.\)*\"" <<<" $2" | head -1)
  if [ -n "$v" ]; then
    v=${v#* "$1"=\"}
    echo "${v%\"}"
  else
    grep -o " $1=[^ ]*" <<<" $2" | head -1 | sed "s/^ $1=//"
  fi
}

turn_round() {
  local wav
  for wav in "$WAVS"/*.wav; do
    [ -f "$wav" ] || continue
    turn "$(basename "$wav" .wav)" "$wav"
    sleep "$TURN_GAP" # the follow-up window closed and the room quiet before the next one
  done
}

# --- Summary ---------------------------------------------------------------------------------

summarize() {
  local out=$1 j=$1/journal.log p95
  [ -f "$out/samples.csv" ] || die "no $out/samples.csv"
  [ -f "$j" ] || : >"$j"
  [ -f "$out/turns.log" ] || : >"$out/turns.log"
  # mawk (Raspberry Pi OS's awk) has no asort: the p95 comes from sort.
  p95=$(awk -F, 'NR>1 && $3!=""{print $3}' "$out/samples.csv" | sort -n |
    awk '{v[NR]=$1} END{i=int(0.95*NR+0.5); if (i<1) i=1; print NR?v[i]:0}')
  {
    echo "# Go Pi node soak: $(cat "$out/host" 2>/dev/null || echo '?')"
    echo
    cat "$out/meta" 2>/dev/null
    echo
    echo "| | value |"
    echo "|---|---|"
    awk -F, -v p95="$p95" 'NR==1{next}
      {n++; if (first=="") first=$1; last=$1}
      $2==0{down++}
      $2!=0 && ($4=="" || ($8>0 && $10=="")){unread++}
      $3!=""{nc++; cs+=$3; if ($3>cmax) cmax=$3}
      $9!=""{ks+=$9; nk++}
      $4>0{rs+=$4; nr++; if ($4>rmax) rmax=$4; if (rmin==""||$4<rmin) rmin=$4; if (rfirst=="") rfirst=$4; rlast=$4}
      $6>0{as+=$6; na++; if ($6>amax) amax=$6; if (afirst=="") afirst=$6; alast=$6}
      $10>0{ss+=$10; ns++; if ($10>smax) smax=$10}
      {if ($13>swmax) swmax=$13; if ($15>tmax) tmax=$15; if ($16!="" && $16!="0x0") thr=thr " " $16
       if (nr0=="") nr0=$17; nr1=$17}
      END{
        printf "| Duration | %.1f h (%d samples, %d with the agent down) |\n", (last-first)/3600, n, down
        printf "| Agent CPU, %% of one core: mean / p95 / max | %.1f / %.1f / %.1f |\n", nc?cs/nc:0, p95, cmax
        if (nr || !unread) printf "| Agent RSS MB: min / mean / max (first → last) | %.1f / %.1f / %.1f (%.1f → %.1f) |\n", rmin/1024, nr?rs/nr/1024:0, rmax/1024, rfirst/1024, rlast/1024
        else print "| Agent RSS MB: min / mean / max (first → last) | unreadable |"
        if (na || !unread) printf "| Agent anonymous MB: mean / max (first → last) | %.1f / %.1f (%.1f → %.1f) |\n", na?as/na/1024:0, amax/1024, afirst/1024, alast/1024
        else print "| Agent anonymous MB: mean / max (first → last) | unreadable |"
        printf "| Samples with the agent up but its memory unreadable | %d |\n", unread
        printf "| Sidecar CPU %% mean; RSS MB mean / max | %.1f; %.1f / %.1f |\n", nk?ks/nk:0, ns?ss/ns/1024:0, smax/1024
        printf "| Swap in use, max MB | %.1f |\n", swmax/1024
        printf "| SoC temperature, max °C; throttling | %.1f; %s |\n", tmax, thr==""?"none":thr
        printf "| Unit restarts (NRestarts) | %d |\n", nr1-nr0
      }' "$out/samples.csv"
    awk '
      /msg=wake /{w++}
      /msg="voice turn"/{t++; match($0, / outcome=[^ ]*/); o=substr($0, RSTART+9, RLENGTH-9); oc[o]++}
      /msg="voice: capture stats"/{match($0, / overruns=[0-9]+/); ov+=substr($0, RSTART+10, RLENGTH-10)
                                   match($0, / dropped_chunks=[0-9]+/); dr+=substr($0, RSTART+16, RLENGTH-16)}
      /msg="voice: pipeline stopped; restarting"/{pr++}
      / level=ERROR /{e++}
      / level=WARN /{wn++}
      /panic:/{pa++}
      END{
        printf "| ALSA overruns / dropped chunks | %d / %d |\n", ov, dr
        printf "| Voice pipeline restarts | %d |\n", pr
        printf "| Wakes / voice turns | %d / %d |\n", w, t
        s=""; for (k in oc) s=s sprintf("%s %d, ", k, oc[k]); sub(/, $/, "", s)
        printf "| Turn outcomes | %s |\n", s==""?"none":s
        printf "| False wakes (non_speech + not_for_me) | %d |\n", oc["non_speech"]+oc["not_for_me"]
        printf "| Errors / warnings / panics in the log | %d / %d / %d |\n", e, wn, pa
      }' "$j"
    awk -F'\t' '{n++; if ($3=="pass") p++}
      END{printf "| Scripted turns passed | %d / %d |\n", p, n}' "$out/turns.log"
    echo
    echo "Baseline: $PY_BASELINE."
    echo
    echo "## Scripted turns by family"
    echo
    echo "| family | passed | outcomes |"
    echo "|---|---|---|"
    awk -F'\t' '{n[$2]++; if ($3=="pass") p[$2]++; o[$2]=o[$2] (o[$2]==""?"":" ") $4}
      END{for (f in n) printf "| %s | %d / %d | %s |\n", f, p[f], n[f], o[f]}' "$out/turns.log" | sort
    echo
    echo "False wakes count every non_speech and not_for_me turn, scripted or not; a scripted turn that"
    echo "failed is listed in turns.log with what the node heard."
  } >"$out/summary.md"
  say "wrote $out/summary.md"
}

# --- run -------------------------------------------------------------------------------------

active() { systemctl is-active --quiet "$1"; }

WAS_PY=0 WAS_AGENT=0 STARTED=0 RESTORED=0
restore() {
  [ "$RESTORED" = 1 ] && return
  RESTORED=1
  trap '' INT TERM HUP
  if [ "$STARTED" = 1 ]; then
    if [ "$WAS_AGENT" = 0 ]; then sudo -n systemctl stop "$AGENT" && say "stopped $AGENT"; fi
    END=$(date +%s)
    # shellcheck disable=SC2024 # the file is the user's; only journalctl needs root
    sudo -n journalctl -u "$AGENT" --since "@$START" --until "@$((END + 5))" -o short-iso --no-pager \
      >"$OUT/journal.log" 2>/dev/null || say "could not read the journal"
  fi
  if [ "$WAS_PY" = 1 ]; then
    if sudo -n systemctl start "$PY"; then say "started $PY again"; else say "COULD NOT START $PY: start it by hand"; fi
  fi
  [ "$STARTED" = 1 ] && summarize "$OUT"
}

run() {
  local lab='' dur=24h iv=60 every=4h host now next_turn end
  WAVS='' OUT='' TURN_WAIT=90
  while [ $# -gt 0 ]; do
    case $1 in
      --lab-host) lab=$2; shift 2 ;;
      --duration) dur=$2; shift 2 ;;
      --interval) iv=$2; shift 2 ;;
      --turns-every) every=$2; shift 2 ;;
      --wavs) WAVS=$2; shift 2 ;;
      --out) OUT=$2; shift 2 ;;
      --turn-wait) TURN_WAIT=$2; shift 2 ;;
      *) die "unknown argument $1" ;;
    esac
  done
  host=$(hostname)
  [ -n "$lab" ] || die "--lab-host NAME is required (the lab Pi's hostname)"
  [ "$host" = "$lab" ] || die "this is $host, not the lab Pi $lab: refusing"
  case $(echo "$host" | tr '[:upper:]' '[:lower:]') in *kitchen*) die "refusing a kitchen node ($host)" ;; esac
  dur=$(secs "$dur") || die "bad --duration"
  iv=$(secs "$iv") || die "bad --interval"
  every=$(secs "$every") || die "bad --turns-every"
  [ -z "$WAVS" ] || [ -d "$WAVS" ] || die "no directory $WAVS"
  systemctl cat "$AGENT" >/dev/null 2>&1 || die "$AGENT is not installed (sudo sh install-pi.sh first)"
  sudo -n true 2>/dev/null || die "sudo needs a password here: run sudo -v first"
  OUT=${OUT:-$HOME/pi-soak-$(date +%Y%m%d-%H%M)}
  mkdir -p "$OUT" || die "cannot create $OUT"
  : >"$OUT/turns.log"
  echo "$host" >"$OUT/host"

  active "$PY" && WAS_PY=1
  active "$AGENT" && WAS_AGENT=1
  trap restore EXIT
  trap 'exit 130' INT TERM HUP
  if [ "$WAS_PY" = 1 ]; then sudo -n systemctl stop "$PY" || die "could not stop $PY"; say "stopped $PY"; fi
  START=$(date +%s)
  STARTED=1
  sudo -n systemctl start "$AGENT" || die "could not start $AGENT"
  say "started $AGENT for $dur s; samples every $iv s into $OUT"
  {
    echo "- Started $(date -d "@$START" '+%F %T %Z' 2>/dev/null || echo "$START") on $host ($(uname -m), $(awk '/^MemTotal:/{printf "%d MB", $2/1024}' "$PROC/meminfo"))"
    echo "- Agent: $(/opt/jarvis-agent/jarvis-agent version 2>/dev/null | head -1 || echo '?')"
    echo "- Interval $iv s; scripted turns every $every s from ${WAVS:-nowhere (none)}"
  } >"$OUT/meta"
  echo "$HEADER" >"$OUT/samples.csv"
  end=$((START + dur))
  next_turn=$((START + FIRST_TURN))
  while :; do
    sample >>"$OUT/samples.csv"
    now=$(date +%s)
    [ "$now" -ge "$end" ] && break
    if [ -n "$WAVS" ] && [ "$now" -ge "$next_turn" ]; then
      turn_round
      next_turn=$((next_turn + every))
    fi
    now=$(date +%s)
    [ "$now" -ge "$end" ] && break
    # In the background so INT/TERM run the trap at once, not after the sleep.
    sleep $((iv < end - now ? iv : end - now)) &
    wait $!
  done
  sample >>"$OUT/samples.csv"
  say "done"
}

case ${1:-} in
  run) shift; run "$@" ;;
  summarize) [ -n "${2:-}" ] || die "summarize OUT"; summarize "$2" ;;
  *) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
