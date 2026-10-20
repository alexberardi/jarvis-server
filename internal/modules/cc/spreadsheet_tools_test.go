package cc

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/xuri/excelize/v2"
)

// fakeFilesAgent is the agent's computer_files: it keeps each call's args and answers with
// answer.
type fakeFilesAgent struct {
	mu     sync.Mutex
	calls  []map[string]any
	nodes  []string
	answer map[string]any
}

func (f *fakeFilesAgent) call(_ context.Context, nodeID, tool string, args map[string]any, _ int64) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tool != computeruse.ToolFiles {
		return map[string]any{"success": false, "error": "unexpected tool " + tool}
	}
	f.calls = append(f.calls, args)
	f.nodes = append(f.nodes, nodeID)
	return f.answer
}

func newFilesEnv(t *testing.T) (*voiceEnv, *fakeFilesAgent) {
	t.Helper()
	agent := &fakeFilesAgent{}
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) { m.recordingAgent = agent.call })
	return ve, agent
}

const budgetSheets = `{"name": "Budget 2026-09", "sheets": [
	{"title": "Transactions", "columns": ["Date", "Description", "Amount", "Category"],
	 "rows": [["2026-09-01", "Corner Grocer", -54.2, "Groceries"], ["2026-09-03", "City Power", "-88.10", "Utilities"]]},
	{"title": "By category", "columns": ["Category", "Total"], "rows": [["Groceries", -54.2], ["Utilities", -88.1]]}]}`

