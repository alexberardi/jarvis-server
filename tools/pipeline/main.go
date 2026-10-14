// Command pipeline drives command-center's voice pipeline from text, as a node would after
// STT, against a sandbox (scripts/sandbox) running on a real model. It is how an agent checks
// what Jarvis does with an utterance without anyone speaking.
//
//	pipeline say [--sandbox NAME] [--tools SET] [--results JSON] [--json] [-v] UTTERANCE...
//	    one conversation, one turn per argument; prints the node and server tool calls, the
//	    node's answers and the reply. Every node tool answers {"success": true} unless
//	    --results gives {"tool": output, ...}.
//	pipeline run [--sandbox NAME] [--repeat N] [--filter RE] [--out DIR] [--json] [-v] [PATH...]
//	    play scenario files (default tools/pipeline/scenarios) and check their expectations;
//	    exits 1 when any fails. --out writes each run's full log (model calls included).
//	pipeline tap [--listen ADDR] [--upstream auto|URL] [--log FILE] [--exclude DIR]
//	    the LLM recording proxy scripts/sandbox starts in front of the installed model.
//
// --sandbox defaults to $JARVIS_SANDBOX, else "default"; the sandbox's sandbox.env supplies
// command-center's URL and the seeded node's credentials, and its llm.jsonl the model calls.
// TOOL SETS: node (a real Pi node's commands, the default), client, none, or a JSON file.
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
		return errors.New("usage: pipeline say|run|tap ... (see the package doc)")
	}
	switch args[0] {
	case "tap":
		return runTap(ctx, args[1:])
	case "say":
		return runSay(ctx, args[1:], stdout)
	case "run":
		return runScenarios(ctx, args[1:], stdout)
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
	return &Driver{
		Node:   &Node{BaseURL: env["SBX_CC"], ID: env["SBX_NODE_ID"], Key: env["SBX_NODE_KEY"]},
		TapLog: filepath.Join(dir, "llm.jsonl"),
		Root:   root,
	}, nil
}

func runTap(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tap", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:17799", "address to serve on")
	upstream := fs.String("upstream", "auto", "engine base URL, or auto: the installed jarvisd's llama-server, looked up per request")
	logPath := fs.String("log", "llm.jsonl", "JSONL log to append to")
	exclude := fs.String("exclude", "", "with auto: ignore engines whose command line contains this")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	up := func() (string, error) { return *upstream, nil }
	if *upstream == "auto" {
		up = func() (string, error) {
			u, _, err := InstalledLlama(*exclude)
			return u, err
		}
	}
	srv := &http.Server{Addr: *listen, Handler: &Tap{Upstream: up, Log: f, Client: &http.Client{}}}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	fmt.Fprintf(os.Stderr, "pipeline tap: %s → %s, logging to %s\n", *listen, *upstream, *logPath)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runSay(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("say", flag.ContinueOnError)
	sandbox := fs.String("sandbox", "", "sandbox name")
	tools := fs.String("tools", "node", "tool set: node, client, none or a JSON file")
	results := fs.String("results", "", `node outputs per tool, e.g. {"get_weather": {"success": true, "message": "Sunny, 21°"}}`)
	asJSON := fs.Bool("json", false, "print the full log as JSON")
	verbose := fs.Bool("v", false, "also print each model call's reasoning and reply")
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
	sc := Scenario{Name: "say", Tools: *tools}
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	d, err := sandboxDriver(*sandbox)
	if err != nil {
		return err
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
	}
	var sums []summary
	failed := 0
	for _, sc := range scs {
		if re != nil && !re.MatchString(sc.Name) {
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
				name := fmt.Sprintf("%s.%d.json", slug(sc.Name), i+1)
				_ = os.WriteFile(filepath.Join(*outDir, name), b, 0o644)
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
		for _, c := range t.ServerCalls {
			fmt.Fprintf(w, "  server %s %s\n", c.Name, jsonText(c.Args))
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
