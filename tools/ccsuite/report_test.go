package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/report.md")

// fixedReport is a run with every kind of line: skipped (pending, macOS), a required case that
// passed 3/3, one that passed 2/3, one that failed 1/3, and a pending case that ran and failed.
func fixedReport() *Report {
	run := func(passed bool, rounds int, secs float64, failure, shot string, i int, name string) RunResult {
		return RunResult{Passed: passed, Rounds: rounds, Seconds: secs, Failure: failure, Screenshot: shot,
			Dir: "cases/" + name + "/run-" + string(rune('0'+i))}
	}
	cases := []CaseResult{
		{Name: "store_order_approve", Phase: "browser", File: "tools/ccsuite/cases/store_order_approve.json", CaseStatus: "required",
			RunResults: []RunResult{run(true, 14, 181.2, "", "", 1, "store_order_approve"), run(true, 12, 160.4, "", "", 2, "store_order_approve"),
				run(true, 13, 170.0, "", "", 3, "store_order_approve")}},
		{Name: "store_order_cancel", Phase: "browser", File: "tools/ccsuite/cases/store_order_cancel.json", CaseStatus: "required",
			RunResults: []RunResult{run(true, 10, 120, "", "", 1, "store_order_cancel"),
				run(false, 22, 900, "no_order: an order was placed: TS-1001 pulse-active-2 $249.00", "cases/store_order_cancel/run-2/failure.png", 2, "store_order_cancel"),
				run(true, 9, 100, "", "", 3, "store_order_cancel")}},
		{Name: "web_form", Phase: "browser", File: "tools/ccsuite/cases/web_form.json", CaseStatus: "required",
			RunResults: []RunResult{run(false, 8, 60, "form_submitted: no form sent with map[plan:standard] (0 sent)\ntimed out after 10 min", "cases/web_form/run-1/failure.png", 1, "web_form"),
				run(false, 8, 60, "form_submitted: no form sent with map[plan:standard] (0 sent)", "", 2, "web_form"),
				run(true, 6, 45, "", "", 3, "web_form")}},
		{Name: "notes_wlroots", Phase: "desktop-wlroots", File: "tools/ccsuite/cases/notes_wlroots.json", CaseStatus: "pending",
			RunResults: []RunResult{run(false, 3, 30, "bring-up: no sway: scripts/desktop-shot deps", "", 1, "notes_wlroots")}},
		{Name: "notes_macos", Phase: "desktop-mac", File: "tools/ccsuite/cases/notes_macos.json", CaseStatus: "pending", Status: "skipped", SkipReason: "macOS only: the Mac test host"},
		{Name: "replay_moved_button", Phase: "teach", File: "tools/ccsuite/cases/replay_moved_button.json", CaseStatus: "pending", Status: "skipped", SkipReason: "pending"},
	}
	for i := range cases {
		if cases[i].Status == "" {
			summarize(&cases[i])
		}
	}
	return &Report{Run: "20261019-120000-abcdef", Model: "qwen3.5-27b", Label: "background", LLM: "shared", DurationMS: 2940000, Cases: cases}
}

// TestReport: report.md from a fixed result set matches testdata/report.md (go test -update
// rewrites it), and the totals and results.json follow the rules: two thirds of the runs pass a
// case, a pending case never fails the run.
func TestReport(t *testing.T) {
	r := fixedReport()
	got := r.Markdown()
	golden := filepath.Join("testdata", "report.md")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("report.md differs from %s (go test ./tools/ccsuite/ -run TestReport -update):\n%s", golden, got)
	}

	byName := map[string]CaseResult{}
	for _, c := range r.Cases {
		byName[c.Name] = c
	}
	for name, want := range map[string]string{"store_order_approve": "passed", "store_order_cancel": "passed",
		"web_form": "failed", "notes_wlroots": "failed", "notes_macos": "skipped"} {
		if got := byName[name].Status; got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	if c := byName["store_order_approve"]; c.Rounds != 13 || c.Seconds != 170.5 {
		t.Errorf("store_order_approve: %d rounds, %v s; want 13, 170.5", c.Rounds, c.Seconds)
	}
	if n := r.failedRequired(); n != 1 {
		t.Errorf("failedRequired = %d, want 1 (web_form; notes_wlroots is pending)", n)
	}

	dir := t.TempDir()
	if err := r.write(dir, "computer"); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Suite  string    `json:"suite"`
		Status string    `json:"status"`
		Tests  []e2eTest `json:"tests"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "results.json"))
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res.Suite != "computer" || res.Status != "failed" || len(res.Tests) != len(r.Cases) {
		t.Fatalf("results.json: suite %q status %q, %d tests", res.Suite, res.Status, len(res.Tests))
	}
	for _, tt := range res.Tests {
		switch tt.ID {
		case "notes_wlroots":
			if tt.Status != "skipped" || tt.SkipReason != "pending, failed 1/1" || tt.Error == nil {
				t.Errorf("a failed pending case: %+v", tt)
			}
		case "web_form":
			if tt.Status != "failed" || len(tt.Screenshots) != 1 || tt.Screenshots[0] != "computer/cases/web_form/run-1/failure.png" {
				t.Errorf("a failed required case: %+v", tt)
			}
		case "replay_moved_button":
			if tt.Status != "skipped" || tt.SkipReason != "pending" {
				t.Errorf("a pending case: %+v", tt)
			}
		}
	}
	for _, f := range []string{"report.md", "report.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
}

// TestPassMark: a case passes with two thirds of its runs.
func TestPassMark(t *testing.T) {
	for runs, want := range map[int]int{1: 1, 2: 2, 3: 2, 4: 3, 5: 4, 6: 4} {
		if got := passMark(runs); got != want {
			t.Errorf("passMark(%d) = %d, want %d", runs, got, want)
		}
	}
}
