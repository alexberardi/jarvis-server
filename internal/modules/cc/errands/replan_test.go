package errands

// Loop item #522: budgetRule plans one computer task per site, but the checkpoint after them
// read only the task right before it, and an LLM replan's prompt never named the downloaded
// files — so the replan dropped the remaining steps and the budget errand ended with no
// spreadsheet.

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
)

func downloadResults() ([]Step, []Result) {
	done := []Step{
		{Command: cmdComputer, Label: "Download the bank statement"},
		{Command: cmdComputer, Label: "Download the card statement"},
		{Command: cmdReplan, Label: "Wait for the downloads"},
	}
	results := []Result{
		{Command: cmdComputer, Label: done[0].Label, Success: true,
			Message: "Done. Downloaded the September statement.",
			Data:    servertools.Obj("downloads", []any{"bank-statement.csv"})},
		{Command: cmdComputer, Label: done[1].Label, Success: true,
			Message: "Done. Downloaded the September statement.",
			Data:    servertools.Obj("downloads", []any{"card-statement.csv"})},
	}
	return done, results
}

// The checkpoint reads every consecutive computer task's downloads, not just the last task's.
func TestReadDownloadsReadsEveryTask(t *testing.T) {
	_, results := downloadResults()
	add := readDownloads(results, 2)
	var got []string
	for _, st := range add {
		if st.Command == cmdReadFile {
			got = append(got, truthyStr(st.Args, "path"))
		}
	}
	want := []string{"~/Downloads/bank-statement.csv", "~/Downloads/card-statement.csv"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("read steps = %v, want %v", got, want)
	}
	if last := add[len(add)-1]; last.Command != cmdReplan {
		t.Fatalf("the reads don't end with a checkpoint: %+v", add)
	}
}

// The walk back stops at a failed or non-computer result: its downloads are not re-read.
func TestReadDownloadsStopsAtFailure(t *testing.T) {
	_, results := downloadResults()
	results[0].Success = false
	add := readDownloads(results, 2)
	var got []string
	for _, st := range add {
		if st.Command == cmdReadFile {
			got = append(got, truthyStr(st.Args, "path"))
		}
	}
	if len(got) != 1 || got[0] != "~/Downloads/card-statement.csv" {
		t.Fatalf("read steps = %v, want only the card statement", got)
	}
}

func TestReadDownloadsNilWithoutData(t *testing.T) {
	// A computer task that reported no downloads (Data nil): the checkpoint falls through to
	// the LLM replan, whose prompt names the files via summarizeProgress instead.
	results := []Result{{Command: cmdComputer, Success: true, Message: "Done."}}
	if add := readDownloads(results, 1); add != nil {
		t.Errorf("expected nil, got %v", add)
	}
}

// An LLM replan's prompt names each computer task's downloaded files with their real paths,
// so the planner never has to guess them (or replan again to learn them).
func TestSummarizeProgressNamesDownloads(t *testing.T) {
	done, results := downloadResults()
	progress := summarizeProgress(done[:2], results)
	for _, f := range []string{"~/Downloads/bank-statement.csv", "~/Downloads/card-statement.csv"} {
		if !strings.Contains(progress, f) {
			t.Errorf("progress does not name %q:\n%s", f, progress)
		}
	}
}

// A message that already names its download isn't repeated.
func TestSummarizeProgressDownloadsNotRepeated(t *testing.T) {
	done, results := downloadResults()
	results[1].Message = "Done. Downloaded: card-statement.csv (in ~/Downloads)."
	progress := summarizeProgress(done[:2], results)
	if n := strings.Count(progress, "card-statement.csv"); n != 1 {
		t.Errorf("card-statement.csv named %d times:\n%s", n, progress)
	}
	if !strings.Contains(progress, "~/Downloads/bank-statement.csv") {
		t.Errorf("progress does not name the bank statement:\n%s", progress)
	}
}
