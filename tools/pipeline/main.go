// Command pipeline drives command-center's voice pipeline from text, as a node would after
// STT, against a sandbox (scripts/sandbox) running on a real model. It is how an agent checks
// what Jarvis does with an utterance without anyone speaking.
//
//	pipeline say [--sandbox NAME] [--tools SET] [--speaker WHO] [--node desktop] [--results JSON] [--voice] [--voice-name V] [--json] [-v] UTTERANCE...
//	    one conversation, one turn per argument; prints the node and server tool calls, the
//	    node's answers and the reply. Every node tool answers {"success": true} unless
//	    --results gives {"tool": output, ...}. The speaker defaults to the seeded superuser
//	    (the sandbox trusts node speaker hints, JARVIS_DEV_SPEAKER_HINTS); --speaker "" plays
//	    an unidentified voice. --node desktop plays the turns as a desktop agent the primary
//	    signed in on, whose turns act as that owner (use it with --speaker "").
//	pipeline run [--sandbox NAME] [--repeat N] [--filter RE] [--out DIR] [--voice] [--voice-name V] [--json] [-v] [PATH...]
//	    play scenario files (default tools/pipeline/scenarios) and check their expectations;
//	    exits 1 when any fails. --out writes each run's full log (model calls included) and,
//	    for voiced turns, the audio.
//	pipeline toolset desktop
//	    print the desktop agent's client tools as its own toolsets render them (the source of
//	    toolsets/desktop.json: pipeline toolset desktop > tools/pipeline/toolsets/desktop.json).
//	pipeline tap [--listen ADDR] [--upstream URL] [--log FILE]
//	    the LLM recording proxy scripts/sandbox starts in front of the installed jarvisd's LLM
//	    API (:7704); JARVIS_TAP_APP_ID / JARVIS_TAP_APP_KEY are sent as its app credentials.
//
// --voice speaks each turn instead of typing it (the sandbox must be up with --voice): the
// sandbox's TTS says it, its STT transcribes it through command-center's node media proxy as
// a Pi would, and the transcript is the voice command; the transcript prints as "heard".
// --voice-name picks a Kokoro voice (e.g. af_heart; default the house voice) and implies --voice.
//
// --sandbox defaults to $JARVIS_SANDBOX, else "default"; the sandbox's sandbox.env supplies
// command-center's URL and the seeded node's credentials, and its llm.jsonl the model calls.
// TOOL SETS: node (a real Pi node's commands, the default), client, desktop (the desktop agent's
// tools, toolsets/desktop.json; the default for a "desktop" node, which also sends no devices),
// none, or a JSON file.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout)
	var fail errFailed
	switch {
	case errors.As(err, &fail):
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "pipeline:", err)
		os.Exit(2)
	}
}

// errFailed is a run whose expectations failed (exit 1, already reported).
type errFailed struct{ n int }

func (e errFailed) Error() string { return fmt.Sprintf("%d failed", e.n) }

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: pipeline say|run|tap|toolset ... (see the package doc)")
	}
	switch args[0] {
	case "tap":
		return runTap(ctx, args[1:])
	case "say":
		return runSay(ctx, args[1:], stdout)
	case "run":
		return runScenarios(ctx, args[1:], stdout)
	case "toolset":
		if len(args) != 2 || args[1] != "desktop" {
			return errors.New("usage: pipeline toolset desktop")
		}
		b, err := RenderDesktopToolset()
		if err != nil {
			return err
		}
		_, err = stdout.Write(b)
		return err
	}
	return fmt.Errorf("unknown command %q (say, run, tap)", args[0])
}

// repoRoot walks up from the working directory to the module root.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(b), "module github.com/alexberardi/jarvis-server") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("not inside the jarvis-server checkout")
		}
		dir = parent
	}
}

