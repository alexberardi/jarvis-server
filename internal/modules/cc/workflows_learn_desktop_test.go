package cc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func desktopTrace(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseDesktopTrace: the desktop traces each platform's recorder writes (X11: AT-SPI
// elements, no window; Wayland without AT-SPI: the compositor's window, no element; macOS: AX
// elements with their paths) parse to their clicks, typing and keys.
func TestParseDesktopTrace(t *testing.T) {
	notesWin := &desktopWindow{Class: "org.gnome.TextEditor", Title: "notes.txt", X: 1000, W: 800, H: 600}
	bankWin := &desktopWindow{Class: "firefox", Title: "Bank — Mozilla Firefox", W: 1000, H: 1000}
	for _, tc := range []struct {
		file string
		want []desktopEvent
	}{
		{"desktop_x11_trace.json", []desktopEvent{
			{Seq: 1, TS: 1760000000000, Kind: "click", App: "Notes", Element: &desktopElement{Role: "button", Name: "New note"}, X: 120, Y: 48},
			{Seq: 3, TS: 1760000001500, Kind: "input", App: "Notes", Element: &desktopElement{Role: "textbox", Name: "Title"}, Value: "Groceries"},
			{Seq: 4, TS: 1760000002000, Kind: "key", App: "Notes", Element: &desktopElement{Role: "textbox", Name: "Title"}, Key: "Enter"},
			{Seq: 6, TS: 1760000004000, Kind: "click", App: "Bank", Element: &desktopElement{Role: "passwordtext", Name: "Password"}, X: 640, Y: 410, Button: "right"},
			{Seq: 7, TS: 1760000005000, Kind: "input", App: "Bank", Element: &desktopElement{Role: "passwordtext", Name: "Password"}, Masked: true},
		}},
		{"desktop_wayland_trace.json", []desktopEvent{
			{Seq: 1, TS: 1760000000000, Kind: "click", App: "org.gnome.TextEditor", X: 1200, Y: 340, Window: notesWin, Context: "none"},
			// Typing with no accessibility context is masked: the recorder couldn't tell a password.
			{Seq: 3, TS: 1760000001800, Kind: "input", App: "org.gnome.TextEditor", Masked: true, Window: notesWin, Context: "none"},
			{Seq: 4, TS: 1760000002500, Kind: "key", App: "org.gnome.TextEditor", Key: "Ctrl+s", Window: notesWin, Context: "none"},
			{Seq: 6, TS: 1760000004000, Kind: "input", App: "firefox", Masked: true, Window: bankWin, Context: "none"},
		}},
		{"desktop_macos_trace.json", []desktopEvent{
			{Seq: 1, TS: 1760000000000, Kind: "click", App: "com.apple.Notes", X: 310, Y: 62,
				Element: &desktopElement{Role: "AXButton", Name: "New Note", AXPath: "AXWindow/AXToolbar/AXButton#new"}},
			{Seq: 2, TS: 1760000001000, Kind: "input", App: "com.apple.Notes", Value: "Call the dentist",
				Element: &desktopElement{Role: "AXTextArea", Name: "Note body", AXPath: "AXWindow/AXScrollArea/AXTextArea"}},
			{Seq: 3, TS: 1760000002000, Kind: "input", App: "com.apple.Notes", Value: "Todo",
				Element: &desktopElement{Role: "AXTextField", Name: "Title", AXPath: "AXWindow/AXTextField"}},
			{Seq: 4, TS: 1760000003000, Kind: "key", App: "com.apple.Notes", Key: "Cmd+s"},
			{Seq: 5, TS: 1760000004000, Kind: "input", App: "com.example.bank", Masked: true,
				Element: &desktopElement{Role: "AXSecureTextField", Name: "Password", AXPath: "AXWindow/AXSecureTextField"}},
		}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			got, err := parseDesktopTrace(desktopTrace(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if g, w := mustJSON(t, got), mustJSON(t, tc.want); g != w {
				t.Fatalf("events\n got %s\nwant %s", g, w)
			}
		})
	}
}

func TestParseDesktopTraceLimits(t *testing.T) {
	if _, err := parseDesktopTrace(json.RawMessage(`{"kind":"click"}`)); err == nil {
		t.Fatal("an object parsed as a trace")
	}
	var evs []desktopEvent
	for i := range learnMaxEvents + 10 {
		evs = append(evs, desktopEvent{Seq: int64(i + 1), Kind: "click", X: i})
	}
	got, err := parseDesktopTrace(json.RawMessage(mustJSON(t, evs)))
	if err != nil || len(got) != learnMaxEvents {
		t.Fatalf("%d events, err %v", len(got), err)
	}
	// Typing into two windows of one app is two events; the same window's typing is one.
	two := `[{"kind":"input","app":"a","value":"x","window":{"class":"a","title":"one"}},
		{"kind":"input","app":"a","value":"y","window":{"class":"a","title":"two"}},
		{"kind":"input","app":"a","value":"yz","window":{"class":"a","title":"two"}}]`
	got, err = parseDesktopTrace(json.RawMessage(two))
	if err != nil || len(got) != 2 || got[1].Value != "yz" {
		t.Fatalf("events %+v, err %v", got, err)
	}
}

// TestLearnFromDesktopRecording: a desktop recording is parsed, not sent to the model and no
// workflow is stored from it yet; a trace that isn't one fails for good.
func TestLearnFromDesktopRecording(t *testing.T) {
	le := newLearnEnv(t, bankModel)
	ctx := context.Background()
	for _, name := range []string{"desktop_x11_trace.json", "desktop_wayland_trace.json", "desktop_macos_trace.json"} {
		id := "rec_desk_" + name[len("desktop_"):len(name)-len("_trace.json")]
		if _, err := le.m.createRecording(ctx, Recording{ID: id, HouseholdID: cbHH, OwnerID: 5, NodeID: "node-mac",
			Mode: "desktop", Trace: desktopTrace(t, name)}); err != nil {
			t.Fatal(err)
		}
		if err := le.m.learnFromRecording(ctx, id); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for id, trace := range map[string]string{"rec_desk_bad": `{"kind":"click"}`, "rec_desk_empty": `[{"kind":"scroll","dy":3}]`} {
		if _, err := le.m.createRecording(ctx, Recording{ID: id, HouseholdID: cbHH, OwnerID: 5, NodeID: "node-mac",
			Mode: "desktop", Trace: json.RawMessage(trace)}); err != nil {
			t.Fatal(err)
		}
		err := le.m.learnFromRecording(ctx, id)
		if err == nil || fmt.Sprintf("%T", err) != "queue.permanent" {
			t.Fatalf("%s: %T %v, want a permanent error", id, err, err)
		}
	}

	le.model.mu.Lock()
	calls := len(le.model.reqs)
	le.model.mu.Unlock()
	if calls != 0 {
		t.Fatalf("%d model calls for desktop recordings", calls)
	}
	if ws, err := le.m.listWorkflows(ctx, cbHH, 5); err != nil || len(ws) != 0 {
		t.Fatalf("workflows %+v, err %v", ws, err)
	}
	if raw := mustJSON(t, le.notify.items); bytes.Contains([]byte(raw), []byte(learnCardCategory)) {
		t.Fatalf("a learnt card for a desktop recording: %s", raw)
	}
}
