package errands

import "testing"

// A conversation's task sees the errand's plan with its own step marked.
func TestPlanLines(t *testing.T) {
	steps := []Step{{Command: "computer_task", Label: "download bank statement"}, {Command: "computer_task", Label: "download  card statement"}, {Command: "write_spreadsheet"}}
	want := "   1. download bank statement (done)\n-> 2. download card statement\n   3. write_spreadsheet\n"
	if got := planLines(steps, 1); got != want {
		t.Fatalf("planLines:\n%s\nwant:\n%s", got, want)
	}
	if got := planLines(steps[:1], 0); got != "" {
		t.Fatalf("a one-step errand has a plan: %q", got)
	}
}
