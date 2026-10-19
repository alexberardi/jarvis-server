package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

// The fake Pi: ssh runs the remote command here with fake systemctl, sudo, aplay, arecord and
// uname first on PATH, so the real pi.sh runs. Its units are files under $FAKE_PI/units/<scope>
// (present = active), and what it starts and stops is appended to $FAKE_PI/actions. The card is
// busy while the user's pulseaudio.service runs (or always, with FAKE_BUSY). The room is the
// run's directory: aplay -D e2e_play copies the WAV there as mic-*.wav, for the node.
var fakePiBin = map[string]string{
	// Not exec: when voicelane kills ssh, the remote side lives on and sees its stdin close.
	"ssh": `echo "$*" >>"$FAKE_PI/ssh.log"
while [ $# -gt 0 ]; do case $1 in -o | -R | -p | -i | -F | -l) shift 2 ;; -*) shift ;; *) break ;; esac; done
shift
bash -c "$*"`,
	"uname": `echo "${FAKE_ARCH:-aarch64}"`,
	"sudo":  `[ "$1" = -n ] && shift; FAKE_ROOT=1 "$@"`,
	"systemctl": `scope=system
[ "$1" = --user ] && { scope=user; shift; }
d=$FAKE_PI/units/$scope
case $1 in
  list-units)
    shift
    pats=()
    for a in "$@"; do case $a in -*) ;; *) pats+=("$a") ;; esac; done
    for f in "$d"/*; do
      [ -e "$f" ] || continue
      u=${f##*/}
      for p in "${pats[@]}"; do
        # shellcheck disable=SC2053
        if [[ $u == $p ]]; then echo "$u loaded active running fake"; break; fi
      done
    done ;;
  stop | start)
    [ $scope = user ] || [ "${FAKE_ROOT:-}" = 1 ] || { echo "Access denied" >&2; exit 1; }
    if [ "$1" = stop ]; then rm -f "$d/$2"; else touch "$d/$2"; fi
    echo "$1 $scope $2" >>"$FAKE_PI/actions" ;;
  *) exit 1 ;;
esac`,
	"arecord": `case "$*" in *plughw:*) ;; *) exit 1 ;; esac
[ -z "${FAKE_BUSY:-}" ] && [ ! -e "$FAKE_PI/units/user/pulseaudio.service" ] || { echo busy >&2; exit 1; }`,
	"aplay": `case "$*" in *plughw:*)
  [ -z "${FAKE_BUSY:-}" ] && [ ! -e "$FAKE_PI/units/user/pulseaudio.service" ] || { echo busy >&2; exit 1; }
  exit 0 ;;
esac
[ "$2" = -D ] && [ "$3" = e2e_play ] && grep -q 'pcm.e2e_play' "$HOME/.asoundrc" || { echo "aplay $*: no e2e_play" >&2; exit 1; }
n=$(date +%s%N)
cp "$4" "$HOME/.mic-$n" && mv "$HOME/.mic-$n" "$HOME/mic-$n.wav"`,
}

