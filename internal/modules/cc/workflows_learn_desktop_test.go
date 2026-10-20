package cc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
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
			{Seq: 1, TS: 1760000000000, Kind: "click", App: "org.gnome.TextEditor", X: 1200, Y: 340, Window: notesWin, Context: "none",
				KeyframePoint: &desktopPoint{X: 200, Y: 340}},
			// Typing with no accessibility context is masked: the recorder couldn't tell a password.
			{Seq: 3, TS: 1760000001800, Kind: "input", App: "org.gnome.TextEditor", Masked: true, Window: notesWin, Context: "none"},
			{Seq: 4, TS: 1760000002500, Kind: "key", App: "org.gnome.TextEditor", Key: "Ctrl+s", Window: notesWin, Context: "none"},
			{Seq: 6, TS: 1760000004000, Kind: "input", App: "firefox", Masked: true, Window: bankWin, Context: "none"},
		}},
		{"desktop_macos_trace.json", []desktopEvent{
			{Seq: 1, TS: 1760000000000, Kind: "click", App: "com.apple.Notes", X: 310, Y: 62,
				Element:       &desktopElement{Role: "AXButton", Name: "New Note", AXPath: "AXWindow/AXToolbar/AXButton#new"},
				KeyframePoint: &desktopPoint{X: 310, Y: 62}},
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

// x11Model is what a model answers for the X11 trace: one step per event, and an invented event.
const x11Model = `{"name": "Note the groceries", "triggers": ["note my groceries"],
 "steps": [
  {"intent": "Create a new note", "event_seqs": [1], "target": "New note"},
  {"intent": "Title it Groceries", "event_seqs": [2], "target": "Title"},
  {"intent": "Confirm the title", "event_seqs": [3], "target": "Title"},
  {"intent": "Open the password field's menu", "event_seqs": [4], "target": "Password"},
  {"intent": "Enter the bank password", "event_seqs": [5, 42], "target": "Password"}
 ],
 "params": [],
 "success": {"text_present": ["Groceries"], "url_contains": "nothing"}}`

// waylandModel groups the Wayland trace's click and typing into one step (its typing is
// replayed), and leaves the save out of none.
const waylandModel = `{"name": "Add to the shopping list",
 "steps": [
  {"intent": "Write the item in notes.txt", "event_seqs": [1, 2], "target": "notes.txt"},
  {"intent": "Save it", "event_seqs": [3], "target": "notes.txt"},
  {"intent": "Sign in to the bank", "event_seqs": [4], "target": "the bank"}
 ],
 "success": {"screen": "the note is saved"}}`

// macModel is what a model answers for the macOS trace: one step per event.
const macModel = `{"name": "Note a todo",
 "steps": [
  {"intent": "New note", "event_seqs": [1]},
  {"intent": "Write the todo", "event_seqs": [2]},
  {"intent": "Title it", "event_seqs": [3]},
  {"intent": "Save it", "event_seqs": [4]},
  {"intent": "Sign in to the bank", "event_seqs": [5]}
 ]}`

// TestLearnFromDesktopRecording: a desktop recording is learnt like a browser one, its steps
// targeting the recorded app and window: clicks at the recorded point (a right click's button
// kept), typing (masked, or recorded without accessibility, as ••• and a sensitive input), keys
// as their chord. A trace that isn't one, or has no event, fails for good.
func TestLearnFromDesktopRecording(t *testing.T) {
	ptr := func(i int) *int { return &i }
	notes := &StepWindow{Class: "org.gnome.TextEditor", Title: "notes.txt"}
	bank := &StepWindow{Class: "firefox", Title: "Bank — Mozilla Firefox"}
	ax := func(role, name, path string) *RecordedElement {
		return &RecordedElement{Role: role, Name: name, AXPath: path, Source: "ax"}
	}
	for _, tc := range []struct {
		file, model string
		name        string
		want        []WStep
		success     string
		prompt      []string
	}{
		{"desktop_x11_trace.json", x11Model, "Note the groceries", []WStep{
			{Intent: "Create a new note", Target: "desktop", App: "Notes", Element: ax("button", "New note", ""),
				Action: computeruse.Action{Kind: "click", Intent: "Create a new note", X: ptr(120), Y: ptr(48)}},
			{Intent: "Title it Groceries", Target: "desktop", App: "Notes", Element: ax("textbox", "Title", ""),
				Action: computeruse.Action{Kind: "type", Intent: "Title it Groceries", Text: "Groceries"}},
			{Intent: "Confirm the title", Target: "desktop", App: "Notes", Element: ax("textbox", "Title", ""),
				Action: computeruse.Action{Kind: "keypress", Intent: "Confirm the title", Keys: []string{"enter"}}},
			{Intent: "Open the password field's menu", Target: "desktop", App: "Bank", Element: ax("passwordtext", "Password", ""),
				Action: computeruse.Action{Kind: "click", Intent: "Open the password field's menu", X: ptr(640), Y: ptr(410), Button: "right"}},
			{Intent: "Enter the bank password", Target: "desktop", App: "Bank", Risk: computeruse.CatSensitiveInput,
				Element: ax("passwordtext", "Password", ""),
				Action:  computeruse.Action{Kind: "type", Intent: "Enter the bank password", Text: maskedValue}},
		}, `{"text_present":["Groceries"]}`, []string{
			`1. click button "New note" in Notes at (120,48)`,
			`2. type into textbox "Title" in Notes: "Groceries"`,
			`3. press Enter in textbox "Title" in Notes`,
			`4. right-click passwordtext "Password" in Bank at (640,410)`,
			`5. type into passwordtext "Password" in Bank: •••`,
		}},
		{"desktop_macos_trace.json", macModel, "Note a todo", []WStep{
			{Intent: "New note", Target: "desktop", App: "com.apple.Notes",
				Element: ax("AXButton", "New Note", "AXWindow/AXToolbar/AXButton#new"),
				Action:  computeruse.Action{Kind: "click", Intent: "New note", X: ptr(310), Y: ptr(62)}},
			{Intent: "Write the todo", Target: "desktop", App: "com.apple.Notes",
				Element: ax("AXTextArea", "Note body", "AXWindow/AXScrollArea/AXTextArea"),
				Action:  computeruse.Action{Kind: "type", Intent: "Write the todo", Text: "Call the dentist"}},
			{Intent: "Title it", Target: "desktop", App: "com.apple.Notes",
				Element: ax("AXTextField", "Title", "AXWindow/AXTextField"),
				Action:  computeruse.Action{Kind: "type", Intent: "Title it", Text: "Todo"}},
			{Intent: "Save it", Target: "desktop", App: "com.apple.Notes",
				Action: computeruse.Action{Kind: "keypress", Intent: "Save it", Keys: []string{"cmd", "s"}}},
			{Intent: "Sign in to the bank", Target: "desktop", App: "com.example.bank", Risk: computeruse.CatSensitiveInput,
				Element: ax("AXSecureTextField", "Password", "AXWindow/AXSecureTextField"),
				Action:  computeruse.Action{Kind: "type", Intent: "Sign in to the bank", Text: maskedValue}},
		}, `{}`, []string{
			`1. click AXButton "New Note" in com.apple.Notes at (310,62)`,
		}},
		{"desktop_wayland_trace.json", waylandModel, "Add to the shopping list", []WStep{
			{Intent: "Write the item in notes.txt", Target: "desktop", App: "org.gnome.TextEditor", Window: notes, Risk: computeruse.CatSensitiveInput,
				Action: computeruse.Action{Kind: "type", Intent: "Write the item in notes.txt", Text: maskedValue}},
			{Intent: "Save it", Target: "desktop", App: "org.gnome.TextEditor", Window: notes,
				Action: computeruse.Action{Kind: "keypress", Intent: "Save it", Keys: []string{"ctrl", "s"}}},
			{Intent: "Sign in to the bank", Target: "desktop", App: "firefox", Window: bank, Risk: computeruse.CatSensitiveInput,
				Action: computeruse.Action{Kind: "type", Intent: "Sign in to the bank", Text: maskedValue}},
		}, `{"screen":"the note is saved"}`, []string{
			`1. click in org.gnome.TextEditor — "notes.txt" at (1200,340)`,
			`2. type into org.gnome.TextEditor — "notes.txt": •••`,
			`3. press Ctrl+s in org.gnome.TextEditor — "notes.txt"`,
			`4. type into firefox — "Bank — Mozilla Firefox": •••`,
		}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			le := newLearnEnv(t, tc.model)
			w := le.storeMode("rec_desk_"+strings.TrimSuffix(strings.TrimPrefix(tc.file, "desktop_"), "_trace.json"), "desktop", desktopTrace(t, tc.file), "")
			if w.Name != tc.name || w.OwnerID != 5 || w.Shared {
				t.Fatalf("workflow %+v", w)
			}
			if g, want := mustJSON(t, w.Steps), mustJSON(t, tc.want); g != want {
				t.Fatalf("steps\n got %s\nwant %s", g, want)
			}
			if g := mustJSON(t, w.Success); g != tc.success {
				t.Fatalf("success %s, want %s", g, tc.success)
			}

			le.model.mu.Lock()
			req := le.model.reqs[0]
			le.model.mu.Unlock()
			if sys := *req.Messages[0].Content.Text; !strings.Contains(sys, "on their computer") {
				t.Fatalf("system prompt %q", sys)
			}
			user := *req.Messages[1].Content.Text
			for _, line := range tc.prompt {
				if !strings.Contains(user, line+"\n") {
					t.Fatalf("prompt lacks %q:\n%s", line, user)
				}
			}
			if strings.Contains(mustJSON(t, w.Steps), "hunter2") {
				t.Fatalf("a masked value reached the steps: %s", mustJSON(t, w.Steps))
			}
			if strings.Contains(user, "hunter2") {
				t.Fatalf("a masked value reached the model:\n%s", user)
			}
			if raw := mustJSON(t, le.notify.items); !bytes.Contains([]byte(raw), []byte(learnCardCategory)) {
				t.Fatalf("no learnt card: %s", raw)
			}
		})
	}
}

// TestLearnDesktopParams: typed desktop values seed the candidates as browser typing does, and
// a param replaces its value in the typed text and the intent.
func TestLearnDesktopParams(t *testing.T) {
	evs := []desktopEvent{
		{Kind: "click", App: "Sheets", X: 5, Y: 6},
		{Kind: "input", App: "Sheets", Value: "report 2026-10.csv"},
		{Kind: "key", App: "Sheets", Key: "F13"}, // no such key: dropped
		{Kind: "input", App: "Sheets"},           // cleared: nothing a replay can type
	}
	if !strings.Contains(desktopLearnInstructions, learnSuccessDesktop) || strings.Contains(desktopLearnInstructions, `"download"`) {
		t.Fatalf("the desktop instructions keep the browser's success check:\n%s", desktopLearnInstructions)
	}
	cands := desktopParamCandidates(evs)
	if g := mustJSON(t, cands); !strings.Contains(g, `"Value":"2026-10.csv"`) || !strings.Contains(g, `"Event":2`) {
		t.Fatalf("candidates %s", g)
	}
	out, err := parseLearnOutput(`{"name": "Save the report", "steps": [
		{"intent": "Name it report 2026-10.csv", "event_seqs": [2]},
		{"intent": "Press F13", "event_seqs": [3]},
		{"intent": "Clear it", "event_seqs": [4]}],
		"params": [{"name": "File name", "kind": "file", "example": "2026-10.csv", "from_seqs": [2]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	w, err := buildDesktopWorkflow(Recording{ID: "r", HouseholdID: cbHH, OwnerID: 5}, evs, cands, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Steps) != 1 || w.Steps[0].Action.Text != "report {file_name}" || w.Steps[0].Intent != "Name it report {file_name}" ||
		len(w.Params) != 1 || w.Params[0].Name != "file_name" {
		t.Fatalf("workflow %s", mustJSON(t, w))
	}
}

// TestLearnFromBadDesktopRecording: a trace that isn't a desktop one, or has no event, fails
// for good without a model call.
func TestLearnFromBadDesktopRecording(t *testing.T) {
	le := newLearnEnv(t, x11Model)
	ctx := context.Background()
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
		t.Fatalf("%d model calls for bad desktop recordings", calls)
	}
}
