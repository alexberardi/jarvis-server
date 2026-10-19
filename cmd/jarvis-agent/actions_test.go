package main

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// action, callback and toggle_command join the runner's other verbs, with or without a sidecar;
// toggle_command switches the command in the settings store, so it leaves report_tools.
func TestWireActionsAddsTheVerbs(t *testing.T) {
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	r := &node.Runner{Verbs: map[string]func(context.Context, *node.CC, map[string]any){"enroll_voice": nil}}
	wireActions(r, nil, st, nil, slog.New(slog.DiscardHandler))
	var verbs []string
	for k := range r.Verbs {
		verbs = append(verbs, k)
	}
	sort.Strings(verbs)
	if !slices.Equal(verbs, []string{"action", "callback", "enroll_voice", "toggle_command"}) {
		t.Fatalf("verbs %v", verbs)
	}
	r.Verbs["toggle_command"](context.Background(), nil, map[string]any{"command_name": "control_device", "enabled": "false"})
	if st.Enabled("control_device") {
		t.Fatal("control_device still enabled")
	}
}