// fakePiAgent is the node on the fake Pi: one long `listen` on e2e_mic and e2e_node_out that
// hears each WAV aplay left in the room, "wakes" on the fake TTS's wake level, appends its reply
// to the tee (reply.raw, S16_LE 16 kHz mono) and prints the turn; it exits 3 when it hears
// the FAKE_DIE_AT'th utterance.
func fakePiAgent(args []string) int {
	home := os.Getenv("HOME")
	flags := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		flags[args[i]] = args[i+1]
	}
	if flags["--voice-input"] != "e2e_mic" || flags["--voice-output"] != "e2e_node_out" || !strings.HasPrefix(flags["--creds"], home+"/") {
		fmt.Fprintln(os.Stderr, "jarvis-agent: bad flags", args)
		return 2
	}
	if _, err := os.Stat(flags["--creds"]); err != nil {
		fmt.Fprintln(os.Stderr, "jarvis-agent:", err)
		return 2
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	fmt.Fprintln(os.Stderr, `time=… level=INFO msg="voice: listening" input=e2e_mic output=e2e_node_out`)
	heard, die := 0, 0
	if s := os.Getenv("FAKE_DIE_AT"); s != "" {
		die, _ = strconv.Atoi(s)
	}
	for {
		select {
		case <-sig:
			return 0
		case <-time.After(20 * time.Millisecond):
		}
		files, _ := filepath.Glob(filepath.Join(home, "mic-*.wav"))
		slices.Sort(files)
		for _, f := range files {
			s, _, err := readWAVFile(f)
			os.Remove(f)
			if heard++; die > 0 && heard >= die {
				return 3
			}
			if err != nil || !slices.ContainsFunc(s, func(v float32) bool { return v > 0.49 && v < 0.51 }) {
				continue
			}
			fmt.Fprintln(os.Stderr, "time=… level=INFO msg=wake conversation_id=c1 score=0.950")
			var pcm bytes.Buffer
			for _, v := range tone(16000, 0.3) {
				x := audio.FloatToPCM16(v)
				pcm.Write([]byte{byte(x), byte(x >> 8)})
			}
			tee, _ := os.OpenFile(filepath.Join(home, "reply.raw"), os.O_APPEND|os.O_WRONLY, 0)
			tee.Write(pcm.Bytes())
			tee.Close()
			tap, _ := os.OpenFile(os.Getenv("VOICELANE_TAP"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
			fmt.Fprintln(tap, `{"reply":{"tool_calls":[{"function":{"name":"get_current_time"}}]}}`)
			tap.Close()
			fmt.Println(`turn c1: wake 0.950, recorded 2.00 s (silence threshold 300), heard "what time is it", ran get_current_time{} -> spoken, said "It is three." (1.00 s of audio)`)
			fmt.Println("  took 5s")
		}
	}
}

type fakePi struct {
	dir, tmp, root, sbx string
	ssh                 string
}

// newFakePi is a fake Pi whose units are active ("system/jarvis-node.service", …), and a
// fake sandbox for it.
func newFakePi(t *testing.T, units ...string) *fakePi {
	p := newFakePiHost(t, units...)
	_, p.sbx = fakeSandbox(t, p.root)
	writeFile(t, p.sbx, "llm.jsonl", "")
	t.Setenv("VOICELANE_TAP", filepath.Join(p.sbx, "llm.jsonl"))
	return p
}

// newFakePiHost is the fake Pi alone, without the sandbox (which listens on ports).
func newFakePiHost(t *testing.T, units ...string) *fakePi {
	p := &fakePi{dir: t.TempDir(), tmp: t.TempDir(), root: t.TempDir()}
	bin := filepath.Join(p.dir, "bin")
	os.MkdirAll(bin, 0o755)
	for name, body := range fakePiBin {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []string{"system", "user"} {
		os.MkdirAll(filepath.Join(p.dir, "units", scope), 0o755)
	}
	for _, u := range units {
		writeFile(t, filepath.Join(p.dir, "units"), u, "")
	}
	writeFile(t, p.dir, "cards", " 0 [Headphones     ]: bcm2835_headpho - bcm2835 Headphones\n"+
		"                      bcm2835 Headphones\n 2 [seeed2micvoicec]: seeed-2mic-voic - seeed-2mic-voicecard\n")
	p.ssh = filepath.Join(bin, "ssh")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TMPDIR", p.tmp)
	t.Setenv("FAKE_PI", p.dir)
	t.Setenv("E2E_PI_CARDS", filepath.Join(p.dir, "cards"))
	t.Setenv("E2E_PI_SETTLE", "0.3")
	t.Setenv("VOICELANE_FAKE_AGENT", "pi")
	return p
}

func (p *fakePi) run(t *testing.T, ctx context.Context, cases string, extra ...string) (string, error) {
	self, _ := os.Executable()
	var stdout strings.Builder
	args := append([]string{"--root", p.root, "--sandbox", "t", "--cases", writeFile(t, p.dir, "cases.json", cases),
		"--out", filepath.Join(p.root, "out"), "--agent", self, "--audio", "pi", "--pi-host", "pi@lab", "--ssh", p.ssh,
		"--repeat", "1", "--silent-wait", "1s", "--reply-wait", "10s"}, extra...)
	err := run(ctx, args, &stdout)
	return stdout.String(), err
}

func (p *fakePi) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(p.dir, name))
	return string(b)
}

func (p *fakePi) units(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, scope := range []string{"system", "user"} {
		es, _ := os.ReadDir(filepath.Join(p.dir, "units", scope))
		for _, e := range es {
			out = append(out, scope+"/"+e.Name())
		}
	}
	return out
}

// runDirsLeft is the run directories left on the fake Pi.
func (p *fakePi) runDirsLeft() []string {
	m, _ := filepath.Glob(filepath.Join(p.tmp, "jarvis-e2e-*"))
	return m
}

const piCases = `{"cases": [
  {"name": "the time", "model": true, "audio": [{"say": "Hey Jarvis."}, {"say": "What time is it?"}],
   "expect": {"tool": "get_current_time", "reply": "(?i)three", "max_secs": 4}},
  {"name": "no wake", "audio": [{"say": "What time is it?"}], "expect": {"silent": true}},
  {"name": "real time only", "needs": "pipewire", "audio": [{"say": "Hey Jarvis."}], "expect": {"outcome": "spoken"}}
]}`

func TestPiRunsTheCasesAndLeavesThePiAsFound(t *testing.T) {
	before := []string{"system/jarvis-node.service", "system/ssh.service", "user/pulseaudio.service", "user/pulseaudio.socket", "user/syncthing.service"}
	p := newFakePi(t, before...)
	stdout, err := p.run(t, context.Background(), piCases)
	if err != nil {
		t.Fatalf("run: %v\n%s\nssh: %s", err, stdout, p.read("ssh.log"))
	}
	for _, want := range []string{"passed  the time (1/1)", "passed  no wake (1/1)", "passed  real time only (1/1)", "voice: pi: card 2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	// The Jarvis node always (it would hear the cases), the audio server because it held the
	// card (its socket first); everything back afterwards, in reverse.
	want := "stop system jarvis-node.service\nstop user pulseaudio.socket\nstop user pulseaudio.service\n" +
		"start user pulseaudio.service\nstart user pulseaudio.socket\nstart system jarvis-node.service\n"
	if got := p.read("actions"); got != want {
		t.Errorf("actions:\n%s\nwant:\n%s", got, want)
	}
	if got := p.units(t); !slices.Equal(got, before) {
		t.Errorf("units after %v, want %v", got, before)
	}
	if left := p.runDirsLeft(); len(left) > 0 {
		t.Errorf("run dirs left on the Pi: %v", left)
	}
	// The sandbox's ports, forwarded to the Pi's loopback.
	env, _ := sandboxEnv(p.root, "t")
	_, port, _ := splitHostPort(strings.TrimPrefix(env["SBX_CC"], "http://"))
	if fwd := fmt.Sprintf("-R 127.0.0.1:%d:127.0.0.1:%d", port, port); !strings.Contains(p.read("ssh.log"), fwd) ||
		!strings.Contains(p.read("ssh.log"), "ExitOnForwardFailure=yes") {
		t.Errorf("ssh.log lacks %s:\n%s", fwd, p.read("ssh.log"))
	}
	var res struct {
		Status string
		Pi     []string
		Tests  []Test
	}
	b, _ := os.ReadFile(filepath.Join(p.root, "out", "results.json"))
	json.Unmarshal(b, &res)
	r := res.Tests[0].Runs[0]
	// took 5 s, less 2 s recorded and 1 s of reply.
	if r.Reply != "It is three." || r.ReplySecs < 0.9 || r.Response != 2 || r.ModelCalls != 1 {
		t.Errorf("the time: %+v", r)
	}
	if nw := res.Tests[1].Runs[0]; nw.Turn != nil || nw.ReplySecs != 0 || nw.ModelCalls != 0 {
		t.Errorf("no wake: %+v", nw)
	}
	notes := strings.Join(res.Pi, "\n")
	for _, w := range []string{"before: system: jarvis-node.service; user: pulseaudio.service pulseaudio.socket",
		"stopped system jarvis-node.service", "restored system jarvis-node.service",
		"after: system: jarvis-node.service; user: pulseaudio.service pulseaudio.socket"} {
		if !strings.Contains(notes, w) {
			t.Errorf("pi notes lack %q:\n%s", w, notes)
		}
	}
}

func TestPiRestoresWhenTheNodeDies(t *testing.T) {
	before := []string{"system/jarvis-node.service"}
	p := newFakePi(t, before...)
	t.Setenv("FAKE_DIE_AT", "2")
	stdout, err := p.run(t, context.Background(), piCases, "--filter", "time")
	var fail errFailed
	if !errors.As(err, &fail) {
		t.Fatalf("run: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "passed  the time") || !strings.Contains(stdout, "failed  real time only") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if got := p.units(t); !slices.Equal(got, before) || !strings.Contains(p.read("actions"), "start system jarvis-node.service") {
		t.Errorf("units after %v, actions %s", got, p.read("actions"))
	}
	if left := p.runDirsLeft(); len(left) > 0 {
		t.Errorf("run dirs left: %v", left)
	}
}

// pi.sh runs on the Pi's bash 5, and in these tests on this machine's: macOS's /bin/bash is 3.2
// (no `wait -n`; an empty array is unbound under set -u) and its stat has no -c. This drives
// pi.sh's serve, play and fetch with each bash here, without the sandbox (which listens on ports).
func TestPiScriptRunsOnEveryBash(t *testing.T) {
	var bashes []string
	for _, b := range []string{"bash", "/bin/bash"} {
		if p, err := exec.LookPath(b); err == nil {
			if p, err = filepath.EvalSymlinks(p); err == nil && !slices.Contains(bashes, p) {
				bashes = append(bashes, p)
			}
		}
	}
	if len(bashes) == 0 {
		t.Skip("no bash")
	}
	var wav bytes.Buffer
	if err := audio.WriteWAV(&wav, tone(16000, 0.5), 16000); err != nil { // the fake TTS's wake level
		t.Fatal(err)
	}
	for _, bash := range bashes {
		t.Run(bash, func(t *testing.T) {
			before := []string{"system/jarvis-node.service", "user/pulseaudio.service"}
			p := newFakePiHost(t, before...)
			t.Setenv("VOICELANE_TAP", filepath.Join(p.dir, "llm.jsonl"))
			run := filepath.Join(p.tmp, "jarvis-e2e-test")
			os.MkdirAll(run, 0o755)
			self, _ := os.Executable()
			if err := os.Symlink(self, filepath.Join(run, "jarvis-agent")); err != nil {
				t.Fatal(err)
			}
			script := writeFile(t, run, "pi.sh", string(piScript))
			creds := writeFile(t, run, "credentials.json", "{}")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			serve := exec.CommandContext(ctx, bash, script, "serve", run, "auto", "--",
				"listen", "--creds", creds, "--voice-input", "e2e_mic", "--voice-output", "e2e_node_out")
			stdin, _ := serve.StdinPipe()
			stdout, _ := serve.StdoutPipe()
			var stderr strings.Builder
			serve.Stderr = &stderr
			if err := serve.Start(); err != nil {
				t.Fatal(err)
			}
			lines := bufio.NewScanner(stdout)
			var said []string
			for lines.Scan() {
				if said = append(said, lines.Text()); lines.Text() == "e2e-pi: ready" {
					break
				}
			}
			if len(said) == 0 || said[len(said)-1] != "e2e-pi: ready" {
				stdin.Close()
				serve.Wait()
				t.Fatalf("serve never got ready:\n%s\n%s", strings.Join(said, "\n"), stderr.String())
			}

			// play prints the reply tee's size before it plays; fetch is the tee from there.
			writeFile(t, run, "reply.raw", "0123456789")
			play := exec.CommandContext(ctx, bash, script, "play", run)
			play.Stdin = bytes.NewReader(wav.Bytes())
			if o, err := play.CombinedOutput(); err != nil || string(o) != "10\n" {
				t.Errorf("play: %q, %v", o, err)
			}
			const reply = 16000 * 2 // the fake node's reply, S16_LE
			for i := 0; i < 200; i++ {
				if fi, err := os.Stat(filepath.Join(run, "reply.raw")); err == nil && fi.Size() >= 10+reply {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if o, err := exec.CommandContext(ctx, bash, script, "fetch", run, "10").Output(); err != nil || len(o) != reply {
				t.Errorf("fetch: %d bytes, %v; want %d", len(o), err, reply)
			}

			// ssh's stdin closes: serve stops the node and puts the Pi back.
			stdin.Close()
			for lines.Scan() {
				said = append(said, lines.Text())
			}
			if err := serve.Wait(); err != nil {
				t.Errorf("serve: %v\n%s", err, stderr.String())
			}
			got := strings.Join(said, "\n")
			for _, w := range []string{"before: system: jarvis-node.service; user: pulseaudio.service",
				"stopped the test node", "after: system: jarvis-node.service; user: pulseaudio.service", "e2e-pi: done"} {
				if !strings.Contains(got, w) {
					t.Errorf("serve lacks %q:\n%s", w, got)
				}
			}
			if got := p.units(t); !slices.Equal(got, before) {
				t.Errorf("units after %v, want %v", got, before)
			}
			if _, err := os.Stat(run); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("run dir left: %v", err)
			}
		})
	}
}

func TestPiRestoresWhenInterrupted(t *testing.T) {
	before := []string{"system/jarvis-node.service", "user/pulseaudio.service"}
	p := newFakePi(t, before...)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { // cancel once the first case is done
		for i := 0; i < 400; i++ {
			if _, err := os.Stat(filepath.Join(p.root, "out", "cases", "the-time", "run-1", "run.json")); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		cancel()
	}()
	p.run(t, ctx, piCases)
	// The Pi side puts things back on its own once ssh is gone.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && (len(p.runDirsLeft()) > 0 || !slices.Equal(p.units(t), before)) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := p.units(t); !slices.Equal(got, before) {
		t.Errorf("units after %v, want %v (actions %s)", got, before, p.read("actions"))
	}
	if left := p.runDirsLeft(); len(left) > 0 {
		t.Errorf("run dirs left: %v", left)
	}
}

func TestPiRefusesAndCleansUp(t *testing.T) {
	for name, tc := range map[string]struct {
		env     map[string]string
		units   []string
		sandbox string // sandbox.env's SBX_CONFIG, when not the fake's
		want    string
		ssh     bool // whether ssh may run at all
	}{
		"not the local sandbox": {sandbox: "http://192.0.2.10:7700", want: "local sandbox"},
		"not aarch64":           {env: map[string]string{"FAKE_ARCH": "x86_64"}, want: "linux-arm64", ssh: true},
		"card still busy": {env: map[string]string{"FAKE_BUSY": "1"}, units: []string{"user/pulseaudio.service"},
			want: "still busy", ssh: true},
		"no seeed card": {env: map[string]string{"E2E_PI_CARDS": "/dev/null"}, want: "no seeed card", ssh: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := newFakePi(t, tc.units...)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if tc.sandbox != "" {
				writeFile(t, p.sbx, "sandbox.env", "export SBX_CC=http://127.0.0.1:1\nexport SBX_CONFIG="+tc.sandbox+
					"\nexport SBX_NODE_ID=node-1\nexport SBX_NODE_KEY=key-1\nexport SBX_VOICE=w\n")
			}
			_, err := p.run(t, context.Background(), piCases)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if ran := p.read("ssh.log") != ""; ran != tc.ssh {
				t.Errorf("ssh ran: %v\n%s", ran, p.read("ssh.log"))
			}
			if got := p.units(t); !slices.Equal(got, tc.units) {
				t.Errorf("units after %v, want %v", got, tc.units)
			}
			if left := p.runDirsLeft(); len(left) > 0 {
				t.Errorf("run dirs left: %v", left)
			}
		})
	}
}

func TestPiPortsRefuseAnythingButLoopback(t *testing.T) {
	ports, err := piPorts(map[string]string{"SBX_CONFIG": "http://127.0.0.1:17700", "SBX_CC": "http://127.0.0.1:17703",
		"SBX_MQTT": "127.0.0.1:11884", "SBX_ADMIN": "http://127.0.0.1:17710"})
	if err != nil || !slices.Equal(ports, []int{17700, 17703, 11884}) {
		t.Errorf("ports %v, %v", ports, err)
	}
	for _, env := range []map[string]string{
		{"SBX_CONFIG": "http://localhost:17700"},
		{"SBX_CONFIG": "http://127.0.0.1:17700", "SBX_CC": "https://jarvis.example:443"},
		{"SBX_CC": "http://127.0.0.1:17703"},
	} {
		if _, err := piPorts(env); err == nil {
			t.Errorf("%v: allowed", env)
		}
	}
}

func TestRecordedVoicesAndResampling(t *testing.T) {
	cc, _ := fakeSandbox(t, t.TempDir())
	dir := t.TempDir()
	var b bytes.Buffer
	audio.WriteWAV(&b, tone(8000, 0.5), 8000)
	os.WriteFile(filepath.Join(dir, "hey-jarvis.wav"), b.Bytes(), 0o644)
	l := &lane{voices: dir, node: &node{cc: cc.URL, id: "node-1", key: "key-1"}}
	// The TTS (0.5 s at 16 kHz, level 0.25) sets the rate; the recording (1 s at 8 kHz, level
	// 0.5) becomes 16000 samples.
	s, rate, err := renderCase(context.Background(), l.speak, []AudioPart{{Say: "What time is it?"}, {Say: "Hey Jarvis."}}, 1)
	if err != nil || rate != 16000 || len(s) != 24000 {
		t.Fatalf("rate %d, %d samples, %v", rate, len(s), err)
	}
	if s[100] != 0.25 || s[10000] != 0.5 {
		t.Errorf("the TTS %v, the recording %v", s[100], s[10000])
	}
	if r := resample([]float32{0, 1}, 1, 4); !slices.Equal(r, []float32{0, 0.25, 0.5, 0.75, 1, 1, 1, 1}) {
		t.Errorf("resample: %v", r)
	}
}
