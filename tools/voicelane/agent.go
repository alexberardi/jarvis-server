package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Turn is what `jarvis-agent listen` printed for one turn (printTurn in cmd/jarvis-agent).
type Turn struct {
	Heard       string   `json:"heard"`
	Outcome     string   `json:"outcome"`
	Said        string   `json:"said,omitempty"`
	ClientTools []string `json:"client_tools,omitempty"`
	Recorded    float64  `json:"recorded_secs"`
	TookSecs    float64  `json:"took_secs"`
}

var (
	turnHead    = regexp.MustCompile(`recorded ([0-9.]+) s .*?heard ("(?:[^"\\]|\\.)*")(.*)`)
	turnRan     = regexp.MustCompile(`, ran ([A-Za-z0-9_.:-]+)`)
	turnOutcome = regexp.MustCompile(` -> ([a-z_]+)`)
	turnSaid    = regexp.MustCompile(`, said ("(?:[^"\\]|\\.)*")`)
	turnTook    = regexp.MustCompile(`^\s+took (\S+)`)
	wakeLine    = regexp.MustCompile(`msg=wake .*score=([0-9.]+)`)
)

// wakes are the wake scores the node logged ("msg=wake … score=0.922"): a wake its turn
// line may not show, when the run ended before the turn did.
func wakes(log string) []float64 {
	var out []float64
	for _, m := range wakeLine.FindAllStringSubmatch(log, -1) {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			out = append(out, f)
		}
	}
	return out
}

// parseTurns reads printTurn's lines out of the agent's stdout: "turn …" starts one and
// "  took …" ends it.
func parseTurns(out string) []Turn {
	var turns []Turn
	var cur *Turn
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "turn ") {
			cur = &Turn{}
			if m := turnHead.FindStringSubmatch(line); m != nil {
				cur.Recorded, _ = strconv.ParseFloat(m[1], 64)
				cur.Heard, _ = strconv.Unquote(m[2])
				rest := m[3]
				// The tools and the outcome come before what was said, which may quote anything.
				head := rest
				if i := strings.Index(rest, ", said \""); i >= 0 {
					head = rest[:i]
				}
				for _, r := range turnRan.FindAllStringSubmatch(head, -1) {
					cur.ClientTools = append(cur.ClientTools, r[1])
				}
				if o := turnOutcome.FindAllStringSubmatch(head, -1); o != nil {
					cur.Outcome = o[len(o)-1][1]
				}
				if s := turnSaid.FindStringSubmatch(rest); s != nil {
					cur.Said, _ = strconv.Unquote(s[1])
				}
			}
			continue
		}
		if m := turnTook.FindStringSubmatch(line); m != nil && cur != nil {
			if d, err := time.ParseDuration(m[1]); err == nil {
				cur.TookSecs = d.Seconds()
			}
			turns = append(turns, *cur)
			cur = nil
		}
	}
	return turns
}

// agentEnv is the node's environment: its own HOME and XDG dirs under home, so the user's own
// agent (its enrollment, settings and caches) is never read or written.
func agentEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"), "XDG_DATA_HOME="+filepath.Join(home, "data"),
		"XDG_STATE_HOME="+filepath.Join(home, "state"))
}

// rig plays a case's audio to the node and records what its speaker played.
type rig interface {
	// play gives the node in (a WAV file) and writes its speaker to out, waiting up to wait
	// after the audio for the turn to end. It returns the node's turns and the response time
	// (0 when there was no reply).
	play(ctx context.Context, in, out, dir string, wait time.Duration) ([]Turn, float64, error)
	close()
}

// wavRig runs one `listen --once` per case on WAV files.
type wavRig struct{ l *lane }

func (w wavRig) play(ctx context.Context, in, out, dir string, wait time.Duration) ([]Turn, float64, error) {
	timeout := time.Duration(wavSecondsFile(in)*float64(time.Second)) + wait
	args := append(w.l.listenArgs("wav:"+in, "wav:"+out), "--once", "--timeout", timeout.Round(time.Second).String())
	cmd := exec.CommandContext(ctx, w.l.agent, args...)
	cmd.Env = agentEnv(w.l.nodeHome)
	logPath := filepath.Join(dir, "agent.log")
	logf, err := os.Create(logPath)
	if err != nil {
		return nil, 0, err
	}
	defer logf.Close()
	var stdout bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, logf)
	cmd.Stderr = logf
	err = cmd.Run()
	turns := parseTurns(stdout.String())
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return nil, 0, err
	}
	// --once exits 1 when no wake word was heard (a silent case's pass) or a turn wasn't
	// spoken (judged from the turn); anything else is the node failing, as its log says.
	if err != nil && len(turns) == 0 && !strings.Contains(tail(logPath), "no wake word heard") {
		return nil, 0, fmt.Errorf("jarvis-agent listen: %v: %s", err, tail(logPath))
	}
	if len(turns) == 0 {
		return nil, 0, nil
	}
	// A WAV speaker is written at once, so the response time is the turn less its recording:
	// speech-to-text, the model and TTS.
	return turns, max(0, turns[0].TookSecs-turns[0].Recorded), nil
}

