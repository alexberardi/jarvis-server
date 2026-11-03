package dictate

import (
	"context"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Tonight's transcripts (2026-10-25) and what stage 1 makes of them.
func TestParseType(t *testing.T) {
	for said, want := range map[string]typeRequest{
		`Type "How are you doing today?" in the message.`:      {text: "How are you doing today?", place: "message"},
		"Type in the cloud message.  How are you doing today?": {text: "How are you doing today?", place: "cloud message"},
		"type hello world":                                   {text: "hello world"},
		"Type I'm in the car.":                               {text: "I'm in the car"},
		"type git status in the terminal and press enter":    {text: "git status in the terminal", submit: true},
		`Type "git status" in the terminal and press enter.`: {text: "git status", place: "terminal", submit: true},
		"type see you at five in the search box":             {text: "see you at five", place: "search box"},
	} {
		got, ok := parseType(said)
		if !ok || got != want {
			t.Errorf("%q: %+v %v, want %+v", said, got, ok, want)
		}
	}
	for _, said := range []string{"write my resume", "what should I type", "typewriter repair near me"} {
		if r, ok := parseType(said); ok {
			t.Errorf("%q parsed as %+v", said, r)
		}
	}
}

func TestDirectTypesIntoTheWindowInFrontAndAsksBeforeSending(t *testing.T) {
	kb := &fakeKB{ok: true}
	win := newWin()
	ts := &Tools{KB: kb, Win: win, Sleep: noSleep, Confirm: &Confirmer{Mode: ConfirmSubmit}}
	ctx := context.Background()

	// "in the message" is the page's message box: typed where the caret is, not into an app.
	out, ok := ts.Direct(ctx, `Type "How are you doing today?" in the message.`)
	if !ok || out["success"] != true || !slices.Equal(kb.typed, []string{"How are you doing today?"}) || len(kb.keys) != 0 {
		t.Fatalf("type: %v %v typed %q keys %v", out, ok, kb.typed, kb.keys)
	}
	// Sending asks first (confirm submit): a choice on the island, answered by voice.
	out, ok = ts.Direct(ctx, "press enter")
	if ok && out[node.ChoiceKey] == true {
		t.Fatalf("a key press asked: %v", out) // a key press is the user's answer: only "all" asks
	}
	kb.typed, kb.keys = nil, nil
	out, ok = ts.Direct(ctx, `Type "ship it" in the chat box and send it.`)
	if !ok || out["status"] != "awaiting_confirmation" || out[node.ChoiceKey] != true || len(kb.typed) != 0 {
		t.Fatalf("send: %v %v typed %q", out, ok, kb.typed)
	}
	if out, ok := ts.AnswerChoice(ctx, "yes, send it"); !ok || out["success"] != true || !slices.Equal(kb.typed, []string{"ship it"}) || len(kb.keys) != 1 {
		t.Fatalf("answer: %v %v typed %q keys %v", out, ok, kb.typed, kb.keys)
	}
	// An app that isn't open goes on to the model, which can open it.
	if _, ok := ts.Direct(ctx, `Type "hi" in Slack.`); ok {
		t.Fatal("typing into a closed app handled locally")
	}
}
