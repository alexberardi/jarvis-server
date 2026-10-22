// Command siplane runs the L1 SIP lane's scenarios (docs/cc/phone-sip.md §5): FreePBX 17
// behaviours on Asterisk 22 in Docker, played by baresip and SIPp far ends, with what each far
// end heard transcribed by the sandbox's CPU whisper and checked against what was said.
//
//	scripts/sandbox up --name N --voice --sip     (TTS, whisper and the lane)
//	go run ./tools/siplane [--sandbox N] [--filter RE] [--out DIR] [--list]
//
// Exits 1 when a scenario fails, 2 when the lane or sandbox is missing. --out keeps every
// far end's recording (<scenario>-<party>.wav), its transcript, the PBX's endpoint and
// contacts and results.md. The Jarvis extension (100) is a baresip stand-in for the
// scenarios that play a desk phone or the inbound route (jarvisd answers no calls yet); the
// jarvis-* scenarios run jarvisd's own SIP provider as 100 instead (jarvis.go).
package main

import (
	"context"
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
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout)
	var fail errFailed
	switch {
	case errors.As(err, &fail):
		fmt.Fprintln(os.Stderr, "siplane:", err)
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "siplane:", err)
		os.Exit(2)
	}
}

type errFailed struct{ n, of int }

func (e errFailed) Error() string { return fmt.Sprintf("%d of %d scenarios failed", e.n, e.of) }

func run(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("siplane", flag.ContinueOnError)
	name := fs.String("sandbox", os.Getenv("JARVIS_SANDBOX"), "the sandbox (up with --voice --sip); default $JARVIS_SANDBOX, else default")
	filter := fs.String("filter", "", "run only the scenarios whose name matches this regexp")
	out := fs.String("out", "", "keep recordings, transcripts and results.md here")
	list := fs.Bool("list", false, "list the scenarios and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *list {
		for _, s := range scenarios {
			fmt.Fprintf(stdout, "%-22s %s\n", s.Name, s.What)
		}
		return nil
	}
	re, err := regexp.Compile(*filter)
	if err != nil {
		return err
	}
	if *name == "" {
		*name = "default"
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	l, err := openLane(root, *name)
	if err != nil {
		return err
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			return err
		}
		l.out = *out
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# SIP lane (L1), sandbox %s, %s\n\n| scenario | result | time | what |\n|---|---|---|---|\n", *name, time.Now().Format(time.RFC3339))
	var details strings.Builder
	ran, failed := 0, 0
	for _, s := range scenarios {
		if !re.MatchString(s.Name) {
			continue
		}
		ran++
		start := time.Now()
		var r report
		if err := s.Run(ctx, l, &r); err != nil {
			r.failf("lane error: %v", err)
		}
		// A far end that died fails this scenario, and the lane restarts for the next one.
		if dead, err := l.deadFarEnds(ctx, s.Name); err != nil {
			r.failf("lane error: %v", err)
		} else if len(dead) > 0 {
			r.failf("far end %s during this scenario (its log: %s-<party>-baresip.log under --out); restarting the lane", strings.Join(dead, ", "), s.Name)
			if err := l.restart(ctx); err != nil {
				return fmt.Errorf("after %s: %w", s.Name, err)
			}
		}
		took := time.Since(start).Round(100 * time.Millisecond)
		res := "PASS"
		if len(r.failures) > 0 {
			res = "FAIL"
			failed++
		}
		fmt.Fprintf(stdout, "%s %s (%s): %s\n", res, s.Name, took, s.What)
		fmt.Fprintf(&md, "| %s | %s | %s | %s |\n", s.Name, res, took, strings.ReplaceAll(s.What, "|", "/"))
		fmt.Fprintf(&details, "\n## %s: %s\n\n", s.Name, res)
		for _, n := range r.notes {
			fmt.Fprintf(stdout, "    %s\n", n)
			fmt.Fprintf(&details, "- %s\n", n)
		}
		for _, f := range r.failures {
			fmt.Fprintf(stdout, "    FAIL: %s\n", f)
			fmt.Fprintf(&details, "- **FAIL**: %s\n", f)
		}
		if ctx.Err() != nil {
			break
		}
	}
	if ran == 0 {
		return fmt.Errorf("no scenario matches %q (--list)", *filter)
	}
	fmt.Fprintf(stdout, "%d/%d passed\n", ran-failed, ran)
	fmt.Fprintf(&md, "\n%d/%d passed (max WER %.2f)\n%s", ran-failed, ran, maxWER, details.String())
	l.keep("results.md", md.String())
	if failed > 0 {
		return errFailed{failed, ran}
	}
	return nil
}

// openLane reads the sandbox's sandbox.env (its voice) and its lane's lane.env.
func openLane(root, name string) (*lane, error) {
	dir := filepath.Join(root, ".sandbox", name)
	sbx, err := readLaneEnv(filepath.Join(dir, "sandbox.env"))
	if err != nil {
		return nil, fmt.Errorf("sandbox %s is not up: scripts/sandbox up --name %s --voice --sip", name, name)
	}
	if sbx["SBX_VOICE"] == "" {
		return nil, fmt.Errorf("sandbox %s has no voice: scripts/sandbox up --name %s --voice --sip", name, name)
	}
	env, err := readLaneEnv(filepath.Join(dir, "sip", "lane.env"))
	if err != nil {
		return nil, err
	}
	return &lane{env: env, root: root,
		voice: &voice{cc: sbx["SBX_CC"], id: sbx["SBX_NODE_ID"], key: sbx["SBX_NODE_KEY"], http: &http.Client{Timeout: 3 * time.Minute}}}, nil
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
