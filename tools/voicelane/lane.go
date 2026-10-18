package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type lane struct {
	node       *node
	tapLog     string
	agent      string
	agentArgs  []string
	creds      string
	wakeModels string
	voices     string
	nodeHome   string
	out        string
	audio      string
	replyWait  time.Duration
	silentWait time.Duration
	rig        rig
}

// listenArgs is `jarvis-agent listen` as the lane runs it: the Pi build, no chimes (they would
// be recorded as the reply) and no follow-up window (one exchange per case).
func (l *lane) listenArgs(in, out string) []string {
	return listenArgs(l.creds, l.wakeModels, in, out, l.agentArgs)
}

// listenArgs is listen's flags with the node's files where it runs (the Pi has its own paths).
func listenArgs(creds, wakeModels, in, out string, extra []string) []string {
	a := []string{"listen", "--profile", "pi", "--creds", creds, "--voice-input", in, "--voice-output", out,
		"--no-chimes", "--follow-up", "0"}
	if wakeModels != "" {
		a = append(a, "--wake-models", wakeModels)
	}
	return append(a, extra...)
}

// Run is one run of a case: what the node did and what was checked.
type Run struct {
	Turn       *Turn     `json:"turn,omitempty"`
	Wakes      []float64 `json:"wakes,omitempty"`       // the node's logged wake scores
	Reply      string    `json:"reply_heard,omitempty"` // whisper's transcript of the recorded reply
	ReplySecs  float64   `json:"reply_secs"`            // recorded audio above the silence floor
	Response   float64   `json:"response_secs,omitempty"`
	ModelCalls int       `json:"model_calls"`
	ModelTools []string  `json:"model_tools,omitempty"`
	Failures   []string  `json:"failures,omitempty"`
	Dir        string    `json:"dir"`
}

// judge checks a run against what the case expects.
func judge(e Expect, r *Run) {
	fail := func(f string, a ...any) { r.Failures = append(r.Failures, fmt.Sprintf(f, a...)) }
	if e.Silent {
		switch {
		case r.Turn != nil:
			fail("expected silence, the node took a turn (heard %q, %s)", r.Turn.Heard, r.Turn.Outcome)
		case len(r.Wakes) > 0:
			fail("expected silence, the node woke (score %.3f)", r.Wakes[0])
		}
		if r.ReplySecs > 0 {
			fail("expected silence, the speaker played %.1f s", r.ReplySecs)
		}
		if r.ModelCalls > 0 {
			fail("expected silence, the model was called %d times", r.ModelCalls)
		}
		return
	}
	if r.Turn == nil {
		fail("no turn: the wake word was not heard")
		return
	}
	want := e.Outcome
	if want == "" {
		want = "spoken"
	}
	if r.Turn.Outcome != want {
		fail("the turn ended %s, want %s (heard %q)", r.Turn.Outcome, want, r.Turn.Heard)
	}
	if e.Reply != "" && !regexp.MustCompile(e.Reply).MatchString(r.Reply) {
		fail("the reply heard was %q, want /%s/ (the node said %q)", r.Reply, e.Reply, r.Turn.Said)
	}
	if e.Tool != "" && !slices.Contains(r.ModelTools, e.Tool) && !slices.Contains(r.Turn.ClientTools, e.Tool) {
		fail("no %s call (the model called %v, the node ran %v)", e.Tool, r.ModelTools, r.Turn.ClientTools)
	}
	if e.MaxSecs > 0 && r.Response > e.MaxSecs {
		fail("the response took %.1f s, the budget is %.1f s", r.Response, e.MaxSecs)
	}
}

