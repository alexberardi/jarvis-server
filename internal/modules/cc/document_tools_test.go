package cc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
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
	files    map[string]map[string]any // other files by path, read as they are
}

func (f *fakeDocAgent) call(_ context.Context, _, _ string, args map[string]any, _ int64) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, args)
	if out, ok := f.files[args["path"].(string)]; ok {
		return out
	}
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

// Words as the source read the top results; earlier steps' results are material too; Markdown
// the model writes anyway is stripped.
func TestWriteDocumentMaterial(t *testing.T) {
	agent := &fakeDocAgent{}
	fetch := &fakeWatchFetcher{status: 200, body: `<main><p>The landing was filmed live.</p></main>`}
	search := &fakeWatchSearcher{results: []servertools.SearchResult{
		{URL: "https://a.example/1"}, {URL: "https://b.example/2"}, {URL: "https://c.example/3"}, {URL: "https://d.example/4"}}}
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) {
		m.recordingAgent, m.WatchFetcher, m.WebSearch = agent.call, fetch, search
	})
	ve.setComputerControl("on")
	ve.set(settingWebSearch, true, settings.Scope{HouseholdID: voiceHH})
	b, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "append", "style": "Heading 1", "text": "## **Claims**\nThe **footage** was live."}}})
	ve.eng.say(string(b))
	turn := ve.recTurn(7)
	turn.Utterance = "deep research the moon landing hoax claims into a document"
	turn.Prior = `- Search prices: 3 results` + "\n  " + `{"answer": "Kodak film costs $9"}`
	out := runTool(t, tool(ve), `{"path": "hoax.odt", "instructions": "a summary", "source": "moon landing hoax claims"}`, turn)
	if out["status"] != "saved" {
		t.Fatalf("saved: %v", out)
	}
	if srcs, _ := out["sources"].([]any); len(srcs) != 3 || len(fetch.urls) != 3 {
		t.Fatalf("sources %v, fetched %v", out["sources"], fetch.urls)
	}
	p := lastUserText(t, ve.eng.last())
	for _, want := range []string{"https://c.example/3", "filmed live", "Kodak film costs $9", "The user asked: deep research"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	ops := agent.ops[1]["ops"].([]any)
	if got := ops[0].(map[string]any)["text"]; got != "Claims\nThe footage was live." {
		t.Fatalf("markdown kept: %q", got)
	}
}

// An errand's earlier step saved files (an email's attachments): write_document reads them as
// material, office files through doc_read and text through read; others are only named.
func TestWriteDocumentPriorFiles(t *testing.T) {
	const dir = "/home/avery/Documents/Jarvis/Attachments/2026-10-30 Timesheet/"
	agent := &fakeDocAgent{files: map[string]map[string]any{
		dir + "hours.xlsx": {"success": true, "kind": "sheet", "sheets": []any{map[string]any{"name": "Hours",
			"rows": []any{[]any{"Week", "Hours"}, []any{"44", 38.5}}}}},
		dir + "notes.csv": {"success": true, "text": "day,hours\nMon,7.5\n"},
		dir + "scan.pdf":  {"success": false, "error": "not reached"},
	}}
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent = agent.call })
	ve.setComputerControl("on")
	b, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "append", "text": "Total: 38.5 hours"}}})
	ve.eng.say(string(b))
	turn := ve.recTurn(7)
	turn.PriorFiles = []string{dir + "hours.xlsx", dir + "notes.csv", dir + "scan.pdf"}
	out := runTool(t, tool(ve), `{"path": "totals.odt", "instructions": "total the hours"}`, turn)
	if out["status"] != "saved" {
		t.Fatalf("saved: %v", out)
	}
	p := lastUserText(t, ve.eng.last())
	for _, want := range []string{"File hours.xlsx", "B2=38.5", "Mon,7.5", "File scan.pdf", "can't read .pdf files yet"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	for _, op := range agent.ops {
		if op["path"] == dir+"scan.pdf" {
			t.Fatal("a PDF was sent to the agent")
		}
	}
}

func tool(ve *voiceEnv) *writeDocumentTool { return &writeDocumentTool{m: ve.m} }