// write_spreadsheet builds the workbook, has the computer save it in its Jarvis folder and
// attaches it for the errand's card; it needs computer control on and a plain file name.
func TestWriteSpreadsheet(t *testing.T) {
	ve, agent := newFilesEnv(t)
	tool := &writeSpreadsheetTool{m: ve.m}

	if out := runTool(t, tool, budgetSheets, ve.recTurn(7)); out["error"] != "computer_off" {
		t.Fatalf("no computer: %v", out)
	}
	ve.setComputerControl("off")
	if out := runTool(t, tool, budgetSheets, ve.recTurn(7)); out["error"] != "computer_off" || !strings.Contains(out["message"].(string), "Computer control") {
		t.Fatalf("control off: %v", out)
	}
	if out := runTool(t, tool, budgetSheets, ve.recTurn(0)); out["error"] != "no_speaker" {
		t.Fatalf("unknown speaker: %v", out)
	}
	ve.setComputerControl("on")
	if out := runTool(t, tool, `{"name": "../budget", "sheets": [{"title": "x", "rows": [[1]]}]}`, ve.recTurn(7)); out["error"] != "invalid_argument" {
		t.Fatalf("a path for a name: %v", out)
	}
	if out := runTool(t, tool, `{"name": "budget", "sheets": "rows"}`, ve.recTurn(7)); out["error"] != "invalid_argument" {
		t.Fatalf("bad sheets: %v", out)
	}
	if len(agent.calls) != 0 {
		t.Fatalf("asked the agent: %v", agent.calls)
	}

	agent.answer = map[string]any{"success": true, "path": "/home/avery/Documents/Jarvis/Budget 2026-09.xlsx", "bytes": 6000.0}
	out := runTool(t, tool, budgetSheets, ve.recTurn(7))
	if out["status"] != "saved" || out["path"] != "/home/avery/Documents/Jarvis/Budget 2026-09.xlsx" || out["rows"] != 4.0 ||
		!strings.Contains(out["message"].(string), "Budget 2026-09.xlsx") {
		t.Fatalf("saved: %v", out)
	}
	// The report says what is in it: the last sheet's rows (the totals, for a budget).
	if msg := out["message"].(string); !strings.HasSuffix(msg, " By category: Groceries -54.2; Utilities -88.1.") {
		t.Fatalf("message: %s", msg)
	}
	if ids, _ := out["attachment_ids"].([]any); len(ids) != 1 || ids[0] != "att-1" {
		t.Fatalf("attachment ids: %v", out)
	}
	if len(agent.calls) != 1 || agent.nodes[0] != ve.node.id {
		t.Fatalf("agent calls: %v %v", agent.calls, agent.nodes)
	}
	args := agent.calls[0]
	if args["op"] != "write" || args["path"] != "~/Documents/Jarvis/Budget 2026-09.xlsx" {
		t.Fatalf("write args: %v", args)
	}
	data, err := base64.StdEncoding.DecodeString(args["content_b64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.GetSheetList(), "|"); got != "Transactions|By category" {
		t.Fatalf("sheets %q", got)
	}
	rows, _ := f.GetRows("Transactions")
	if len(rows) != 3 || strings.Join(rows[0], ",") != "Date,Description,Amount,Category" || strings.Join(rows[2], ",") != "2026-09-03,City Power,-88.1,Utilities" {
		t.Fatalf("rows %q", rows)
	}
	if typ, _ := f.GetCellType("Transactions", "C3"); typ == excelize.CellTypeSharedString || typ == excelize.CellTypeInlineString {
		t.Fatal("an amount is text")
	}
	up := ve.notify.uploads
	if len(up) != 1 || up[0].owner != spreadsheetOwner || up[0].household != voiceHH || up[0].contentType != notifications.XLSXMime || up[0].body != string(data) {
		t.Fatalf("uploads: %+v", up)
	}

	// The agent refusing is said with its code; nothing is attached.
	agent.answer = map[string]any{"success": false, "error_code": "too_large", "error": "6000000 bytes, at most 5242880"}
	if out := runTool(t, tool, budgetSheets, ve.recTurn(7)); out["error"] != "too_large" || !strings.Contains(out["message"].(string), "couldn't save") {
		t.Fatalf("agent refused: %v", out)
	}
	if len(ve.notify.uploads) != 1 {
		t.Fatalf("attached a file that wasn't saved: %+v", ve.notify.uploads)
	}
}

// read_computer_file hands back a statement's text, capped; a file the agent sent no text for
// (not CSV/OFX/TXT/JSON, or over 1 MiB) and the agent's refusals are said plainly.
func TestReadComputerFileLimits(t *testing.T) {
	ve, agent := newFilesEnv(t)
	tool := &readComputerFileTool{m: ve.m}
	if out := runTool(t, tool, `{"path": "~/Downloads/statement-2026-09.csv"}`, ve.recTurn(7)); out["error"] != "computer_off" {
		t.Fatalf("no computer: %v", out)
	}
	ve.setComputerControl("on")
	if out := runTool(t, tool, `{"path": " "}`, ve.recTurn(7)); out["error"] != "invalid_argument" {
		t.Fatalf("no path: %v", out)
	}

	csv := "date,description,amount\n2026-09-01,Corner Grocer,-54.20\n"
	agent.answer = map[string]any{"success": true, "path": "/home/avery/Downloads/statement-2026-09.csv", "bytes": float64(len(csv)), "mime": "text/csv", "text": csv}
	out := runTool(t, tool, `{"path": "~/Downloads/statement-2026-09.csv"}`, ve.recTurn(7))
	if out["text"] != csv || out["truncated"] != false || out["path"] != "/home/avery/Downloads/statement-2026-09.csv" {
		t.Fatalf("read: %v", out)
	}
	if len(agent.calls) != 1 || agent.calls[0]["op"] != "read" || agent.calls[0]["path"] != "~/Downloads/statement-2026-09.csv" {
		t.Fatalf("agent calls: %v", agent.calls)
	}

	long := strings.Repeat("2026-09-01,Corner Grocer,-54.20\n", maxFileText/16)
	agent.answer = map[string]any{"success": true, "path": "/x.csv", "bytes": float64(len(long)), "text": long}
	out = runTool(t, tool, `{"path": "~/Downloads/x.csv"}`, ve.recTurn(7))
	if text := out["text"].(string); out["truncated"] != true || len(text) > maxFileText || !strings.HasPrefix(long, text) {
		t.Fatalf("long: %d bytes, truncated %v", len(text), out["truncated"])
	}

	agent.answer = map[string]any{"success": true, "path": "/home/avery/Downloads/statement.pdf", "bytes": 2e6, "mime": "application/pdf"}
	if out := runTool(t, tool, `{"path": "~/Downloads/statement.pdf"}`, ve.recTurn(7)); out["error"] != "not_text" ||
		!strings.Contains(out["message"].(string), "CSV") || !strings.Contains(out["message"].(string), "1 MiB") {
		t.Fatalf("no text: %v", out)
	}
	agent.answer = map[string]any{"success": false, "error_code": "path_refused", "error": "only your Downloads, Documents and Desktop folders"}
	if out := runTool(t, tool, `{"path": "~/Private/notes.txt"}`, ve.recTurn(7)); out["error"] != "path_refused" || !strings.Contains(out["message"].(string), "Downloads") {
		t.Fatalf("refused: %v", out)
	}
	agent.answer = map[string]any{"success": false, "error_code": "offline", "error": "the computer is offline"}
	if out := runTool(t, tool, `{"path": "~/Downloads/x.csv"}`, ve.recTurn(7)); out["error"] != "offline" {
		t.Fatalf("offline: %v", out)
	}
}

// An errand card carries the files its steps attached; a refused attachment still posts the card.
func TestErrandCardAttachments(t *testing.T) {
	ve, _ := newFilesEnv(t)
	cards := errandCards{m: ve.m}
	uid := int64(7)
	if id := cards.Post(context.Background(), errands.Card{HouseholdID: voiceHH, UserID: &uid, Title: "✅ Errand done: budget",
		Category: "errand", AttachmentIDs: []string{"att-1"}}); id == "" {
		t.Fatal("not posted")
	}
	if id := cards.Post(context.Background(), errands.Card{HouseholdID: voiceHH, UserID: &uid, Title: "✅ Errand done: again",
		Category: "errand", AttachmentIDs: []string{"bad"}}); id == "" {
		t.Fatal("a refused attachment lost the card")
	}
	items := ve.notify.items
	if len(items) != 2 || strings.Join(items[0].AttachmentIDs, ",") != "att-1" || items[0].AttachmentOwner != errands.AttachmentOwner ||
		len(items[1].AttachmentIDs) != 0 || items[1].Title != "✅ Errand done: again" {
		t.Fatalf("items: %+v", items)
	}
}
