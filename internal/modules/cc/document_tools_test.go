package cc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// fakeDocAgent is the agent's computer_files doc ops: a file at most, its paragraphs, and an
// edit that appends what it is given (dropText: a save that loses the text).
type fakeDocAgent struct {
	mu       sync.Mutex
	exists   bool
	paras    []any
	ops      []map[string]any
	dropText bool
	editErr  map[string]any
}

func (f *fakeDocAgent) call(_ context.Context, _, _ string, args map[string]any, _ int64) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, args)
	switch args["op"] {
	case "doc_read":
		if !f.exists {
			return map[string]any{"success": false, "error_code": "not_found", "error": "no file"}
		}
		return map[string]any{"success": true, "path": "/home/avery/Documents/apollo.odt", "kind": "text", "text": f.paras}
	case "doc_edit":
		if f.editErr != nil {
			return f.editErr
		}
		out := map[string]any{"success": true, "path": "/home/avery/Documents/apollo.odt"}
		if f.exists {
			out["backup"] = "/home/avery/Documents/Jarvis/Backups/apollo-1.odt"
		}
		f.exists = true
		for _, o := range args["ops"].([]any) {
			om := o.(map[string]any)
			if om["op"] == "append" && !f.dropText {
				style, _ := om["style"].(string)
				for _, line := range strings.Split(om["text"].(string), "\n") {
					f.paras = append(f.paras, map[string]any{"style": style, "text": line})
				}
			}
		}
		return out
	}
	return map[string]any{"success": false, "error": "unexpected op"}
}

func TestWriteDocument(t *testing.T) {
	agent := &fakeDocAgent{}
	fetch := &fakeWatchFetcher{status: 200, body: `<main><h1>Apollo 11</h1><p>Apollo 11 landed on July 20, 1969.</p></main>`}
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent, m.WatchFetcher = agent.call, fetch })
	ve.setComputerControl("on")
	tool := &writeDocumentTool{m: ve.m}
	edits := func(ops ...map[string]any) {
		b, _ := json.Marshal(map[string]any{"ops": ops})
		ve.eng.say(string(b))
	}

	// A new document from a source page: created, then checked in the saved file.
	edits(map[string]any{"op": "append", "style": "Title", "text": "Apollo 11"},
		map[string]any{"op": "append", "text": "It landed on July 20, 1969.\nArmstrong walked first."})
	out := runTool(t, tool, `{"path": "apollo.odt", "instructions": "a one-page summary", "source": "https://en.wikipedia.org/wiki/Apollo_11"}`, ve.recTurn(7))
	if out["status"] != "saved" || out["created"] != true || out["path"] != "/home/avery/Documents/apollo.odt" ||
		!strings.Contains(out["message"].(string), "checked in the saved file") {
		t.Fatalf("create: %v", out)
	}
	if a := agent.ops[1]; a["op"] != "doc_edit" || a["path"] != "~/Documents/apollo.odt" || a["create"] != true {
		t.Fatalf("edit call: %v", a)
	}
	prompt := lastUserText(t, ve.eng.last())
	if !strings.Contains(prompt, "landed on July 20, 1969") || !strings.Contains(prompt, "new, empty text document") ||
		!strings.Contains(prompt, "<<<PAGE") {
		t.Fatalf("prompt:\n%s", prompt)
	}

	// An edit: the model sees the document as it is; the backup is reported.
	edits(map[string]any{"op": "append", "style": "Heading 1", "text": "Crew"})
	out = runTool(t, tool, `{"path": "~/Documents/apollo.odt", "instructions": "add a Crew heading"}`, ve.recTurn(7))
	if out["created"] != false || out["backup"] != "/home/avery/Documents/Jarvis/Backups/apollo-1.odt" {
		t.Fatalf("edit: %v", out)
	}
	if p := lastUserText(t, ve.eng.last()); !strings.Contains(p, "[Title] Apollo 11") || !strings.Contains(p, "Armstrong walked first.") {
		t.Fatalf("edit prompt:\n%s", p)
	}

	// A save that doesn't hold what was written fails the step: the model's word isn't enough.
	agent.dropText = true
	edits(map[string]any{"op": "append", "text": "Aldrin followed."})
	out = runTool(t, tool, `{"path": "~/Documents/apollo.odt", "instructions": "add Aldrin"}`, ve.recTurn(7))
	if out["error"] != "verify_failed" || !strings.Contains(out["message"].(string), "Aldrin followed.") {
		t.Fatalf("unchecked save: %v", out)
	}
	agent.dropText = false

	// The agent refusing (open in LibreOffice) is said; a bad type never reaches it.
	agent.editErr = map[string]any{"success": false, "error_code": "file_open", "error": "apollo.odt is open in LibreOffice: close it first"}
	edits(map[string]any{"op": "append", "text": "x"})
	if out := runTool(t, tool, `{"path": "apollo.odt", "instructions": "x"}`, ve.recTurn(7)); out["error"] != "file_open" {
		t.Fatalf("open file: %v", out)
	}
	n := len(agent.ops)
	if out := runTool(t, tool, `{"path": "notes.txt", "instructions": "x"}`, ve.recTurn(7)); out["error"] != "invalid_argument" || len(agent.ops) != n {
		t.Fatalf("txt: %v", out)
	}
}

func lastUserText(t *testing.T, req map[string]any) string {
	t.Helper()
	msgs, _ := req["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m["role"] != "user" {
			continue
		}
		if s, ok := m["content"].(string); ok {
			return s
		}
		b, _ := json.Marshal(m["content"])
		return string(b)
	}
	t.Fatal("no user message")
	return ""
}

// The saved document as text, and the check of each edit against it.
func TestMissingEdits(t *testing.T) {
	saved := renderDoc(map[string]any{"sheets": []any{map[string]any{"name": "Budget", "rows": []any{
		[]any{"Item", "Cost"}, []any{"Rent", "1200"}, []any{"", "=SUM(B2:B2)"}}}}})
	if saved != "Sheet Budget\nA1=Item\tB1=Cost\nA2=Rent\tB2=1200\nB3==SUM(B2:B2)" {
		t.Fatalf("rendered %q", saved)
	}
	ok := []any{
		map[string]any{"op": "add_sheet", "name": "Budget"},
		map[string]any{"op": "append_row", "values": []any{"Rent", 1200.0}},
		map[string]any{"op": "set", "cell": "b3", "value": "=SUM(B2:B2)"},
	}
	if m := missingEdits(ok, saved); m != "" {
		t.Fatalf("missing %s", m)
	}
	if m := missingEdits([]any{map[string]any{"op": "append_row", "values": []any{"Food", 400.5}}}, saved); m != `edit 1 ("Food")` {
		t.Fatalf("missing %q", m)
	}
	if colName(0) != "A" || colName(25) != "Z" || colName(26) != "AA" {
		t.Fatal("column names")
	}
}
