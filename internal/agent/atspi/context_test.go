//go:build linux

package atspi

import (
	"context"
	"os"
	"testing"
)

func TestHiddenContainerDoesNotBecomeContext(t *testing.T) {
	// SHOWING=25 and VISIBLE=30 are both absent from the closed panel and its child.
	button := &fakeNode{role: roleButton, name: "OK", states: usable}
	hidden := &fakeNode{role: rolePanel, name: "HIDDEN_PANEL_SENTINEL", states: usable, kids: []*fakeNode{button}}
	main := &fakeNode{role: roleFrame, name: "Window", states: with(stateActive), kids: []*fakeNode{hidden}}
	fakeDesktop(t, &fakeNode{role: roleApplication, name: "App", kids: []*fakeNode{main}})
	snap, err := Snapshot(context.Background(), os.Getpid(), 150)
	if err != nil {
		t.Fatal(err)
	}
	for _, el := range snap.Elements {
		if el.Context == hidden.name {
			t.Fatalf("hidden subtree supplied context: %+v", el)
		}
	}
}

// F6: a voice (showing) snapshot never reads a field's text; a computer snapshot keeps it.
func TestShowingSnapshotCarriesNoValues(t *testing.T) {
	shown := append(with(), stateShowingT)
	field := &fakeNode{role: roleEntry, name: "Font Size", states: shown, text: "12 pt", box: [4]int32{10, 10, 60, 20}}
	main := &fakeNode{role: roleFrame, name: "Window", states: append(with(stateActive), stateShowingT), box: [4]int32{0, 0, 800, 600}, kids: []*fakeNode{field}}
	fakeDesktop(t, &fakeNode{role: roleApplication, name: "App", kids: []*fakeNode{main}})
	voice, err := SnapshotShowing(context.Background(), os.Getpid(), 0)
	if err != nil || len(voice.Elements) != 1 || voice.Elements[0].Value != "" {
		t.Fatalf("voice snapshot: %+v %v", voice.Elements, err)
	}
	full, err := Snapshot(context.Background(), os.Getpid(), 0)
	if err != nil || len(full.Elements) != 1 || full.Elements[0].Value != "12 pt" {
		t.Fatalf("computer snapshot lost its value: %+v %v", full.Elements, err)
	}
}
