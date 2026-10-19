package computer

import (
	"context"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// SwitchTools is the switch's node tool, computer_switch {on}: jarvisd's
// enable_computer_control / disable_computer_control (voice, from this computer or another
// device) call it. It is the menu's own path (Set), so agent.json, the tray and the heartbeat
// follow, and off stops a task going on. The model never sees it (computeruse.IsServerOnlyTool).
type SwitchTools struct{ S *Switch }

var switchSpec = node.Spec{Name: computeruse.ToolSwitch, Description: `Turn "Computer control" on or off on this computer.`,
	Params: []node.Param{{Name: "on", Type: "bool", Required: true}}}

func (t *SwitchTools) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{switchSpec.Render()}
}

func (t *SwitchTools) Call(_ context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != computeruse.ToolSwitch {
		return nil, false
	}
	on, ok := args["on"].(bool)
	if !ok {
		return node.Fail("on: want true or false"), true
	}
	out := map[string]any{"success": true}
	if err := t.S.Set(on); err != nil {
		out["warning"] = "couldn't save the choice; it lasts until the agent restarts: " + err.Error()
	}
	out["computer_control"] = map[bool]string{true: "on", false: "off"}[t.S.On()]
	return out, true
}
