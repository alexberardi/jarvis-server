package browser

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// taskExt plays a browser whose current tab is the Jarvis chat (3) and where open creates tab
// 42 (google.com). closed makes tab 42 gone; current changes the focused tab.
type taskExt struct {
	mu      sync.Mutex
	closed  bool
	current float64
}

func (e *taskExt) handle(m Message) Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	tab, hasTab := m.Args["tab_id"].(float64)
	if hasTab && tab == 42 && e.closed {
		return Message{OK: false, Code: "tab_not_found", Error: "no tab 42 (it may have been closed); list the tabs again"}
	}
	switch m.Cmd {
	case "list_tabs":
		tabs := []any{
			map[string]any{"tab_id": 3.0, "url": "http://10.0.0.5:7710/chat", "title": "Chat", "current": e.current == 3},
			map[string]any{"tab_id": 5.0, "url": "https://docs.example.com/", "title": "Docs", "current": e.current == 5},
		}
		if !e.closed {
			tabs = append(tabs, map[string]any{"tab_id": 42.0, "url": "https://www.google.com/", "title": "Google", "current": false})
		}
		return ok(map[string]any{"tabs": tabs, "count": len(tabs)})
	case "open":
		return ok(map[string]any{"tab": map[string]any{"tab_id": 42.0, "window_id": 9.0, "url": m.Args["url"], "active": true}})
	case "fill", "click", "read_page", "navigate", "scroll", "find":
		if !hasTab {
			tab = e.current
		}
		return ok(map[string]any{"tab": map[string]any{"tab_id": tab}, "chars": 4, "text": "results"})
	}
	return ok(map[string]any{})
}

// lastArgs is the arguments of the last command cmd the fake browser got.
func lastArgs(t *testing.T, f *fakeExt, cmd string) map[string]any {
	t.Helper()
	ms := f.received()
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Cmd == cmd {
			return ms[i].Args
		}
	}
	t.Fatalf("no %s sent", cmd)
	return nil
}

// #578: "go to google.com and search for dogs" from the chat in the same browser. The page
// opens behind the chat, and the fill (no tab_id) goes to the Google tab, not to the chat that
// has the focus; so do the next turn's ("search for cats instead"), a click and a read.
func TestCommandsGoToTheTaskTab(t *testing.T) {
	h, path := startHub(t)
	e := &taskExt{current: 3}
	f := connectExt(t, h, path, "Firefox", true, e.handle)
	ts := NewTools(h)
	ts.Origins = pageOrigins()
	ts.Confirm = &dictate.Confirmer{Mode: dictate.ConfirmSubmit}
	ctx := context.Background()
	call := func(name string, args map[string]any) map[string]any {
		out, _ := ts.Call(ctx, name, args, node.Request{})
		return out
	}

	out := call(ToolOpen, map[string]any{"url": "google.com"})
	if out["success"] != true || out["tab_id"] != "firefox:42" || out["opened_behind_chat"] != true {
		t.Fatalf("open: %v", out)
	}
	if a := lastArgs(t, f, "open"); a["background"] != true {
		t.Errorf("open from the chat must not take its focus: %v", a)
	}
	for _, c := range []struct {
		tool, cmd string
		args      map[string]any
	}{
		{ToolFill, "fill", map[string]any{"field": "search", "text": "dogs", "submit": true}},
		{ToolFill, "fill", map[string]any{"field": "search", "text": "cats", "submit": true}},
		{ToolClick, "click", map[string]any{"text": "Images"}},
		{ToolReadPage, "read_page", map[string]any{}},
		{ToolScroll, "scroll", map[string]any{"direction": "down"}},
		{ToolNavigate, "navigate", map[string]any{"action": "back"}},
		{ToolFind, "find", map[string]any{"text": "dogs"}},
	} {
		out := call(c.tool, c.args)
		if out["success"] != true {
			t.Fatalf("%s %v: %v", c.tool, c.args, out)
		}
		if a := lastArgs(t, f, c.cmd); a["tab_id"] != 42.0 {
			t.Errorf("%s %v went to tab %v, want the task tab 42", c.tool, c.args, a["tab_id"])
		}
	}
	if c := lastArgs(t, f, "fill"); c["text"] != "cats" {
		t.Errorf("second search: %v", c)
	}

	// A tab_id given wins, and becomes the task tab.
	call(ToolFill, map[string]any{"text": "x", "tab_id": "firefox:5"})
	if a := lastArgs(t, f, "fill"); a["tab_id"] != 5.0 {
		t.Errorf("explicit tab: %v", a)
	}
	call(ToolReadPage, map[string]any{})
	if a := lastArgs(t, f, "read_page"); a["tab_id"] != 5.0 {
		t.Errorf("after an explicit tab: %v", a)
	}
	// Closing the task tab forgets it: the current tab again.
	call(ToolCloseTab, map[string]any{"tab_id": "firefox:5"})
	e.mu.Lock()
	e.current = 5
	e.mu.Unlock()
	call(ToolReadPage, map[string]any{})
	if a := lastArgs(t, f, "read_page"); a["tab_id"] != nil {
		t.Errorf("after close: %v", a)
	}
}

// A task tab the user closed: the command goes to the current tab, as without one.
func TestClosedTaskTabFallsBackToTheCurrentTab(t *testing.T) {
	h, path := startHub(t)
	e := &taskExt{current: 5}
	f := connectExt(t, h, path, "Firefox", true, e.handle)
	ts := NewTools(h)
	ts.Origins = pageOrigins()
	ctx := context.Background()
	out, _ := ts.Call(ctx, ToolOpen, map[string]any{"url": "https://www.google.com"}, node.Request{})
	if out["opened_behind_chat"] != nil || lastArgs(t, f, "open")["background"] != nil {
		t.Errorf("not on the chat: open in front as before: %v %v", out, lastArgs(t, f, "open"))
	}
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	out, _ = ts.Call(ctx, ToolFill, map[string]any{"field": "search", "text": "dogs"}, node.Request{})
	if out["success"] != true || lastArgs(t, f, "fill")["tab_id"] != nil {
		t.Fatalf("fallback: %v %v", out, lastArgs(t, f, "fill"))
	}
	if ts.taskTab() != "" {
		t.Errorf("closed task tab kept: %q", ts.taskTab())
	}
}

// After TaskTabIdle without use, "this page" is the current tab again.
func TestTaskTabGoesIdle(t *testing.T) {
	h, path := startHub(t)
	e := &taskExt{current: 5}
	f := connectExt(t, h, path, "Firefox", true, e.handle)
	ts := NewTools(h)
	now := time.Unix(1_800_000_000, 0)
	ts.now = func() time.Time { return now }
	ctx := context.Background()
	ts.Call(ctx, ToolOpen, map[string]any{"url": "https://www.google.com"}, node.Request{})
	now = now.Add(TaskTabIdle - time.Second)
	ts.Call(ctx, ToolReadPage, map[string]any{}, node.Request{})
	if a := lastArgs(t, f, "read_page"); a["tab_id"] != 42.0 {
		t.Fatalf("before idle: %v", a)
	}
	now = now.Add(TaskTabIdle + time.Second)
	ts.Call(ctx, ToolReadPage, map[string]any{}, node.Request{})
	if a := lastArgs(t, f, "read_page"); a["tab_id"] != nil {
		t.Fatalf("idle task tab still used: %v", a)
	}
}
