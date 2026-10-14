package nodesettings

import (
	"context"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Gate offers only the commands the household hasn't turned off (the command_registry config
// push): a disabled command is left out of report_tools and voice turns, and a call to one is
// refused, as the Pi's command discovery skips disabled commands.
type Gate struct {
	Tools node.Toolset
	Store *Store
}

func (g Gate) Available(ctx context.Context) []node.ToolDef {
	var out []node.ToolDef
	for _, t := range g.Tools.Available(ctx) {
		if g.Store.Enabled(t.Name) {
			out = append(out, t)
		}
	}
	return out
}

func (g Gate) Call(ctx context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if !g.Store.Enabled(name) {
		return node.Fail("%s is turned off in this computer's settings", name), true
	}
	return g.Tools.Call(ctx, name, args, req)
}
