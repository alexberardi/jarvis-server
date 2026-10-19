#!/usr/bin/env bash
# Check of pi-soak.sh (go-pi-node.md §3.19) without a Pi: a fake /proc (the agent, its sidecar
# child, an unrelated process), a fake /sys temperature, and stand-ins for hostname, sudo,
# systemctl (two units that conflict, as jarvis-agent.service and jarvis-node.service do; CPU
# ticks and NRestarts that grow with each sample), journalctl (a fixture journal, and one per
# scripted turn) and aplay. It checks the refusals, a short run (the units stopped and put back
# in order, the samples and their CPU, the scripted turns: passed, wrong reply, non_speech, no
# turn; the summary's counts) and a run stopped by TERM, which must still put the Python node
# back.
#
#   scripts/pi-soak-check.sh
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT=$HERE/pi-soak.sh
fail() { echo "pi-soak-check: $*" >&2; exit 1; }
has() { grep -qF -- "$2" "$1" || { echo "--- $1:" >&2; cat "$1" >&2; fail "$1 lacks: $2"; }; }

T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/units" "$T/proc/4242" "$T/proc/4300" "$T/proc/1" \
  "$T/sys/class/thermal/thermal_zone0" "$T/wavs"

cat >"$T/bin/hostname" <<'EOF'
#!/bin/sh
echo "$FAKE_HOST"
EOF
cat >"$T/bin/sudo" <<'EOF'
#!/bin/sh
[ "$1" = -n ] && shift
exec "$@"
EOF
# systemctl: a unit is active while $T/units/NAME exists; starting one stops the other. Each
# sample (its MainPID lookup) is counted, the agent and the sidecar use AGENT_TICKS and SIDE_TICKS
# more CPU ticks per sample, and NRestarts is 2 + the count, so the deltas are known.
cat >"$T/bin/systemctl" <<'EOF'
#!/bin/sh
T=$FAKE_T U=$FAKE_T/units
echo "systemctl $*" >>"$T/calls.log"
other() { case $1 in jarvis-agent.service) echo jarvis-node.service ;; *) echo jarvis-agent.service ;; esac; }
n=$(cat "$T/samples" 2>/dev/null || echo 0)
case $1 in
  is-active) [ -e "$U/$3" ] ;;
  start) rm -f "$U/$(other "$2")"; touch "$U/$2" ;;
  stop) rm -f "$U/$2" ;;
  cat) [ "$2" = jarvis-agent.service ] ;;
  show)
    case $3 in
      MainPID)
        [ -e "$U/$5" ] || { echo 0; exit 0; }
        n=$((n + 1)) && echo "$n" >"$T/samples"
        echo "4242 (jarvis agent) S 1 4242 4242 0 -1 4194560 100 0 0 0 $((500 + (AGENT_TICKS - 20) * n)) $((120 + 20 * n)) 0 0 20 0 12 0 100 0 0" >"$T/proc/4242/stat"
        echo "4300 (python3) S 4242 4242 4242 0 -1 4194560 100 0 0 0 $((300 + SIDE_TICKS * n)) 30 0 0 20 0 1 0 200 0 0" >"$T/proc/4300/stat"
        echo 4242 ;;
      NRestarts) echo $((2 + n)) ;;
    esac ;;
esac
EOF
# journalctl: the run's journal (-o short-iso) is $T/journal; a scripted turn's (-o cat) is the
# family's FAMILY.journal, the family being the WAV last played, or nothing when it has none.
cat >"$T/bin/journalctl" <<'EOF'
#!/bin/sh
case " $* " in
  *" -o cat "*) f=$FAKE_T/wavs/$(cat "$FAKE_T/playing" 2>/dev/null).journal; [ ! -f "$f" ] || cat "$f" ;;
  *) cat "$FAKE_T/journal" ;;
esac
EOF
cat >"$T/bin/play" <<'EOF'
#!/bin/sh
basename "$1" .wav >"$FAKE_T/playing"
EOF
chmod +x "$T/bin/"*

