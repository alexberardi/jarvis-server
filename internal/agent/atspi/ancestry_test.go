//go:build linux

package atspi

import (
	"context"
	"os"
	"testing"
)

func TestContextExcludesProtectedAncestry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		role   uint32
		states []int
	}{
		{"hidden", rolePanel, usable},
		{"editable", rolePanel, append(with(), stateShowingT, 7)}, // ATSPI_STATE_EDITABLE
		{"secret", rolePasswordText, append(with(), stateShowingT)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shown := append(with(), stateShowingT)
			label := tc.name + "_CONTEXT_SENTINEL"
			child := &fakeNode{role: rolePanel, name: label, states: shown, kids: []*fakeNode{
				{role: roleButton, name: "OK", states: shown},
			}}
			ancestor := &fakeNode{role: tc.role, name: "Ancestor", states: tc.states, kids: []*fakeNode{child}}
			main := &fakeNode{role: roleFrame, name: "Window", states: append(with(stateActive), stateShowingT), kids: []*fakeNode{ancestor}}
			fakeDesktop(t, &fakeNode{role: roleApplication, name: "App", kids: []*fakeNode{main}})
			snap, err := Snapshot(context.Background(), os.Getpid(), 150)
			if err != nil {
				t.Fatal(err)
			}
			for _, el := range snap.Elements {
				if el.Context == label {
					t.Fatalf("%s ancestor did not suppress descendant context: %+v", tc.name, el)
				}
			}
		})
	}
}