func (wavRig) close() {}

// pwRig runs one long `listen` on an ALSA PCM that reaches PipeWire ("pipewire", from
// alsa-lib's 50-pipewire.conf). Through the private runtime dir scripts/e2e voice exported, it
// and pw-play / pw-record reach that PipeWire's virtual devices, which are its defaults.
type pwRig struct {
	l        *lane
	cmd      *exec.Cmd
	lines    chan string
	exited   chan struct{}
	mic, spk string
}

func newPWRig(ctx context.Context, l *lane, dev, mic, spk string) (*pwRig, error) {
	// Never the user's own audio session: only scripts/e2e's private one, which says so.
	if os.Getenv("JARVIS_E2E_AUDIO") != "private" || os.Getenv("PIPEWIRE_RUNTIME_DIR") == "" {
		return nil, errors.New("--audio pipewire runs only inside scripts/e2e voice's private PipeWire (JARVIS_E2E_AUDIO=private)")
	}
	cmd := exec.CommandContext(ctx, l.agent, l.listenArgs(dev, dev)...)
	cmd.Env = agentEnv(l.nodeHome)
	logf, err := os.Create(filepath.Join(l.out, "agent.log"))
	if err != nil {
		return nil, err
	}
	// Its log lines too, to see a wake as it happens; one writer, so one Write at a time.
	pr, pw := io.Pipe()
	both := io.MultiWriter(pw, logf)
	cmd.Stdout, cmd.Stderr = both, both
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, err
	}
	r := &pwRig{l: l, cmd: cmd, lines: make(chan string, 256), exited: make(chan struct{}), mic: mic, spk: spk}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			r.lines <- sc.Text()
		}
		close(r.lines)
	}()
	go func() { cmd.Wait(); pw.Close(); logf.Close(); close(r.exited) }()
	select { // the detector and the devices load
	case <-r.exited:
		return nil, fmt.Errorf("jarvis-agent listen exited: %s", tail(filepath.Join(l.out, "agent.log")))
	case <-time.After(3 * time.Second):
	}
	return r, nil
}

func (r *pwRig) play(ctx context.Context, in, out, dir string, wait time.Duration) ([]Turn, float64, error) {
	// Whatever the node printed since the last case (a turn a barge-in started) is not this one's.
drain:
	for {
		select {
		case <-r.lines:
		default:
			break drain
		}
	}
	// The speaker is a sink: record its monitor.
	rec := exec.CommandContext(ctx, "pw-record", "--target", r.spk, "-P", "{ stream.capture.sink = true }",
		"--rate", "16000", "--channels", "1", out)
	if err := rec.Start(); err != nil {
		return nil, 0, fmt.Errorf("pw-record: %w", err)
	}
	recStart := time.Now()
	stopRec := func() {
		rec.Process.Signal(os.Interrupt)
		rec.Wait()
	}
	if b, err := exec.CommandContext(ctx, "pw-play", "--target", r.mic, in).CombinedOutput(); err != nil {
		stopRec()
		return nil, 0, fmt.Errorf("pw-play: %v: %s", err, b)
	}
	playEnd := time.Now()
	// The turn ends with its "took" line; a silent case listens for the whole window, and
	// longer once the node has woken, to see the turn through.
	var buf strings.Builder
	deadline := time.After(wait)
	extended := false
wait:
	for {
		select {
		case line, ok := <-r.lines:
			if !ok {
				break wait
			}
			buf.WriteString(line + "\n")
			if turnTook.MatchString(line) {
				break wait
			}
			if !extended && wakeLine.MatchString(line) {
				deadline, extended = time.After(r.l.replyWait), true
			}
		case <-deadline:
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	time.Sleep(500 * time.Millisecond) // the reply's tail
	stopRec()
	os.WriteFile(filepath.Join(dir, "agent.log"), []byte(buf.String()), 0o644)
	turns := parseTurns(buf.String())
	resp := 0.0
	if s, rate, err := readWAVFile(out); err == nil {
		if secs, first := voiced(s, rate); secs > 0 {
			resp = max(0, recStart.Add(time.Duration(first*float64(time.Second))).Sub(playEnd).Seconds())
		}
	}
	return turns, resp, nil
}

func (r *pwRig) close() {
	r.cmd.Process.Signal(os.Interrupt)
	go func() {
		for range r.lines {
		}
	}()
	select {
	case <-r.exited:
	case <-time.After(5 * time.Second):
		r.cmd.Process.Kill()
	}
}

func tail(path string) string {
	b, _ := os.ReadFile(path)
	if len(b) > 1500 {
		b = b[len(b)-1500:]
	}
	return strings.TrimSpace(string(b))
}

// wavSecondsFile is a WAV's length in seconds (0 when it can't be read).
func wavSecondsFile(path string) float64 {
	s, rate, err := readWAVFile(path)
	if err != nil || rate == 0 {
		return 0
	}
	return float64(len(s)) / float64(rate)
}