# The journal: three wakes, one spoken turn, one non_speech (a false wake), one not for me after
# a follow-up; a pipeline restart; the capture stats printed twice (a restart and the stop).
J='2026-10-19T10:00:00+0000 lab-pi4 jarvis-agent[4242]: time=2026-10-19T10:00:00Z'
cat >"$T/journal" <<EOF
$J level=INFO msg="voice: listening" input=dsnoopmic output=output backend=alsa wake_threshold=0.5
$J level=INFO msg=wake score=0.815
$J level=INFO msg="voice turn" outcome=spoken transcript="What time is it?" reply="It's just past noon." recorded_s=1.80 follow_ups=0 took=2.1s err=<nil>
$J level=INFO msg=wake score=0.612
$J level=WARN msg="voice turn" outcome=non_speech transcript="" reply="" recorded_s=0.40 follow_ups=0 took=0.9s err=<nil>
$J level=INFO msg="voice: capture stats" overruns=2 dropped_chunks=1
$J level=WARN msg="voice: pipeline stopped; restarting" input=dsnoopmic err="device busy" in=2s
$J level=INFO msg=wake score=0.700
$J level=INFO msg="voice turn" outcome=not_for_me transcript="talking to the dog" reply="" follow_ups=0 took=1s err=<nil>
$J level=ERROR msg="sidecar: describe failed" err=timeout
$J level=INFO msg="voice: capture stats" overruns=1 dropped_chunks=0
EOF

# /proc: the agent (a comm with a space), its sidecar, and init, which is neither.
echo '4242 (jarvis agent) S 1 4242 4242 0 -1 4194560 100 0 0 0 500 120 0 0 20 0 12 0 100 0 0' >"$T/proc/4242/stat"
echo '4300 (python3) S 4242 4242 4242 0 -1 4194560 100 0 0 0 300 30 0 0 20 0 1 0 200 0 0' >"$T/proc/4300/stat"
echo '1 (systemd) S 0 1 1 0 -1 4194560 100 0 0 0 900 900 0 0 20 0 1 0 1 0 0' >"$T/proc/1/stat"
printf 'Rss:               87040 kB\nPss:               40000 kB\nAnonymous:         24268 kB\nSwap:                 12 kB\n' >"$T/proc/4242/smaps_rollup"
printf 'Rss:               61440 kB\nPss:               50000 kB\nAnonymous:         45000 kB\nSwap:                  0 kB\n' >"$T/proc/4300/smaps_rollup"
printf 'Rss:                9000 kB\nPss:                3000 kB\nAnonymous:          2000 kB\nSwap:                  0 kB\n' >"$T/proc/1/smaps_rollup"
printf 'MemTotal:         427000 kB\nMemAvailable:     200000 kB\nSwapTotal:       1048576 kB\nSwapFree:         943720 kB\n' >"$T/proc/meminfo"
echo '0.52 0.48 0.40 1/150 4300' >"$T/proc/loadavg"
echo 54800 >"$T/sys/class/thermal/thermal_zone0/temp"

# Four families: time expects "past" (the reply has it), weather expects "sunny" (it hasn't),
# music is heard as non_speech, and lights gets no voice turn at all.
for f in time weather music lights; do : >"$T/wavs/$f.wav"; done
echo 'past|o.clock' >"$T/wavs/time.expect"
echo 'sunny' >"$T/wavs/weather.expect"
echo 'level=INFO msg="voice turn" outcome=spoken transcript="What time is it?" reply="It'"'"'s just past noon." follow_ups=0 took=2.1s err=<nil>' >"$T/wavs/time.journal"
echo 'level=INFO msg="voice turn" outcome=spoken transcript="What'"'"'s the weather?" reply="Rain all day." follow_ups=0 took=2.4s err=<nil>' >"$T/wavs/weather.journal"
echo 'level=WARN msg="voice turn" outcome=non_speech transcript="" reply="" follow_ups=0 took=0.9s err=<nil>' >"$T/wavs/music.journal"

