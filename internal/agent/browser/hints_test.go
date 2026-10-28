package browser

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestSpokenNumber(t *testing.T) {
	for in, want := range map[string]int{"7": 7, "7.": 7, "seven": 7, "Twenty three": 23, "twenty-three": 23,
		"one hundred and five": 105, "12!": 12} {
		if n, ok := spokenNumber(in); !ok || n != want {
			t.Errorf("%q: %d %v, want %d", in, n, ok, want)
		}
	}
	for _, in := range []string{"", "0", "the first one", "seven dogs"} {
		if _, ok := spokenNumber(in); ok {
			t.Errorf("%q read as a number", in)
		}
	}
}

// hintsPage is a fake extension's search results page: hints numbers a link and a Send button.
func hintsPage(m Message) Message {
	switch m.Cmd {
	case "hints":
		if m.Args["show"] == false {
			return ok(map[string]any{"tab": map[string]any{"tab_id": 4.0}})
		}
		return ok(map[string]any{"tab": map[string]any{"tab_id": 4.0}, "gen": 9.0, "url": "https://www.google.com/search?q=dogs",
			"elements": []any{map[string]any{"ref": "e1", "name": "Dog - Wikipedia", "role": "link"},
				map[string]any{"ref": "e2", "name": "Send", "role": "button"}}})
	case "act":
		return ok(map[string]any{"ok": true})
	}
	return ok(map[string]any{})
}

func TestShowNumbersThenSayOne(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	ctx := context.Background()
	if _, ok := ts.Direct(ctx, "seven"); ok {
		t.Fatal("a bare number taken with no browser and no numbers")
	}
	f := connectExt(t, h, path, "Firefox", true, hintsPage)
	if _, ok := ts.Direct(ctx, "7"); ok {
		t.Fatal("a bare number taken with no numbers on screen")
	}
	out, ok := ts.Direct(ctx, "Show numbers.")
	if !ok || out["success"] != true || out[node.ChoiceKey] != true || out["count"] != 2 {
		t.Fatalf("show: %v %v", out, ok)
	}
	if out, ok := ts.AnswerChoice(ctx, "Nine."); !ok || out["success"] != false || out[node.ChoiceKey] != true {
		t.Fatalf("a number not on the page: %v %v", out, ok)
	}
	out, ok = ts.AnswerChoice(ctx, "One.")
	if !ok || out["success"] != true || out["clicked"] != "Dog - Wikipedia" {
		t.Fatalf("one: %v %v", out, ok)
	}
	a := lastArgs(t, f, "act")
	if a["ref"] != "e1" || a["gen"] != 9.0 || a["op"] != "press" || a["tab_id"] != 4.0 {
		t.Fatalf("act %v", a)
	}
	if lastArgs(t, f, "hints")["show"] != false {
		t.Fatal("the numbers stayed up after the click")
	}
	if _, ok := ts.Direct(ctx, "2"); ok {
		t.Fatal("a used-up number still taken")
	}
	// "click 2" after "show links", in a later turn; and "hide numbers".
	ts.Direct(ctx, "show links")
	if out, ok := ts.Direct(ctx, "hide numbers"); !ok || out["success"] != true {
		t.Fatalf("hide: %v", out)
	}
	if _, ok := ts.Direct(ctx, "click 2"); ok {
		t.Fatal("click 2 with the numbers hidden")
	}
}

// A numbered "Send" asks first, as browser_click does, and the spoken yes presses it.
func TestRiskyNumberAsksFirst(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	ts.Confirm = &dictate.Confirmer{Mode: dictate.ConfirmSubmit}
	f := connectExt(t, h, path, "Firefox", true, hintsPage)
	ctx := context.Background()
	ts.Direct(ctx, "show numbers")
	out, ok := ts.Direct(ctx, "click number two")
	if !ok || out["status"] != "awaiting_confirmation" || out[node.ChoiceKey] != true {
		t.Fatalf("send: %v %v", out, ok)
	}
	for _, m := range f.received() {
		if m.Cmd == "act" {
			t.Fatal("pressed before the yes")
		}
	}
	if out, ok := ts.AnswerChoice(ctx, "yes"); !ok || out["success"] != true {
		t.Fatalf("yes: %v %v", out, ok)
	}
	if lastArgs(t, f, "act")["ref"] != "e2" {
		t.Fatal("the yes didn't press Send")
	}
}