// Test is one case's record in results.json: the E2E report's test fields (docs/dev/e2e.md)
// plus the case's runs.
type Test struct {
	Suite         string   `json:"suite"`
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	File          string   `json:"file"`
	Line          int      `json:"line"`
	Status        string   `json:"status"`
	DurationMS    int64    `json:"duration_ms"`
	Error         *string  `json:"error"`
	Screenshots   []string `json:"screenshots"`
	Trace         *string  `json:"trace"`
	ConsoleErrors []string `json:"console_errors"`
	// Artifacts are each run's audio and node log, relative to the run's directory.
	Artifacts      []string `json:"artifacts"`
	ModelDependent bool     `json:"model_dependent"`
	Passes         int      `json:"passes"`
	Runs           []Run    `json:"runs"`
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("voicelane", flag.ContinueOnError)
	root := fs.String("root", ".", "the jarvis-server checkout (for .sandbox/<name>)")
	sandbox := fs.String("sandbox", os.Getenv("JARVIS_SANDBOX"), "sandbox name")
	casesPath := fs.String("cases", "e2e/voice/cases.json", "cases file")
	filter := fs.String("filter", "", "only cases whose name matches this regexp")
	repeat := fs.Int("repeat", 3, "runs of each model-dependent case")
	out := fs.String("out", "", "output directory (results.json, per-case audio and logs); required")
	rel := fs.String("rel", "", "the prefix artifact paths get in results.json (the report's dir, relative)")
	agent := fs.String("agent", "", "the jarvis-agent binary; required")
	wakeModels := fs.String("wake-models", "", "openWakeWord models directory (default: the node downloads them into its own cache)")
	audioMode := fs.String("audio", "wav", "wav (WAV files in and out), pipewire (scripts/e2e's private PipeWire) or pi (a Pi's own mic and speaker, over ssh)")
	dev := fs.String("alsa-device", "pipewire", "pipewire: the ALSA PCM the node opens for its mic and speaker")
	mic := fs.String("mic", "e2e_mic", "pipewire: the virtual source the node listens to (played into)")
	spk := fs.String("speaker", "e2e_speaker", "pipewire: the sink the node plays to (its monitor is recorded)")
	voices := fs.String("voices", "", "recorded voices: DIR/<slug of the text>.wav says a case's text instead of the TTS (\"Hey Jarvis.\" is hey-jarvis.wav)")
	piHost := fs.String("pi-host", os.Getenv("JARVIS_E2E_PI"), "pi: the Pi, as ssh reaches it (user@host)")
	piCard := fs.String("pi-card", "auto", "pi: the HAT's ALSA card index (auto: the seeed card)")
	sshCmd := fs.String("ssh", "ssh", "pi: the ssh command")
	replyWait := fs.Duration("reply-wait", 60*time.Second, "how long after the audio a reply may take")
	silentWait := fs.Duration("silent-wait", 5*time.Second, "how long after the audio a silent case is listened to")
	var agentArgs stringList
	fs.Var(&agentArgs, "agent-arg", "an extra jarvis-agent listen flag (repeatable), e.g. --agent-arg=--wake-threshold=0.9")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || *agent == "" || *sandbox == "" {
		return errors.New("--sandbox, --out and --agent are required")
	}
	// The node's XDG dirs must be absolute, and its flags outlive the working directory.
	for _, p := range []*string{out, agent, wakeModels, root, voices} {
		if *p == "" {
			continue
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return err
		}
		*p = abs
	}
	var re *regexp.Regexp
	if *filter != "" {
		var err error
		if re, err = regexp.Compile(*filter); err != nil {
			return fmt.Errorf("--filter: %w", err)
		}
	}
	env, err := sandboxEnv(*root, *sandbox)
	if err != nil {
		return err
	}
	cases, err := LoadCases(*casesPath)
	if err != nil {
		return err
	}
	l := &lane{node: &node{cc: env["SBX_CC"], id: env["SBX_NODE_ID"], key: env["SBX_NODE_KEY"]},
		tapLog: filepath.Join(*root, ".sandbox", *sandbox, "llm.jsonl"), agent: *agent, agentArgs: agentArgs,
		wakeModels: *wakeModels, voices: *voices, out: *out, audio: *audioMode, replyWait: *replyWait, silentWait: *silentWait, nodeHome: filepath.Join(*out, "node")}
	if err := os.MkdirAll(filepath.Join(l.nodeHome, "config"), 0o700); err != nil {
		return err
	}
	if l.creds, err = writeCreds(filepath.Join(l.nodeHome, "config"), env); err != nil {
		return err
	}
	switch *audioMode {
	case "wav":
		l.rig = wavRig{l}
	case "pipewire":
		r, err := newPWRig(ctx, l, *dev, *mic, *spk)
		if err != nil {
			return err
		}
		l.rig = r
	case "pi":
		ssh := []string{*sshCmd, "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4"}
		r, err := newPiRig(ctx, l, env, ssh, *piHost, *piCard)
		if err != nil {
			return err
		}
		l.rig = r
	default:
		return fmt.Errorf("--audio %q: wav, pipewire or pi", *audioMode)
	}
	defer l.rig.close()

	start := time.Now()
	tests := []Test{}
	failed := 0
	for _, c := range cases {
		if re != nil && !re.MatchString(c.Name) {
			continue
		}
		t := l.runCase(ctx, c, *casesPath, max(1, *repeat), *rel)
		if t.Status == "failed" {
			failed++
		}
		fmt.Fprintf(stdout, "voice: %-7s %s (%d/%d)\n", t.Status, c.Name, t.Passes, len(t.Runs))
		tests = append(tests, t)
	}
	status := "passed"
	if failed > 0 || len(tests) == 0 {
		status = "failed"
	}
	res := map[string]any{"suite": "voice", "status": status, "audio": *audioMode,
		"duration_ms": time.Since(start).Milliseconds(), "tests": tests}
	if r, ok := l.rig.(*piRig); ok {
		// What the Pi ran before and after, and what was stopped and restored.
		r.close()
		res["pi"] = r.notes()
		for _, n := range r.notes() {
			fmt.Fprintln(stdout, "voice: pi:", n)
		}
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(filepath.Join(*out, "results.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if status != "passed" {
		return errFailed{failed}
	}
	return nil
}

func (l *lane) runCase(ctx context.Context, c Case, file string, repeat int, rel string) (t Test) {
	t = Test{Suite: "voice", ID: slug(c.Name), Title: c.Name, File: file, Line: c.Line, ModelDependent: c.Model,
		Screenshots: []string{}, ConsoleErrors: []string{}, Artifacts: []string{}, Runs: []Run{}}
	start := time.Now()
	defer func() { t.DurationMS = time.Since(start).Milliseconds() }()
	if c.Needs == "pipewire" && l.audio == "wav" {
		t.Status = "skipped"
		e := "needs real-time audio (--audio pipewire or pi): a WAV speaker is written at once, so nothing can be said over it"
		t.Error = &e
		return t
	}
	runs := 1
	if c.Model {
		runs = repeat
	}
	var errs []string
	for i := 1; i <= runs; i++ {
		sub := filepath.Join("cases", t.ID, fmt.Sprintf("run-%d", i))
		r := l.runOnce(ctx, c, filepath.Join(l.out, sub), int64(i))
		for _, f := range []string{"utterance.wav", "reply.wav", "agent.log"} {
			if _, err := os.Stat(filepath.Join(l.out, sub, f)); err == nil {
				t.Artifacts = append(t.Artifacts, filepath.Join(rel, sub, f))
			}
		}
		if len(r.Failures) == 0 {
			t.Passes++
		} else {
			errs = append(errs, fmt.Sprintf("run %d: %s", i, strings.Join(r.Failures, "; ")))
		}
		if b, err := json.MarshalIndent(r, "", "  "); err == nil {
			os.WriteFile(filepath.Join(l.out, sub, "run.json"), b, 0o644)
		}
		t.Runs = append(t.Runs, r)
	}
	t.Status = "passed"
	if t.Passes < runs {
		t.Status = "failed"
		e := fmt.Sprintf("%d/%d runs passed\n%s", t.Passes, runs, strings.Join(errs, "\n"))
		t.Error = &e
	}
	return t
}

func (l *lane) runOnce(ctx context.Context, c Case, dir string, seed int64) (r Run) {
	r.Dir = dir
	failed := func(err error) Run { r.Failures = append(r.Failures, err.Error()); return r }
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return failed(err)
	}
	s, rate, err := renderCase(ctx, l.speak, c.Audio, seed)
	if err != nil {
		return failed(err)
	}
	// A second of room tone first, so the detector has settled before the wake word.
	s = append(make([]float32, rate), s...)
	in, out := filepath.Join(dir, "utterance.wav"), filepath.Join(dir, "reply.wav")
	if err := writeWAVFile(in, s, rate); err != nil {
		return failed(err)
	}
	mark := fileSize(l.tapLog)
	wait := l.replyWait
	if c.Expect.Silent {
		wait = l.silentWait
	}
	turns, resp, err := l.rig.play(ctx, in, out, dir, wait)
	if err != nil {
		return failed(err)
	}
	if len(turns) > 0 {
		r.Turn = &turns[0]
	}
	r.Response = resp
	if b, err := os.ReadFile(filepath.Join(dir, "agent.log")); err == nil {
		r.Wakes = wakes(string(b))
	}
	r.ModelCalls, r.ModelTools = modelCalls(l.tapLog, mark)
	if rs, rrate, err := readWAVFile(out); err == nil {
		if r.ReplySecs, _ = voiced(rs, rrate); r.ReplySecs > 0 {
			b, _ := os.ReadFile(out)
			if r.Reply, err = l.node.Transcribe(ctx, b); err != nil {
				r.Failures = append(r.Failures, "transcribing the reply: "+err.Error())
			}
		}
	}
	judge(c.Expect, &r)
	return r
}

// speak is a recorded voice saying text (--voices DIR/<slug of the text>.wav) or, without
// one, the sandbox's TTS.
func (l *lane) speak(ctx context.Context, text string) ([]byte, error) {
	if l.voices != "" {
		if b, err := os.ReadFile(filepath.Join(l.voices, slug(text)+".wav")); err == nil {
			return b, nil
		}
	}
	return l.node.Speak(ctx, text)
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, " ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }
