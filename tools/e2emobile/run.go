package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// userSerial is the user's own dev emulator. The harness never sends it a command.
const userSerial = "emulator-5554"

type config struct {
	flowsDir, rel, out      string
	coverage, nav           string // e2e/coverage.json and the checkout's src/navigation; both or neither
	maestro, adb, serial    string
	env                     []string // KEY=VALUE, passed to every flow as -e
	filter                  string
	flowTimeout, adbTimeout time.Duration
}

// Record is one flow in results.json: the record shape of e2e/web/reporter.ts (merged into the
// run's report.json by scripts/lib/e2e_report.py), plus skip_reason and attachments. Paths are
// relative to the run directory (the parent of --out).
type Record struct {
	Suite         string   `json:"suite"`
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	File          string   `json:"file"`
	Line          int      `json:"line"`
	Status        string   `json:"status"` // passed, failed, timedOut, skipped
	DurationMS    int64    `json:"duration_ms"`
	Error         *string  `json:"error"`
	Screenshots   []string `json:"screenshots"`
	Trace         *string  `json:"trace"`
	ConsoleErrors []string `json:"console_errors"`
	SkipReason    string   `json:"skip_reason,omitempty"`
	Attachments   []string `json:"attachments,omitempty"`
}

// Results is results.json.
type Results struct {
	Suite      string   `json:"suite"`
	Status     string   `json:"status"`
	DurationMS int64    `json:"duration_ms"`
	Tests      []Record `json:"tests"`
}

// errNoFlows: nothing to run; scripts/e2e reports the suite as not run (an error), not as green.
var errNoFlows = errors.New("no flows")

// run plays every flow one at a time (each with its own Maestro process, logcat cleared before
// it, and its artifacts on failure) and writes <out>/results.json.
func run(ctx context.Context, c *config, flows []Flow) (*Results, error) {
	if c.serial == "" || c.serial == userSerial {
		return nil, fmt.Errorf("refusing serial %q: the harness only drives its own emulator", c.serial)
	}
	if len(flows) == 0 {
		return nil, errNoFlows
	}
	if err := os.MkdirAll(c.out, 0o755); err != nil {
		return nil, err
	}
	t0 := time.Now()
	res := &Results{Suite: "mobile", Status: "passed", Tests: []Record{}}
	if c.coverage != "" {
		cr, rec, err := coverage(c.coverage, c.nav, c.flowsDir, c.rel, c.out)
		if err != nil {
			msg := err.Error()
			rec.Status, rec.Error = "failed", &msg
		}
		fmt.Fprintf(os.Stderr, "e2emobile: %-8s %s (%d screens: %d covered, %d manual, %d pending)\n",
			rec.Status, rec.Title, cr.Screens, len(cr.Covered), len(cr.Manual), len(cr.Pending))
		if rec.Error != nil {
			fmt.Fprint(os.Stderr, *rec.Error)
			res.Status = "failed"
		}
		res.Tests = append(res.Tests, rec)
	}
	for _, f := range flows {
		if ctx.Err() != nil {
			break
		}
		r := c.runFlow(ctx, f)
		fmt.Fprintf(os.Stderr, "e2emobile: %-8s %s (%s, %.1f s)\n", r.Status, f.Name, f.File, float64(r.DurationMS)/1000)
		if r.Status != "passed" && r.Status != "skipped" {
			res.Status = "failed"
		}
		res.Tests = append(res.Tests, r)
	}
	if ctx.Err() != nil {
		res.Status = "interrupted"
	}
	res.DurationMS = time.Since(t0).Milliseconds()
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, err
	}
	return res, os.WriteFile(filepath.Join(c.out, "results.json"), append(raw, '\n'), 0o644)
}

