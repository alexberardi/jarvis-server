package cc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
)

// computerAgent is an agent answering the computer tools: reply maps a tool call to its output.
type computerAgent struct {
	mu    sync.Mutex
	m     *Module
	calls []map[string]any // each tool_call's details
	reply func(name string, args map[string]any, n int) string
}

func (a *computerAgent) Publish(topic string, payload []byte, _ byte, _ bool) error {
	var msgs []map[string]any
	if err := json.Unmarshal(payload, &msgs); err != nil || len(msgs) == 0 || msgs[0]["command"] != "tool_call" {
		return nil
	}
	d := msgs[0]["details"].(map[string]any)
	a.mu.Lock()
	a.calls = append(a.calls, d)
	n := len(a.calls)
	a.mu.Unlock()
	args, _ := d["arguments"].(map[string]any)
	body := `{"output": ` + a.reply(d["command_name"].(string), args, n) + `}`
	go a.m.bus.Deliver(d["reply_request_id"].(string), strings.Split(topic, "/")[2], json.RawMessage(body))
	return nil
}

func (a *computerAgent) Request(context.Context, string, []byte, string) ([]byte, error) {
	return nil, nil
}

func (a *computerAgent) sent() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]map[string]any(nil), a.calls...)
}

// The executor runs the loop's observe, act and stop as the agent's computer tools on the run's
// computer: the first look opens the task window on the site, a screenshot comes back as the
// observation's image (never in the output), and act's results are read per action.
func TestComputerExecOverTheBus(t *testing.T) {
	shot := base64.StdEncoding.EncodeToString(pngBytes)
	agent := &computerAgent{reply: func(name string, args map[string]any, n int) string {
		switch {
		case name == computeruse.ToolObserve && args["url"] == nil:
			return `{"success": false, "error": "the task has no window yet: give the url to open it on", "error_code": "invalid_argument"}`
		case name == computeruse.ToolObserve:
			return `{"success": true, "target": "browser", "url": "https://shop.example/", "title": "Shop", "text": "Buy\n$4.99",
				"snapshot": {"gen": 3, "elements": [{"ref": "e1", "role": "button", "name": "Buy"}, {"ref": "e2", "role": "textbox", "name": "Password", "type": "password"}]},
				"capture": {"capture_id": "cap_1", "width": 800, "height": 600},
				"image_attachments": [{"mime": "image/png", "data": "` + shot + `", "label": "screenshot"}]}`
		case name == computeruse.ToolAct:
			return `{"success": false, "stopped_at": 1, "results": [{"i": 0, "ok": true}, {"i": 1, "ok": false, "code": "stale_ref", "error": "gone"}]}`
		case name == computeruse.ToolFiles && args["op"] == "list":
			return `{"success": true, "path": "/home/a/Downloads", "entries": [{"name": "statement.pdf", "dir": false, "bytes": 4096}, {"name": "old", "dir": true}]}`
		case name == computeruse.ToolFiles && args["path"] == "missing.csv":
			return `{"success": false, "error": "no such file", "error_code": "not_found"}`
		case name == computeruse.ToolFiles:
			return `{"success": true, "path": "/home/a/Documents/Jarvis/budget.csv", "bytes": 9, "text": "a,b\n1,2\n"}`
		}
		return `{"success": true}`
	}}
	e := newEnv(t, envOpts{configure: func(m *Module) { m.Publisher = agent; agent.m = m }})
	ctx := context.Background()
	store := &computer.Store{DB: e.m.deps.DB}
	run, err := store.Create(ctx, computer.Run{HouseholdID: "hh-1", NodeID: "node-1", WorkflowID: "wf_1", Goal: "buy", Target: "browser"})
	if err != nil {
		t.Fatal(err)
	}
	ex := computerExec{m: e.m, store: store}
	tgt := computeruse.Target{Kind: "browser", Site: "shop.example"}

	obs, err := ex.Observe(ctx, computer.Observe{RunID: run.ID, Target: tgt, Snapshot: true, Screenshot: true})
	if err != nil {
		t.Fatal(err)
	}
	if obs.URL != "https://shop.example/" || obs.Text != "Buy\n$4.99" || obs.Gen != 3 || len(obs.Elements) != 2 || obs.Elements[0].Name != "Buy" ||
		!obs.Elements[1].Secure || obs.Image == nil || obs.Image.CaptureID != "cap_1" || obs.Image.Width != 800 ||
		!bytes.Equal(obs.Image.Data, pngBytes) {
		t.Fatalf("observation = %+v", obs)
	}
	calls := agent.sent()
	if len(calls) != 2 || calls[1]["arguments"].(map[string]any)["url"] != "https://shop.example" ||
		calls[0]["arguments"].(map[string]any)["run_id"] != run.ID {
		t.Fatalf("calls = %v", calls)
	}

	res, err := ex.Act(ctx, computer.Batch{RunID: run.ID, Target: tgt, Actions: []computeruse.Action{
		{Kind: "press", Ref: "e1", Intent: "Buy it"}, {Kind: "press", Ref: "e9", Intent: "Next"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 2 || !res.Results[0].OK || res.Results[1].OK || res.Results[1].Code != "stale_ref" || res.Results[1].I != 1 {
		t.Fatalf("results = %+v", res)
	}
	act := agent.sent()[2]
	acts, _ := act["arguments"].(map[string]any)["actions"].([]any)
	if act["command_name"] != computeruse.ToolAct || len(acts) != 2 || acts[0].(map[string]any)["intent"] != "Buy it" {
		t.Fatalf("act = %v", act)
	}

	// The result checks read files through computer_files (piece 12).
	list, err := ex.Files(ctx, computer.Files{RunID: run.ID, Op: "list", Path: "~/Downloads"})
	if err != nil || len(list.Entries) != 2 || list.Entries[0] != (computer.FileEntry{Name: "statement.pdf", Bytes: 4096}) || !list.Entries[1].Dir {
		t.Fatalf("list = %+v %v", list, err)
	}
	if fa := agent.sent()[3]; fa["command_name"] != computeruse.ToolFiles || fa["arguments"].(map[string]any)["path"] != "~/Downloads" ||
		fa["arguments"].(map[string]any)["run_id"] != run.ID {
		t.Fatalf("files = %v", fa)
	}
	read, err := ex.Files(ctx, computer.Files{RunID: run.ID, Op: "read", Path: "budget.csv"})
	if err != nil || read.Text != "a,b\n1,2\n" || read.Bytes != 9 {
		t.Fatalf("read = %+v %v", read, err)
	}
	if _, err := ex.Files(ctx, computer.Files{RunID: run.ID, Op: "read", Path: "missing.csv"}); err == nil || err.Error() != "no such file" {
		t.Fatalf("missing file: %v", err)
	}

	ex.Stop(ctx, run.ID)
	if st := agent.sent()[6]; st["command_name"] != computeruse.ToolStop || st["arguments"].(map[string]any)["run_id"] != run.ID {
		t.Fatalf("stop = %v", st)
	}
	for _, c := range agent.sent() {
		if c["node_id"] != nil && c["node_id"] != "node-1" {
			t.Fatalf("sent to %v", c["node_id"])
		}
	}
}

func TestComputerTargetsForErrands(t *testing.T) {
	ve := newVoiceEnv(t, "")
	ctx := context.Background()
	if ids, off := ve.m.computerTargets(ctx, voiceHH); len(ids) != 0 || !strings.Contains(off, "Jarvis Automation agent") {
		t.Fatalf("no computers: %v %q", ids, off)
	}
	ve.setComputerControl("off")
	if ids, off := ve.m.computerTargets(ctx, voiceHH); len(ids) != 0 || !strings.HasPrefix(off, "Computer control is off.") {
		t.Fatalf("off: %v %q", ids, off)
	}
	ve.setComputerControl("on")
	if ids, off := ve.m.computerTargets(ctx, voiceHH); len(ids) != 1 || ids[0] != ve.node.id || off != "" {
		t.Fatalf("on: %v %q", ids, off)
	}
	// The errand planner offers computer_task only now.
	menu := ve.m.errands.FullMenu(ctx, nil, voiceHH, nil)
	if menu[len(menu)-1].Command != "computer_task" {
		t.Fatalf("menu = %+v", menu)
	}
}

// The first look opens the task's window on its start address, as the user gave it (scheme,
// port and path), and on https://<site> without one.
func TestComputerExecOpensStartURL(t *testing.T) {
	agent := &computerAgent{reply: func(name string, args map[string]any, n int) string {
		if args["url"] == nil {
			return `{"success": false, "error": "the task has no window yet: give the url to open it on", "error_code": "invalid_argument"}`
		}
		return `{"success": true, "target": "browser", "url": "x", "snapshot": {"gen": 1, "elements": []}}`
	}}
	e := newEnv(t, envOpts{configure: func(m *Module) { m.Publisher = agent; agent.m = m }})
	ctx := context.Background()
	store := &computer.Store{DB: e.m.deps.DB}
	run, err := store.Create(ctx, computer.Run{HouseholdID: "hh-1", NodeID: "node-1", WorkflowID: "wf_1", Goal: "buy", Target: "browser"})
	if err != nil {
		t.Fatal(err)
	}
	ex := computerExec{m: e.m, store: store}
	tgt := computeruse.Target{Kind: "browser", Site: "127.0.0.1:8080"}
	for _, c := range []struct{ start, want string }{
		{"http://127.0.0.1:8080/store/", "http://127.0.0.1:8080/store/"},
		{"", "https://127.0.0.1:8080"},
	} {
		if _, err := ex.Observe(ctx, computer.Observe{RunID: run.ID, Target: tgt, StartURL: c.start, Snapshot: true}); err != nil {
			t.Fatal(err)
		}
		calls := agent.sent()
		if got := calls[len(calls)-1]["arguments"].(map[string]any)["url"]; got != c.want {
			t.Fatalf("start %q: opened %v", c.start, got)
		}
	}
}

// A desktop task's batch says when it resumes a paused run, and its results carry the app it
// was the first in and what the clipboard held.
func TestComputerExecDesktop(t *testing.T) {
	agent := &computerAgent{reply: func(name string, args map[string]any, n int) string {
		if name == computeruse.ToolAct {
			return `{"success": false, "stopped_at": 2, "paused": true, "new_apps": ["Finder"], "results": [
				{"i": 0, "ok": true, "new_app": "Finder"}, {"i": 1, "ok": true, "text": "milk"},
				{"i": 2, "ok": false, "code": "paused", "error": "paused: the user is using the computer"}]}`
		}
		return `{"success": true}`
	}}
	e := newEnv(t, envOpts{configure: func(m *Module) { m.Publisher = agent; agent.m = m }})
	ctx := context.Background()
	store := &computer.Store{DB: e.m.deps.DB}
	run, err := store.Create(ctx, computer.Run{HouseholdID: "hh-1", NodeID: "node-1", WorkflowID: "wf_1", Goal: "note", Target: "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	ex := computerExec{m: e.m, store: store}
	res, err := ex.Act(ctx, computer.Batch{RunID: run.ID, Target: computeruse.Target{Kind: "desktop", App: "Notes"}, Resume: true,
		Actions: []computeruse.Action{{Kind: "press", Ref: "a1", Intent: "Open"}, {Kind: "clipboard_read", Intent: "Read"},
			{Kind: "keypress", Keys: []string{"cmd", "s"}, Intent: "Save"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Paused || len(res.Results) != 3 || res.Results[0].NewApp != "Finder" || res.Results[1].Text != "milk" || res.Results[2].Code != "paused" {
		t.Fatalf("result = %+v", res)
	}
	args := agent.sent()[0]["arguments"].(map[string]any)
	if args["resume"] != true || args["target"].(map[string]any)["app"] != "Notes" {
		t.Fatalf("act args = %v", args)
	}
}
