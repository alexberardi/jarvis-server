package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// settingsCommands lists each tool's parameters from its available_commands entry, so a Go
// built-in's options_source (control_device's device picker) reaches the app.
func TestSettingsCommandsCarryParameters(t *testing.T) {
	spec := node.Spec{Name: "control_device", Params: []node.Param{
		{Name: "device_name", Type: "string", Required: true, OptionsSource: "devices"},
		{Name: "action", Type: "string", Enum: []string{"turn_on"}},
	}}
	tools := node.Multi{staticTools{spec.Render()}}
	cmds := settingsCommands(tools)(context.Background())
	if len(cmds) != 1 || len(cmds[0].Parameters) != 2 {
		t.Fatalf("commands = %+v", cmds)
	}
	p := cmds[0].Parameters[0].(map[string]any)
	if p["name"] != "device_name" || p["options_source"] != "devices" || p["required"] != true {
		t.Fatalf("device_name = %v", p)
	}
	if a := cmds[0].Parameters[1].(map[string]any); a["options_source"] != nil {
		t.Fatalf("action options_source = %v", a["options_source"])
	}
}

type staticTools []node.ToolDef

func (s staticTools) Available(context.Context) []node.ToolDef { return s }
func (s staticTools) Call(context.Context, string, map[string]any, node.Request) (map[string]any, bool) {
	return nil, false
}

// A toolset's fast-path patterns (the routine built-in's) go on its command in the snapshot.
func TestSettingsCommandsCarryFastPaths(t *testing.T) {
	tools := node.Multi{fastPaths{staticTools{node.Spec{Name: "routine"}.Render(), node.Spec{Name: "tell_joke"}.Render()}}}
	cmds := settingsCommands(tools)(context.Background())
	if len(cmds) != 2 || len(cmds[0].FastPaths) != 1 || cmds[0].FastPaths[0].ID != "routine.good_night" || len(cmds[1].FastPaths) != 0 {
		t.Fatalf("commands = %+v", cmds)
	}
}

type fastPaths struct{ staticTools }

func (fastPaths) FastPaths(command string) []nodesettings.FastPath {
	if command != "routine" {
		return nil
	}
	return []nodesettings.FastPath{{ID: "routine.good_night", Example: "good night"}}
}

// A desktop runs wireNodeSettings without the Pi's tasks: it still answers call plans' context
// requests (a desktop's calendar availability never reached a plan before).
func TestDesktopAnswersContextRequests(t *testing.T) {
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	r := &node.Runner{Creds: node.Creds{NodeID: "n1"}}
	wireNodeSettings(r, st, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := r.Requests[nodesettings.TopicContext]
	if h == nil {
		t.Fatal("no context/+ request handler")
	}
	if topic, body := h(context.Background(), "jarvis/nodes/n1/context/operations", []byte(`{"correlation_id":"c1"}`)); topic != "jarvis/nodes/n1/context/operations/response/c1" || string(body) != `{"ok":true,"providers":[]}` {
		t.Fatalf("context = %q %s", topic, body)
	}
}