// A bare file name goes in the documents folder setting.
func TestWriteDocumentFolderSetting(t *testing.T) {
	agent := &fakeDocAgent{}
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent = agent.call })
	ve.setComputerControl("on")
	ve.set(errands.SettingDocumentsFolder, "~/Documents/Jarvis/", settings.Scope{})
	ve.eng.say(`{"ops": [{"op": "append", "text": "hi"}]}`)
	runTool(t, tool(ve), `{"path": "a.odt", "instructions": "hi"}`, ve.recTurn(7))
	if p := agent.ops[0]["path"]; p != "~/Documents/Jarvis/a.odt" {
		t.Fatalf("path %v", p)
	}
}

// A formula over empty cells is caught before saving (C29: the moons were in B, the total
// summed C2:C11), and only real cell refs count.
func TestEmptyFormulaRef(t *testing.T) {
	rows := func(total string) []any {
		ops := []any{map[string]any{"op": "add_sheet", "name": "Planets"},
			map[string]any{"op": "append_row", "sheet": "Planets", "values": []any{"Category", "Total"}}}
		for _, r := range []any{[]any{"Mercury", 0.0}, []any{"Earth", 1.0}, []any{"Saturn", 146.0}} {
			ops = append(ops, map[string]any{"op": "append_row", "sheet": "Planets", "values": r})
		}
		return append(ops, map[string]any{"op": "set", "sheet": "Planets", "cell": "B10", "value": total})
	}
	if got := emptyFormulaRef(rows("=SUM(C2:C11)"), "", false); got != "=SUM(C2:C11) in Planets!B10 refers to C2:C11, which is empty" {
		t.Fatalf("bad column: %q", got)
	}
	for _, ok := range []string{"=SUM(B2:B9)", "=SUM($B$2:$B$4)", "=B2+B3", "=LOG10(B4)", "=Other!C2", "Total", ""} {
		if got := emptyFormulaRef(rows(ok), "", false); got != "" {
			t.Errorf("%q flagged: %s", ok, got)
		}
	}
	// No sheet named: the first. A formula in an appended row is checked too; its own cell doesn't count.
	ops := []any{map[string]any{"op": "append_row", "values": []any{"x", 5.0}},
		map[string]any{"op": "append_row", "values": []any{"total", "=SUM(C1:C1)"}}}
	if got := emptyFormulaRef(ops, "", false); !strings.Contains(got, "Sheet1!B2") {
		t.Fatalf("appended formula: %q", got)
	}
	self := []any{map[string]any{"op": "set", "cell": "A1", "value": "=SUM(A1:A1)"}}
	if emptyFormulaRef(self, "", false) == "" {
		t.Fatal("a formula over only itself passed")
	}
	// An existing workbook's cells count, as renderDoc shows them.
	current := "Sheet Budget\nA1=Item\tB1=Cost\nA2=Rent\tB2=1200"
	set := []any{map[string]any{"op": "set", "cell": "B3", "value": "=SUM(B2:B2)"}}
	if got := emptyFormulaRef(set, current, true); got != "" {
		t.Fatalf("existing cells: %q", got)
	}
	set[0].(map[string]any)["value"] = "=SUM(D2:D2)"
	if emptyFormulaRef(set, current, true) == "" {
		t.Fatal("an empty column in an existing workbook passed")
	}
}

// A bad formula gets one more try with what was wrong; an answer wrapped in a JSON string is read.
func TestDocumentEditsRetriesAFormula(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	bad, _ := json.Marshal(map[string]any{"ops": []any{
		map[string]any{"op": "append_row", "values": []any{"Saturn", 146}},
		map[string]any{"op": "set", "cell": "B2", "value": "=SUM(C1:C1)"}}})
	good, _ := json.Marshal(map[string]any{"ops": []any{
		map[string]any{"op": "append_row", "values": []any{"Saturn", 146}},
		map[string]any{"op": "set", "cell": "B2", "value": "=SUM(B1:B1)"}}})
	wrapped, _ := json.Marshal(string(good))
	ve.eng.say(string(bad))
	ve.eng.say(string(wrapped))
	ops, err := ve.m.documentEdits(context.Background(), "total the moons", "total the moons", true, "", false, "")
	if err != nil || len(ops) != 2 || ops[1].(map[string]any)["value"] != "=SUM(B1:B1)" {
		t.Fatalf("ops %v, %v", ops, err)
	}
	if p := lastUserText(t, ve.eng.last()); !strings.Contains(p, "=SUM(C1:C1) in Sheet1!B2 refers to C1:C1, which is empty") {
		t.Fatalf("retry prompt lacks the problem:\n%s", p)
	}
}
