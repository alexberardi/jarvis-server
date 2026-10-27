package main

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
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
