package cc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// TestDocumentWorkflowsLive plans and runs document errands of different shapes end to end
// (ELEMENT-LAYER-WORK.md C27): the real planner and writer on a real model, the real web, and the
// agent's real computer_files with a headless LibreOffice in a scratch home. Each case checks the
// plan's shape and what ended up in the file. Computer tasks aren't run (no desktop here): a
// plan that needs one fails its case.
//
//	JARVIS_DOC_EVAL_LLM=http://127.0.0.1:<llama-server port> \
//	  go test -run TestDocumentWorkflowsLive -v -timeout 60m ./internal/modules/cc/
func TestDocumentWorkflowsLive(t *testing.T) {
	base := os.Getenv("JARVIS_DOC_EVAL_LLM")
	if base == "" {
		t.Skip("JARVIS_DOC_EVAL_LLM not set")
	}
	cases := []struct {
		name, goal string
		files      map[string]string // made before the run (a .odt/.ods from text: "doc:" lines)
		file       string            // the document to check, under ~/Documents
		want       []string          // all in the saved document's text
		edited     bool              // the file existed: a backup must exist
	}{
		{name: "article_from_wikipedia", file: "apollo.odt", want: []string{"Armstrong", "1969"},
			goal: "Write a 2 page article about the Apollo 11 moon landing using its Wikipedia page as the source, and save it as apollo.odt in my Documents."},
		{name: "deep_research_into_docx", file: "hoax.docx", want: []string{"Apollo"},
			goal: "Deep research the Apollo 11 moon landing conspiracy theories and put a summary of them in a Word document called hoax.docx in my Documents."},
		{name: "search_into_spreadsheet", file: "planets.xlsx", want: []string{"Jupiter", "Saturn"},
			goal: "Look up the planets of the solar system and their number of moons, and put them in a spreadsheet called planets.xlsx in my Documents, one row per planet."},
		{name: "edit_document", file: "notes.odt", edited: true, want: []string{"Shopping", "eggs", "Call the plumber"},
			files: map[string]string{"Documents/notes.odt": "doc:Call the plumber on Monday."},
			goal:  "Add a heading called Shopping to the end of my notes.odt document, with milk, eggs and bread under it."},
		{name: "edit_spreadsheet_formula", file: "budget.ods", edited: true, want: []string{"Internet", "=SUM("},
			files: map[string]string{"Documents/budget.ods": "sheet:Item,Cost\nRent,1200\nFood,400"},
			goal:  "In my budget.ods spreadsheet, add a row for Internet costing 60, then a Total row that sums the costs with a formula."},
		{name: "file_into_spreadsheet", file: "spending.xlsx", want: []string{"Groceries", "Utilities"},
			files: map[string]string{"Downloads/statement.csv": "date,description,amount,category\n2026-10-01,Corner Grocer,54.20,Groceries\n" +
				"2026-10-03,City Power,88.10,Utilities\n2026-10-09,Corner Grocer,31.80,Groceries\n"},
			goal: "Read statement.csv in my Downloads and make a spreadsheet called spending.xlsx in my Documents with the total spent per category."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			tl := &computer.Tools{Home: home, Switch: computer.NewSwitch(true, nil)}
			ve := newVoiceEnv(t, prompts.Qwen3_5_9B, func(m *Module) {
				m.recordingAgent = func(ctx context.Context, _, tool string, args map[string]any, _ int64) map[string]any {
					out, _ := tl.Call(ctx, tool, args, node.Request{})
					return out
				}
			})
			ve.eng.strict.Store(true)
			ve.eng.ctxLen = 32768
			ve.setComputerControl("on")
			ve.set(settingWebSearch, true, settings.Scope{HouseholdID: voiceHH})
			ve.eng.mu.Lock()
			ve.eng.respond = realEngine(t, base)
			ve.eng.mu.Unlock()
			makeFiles(t, tl, home, c.files)

			ctx := context.Background()
			uid := int64(7)
			menu := ve.m.errands.FullMenu(ctx, nil, voiceHH, &uid)
			start := time.Now()
			plan, err := ve.m.errands.PlanErrand(ctx, c.goal, menu)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			t.Logf("plan (%s): %s", time.Since(start).Round(time.Second), planText(plan.Steps))
			turn := ve.recTurn(7)
			turn.Utterance = c.goal
			var done []errands.Step
			var results []errands.Result
			for steps := plan.Steps; len(steps) > 0; {
				st := steps[0]
				steps = steps[1:]
				if st.Command == "request_replan" {
					next, err := ve.m.errands.ReplanFromProgress(ctx, c.goal, done, results, "checkpoint", menu)
					if err != nil {
						t.Fatalf("replan: %v", err)
					}
					t.Logf("replan: %s", planText(next.Steps))
					steps = next.Steps
					continue
				}
				tool, ok := ve.m.tools.Get(st.Command)
				if !ok {
					t.Fatalf("step %s can't run here (plan: %s)", st.Command, planText(plan.Steps))
				}
				turn.Prior = errands.PriorResults(results)
				start := time.Now()
				raw, err := tool.Execute(ctx, servertools.Call{Name: st.Command, Args: st.Args}, turn)
				out := toolOut(t, raw, err)
				r := errands.Result{Command: st.Command, Label: st.Label, Success: out["error"] == nil, Data: toObj(t, out)}
				r.Message, _ = out["message"].(string)
				t.Logf("%s (%s): success=%v %s", st.Command, time.Since(start).Round(time.Second), r.Success, r.Message)
				done, results = append(done, st), append(results, r)
			}
			read := tl.Call
			out, _ := read(ctx, "computer_files", map[string]any{"op": "doc_read", "path": "~/Documents/" + c.file}, node.Request{})
			if out["success"] != true {
				t.Fatalf("no %s: %v", c.file, out)
			}
			text := renderDoc(out)
			t.Logf("%s:\n%s", c.file, truncateRunes(text, 1500))
			for _, w := range c.want {
				if !strings.Contains(strings.ToLower(text), strings.ToLower(w)) {
					t.Errorf("%s lacks %q", c.file, w)
				}
			}
			if strings.Contains(text, "**") {
				t.Error("Markdown in the document")
			}
			backups, _ := os.ReadDir(filepath.Join(home, "Documents", "Jarvis", "Backups"))
			if c.edited != (len(backups) > 0) {
				t.Errorf("backups %d, edited %v", len(backups), c.edited)
			}
		})
	}
}

