package main

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// action, callback and toggle_command join the runner's other verbs, with or without a sidecar;
// toggle_command switches the command in the settings store, so it leaves report_tools.
func TestWireActionsAddsTheVerbs(t *testing.T) {
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	r := &node.Runner{Verbs: map[string]func(context.Context, *node.CC, map[string]any){"enroll_voice": nil}}
	wireActions(r, nil, nil, st, nil, slog.New(slog.DiscardHandler))
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

type ownCallback struct{ command string }

func (o ownCallback) Callback(p map[string]any) (map[string]any, bool) {
	if p["command_name"] != o.command {
		return nil, false
	}
	return map[string]any{"success": true, "by": "go"}, true
}

// A Go built-in's callback (the to-do export's Save) is its own; any other goes to the sidecar.
func TestCallbacksRouteToTheirCommand(t *testing.T) {
	r := routedCommands{live: &sidecar.Live{}, own: []callbacker{ownCallback{"export_todo_list"}}}
	out, err := r.Callback(map[string]any{"command_name": "export_todo_list"})
	if err != nil || out["by"] != "go" {
		t.Fatalf("go command = %v, %v", out, err)
	}
	if _, err := r.Callback(map[string]any{"command_name": "comfyui"}); err == nil {
		t.Fatal("a package's callback didn't go to the sidecar (none running: an error)")
	}
}