func (c *config) runFlow(ctx context.Context, f Flow) Record {
	r := Record{Suite: "mobile", ID: f.Slug, Title: f.Name, File: f.File, Line: 1, Status: "skipped",
		Screenshots: []string{}, ConsoleErrors: []string{}}
	if f.Skipped() {
		r.SkipReason = f.SkipReason
		return r
	}
	dir := filepath.Join(c.out, f.Slug)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return failed(r, err.Error())
	}
	rel := func(p string) string {
		s, err := filepath.Rel(filepath.Dir(c.out), p)
		if err != nil {
			return p
		}
		return filepath.ToSlash(s)
	}
	_, _ = c.adbCmd(ctx, "logcat", "-c") // this flow's log only

	junit, debug, logf := filepath.Join(dir, "junit.xml"), filepath.Join(dir, "maestro"), filepath.Join(dir, "maestro.log")
	args := []string{"--device", c.serial, "test", f.Path, "--format", "junit", "--output", junit, "--debug-output", debug}
	for _, kv := range c.env {
		args = append(args, "-e", kv)
	}
	fctx, cancel := context.WithTimeout(ctx, c.flowTimeout)
	defer cancel()
	var log bytes.Buffer
	cmd := exec.CommandContext(fctx, c.maestro, args...)
	cmd.Stdout, cmd.Stderr = &log, &log
	cmd.WaitDelay = 10 * time.Second
	start := time.Now()
	err := cmd.Run()
	r.DurationMS = time.Since(start).Milliseconds()
	_ = os.WriteFile(logf, log.Bytes(), 0o644)
	r.Attachments = []string{rel(logf)}
	if err == nil {
		r.Status = "passed"
		return r
	}

	msg := junitFailure(junit)
	if msg == "" {
		msg = tail(log.String(), 20)
	}
	if fctx.Err() == context.DeadlineExceeded {
		r.Status = "timedOut"
		msg = fmt.Sprintf("the flow did not finish in %s\n%s", c.flowTimeout, msg)
	} else {
		r.Status = "failed"
	}
	if msg == "" {
		msg = err.Error()
	}
	r.Error = &msg

	// The screen as the flow left it: a picture, the view tree (texts, testIDs) and the log.
	png := filepath.Join(dir, "screenshot.png")
	if out, err := c.adbCmd(ctx, "exec-out", "screencap -p"); err == nil && bytes.HasPrefix(out, []byte("\x89PNG")) &&
		os.WriteFile(png, out, 0o644) == nil {
		r.Screenshots = append(r.Screenshots, rel(png))
	}
	if _, err := c.adbCmd(ctx, "shell", "uiautomator dump /data/local/tmp/e2emobile.xml >/dev/null"); err == nil {
		if out, err := c.adbCmd(ctx, "exec-out", "cat /data/local/tmp/e2emobile.xml"); err == nil && len(out) > 0 {
			p := filepath.Join(dir, "window.xml")
			if os.WriteFile(p, out, 0o644) == nil {
				r.Attachments = append(r.Attachments, rel(p))
			}
		}
	}
	if out, err := c.adbCmd(ctx, "logcat", "-d"); err == nil {
		p := filepath.Join(dir, "logcat.txt")
		if os.WriteFile(p, out, 0o644) == nil {
			r.Attachments = append(r.Attachments, rel(p))
		}
		r.ConsoleErrors = jsErrors(string(out), 20)
	}
	if st, err := os.Stat(debug); err == nil && st.IsDir() {
		r.Attachments = append(r.Attachments, rel(debug))
	}
	return r
}

func failed(r Record, msg string) Record {
	r.Status = "failed"
	r.Error = &msg
	return r
}

// adbCmd runs adb against the harness emulator only (`-s serial` on every call).
func (c *config) adbCmd(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.adbTimeout)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, c.adb, append([]string{"-s", c.serial}, args...)...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// junitFailure is the failure text in Maestro's JUnit report, if it wrote one.
func junitFailure(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Cases []struct {
			Failures []struct {
				Message string `xml:"message,attr"`
				Text    string `xml:",chardata"`
			} `xml:"failure"`
			Errors []struct {
				Message string `xml:"message,attr"`
				Text    string `xml:",chardata"`
			} `xml:"error"`
		} `xml:"testsuite>testcase"`
	}
	if xml.Unmarshal(raw, &doc) != nil {
		return ""
	}
	var parts []string
	for _, tc := range doc.Cases {
		for _, f := range append(tc.Failures, tc.Errors...) {
			for _, s := range []string{f.Message, f.Text} {
				if s = strings.TrimSpace(s); s != "" && !contains(parts, s) {
					parts = append(parts, s)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

// jsErrors picks the app's JavaScript errors out of a logcat (threadtime format: the level
// letter, then the tag): React Native's console.error and uncaught exceptions.
func jsErrors(logcat string, max int) []string {
	out := []string{}
	for _, l := range strings.Split(logcat, "\n") {
		l = strings.TrimSpace(l)
		if (strings.Contains(l, " E ReactNativeJS") || strings.Contains(l, " E AndroidRuntime")) && len(out) < max {
			out = append(out, l)
		}
	}
	return out
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