AGENT_TICKS=60 SIDE_TICKS=25
TCK=$(getconf CLK_TCK 2>/dev/null || echo 100)
export PATH="$T/bin:$PATH" SOAK_PROC=$T/proc SOAK_SYS=$T/sys PLAY=$T/bin/play SOAK_FIRST_TURN=0 SOAK_TURN_GAP=0 \
  FAKE_T=$T AGENT_TICKS SIDE_TICKS
reset_units() { rm -f "$T/units/"* "$T/calls.log" "$T/samples"; touch "$T/units/jarvis-node.service"; }

# 1. The refusals: another host, a kitchen node, no --lab-host. Nothing stopped. Each would be a
# 2 s run if its refusal broke, so a broken one fails here instead of soaking for 24 h.
reset_units
for args in "lab-pi4 --lab-host other-pi" "kitchen-pi --lab-host kitchen-pi" "lab-pi4"; do
  # shellcheck disable=SC2086 # each case is "HOST ARGS..."
  set -- $args
  out=$(FAKE_HOST=$1 timeout 20 "$SCRIPT" run "${@:2}" --duration 2s --interval 1 --out "$T/refused" 2>&1) && fail "ran on host $1 with '${*:2}'"
  echo "$out" | grep -q 'refusing\|required' || fail "no refusal message for $args: $out"
done
[ ! -e "$T/calls.log" ] || ! grep -q 'start\|stop' "$T/calls.log" || fail "a refused run touched a unit"
[ -e "$T/units/jarvis-node.service" ] || fail "a refused run stopped the Python node"

# 2. A short run with the scripted turns.
reset_units
FAKE_HOST=lab-pi4 "$SCRIPT" run --lab-host lab-pi4 --duration 8s --interval 1 --turns-every 1h \
  --wavs "$T/wavs" --out "$T/out" --turn-wait 2 2>"$T/run.err" || { cat "$T/run.err"; fail "run failed"; }
grep -E 'systemctl (start|stop)' "$T/calls.log" >"$T/order"
printf '%s\n' 'systemctl stop jarvis-node.service' 'systemctl start jarvis-agent.service' \
  'systemctl stop jarvis-agent.service' 'systemctl start jarvis-node.service' >"$T/want"
diff -u "$T/want" "$T/order" || fail "units not stopped and put back in order"
[ -e "$T/units/jarvis-node.service" ] && [ ! -e "$T/units/jarvis-agent.service" ] || fail "not back as before"

S=$T/out/samples.csv
head -1 "$S" | grep -q '^ts,pid,agent_cpu_pct,agent_rss_kb' || fail "samples.csv header"
[ "$(wc -l <"$S")" -ge 3 ] || fail "too few samples: $(cat "$S")"
# The first row has no CPU yet. The sidecar is one process; init is not counted. NRestarts
# starts at 3 (the fake's 2 + the first sample).
sed -n 2p "$S" | grep -q '^[0-9]*,4242,,87040,40000,24268,12,1,,61440,50000,200000,104856,0.52,54.8,,3$' ||
  fail "first sample: $(sed -n 2p "$S")"
sed -n 3p "$S" | grep -q '^[0-9]*,4242,[0-9.]*,87040,40000,24268,12,1,[0-9.]*,61440,50000,' || fail "second sample: $(sed -n 3p "$S")"
# Later rows: AGENT_TICKS and SIDE_TICKS over the seconds since the row before, as % of one core
# (none in a row taken the same second as the one before).
awk -F, -v t="$TCK" -v at="$AGENT_TICKS" -v st="$SIDE_TICKS" '
  NR>2{s=$1-ts; a=k=""; if (s>0) {a=sprintf("%.1f", 100*at/t/s); k=sprintf("%.1f", 100*st/t/s)}
    if ($3!=a || $9!=k) {print "row " NR-1 ": CPU " $3 " / " $9 ", want " a " / " k; bad=1}}
  NR>1{ts=$1}
  END{exit bad}' "$S" >&2 || fail "CPU: $(cat "$S")"