func TestNumbersOffWhenClickIsTurnedOff(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	connectExt(t, h, path, "Firefox", true, hintsPage)
	off := node.WithEnabled(context.Background(), func(n string) bool { return n != ToolClick })
	if _, ok := ts.Direct(off, "show numbers"); ok {
		t.Fatal("numbers shown while clicking is turned off")
	}
}

// Numbers without "show numbers" (user, 2026-10-27): up when a turn starts listening with the
// browser in front, redrawn after a step changes the page, down when the turn ends.
func TestNumbersFollowTheTurn(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	f := connectExt(t, h, path, "Firefox", true, hintsPage)
	ctx := context.Background()
	shows := func() (n int) {
		for _, m := range f.received() {
			if m.Cmd == "hints" && m.Args["show"] == true {
				n++
			}
		}
		return n
	}
	ts.TurnListening(ctx, true)
	if shows() != 1 {
		t.Fatal("no numbers when the turn started")
	}
	if out, ok := ts.Direct(ctx, "1"); !ok || out["success"] != true {
		t.Fatalf("a bare number with numbers up: %v %v", out, ok)
	}
	waitFor(t, func() bool { return shows() == 2 }) // redrawn after the click
	if a := lastArgs(t, f, "hints"); a["show"] != true || a["wait"] != true {
		t.Fatalf("redraw %v", a)
	}
	ts.Direct(ctx, "scroll down")
	waitFor(t, func() bool { return shows() == 3 })
	ts.TurnListening(ctx, false)
	if lastArgs(t, f, "hints")["show"] != false {
		t.Fatal("numbers left up after the turn")
	}
	ts.Direct(ctx, "scroll down") // no turn: no redraw
	if shows() != 3 {
		t.Fatal("numbers drawn outside a turn")
	}
}

func TestNoNumbersWhenTheBrowserIsBehind(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	f := connectExt(t, h, path, "Firefox", false, hintsPage)
	ts.TurnListening(context.Background(), true)
	for _, m := range f.received() {
		if m.Cmd == "hints" {
			t.Fatal("numbers drawn on a browser that isn't in front")
		}
	}
}

// "type ABC into 29" fills that number on the node; "fill out 33 with a summary of me" is the
// model's (it writes the text), through browser_fill's number.
func TestTypeIntoANumber(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	f := connectExt(t, h, path, "Firefox", true, hintsPage)
	ctx := context.Background()
	ts.Direct(ctx, "show numbers")
	out, ok := ts.Direct(ctx, `Type "ABC, 123" into 1.`)
	if !ok || out["success"] != true || out["filled"] != "Dog - Wikipedia" {
		t.Fatalf("type into: %v %v", out, ok)
	}
	if a := lastArgs(t, f, "act"); a["op"] != "set_value" || a["value"] != "ABC, 123" || a["ref"] != "e1" {
		t.Fatalf("act %v", a)
	}
	if out, ok := ts.Direct(ctx, "type hello into number nine"); !ok || out["success"] != false {
		t.Fatalf("a number not on the page: %v %v", out, ok)
	}
	// The model's call: no island markers, a plain result.
	res, handled := ts.Call(ctx, ToolFill, map[string]any{"number": 1.0, "text": "A short bio."}, node.Request{})
	if !handled || res["success"] != true || res[node.IslandOnlyKey] != nil || lastArgs(t, f, "act")["value"] != "A short bio." {
		t.Fatalf("browser_fill number: %v", res)
	}
	res, _ = ts.Call(ctx, ToolClick, map[string]any{"number": 7.0}, node.Request{})
	if res["success"] != false || res["error_code"] != "no_such_number" {
		t.Fatalf("browser_click unknown number: %v", res)
	}
}
