package browser

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/jarvispage"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// tabsExt answers list_tabs with a chat tab (current, 3) on the server and a docs tab (5), and
// counts fills and clicks.
func tabsExt(current float64) (func(Message) Message, *int) {
	acted := 0
	return func(m Message) Message {
		switch m.Cmd {
		case "list_tabs":
			return ok(map[string]any{"tabs": []any{
				map[string]any{"tab_id": 3.0, "url": "http://10.0.0.5:7710/chat", "title": "Chat", "current": current == 3},
				map[string]any{"tab_id": 5.0, "url": "https://docs.example.com/form", "title": "Form", "current": current == 5},
			}, "count": 2})
		case "fill", "click":
			acted++
			return ok(map[string]any{"chars": 2, "clicked": "ok"})
		}
		return ok(map[string]any{})
	}, &acted
}

func pageOrigins() *jarvispage.Origins {
	o := &jarvispage.Origins{}
	o.Add("http://10.0.0.5:7700")
	o.Add("http://10.0.0.5:7710")
	return o
}

func TestFillAndClickRefuseTheJarvisPage(t *testing.T) {
	h, path := startHub(t)
	handle, acted := tabsExt(3)
	connectExt(t, h, path, "Firefox", true, handle)
	ts := NewTools(h)
	ts.Origins = pageOrigins()
	ctx := context.Background()
	call := func(name string, args map[string]any) map[string]any {
		out, handled := ts.Call(ctx, name, args, node.Request{})
		if !handled {
			t.Fatalf("%s not handled", name)
		}
		return out
	}
	for _, c := range []struct {
		name, tool string
		args       map[string]any
		refused    string // "" acts
	}{
		{"fill the current tab (the chat)", ToolFill, map[string]any{"field": "message", "text": "hi"}, "Nothing was filled"},
		{"click on the chat", ToolClick, map[string]any{"text": "Send"}, "Nothing was clicked"},
		{"fill the chat by tab id", ToolFill, map[string]any{"text": "hi", "tab_id": "firefox:3"}, "Nothing was filled"},
		{"fill another tab by id", ToolFill, map[string]any{"text": "hi", "tab_id": "firefox:5"}, ""},
		{"click in another tab by id", ToolClick, map[string]any{"text": "OK", "tab_id": 5.0}, ""},
	} {
		before := *acted
		out := call(c.tool, c.args)
		if c.refused == "" {
			if out["success"] != true || *acted != before+1 {
				t.Errorf("%s: %v", c.name, out)
			}
			continue
		}
		msg, _ := out["error"].(string)
		if out["success"] != false || out["error_code"] != CodeJarvisPage || *acted != before ||
			!strings.HasPrefix(msg, c.refused+": that tab is the Jarvis page") || !strings.Contains(msg, "ask which page they meant") {
			t.Errorf("%s: %v", c.name, out)
		}
	}
}

func TestFillOnTheJarvisPageRefusedAfterConfirmation(t *testing.T) {
	h, path := startHub(t)
	cur := 5.0
	handle, acted := tabsExt(0)
	connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		if m.Cmd == "list_tabs" {
			h2, _ := tabsExt(cur)
			return h2(m)
		}
		return handle(m)
	})
	ts := NewTools(h)
	ts.Origins = pageOrigins()
	ts.Confirm = &dictate.Confirmer{Mode: dictate.ConfirmAll}
	ctx := context.Background()
	out, _ := ts.Call(ctx, ToolFill, map[string]any{"field": "name", "text": "Ann"}, node.Request{})
	id, _ := out["confirmation_id"].(string)
	if out["status"] != "awaiting_confirmation" || id == "" {
		t.Fatalf("not asked: %v", out)
	}
	cur = 3 // the user switched to the chat tab to answer
	res, err := ts.Confirm.Answer(ctx, id, "yes")
	if err == nil || *acted != 0 || !strings.Contains(err.Error(), "Jarvis page") {
		t.Fatalf("answer %v %v acted %d", res, err, *acted)
	}
}

// No server known (or no Origins): no check, and no extra list_tabs.
func TestFillWithoutOrigins(t *testing.T) {
	h, path := startHub(t)
	handle, acted := tabsExt(3)
	f := connectExt(t, h, path, "Firefox", true, handle)
	ts := NewTools(h)
	if out, _ := ts.Call(context.Background(), ToolFill, map[string]any{"text": "hi"}, node.Request{}); out["success"] != true || *acted != 1 {
		t.Fatalf("%v", out)
	}
	for _, m := range f.received() {
		if m.Cmd == "list_tabs" {
			t.Fatal("list_tabs without a server to compare with")
		}
	}
}

func TestHubFocusedTab(t *testing.T) {
	h, path := startHub(t)
	handle, _ := tabsExt(3)
	connectExt(t, h, path, "Chrome", false, handle)
	ctx := context.Background()
	if _, _, ok := h.FocusedTab(ctx); ok {
		t.Fatal("a tab from a browser without focus")
	}
	connectExt(t, h, path, "Firefox", true, handle)
	u, title, ok := h.FocusedTab(ctx)
	if !ok || u != "http://10.0.0.5:7710/chat" || title != "Chat" {
		t.Fatalf("%q %q %v", u, title, ok)
	}
}
