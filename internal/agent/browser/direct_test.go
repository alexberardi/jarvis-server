package browser

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Tonight's transcripts: the model answered "scroll down a bit more" with "already scrolled".
func TestDirectScrollsWithNoModel(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	if _, ok := ts.Direct(context.Background(), "Scroll down."); ok {
		t.Fatal("scrolled with no browser connected")
	}
	f := connectExt(t, h, path, "Firefox", true, func(Message) Message { return ok(map[string]any{}) })
	for said, want := range map[string]string{
		"Scroll down a little bit.":    "down",
		"Scroll down a bit more.":      "down",
		"scroll up":                    "up",
		"Jarvis, scroll down again":    "down",
		"Scroll to the top.":           "top",
		"go to the bottom of the page": "bottom",
	} {
		out, ok := ts.Direct(context.Background(), said)
		if !ok || out["success"] != true || out[node.IslandOnlyKey] != true {
			t.Fatalf("%q: %v %v", said, out, ok)
		}
		if got := lastArgs(t, f, "scroll")["direction"]; got != want {
			t.Fatalf("%q scrolled %v, want %s", said, got, want)
		}
	}
	for _, said := range []string{"scroll down to the reviews", "Scroll through my emails", "go to the top tab"} {
		if _, ok := ts.Direct(context.Background(), said); ok {
			t.Fatalf("%q handled on the node", said)
		}
	}
	// A click with the browser in front stays on the node (2026-10-28 decision): the page has
	// nothing to click here, so it's said back, never a free model turn.
	if out, ok := ts.Direct(context.Background(), "Click the first link"); !ok || out["success"] != false || out["error_code"] != "target_not_found" {
		t.Fatalf("click with nothing on the page: %v %v", out, ok)
	}
	off := node.WithEnabled(context.Background(), func(n string) bool { return n != ToolScroll })
	if _, ok := ts.Direct(off, "scroll down"); ok {
		t.Fatal("scrolled while turned off")
	}
}

// Q32: at the bottom, "scroll down" says so on the island instead of a scroll that didn't happen.
func TestDirectScrollAtTheEnd(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		return ok(map[string]any{"direction": m.Args["direction"], "moved": false, "position_percent": 100.0})
	})
	out, ok := ts.Direct(context.Background(), "scroll down")
	if !ok || out["success"] != false || out["error_code"] != CodeAtEnd || out[node.IslandKey] != "Already at the bottom" {
		t.Fatalf("%v %v", out, ok)
	}
	// The model's call says the same: no "scrolled" when nothing moved.
	out, _ = ts.Call(context.Background(), ToolScroll, map[string]any{"direction": "down"}, node.Request{})
	if out["success"] != false || !strings.Contains(out["error"].(string), "already at the bottom") {
		t.Fatalf("model's call: %v", out)
	}
}
