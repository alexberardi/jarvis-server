package errands

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
)

// fileTools registers read_computer_file and write_spreadsheet as the cc module does, answering
// a statement's text and a saved, attached spreadsheet.
func fileTools(t *testing.T, e *tenv) (read, write *fakeTool) {
	t.Helper()
	read = &fakeTool{name: cmdReadFile, def: toolDef(cmdReadFile, "Read a text file on the user's computer",
		"path", mustObj(t, `{"type": "string", "description": "the file's path"}`)),
		result: servertools.Obj("path", "/home/avery/Downloads/statement-2026-09.csv", "text",
			"date,description,amount\n2026-09-01,Corner Grocer,-54.20\n2026-09-03,City Power,-88.10\n", "truncated", false,
			"message", "Read statement-2026-09.csv (80 bytes).")}
	write = &fakeTool{name: cmdWriteSheet, def: toolDef(cmdWriteSheet, "Save a spreadsheet on the user's computer",
		"name", mustObj(t, `{"type": "string", "description": "the file name"}`)),
		result: servertools.Obj("status", "saved", "path", "/home/avery/Documents/Jarvis/Budget 2026-09.xlsx", "rows", 2,
			"attachment_ids", []any{"att-1"}, "message", "Saved the spreadsheet as /home/avery/Documents/Jarvis/Budget 2026-09.xlsx (1 sheets, 2 rows).")}
	e.tools.Register(read)
	e.tools.Register(write)
	return read, write
}

// The file tools are on the errand menu only with a computer a computer task can use, and the
// budget rule comes with them; a menu without them keeps the prompts as they were.
func TestPlannerOffersFileToolsWithComputer(t *testing.T) {
	c := newComputerEnv(t)
	fileTools(t, c.tenv)
	ctx := context.Background()
	names := func() string {
		var out []string
		for _, m := range c.s.FullMenu(ctx, nil, hh, ptr(7)) {
			out = append(out, m.Command)
		}
		return strings.Join(out, ",")
	}
	if got := names(); got != "read_computer_file,write_spreadsheet,computer_task" {
		t.Fatalf("menu = %s", got)
	}
	menu := c.s.FullMenu(ctx, nil, hh, ptr(7))
	for _, p := range []string{BuildPrompt("do my budget", menu), BuildRefinePrompt("s", nil, "i", menu), BuildReplanPrompt("g", nil, "p", "r", menu)} {
		if !strings.Contains(p, budgetRule) {
			t.Fatalf("prompt lacks the budget rule:\n%s", p)
		}
	}
	// The files are read once their names are known: the checkpoint comes after the downloads.
	if i, j := strings.Index(budgetRule, "request_replan"), strings.Index(budgetRule, "read_computer_file"); i < 0 || j < i {
		t.Fatalf("the budget rule reads before its checkpoint: %s", budgetRule)
	}
	if !strings.Contains(BuildPrompt("g", menu), "actually asked for.\n\n"+websiteRule+budgetRule+"CHECKPOINT") {
		t.Fatal("the website and budget rules aren't after the decomposition rule")
	}

	c.s.Computer.Targets = func(context.Context, string) ([]string, string) { return nil, "Computer control is off." }
	if got := names(); got != "" {
		t.Fatalf("offered without a computer: %s", got)
	}
	if p := BuildPrompt("g", commandMenu()); strings.Contains(p, "BUDGET") {
		t.Fatal("budget rule without the file tools")
	}
}

// "Do my budget": the statement's text reaches the replan (not just its one-line message), and
// the spreadsheet written after it is attached to the completion card.
func TestBudgetErrandAttachesSpreadsheet(t *testing.T) {
	e := newComputerEnv(t).tenv
	read, write := fileTools(t, e)
	e.llm.say("plan", planJSON("Budget for September",
		step(cmdReadFile, "Read the statement", `{"path": "~/Downloads/statement-2026-09.csv"}`),
		`{"command": "request_replan", "args": {"reason": "categorise the transactions"}, "label": "Categorise"}`))
	e.llm.say("replan", planJSON("Write the budget", step(cmdWriteSheet, "Write the budget", `{"name": "Budget 2026-09"}`)))
	e.llm.say("compose", "Groceries 54.20, utilities 88.10. The budget is in Documents/Jarvis.")
	p := e.draft("do my budget for September", ptr(7), e.s.FullMenu(context.Background(), nil, hh, ptr(7)))
	wf := launch(t, e, p)
	e.idle()
	w := e.wf(wf)
	if w.State != "waiting" { // write_spreadsheet is a new command: the delta card asks
		t.Fatalf("run = %+v", w)
	}
	rp := *e.llm.requests("replan")[0].Messages[0].Content.Text
	if !strings.Contains(rp, "- Read the statement: Read statement-2026-09.csv (80 bytes).\n  date,description,amount\n  2026-09-01,Corner Grocer,-54.20\n  2026-09-03,City Power,-88.10\n") {
		t.Fatalf("the replan doesn't see the statement:\n%s", rp)
	}
	if r := e.tap(CallbackApproveRepl, map[string]any{"workflow_id": wf, "revision": float64(w.Revision)}); !r.Success {
		t.Fatalf("approve = %+v", r)
	}
	e.idle()
	if w = e.wf(wf); w.State != "done" || len(read.args) != 1 || len(write.args) != 1 {
		t.Fatalf("run = %+v, reads %d, writes %d", w, len(read.args), len(write.args))
	}
	var done Card
	for _, c := range e.cards.all() {
		if strings.HasPrefix(c.Title, "✅") {
			done = c
		}
	}
	if len(done.AttachmentIDs) != 1 || done.AttachmentIDs[0] != "att-1" {
		t.Fatalf("completion card = %+v", done)
	}
	files, _ := done.Metadata["files"].([]any)
	if f, _ := files[0].(map[string]any); len(files) != 1 || f["name"] != "Budget 2026-09.xlsx" || f["path"] != "/home/avery/Documents/Jarvis/Budget 2026-09.xlsx" {
		t.Fatalf("files = %v", done.Metadata["files"])
	}
}
