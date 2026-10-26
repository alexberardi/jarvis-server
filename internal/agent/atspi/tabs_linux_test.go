package atspi

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
)

// A browser window: two tabs in the strip, and a page whose document holds a tab widget of
// its own, which is the page's, not the window's.
func TestTabsAndSelectTab(t *testing.T) {
	claude := &fakeNode{role: rolePageTab, name: "New session - Claude", actions: []string{"close", "switch"}}
	yt := &fakeNode{role: rolePageTab, name: "YouTube", states: with(stateSelected), actions: []string{"switch"}}
	inPage := &fakeNode{role: rolePageTab, name: "Page widget"}
	win := &fakeNode{role: roleFrame, name: "Firefox", states: with(stateActive), kids: []*fakeNode{
		{role: rolePageTabList, kids: []*fakeNode{claude, yt}},
		{role: roleDocumentWeb, kids: []*fakeNode{{role: rolePageTabList, kids: []*fakeNode{inPage}}}},
	}}
	other := &fakeNode{role: roleFrame, name: "Discord — Firefox", kids: []*fakeNode{
		{role: rolePageTabList, kids: []*fakeNode{{role: rolePageTab, name: "Discord", actions: []string{"switch"}}}},
	}}
	// The inactive window comes first in the tree; its tabs are listed after the active one's.
	fakeDesktop(t, &fakeNode{role: roleApplication, name: "Firefox", kids: []*fakeNode{other, win}})
	ctx := context.Background()

	got, err := Tabs(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if want := []Tab{{0, "New session - Claude", false, "Firefox"}, {1, "YouTube", true, "Firefox"}, {2, "Discord", false, "Discord — Firefox"}}; !slices.Equal(got, want) {
		t.Fatalf("tabs %+v, want %+v (a document's tabs aren't the window's)", got, want)
	}
	if err := SelectTab(ctx, os.Getpid(), 0, "New session - Claude"); err != nil {
		t.Fatal(err)
	}
	if c := claude.called(); !slices.Equal(c, []string{"DoAction 1"}) {
		t.Fatalf("calls %v, want the switch action (1), not close", c)
	}
	var e *Error
	if err := SelectTab(ctx, os.Getpid(), 0, "YouTube"); !errors.As(err, &e) || e.Code != CodeStaleRef {
		t.Fatalf("a name that moved: %v, want stale_ref", err)
	}
}
