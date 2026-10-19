package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunResult is one run of a case.
type RunResult struct {
	Passed     bool    `json:"passed"`
	Rounds     int     `json:"rounds"` // model calls during the run (the sandbox's llm.jsonl)
	Seconds    float64 `json:"seconds"`
	Failure    string  `json:"failure,omitempty"`
	Screenshot string  `json:"screenshot,omitempty"` // relative to the report's directory
	Dir        string  `json:"dir"`                  // the run's files (state.json, logs), relative
}

// CaseResult is a case's line in the report: Status is passed, failed or skipped; CaseStatus
// the case file's pending or required.
type CaseResult struct {
	Name       string      `json:"name"`
	Phase      string      `json:"phase"`
	File       string      `json:"file"`
	CaseStatus string      `json:"case_status"`
	Status     string      `json:"status"`
	SkipReason string      `json:"skip_reason,omitempty"`
	Passed     int         `json:"passed"` // runs that passed
	Runs       int         `json:"runs"`
	Rounds     int         `json:"rounds"`  // model calls per run, the mean
	Seconds    float64     `json:"seconds"` // per run, the mean
	Failure    string      `json:"failure,omitempty"`
	Screenshot string      `json:"screenshot,omitempty"`
	RunResults []RunResult `json:"run_results,omitempty"`
}

// Report is report.json.
type Report struct {
	Run        string       `json:"run"`
	Model      string       `json:"model"`
	Label      string       `json:"label"`
	LLM        string       `json:"llm"`
	DurationMS int64        `json:"duration_ms"`
	Cases      []CaseResult `json:"cases"`
}

// passMark: a case passes when at least two thirds of its runs did (all of one, 2 of 3).
func passMark(runs int) int { return (2*runs + 2) / 3 }

// summarize fills a ran case's totals from its runs.
func summarize(c *CaseResult) {
	c.Runs, c.Passed, c.Rounds, c.Seconds = len(c.RunResults), 0, 0, 0
	if c.Runs == 0 {
		return
	}
	rounds, secs := 0, 0.0
	for _, r := range c.RunResults {
		if r.Passed {
			c.Passed++
		} else if c.Failure == "" {
			c.Failure, c.Screenshot = r.Failure, r.Screenshot
		}
		rounds += r.Rounds
		secs += r.Seconds
	}
	c.Rounds = (rounds + c.Runs/2) / c.Runs
	c.Seconds = float64(int(secs/float64(c.Runs)*10+0.5)) / 10
	c.Status = "failed"
	if c.Passed >= passMark(c.Runs) {
		c.Status = "passed"
	}
}

// failedRequired counts the required cases that failed: the run's verdict.
func (r *Report) failedRequired() int {
	n := 0
	for _, c := range r.Cases {
		if c.Status == "failed" && c.CaseStatus == "required" {
			n++
		}
	}
	return n
}

// Markdown is report.md.
func (r *Report) Markdown() string {
	var b strings.Builder
	n := map[string]int{}
	for _, c := range r.Cases {
		n[c.Status]++
	}
	verdict := "PASSED"
	if r.failedRequired() > 0 {
		verdict = "FAILED"
	}
	fmt.Fprintf(&b, "# Computer-task suite %s: %s\n\n", r.Run, verdict)
	fmt.Fprintf(&b, "- model `%s`, label `%s`, --llm %s\n", r.Model, r.Label, r.LLM)
	fmt.Fprintf(&b, "- %d passed, %d failed, %d skipped of %d; %.1f min\n\n", n["passed"], n["failed"], n["skipped"], len(r.Cases), float64(r.DurationMS)/60000)
	b.WriteString("| case | phase | case status | result | runs passed | rounds | time |\n|---|---|---|---|---|---|---|\n")
	for _, c := range r.Cases {
		res, runs, rounds, secs := c.Status, "–", "–", "–"
		if c.Status == "skipped" {
			res = "skipped (" + c.SkipReason + ")"
		} else {
			runs, rounds, secs = fmt.Sprintf("%d/%d", c.Passed, c.Runs), fmt.Sprint(c.Rounds), fmt.Sprintf("%.1f s", c.Seconds)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", c.Name, c.Phase, c.CaseStatus, res, runs, rounds, secs)
	}
	first := true
	for _, c := range r.Cases {
		if c.Failure == "" {
			continue
		}
		if first {
			b.WriteString("\n## Failures\n")
			first = false
		}
		fmt.Fprintf(&b, "\n### %s (%s, %d/%d runs passed)\n\n`%s`\n\n```\n%s\n```\n", c.Name, c.CaseStatus, c.Passed, c.Runs, c.File, strings.TrimSpace(c.Failure))
		if c.Screenshot != "" {
			fmt.Fprintf(&b, "\n![%s](%s)\n", c.Name, c.Screenshot)
		}
	}
	return b.String()
}

// e2eTest is a case in results.json, the E2E report's test shape (scripts/lib/e2e_report.py,
// docs/dev/e2e.md). A pending case that ran and failed is reported skipped, with why, so it never
// fails the E2E run; only required cases do.
type e2eTest struct {
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
	SkipReason    string   `json:"skip_reason,omitempty"`
	Note          string   `json:"note,omitempty"`
	Artifacts     []string `json:"artifacts"`
}

// results is results.json; rel prefixes artifact paths (the suite's directory, relative to the
// E2E run's).
func (r *Report) results(rel string) map[string]any {
	join := func(p string) string {
		if rel == "" {
			return p
		}
		return filepath.ToSlash(filepath.Join(rel, p))
	}
	tests := []e2eTest{}
	for _, c := range r.Cases {
		t := e2eTest{Suite: "computer", ID: c.Name, Title: c.Name, File: c.File, Line: 1, Status: c.Status,
			DurationMS: int64(c.Seconds * float64(c.Runs) * 1000), Screenshots: []string{}, ConsoleErrors: []string{},
			SkipReason: c.SkipReason, Artifacts: []string{}}
		if c.Runs > 0 {
			t.Note = fmt.Sprintf("%d/%d runs passed, %d rounds, %.1f s a run", c.Passed, c.Runs, c.Rounds, c.Seconds)
		}
		for _, rr := range c.RunResults {
			t.Artifacts = append(t.Artifacts, join(rr.Dir))
		}
		if c.Failure != "" {
			e := c.Failure
			t.Error = &e
			if c.Screenshot != "" {
				t.Screenshots = append(t.Screenshots, join(c.Screenshot))
			}
		}
		if c.Status == "failed" && c.CaseStatus == "pending" {
			t.Status, t.SkipReason = "skipped", fmt.Sprintf("pending, failed %d/%d", c.Runs-c.Passed, c.Runs)
		}
		tests = append(tests, t)
	}
	status := "passed"
	if r.failedRequired() > 0 {
		status = "failed"
	}
	return map[string]any{"suite": "computer", "status": status, "duration_ms": r.DurationMS,
		"model": r.Model, "label": r.Label, "tests": tests}
}

// write writes report.md, report.json and results.json into dir.
func (r *Report) write(dir, rel string) error {
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(r.Markdown()), 0o644); err != nil {
		return err
	}
	for name, v := range map[string]any{"report.json": r, "results.json": r.results(rel)} {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}