// makeFiles writes files under home: "doc:" text becomes a text document, "sheet:" CSV a
// spreadsheet (through the agent, as a user's own file would be), anything else as is.
func makeFiles(t *testing.T, tl *computer.Tools, home string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		var ops []any
		switch {
		case strings.HasPrefix(body, "doc:"):
			ops = []any{map[string]any{"op": "append", "text": strings.TrimPrefix(body, "doc:")}}
		case strings.HasPrefix(body, "sheet:"):
			for _, line := range strings.Split(strings.TrimPrefix(body, "sheet:"), "\n") {
				var vals []any
				for _, v := range strings.Split(line, ",") {
					vals = append(vals, v)
				}
				ops = append(ops, map[string]any{"op": "append_row", "values": vals})
			}
		default:
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		out, _ := tl.Call(context.Background(), "computer_files", map[string]any{"op": "doc_edit", "path": p, "create": true, "ops": ops}, node.Request{})
		if out["success"] != true {
			t.Fatalf("making %s: %v", rel, out)
		}
	}
}

func planText(steps []errands.Step) string {
	var parts []string
	for _, s := range steps {
		parts = append(parts, s.Command+pyjson.Dumps(s.Args, false))
	}
	return strings.Join(parts, " → ")
}

func toolOut(t *testing.T, raw any, err error) map[string]any {
	t.Helper()
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(pyjson.Dumps(raw, false)), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func toObj(t *testing.T, m map[string]any) *pyjson.Object {
	t.Helper()
	v, err := pyjson.Loads(pyjson.Dumps(m, false))
	if err != nil {
		t.Fatal(err)
	}
	o, _ := v.(*pyjson.Object)
	return o
}
