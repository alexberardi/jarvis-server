//go:build linux

package computer

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// A desktop task in the fixture notes app (tools/ccsites/notesapp) through the real Linux
// desktop: the window list, a capture, the pointer and the keyboard, with no model in the loop.
// It types and clicks, so it is for a throwaway headless session only (Xvfb, headless sway),
// never a desktop someone is using: start the app there with --state FILE and name that file in
// JARVIS_NOTES_STATE.
func TestDesktopNotesLive(t *testing.T) {
	state := os.Getenv("JARVIS_NOTES_STATE")
	if os.Getenv("JARVIS_DESKTOP_LIVE") != "1" || state == "" {
		t.Skip("JARVIS_DESKTOP_LIVE=1 JARVIS_NOTES_STATE=FILE drives the notes app in this (headless) session")
	}
	d := NativeDesktop(nil)
	if d == nil {
		t.Fatal("no desktop")
	}
	tl := &Tools{Switch: NewSwitch(true, nil), Desktop: d, Activity: &Activity{}, Home: t.TempDir()}
	defer tl.closeDesktop()
	// The user is at the computer: the monitor is watching, then they press Shift.
	if _, err := d.Monitor.Since(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // input right as the monitor begins isn't told apart
	if err := d.Input.Keys(t.Context(), []string{"shift"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // the compositor's idle notification

	ws, err := d.Windows.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("windows: %+v", ws)
	out := call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_live", "target": desktopTarget("Jarvis Test Notes"),
		"snapshot": true, "screenshot": true})
	if out["success"] != true {
		t.Fatalf("observe: %v", out)
	}
	capture, _ := out["capture"].(map[string]any)
	id, _ := capture["capture_id"].(string)
	w, h := capture["width"].(int), capture["height"].(int)
	t.Logf("observe: app %v, window %v, title %v, capture %dx%d, snapshot error %v", out["app"], out["window_id"], out["title"], w, h, out["snapshot_error"])
	if !strings.Contains(strings.ToLower(out["title"].(string)), "notes") {
		t.Errorf("title = %v", out["title"])
	}
	// The title entry has the focus when the app starts: the pointer goes over the window (a
	// bare X server focuses the window under it), the title is typed, a click in the body (the
	// big text area to the right, half way down) and its text, then Ctrl+S.
	bx, by := w*7/10, h/2
	at := func(x, y int) map[string]any { return map[string]any{"x": x, "y": y, "capture_id": id} }
	with := func(m map[string]any, kv map[string]any) map[string]any {
		for k, v := range kv {
			m[k] = v
		}
		return m
	}
	res := call(t, tl, computeruse.ToolAct, map[string]any{"run_id": "cr_live", "target": desktopTarget("Jarvis Test Notes"),
		"blocked": map[string]any{"apps": []string{}},
		"actions": []map[string]any{
			with(at(bx, by), map[string]any{"kind": "move", "intent": "over the notes window"}),
			{"kind": "type", "intent": "the title", "text": "Groceries"},
			with(at(bx, by), map[string]any{"kind": "click", "intent": "the body"}),
			{"kind": "type", "intent": "the body", "text": "milk, eggs and bread"},
			{"kind": "keypress", "intent": "save", "keys": []string{"ctrl", "s"}},
		}})
	for _, r := range results(res) {
		if r["ok"] != true {
			t.Fatalf("act: %v", res)
		}
	}
	var got struct {
		Notes []struct{ Title, Body string }
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(state)
		if err == nil && json.Unmarshal(b, &got) == nil && len(got.Notes) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no note saved (%s: %v)", state, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := got.Notes[0]; n.Title != "Groceries" || !strings.Contains(n.Body, "eggs") {
		t.Fatalf("note = %+v", got.Notes)
	}
	t.Logf("saved: %+v", got.Notes)
}