# The summary's CPU lines from those rows: mean, nearest-rank p95 and max.
cpu_line=$(awk -F, 'NR>1 && $3!=""{print $3}' "$S" | sort -n | awk '{v[NR]=$1; s+=$1; m=$1}
  END{i=int(0.95*NR+0.5); if (i<1) i=1; printf "| Agent CPU, %% of one core: mean / p95 / max | %.1f / %.1f / %.1f |", s/NR, v[i], m}')
side_cpu=$(awk -F, 'NR>1 && $9!=""{s+=$9; n++} END{printf "%.1f", s/n}' "$S")

L=$T/out/turns.log
[ "$(wc -l <"$L")" = 4 ] || fail "turns.log: $(cat "$L")"
has "$L" "$(printf 'time\tpass\tspoken\tWhat time is it?\tIt'"'"'s just past noon.')"
has "$L" "$(printf 'weather\tFAIL\tspoken\tWhat'"'"'s the weather?\tRain all day.')"
has "$L" "$(printf 'music\tFAIL\tnon_speech')"
has "$L" "$(printf 'lights\tFAIL\tno_turn')"

M=$T/out/summary.md
has "$M" '# Go Pi node soak: lab-pi4'
has "$M" '| Agent RSS MB: min / mean / max (first → last) | 85.0 / 85.0 / 85.0 (85.0 → 85.0) |'
has "$M" '| Agent anonymous MB: mean / max (first → last) | 23.7 / 23.7 (23.7 → 23.7) |'
has "$M" "$cpu_line"
has "$M" "| Sidecar CPU % mean; RSS MB mean / max | $side_cpu; 60.0 / 60.0 |"
has "$M" "| Unit restarts (NRestarts) | $(($(wc -l <"$S") - 2)) |"
has "$M" '| SoC temperature, max °C; throttling | 54.8; none |'
has "$M" '| ALSA overruns / dropped chunks | 3 / 1 |'
has "$M" '| Voice pipeline restarts | 1 |'
has "$M" '| Wakes / voice turns | 3 / 3 |'
has "$M" '| False wakes (non_speech + not_for_me) | 2 |'
has "$M" '| Errors / warnings / panics in the log | 1 / 2 / 0 |'
has "$M" '| Scripted turns passed | 1 / 4 |'
has "$M" '| time | 1 / 1 | spoken |'
has "$M" '| weather | 0 / 1 | spoken |'
has "$M" '| music | 0 / 1 | non_speech |'
has "$M" '| lights | 0 / 1 | no_turn |'
has "$M" '87.1% of one core, 129 MB anonymous'
[ -s "$T/out/journal.log" ] || fail "no journal.log"

# summarize again from the files gives the same summary.
cp "$M" "$T/summary.first"
"$SCRIPT" summarize "$T/out" 2>/dev/null
diff -q "$T/summary.first" "$M" >/dev/null || fail "summarize differs from run's summary"

# 3. TERM in the middle of a long run: the Python node is back.
reset_units
FAKE_HOST=lab-pi4 "$SCRIPT" run --lab-host lab-pi4 --duration 1h --interval 1 --out "$T/term" 2>"$T/term.err" &
pid=$!
for _ in $(seq 1 50); do [ -e "$T/units/jarvis-agent.service" ] && break; sleep 0.1; done
sleep 1
kill -TERM "$pid"
rc=0
wait "$pid" || rc=$?
[ "$rc" = 130 ] || { cat "$T/term.err"; fail "TERM: exit $rc, want 130"; }
[ -e "$T/units/jarvis-node.service" ] && [ ! -e "$T/units/jarvis-agent.service" ] || fail "TERM: not back as before"
[ -s "$T/term/summary.md" ] || fail "TERM: no summary"

echo "pi-soak-check: ok"