// sandboxDriver builds a Driver from a sandbox's env file.
func sandboxDriver(name string) (*Driver, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = os.Getenv("JARVIS_SANDBOX")
	}
	if name == "" {
		name = "default"
	}
	dir := filepath.Join(root, ".sandbox", name)
	f, err := os.Open(filepath.Join(dir, "sandbox.env"))
	if err != nil {
		return nil, fmt.Errorf("sandbox %s: %w (scripts/sandbox up --name %s)", name, err, name)
	}
	defer f.Close()
	kvs, err := config.ParseEnv(f)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range kvs {
		env[kv[0]] = kv[1]
	}
	if env["SBX_CC"] == "" || env["SBX_NODE_ID"] == "" || env["SBX_NODE_KEY"] == "" {
		return nil, fmt.Errorf("sandbox %s: sandbox.env lacks SBX_CC / SBX_NODE_ID / SBX_NODE_KEY", name)
	}
	primary, _ := strconv.ParseInt(env["SBX_USER_ID"], 10, 64)
	return &Driver{
		Node:    &Node{BaseURL: env["SBX_CC"], ID: env["SBX_NODE_ID"], Key: env["SBX_NODE_KEY"]},
		TapLog:  filepath.Join(dir, "llm.jsonl"),
		Root:    root,
		Primary: primary,
		Voices:  &AdminVoice{AdminURL: env["SBX_ADMIN"], AuthURL: env["SBX_AUTH"], Email: env["SBX_EMAIL"], Password: env["SBX_PASSWORD"]},
		// scripts/sandbox up --voice exports the stt model it set up.
		VoiceReady: env["SBX_VOICE"] != "",
		Desktop: &DesktopSetup{AuthURL: env["SBX_AUTH"], Email: env["SBX_EMAIL"], Password: env["SBX_PASSWORD"],
			StateFile: filepath.Join(dir, "desktop-node.json")},
	}, nil
}

func runTap(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tap", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:17799", "address to serve on")
	upstream := fs.String("upstream", "http://127.0.0.1:7704", "where model calls go: the installed jarvisd's LLM API (default) or any OpenAI-compatible base URL")
	logPath := fs.String("log", "llm.jsonl", "JSONL log to append to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// Credentials come from the environment, never argv (ps shows argv to every user).
	h := http.Header{}
	if id, key := os.Getenv("JARVIS_TAP_APP_ID"), os.Getenv("JARVIS_TAP_APP_KEY"); id != "" && key != "" {
		h.Set("X-Jarvis-App-Id", id)
		h.Set("X-Jarvis-App-Key", key)
	}
	up := *upstream
	srv := &http.Server{Addr: *listen, Handler: &Tap{Upstream: func() (string, error) { return up, nil }, Header: h, Log: f, Client: &http.Client{}}}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	fmt.Fprintf(os.Stderr, "pipeline tap: %s → %s (app credentials: %v), logging to %s\n", *listen, up, len(h) > 0, *logPath)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runSay(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("say", flag.ContinueOnError)
	sandbox := fs.String("sandbox", "", "sandbox name")
	tools := fs.String("tools", "", "tool set: node, client, desktop, none or a JSON file (default node, or desktop with --node desktop)")
	home := fs.String("home", "", `device payload: a tools/pipeline/homes name (default avery), "none", or a JSON file`)
	vocab := fs.String("stt-vocabulary", "", `app names for whisper's prompt, as a desktop agent sends them ("Alacritty, Firefox, Omawrite"); default with the desktop tools or node: a typical agent's, "" none`)
	speaker := fs.String("speaker", "primary", `who is speaking: "primary" (the seeded superuser), a user id, or "" (unidentified)`)
	node := fs.String("node", "", `who plays the turns: "" the seeded node, or "desktop" (a desktop agent the primary signed in on; pair with --speaker "")`)
	owner := fs.String("owner", "", `with --node desktop: "" the primary owns it, "removed" its owner left the household`)
	results := fs.String("results", "", `node outputs per tool, e.g. {"get_weather": {"success": true, "message": "Sunny, 21°"}}`)
	asJSON := fs.Bool("json", false, "print the full log as JSON")
	verbose := fs.Bool("v", false, "also print each model call's reasoning and reply")
	voice := fs.Bool("voice", false, "speak each turn: TTS, then STT as the node, and send what was heard (sandbox up --voice)")
	voiceName := fs.String("voice-name", "", "Kokoro voice to speak in (implies --voice; default the house voice)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("say: nothing to say")
	}
	d, err := sandboxDriver(*sandbox)
	if err != nil {
		return err
	}
	d.Voice, d.VoiceName = *voice, *voiceName
	if (d.Voice || d.VoiceName != "") && !d.VoiceReady {
		return errors.New("--voice: this sandbox has no TTS/STT (scripts/sandbox up --voice)")
	}
	sc := Scenario{Name: "say", Tools: *tools, Speaker: *speaker, Node: *node, Owner: *owner, Home: *home}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "stt-vocabulary" {
			sc.STTVocabulary = vocab
		}
	})
	if *results != "" {
		if err := json.Unmarshal([]byte(*results), &sc.Results); err != nil {
			return fmt.Errorf("--results: %w", err)
		}
	}
	for _, u := range fs.Args() {
		sc.Turns = append(sc.Turns, Turn{Say: u})
	}
	log := d.Run(ctx, sc)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(log)
	}
	printLog(stdout, log, *verbose)
	return nil
}

