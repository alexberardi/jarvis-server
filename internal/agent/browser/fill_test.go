package browser

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestFillAndClick(t *testing.T) {
	h, path := startHub(t)
	f := connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		switch m.Cmd {
		case "fill":
			return ok(map[string]any{"field": m.Args["field"], "chars": 5, "submitted": false, "tab": map[string]any{"tab_id": 3.0}})
		case "click":
			if m.Args["text"] == "Delete" {
				return Message{OK: false, Code: "ambiguous", Error: "2 buttons match: delete; delete all"}
			}
			return ok(map[string]any{"clicked": "ok", "in_dialog": true})
		}
		return ok(map[string]any{})
	})
	var island []string
	ts := NewTools(h)
	ts.Show = func(s string, _ time.Duration) { island = append(island, s) }
	ctx := context.Background()
	call := func(name string, args map[string]any) map[string]any {
		out, handled := ts.Call(ctx, name, args, node.Request{})
		if !handled {
			t.Fatalf("%s not handled", name)
		}
		return out
	}

	out := call(ToolFill, map[string]any{"field": "username", "text": "abcde\n"})
	if out["success"] != true || out["field"] != "username" {
		t.Fatalf("fill: %v", out)
	}
	cmds := f.received()
	if a := cmds[len(cmds)-1].Args; a["text"] != "abcde" || a["field"] != "username" || a["submit"] != false {
		t.Errorf("fill args %v (text must be sanitized)", a)
	}
	if island[0] != "Filling the username field: abcde" {
		t.Errorf("island %q", island)
	}
	// A password: what is typed never reaches the island.
	call(ToolFill, map[string]any{"field": "the password", "text": "hunter2"})
	if last := island[len(island)-1]; strings.Contains(last, "hunter2") || !strings.Contains(last, "•••••••") {
		t.Errorf("password shown: %q", last)
	}
	if out := call(ToolFill, map[string]any{"field": "x", "text": " \x1b "}); out["error_code"] != "invalid_argument" {
		t.Errorf("empty text: %v", out)
	}
	if out := call(ToolClick, map[string]any{"text": "OK"}); out["success"] != true || out["clicked"] != "ok" {
		t.Errorf("click: %v", out)
	}
	if out := call(ToolClick, map[string]any{"text": "Delete"}); out["success"] != false || out["error_code"] != "ambiguous" {
		t.Errorf("ambiguous click: %v", out)
	}
	if out := call(ToolClick, map[string]any{"text": " "}); out["error_code"] != "invalid_argument" {
		t.Errorf("empty click: %v", out)
	}

	// With confirmation on, a click and a submitting fill wait for the user's yes; a plain fill doesn't.
	conf := &dictate.Confirmer{Mode: dictate.ConfirmSubmit}
	ts.Confirm = conf
	desk := &dictate.Tools{Confirm: conf}
	n := len(f.received())
	out = call(ToolClick, map[string]any{"text": "OK"})
	if out["status"] != "awaiting_confirmation" || len(f.received()) != n {
		t.Fatalf("click not gated: %v", out)
	}
	res, _ := desk.Call(ctx, dictate.ToolConfirm, map[string]any{"confirmation_id": out["confirmation_id"], "answer": "yes"}, node.Request{})
	if res["clicked"] != "ok" || len(f.received()) != n+1 {
		t.Errorf("confirmed click: %v", res)
	}
	out = call(ToolFill, map[string]any{"field": "password", "text": "hunter2", "submit": true})
	if q, _ := out["question"].(string); out["status"] != "awaiting_confirmation" || strings.Contains(q, "hunter2") {
		t.Errorf("submitting fill: %v", out)
	}
	res, _ = desk.Call(ctx, dictate.ToolConfirm, map[string]any{"confirmation_id": out["confirmation_id"], "answer": "cancel"}, node.Request{})
	if res["cancelled"] != true || len(f.received()) != n+1 {
		t.Errorf("cancelled fill ran: %v", res)
	}
	if out := call(ToolFill, map[string]any{"field": "search", "text": "pizza"}); out["status"] == "awaiting_confirmation" {
		t.Errorf("plain fill gated in submit mode: %v", out)
	}
}

// An extension older than fill and click answers unknown_command: the user is told to update
// it, in a sentence a small model can't turn into "done".
func TestFillAndClickOnAnOldExtension(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		return Message{OK: false, Code: "unknown_command", Error: "unknown command: " + m.Cmd}
	})
	ts := NewTools(h)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{ToolFill, map[string]any{"text": "how to walk your dog", "submit": true}},
		{ToolClick, map[string]any{"text": "Search"}},
	} {
		out, _ := ts.Call(context.Background(), c.tool, c.args, node.Request{})
		msg, _ := out["error"].(string)
		if out["success"] != false || out["error_code"] != "extension_outdated" ||
			!strings.Contains(msg, "Nothing was done") || !strings.Contains(msg, "Firefox is version 0.1.0") ||
			!strings.Contains(msg, "update the Jarvis browser extension") || !strings.Contains(msg, ExtensionVersion()) {
			t.Errorf("%s: %v", c.tool, out)
		}
	}
}