func runScenarios(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	sandbox := fs.String("sandbox", "", "sandbox name")
	repeat := fs.Int("repeat", 1, "runs per scenario (the model is not deterministic)")
	filter := fs.String("filter", "", "only scenarios whose name matches this regexp")
	outDir := fs.String("out", "", "write each run's full log here")
	asJSON := fs.Bool("json", false, "print a JSON summary instead of text")
	verbose := fs.Bool("v", false, "print every turn, not only failures")
	voice := fs.Bool("voice", false, `voice every scenario (sandbox up --voice); scenarios with "voice": true always are`)
	voiceName := fs.String("voice-name", "", "Kokoro voice for every voiced turn (implies --voice)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d, err := sandboxDriver(*sandbox)
	if err != nil {
		return err
	}
	d.Voice, d.VoiceName = *voice, *voiceName
	if (d.Voice || d.VoiceName != "") && !d.VoiceReady {
		return errors.New("--voice: this sandbox has no TTS/STT (scripts/sandbox up --voice)")
	}
	paths := fs.Args()
	if len(paths) == 0 {
		paths = []string{filepath.Join(d.Root, "tools/pipeline/scenarios")}
	}
	scs, err := LoadScenarios(paths)
	if err != nil {
		return err
	}
	var re *regexp.Regexp
	if *filter != "" {
		if re, err = regexp.Compile(*filter); err != nil {
			return err
		}
	}
	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			return err
		}
	}
	type summary struct {
		Name     string   `json:"name"`
		Passed   int      `json:"passed"`
		Runs     int      `json:"runs"`
		Failures []string `json:"failures,omitempty"`
		Skipped  string   `json:"skipped,omitempty"`
	}
	var sums []summary
	failed := 0
	for _, sc := range scs {
		if re != nil && !re.MatchString(sc.Name) {
			continue
		}
		if sc.Voice && !d.VoiceReady && sc.Skip == "" {
			sc.Skip = "a voice scenario; the sandbox is not up with --voice"
		}
		if sc.Skip != "" {
			if !*asJSON {
				fmt.Fprintf(stdout, "SKIP      %s (%s)\n", sc.Name, sc.Skip)
			}
			sums = append(sums, summary{Name: sc.Name, Skipped: sc.Skip})
			continue
		}
		s := summary{Name: sc.Name, Runs: *repeat}
		for i := 0; i < *repeat; i++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log := d.Run(ctx, sc)
			if log.Pass {
				s.Passed++
			}
			for ti, t := range log.Turns {
				for _, f := range t.Failures {
					s.Failures = append(s.Failures, fmt.Sprintf("run %d turn %d: %s", i+1, ti+1, f))
				}
				if t.Error != "" && len(t.Failures) == 0 {
					s.Failures = append(s.Failures, fmt.Sprintf("run %d turn %d: %s", i+1, ti+1, t.Error))
				}
			}
			if *outDir != "" {
				b, _ := json.MarshalIndent(log, "", "  ")
				name := fmt.Sprintf("%s.%d", slug(sc.Name), i+1)
				_ = os.WriteFile(filepath.Join(*outDir, name+".json"), b, 0o644)
				for ti, t := range log.Turns {
					if t.Voice != nil && len(t.Voice.WAV) > 0 {
						_ = os.WriteFile(filepath.Join(*outDir, fmt.Sprintf("%s.t%d.wav", name, ti+1)), t.Voice.WAV, 0o644)
					}
				}
			}
			if !*asJSON && (*verbose || !log.Pass) {
				printLog(stdout, log, false)
			}
		}
		if s.Passed < s.Runs {
			failed++
		}
		if !*asJSON {
			mark := "PASS"
			if s.Passed < s.Runs {
				mark = "FAIL"
			}
			fmt.Fprintf(stdout, "%s %d/%d  %s\n", mark, s.Passed, s.Runs, sc.Name)
		}
		sums = append(sums, s)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(sums); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "\n%d scenarios, %d failed\n", len(sums), failed)
	}
	if failed > 0 {
		return errFailed{failed}
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// printLog is the human transcript of one scenario run.
func printLog(w io.Writer, l ScenarioLog, verbose bool) {
	fmt.Fprintf(w, "── %s (%s)\n", l.Name, l.ConversationID)
	for _, t := range l.Turns {
		fmt.Fprintf(w, "» %s\n", t.Say)
		if v := t.Voice; v != nil {
			heard := "heard = said"
			if v.WER > 0 || v.Heard == "" {
				heard = fmt.Sprintf("heard: %q (WER %.2f)", v.Heard, v.WER)
			} else if v.Heard != t.Say {
				heard = fmt.Sprintf("heard: %q", v.Heard)
			}
			fmt.Fprintf(w, "  %s  [%.1fs audio, tts %.1fs, stt %.1fs]\n", heard, v.AudioSeconds, v.TTSSeconds, v.STTSeconds)
		}
		for _, c := range t.ServerCalls {
			if c.Result == nil {
				fmt.Fprintf(w, "  server %s %s\n", c.Name, jsonText(c.Args))
			} else {
				fmt.Fprintf(w, "  server %s %s → %s\n", c.Name, jsonText(c.Args), truncate(jsonText(c.Result), 200))
			}
		}
		for _, c := range t.NodeCalls {
			fmt.Fprintf(w, "  node   %s %s → %s\n", c.Name, jsonText(c.Args), truncate(jsonText(c.Result), 200))
		}
		if verbose {
			for _, e := range t.LLM {
				fmt.Fprintf(w, "  llm #%d %.1fs %s\n", e.Seq, e.DurationMS/1000, e.Reply.Finish)
				if e.Reply.Reasoning != "" {
					fmt.Fprintf(w, "    thinking: %s\n", truncate(strings.ReplaceAll(e.Reply.Reasoning, "\n", " "), 600))
				}
				if e.Reply.Content != "" {
					fmt.Fprintf(w, "    content: %s\n", truncate(strings.ReplaceAll(e.Reply.Content, "\n", " "), 600))
				}
			}
		}
		if t.Error != "" {
			fmt.Fprintf(w, "  ERROR %s\n", t.Error)
		} else {
			fmt.Fprintf(w, "  ‹ %s  (%s, %.1fs, %d model calls)\n", t.Reply, t.Stop, t.Seconds, len(t.LLM))
		}
		for _, f := range t.Failures {
			fmt.Fprintf(w, "  ✗ %s\n", f)
		}
	}
}
