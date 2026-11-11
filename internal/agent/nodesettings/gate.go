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

// Direct passes funnel stage 1 through (node.Director), telling it which commands are on: its
// own calls don't come back through the gate.
func (g Gate) Direct(ctx context.Context, said string) (map[string]any, bool) {
	d, ok := g.Tools.(node.Director)
	if !ok {
		return nil, false
	}
	return d.Direct(node.WithEnabled(ctx, g.Store.Enabled), said)
}

// AnswerChoice passes an answer to the island's list through (node.ChoiceAnswerer): it runs a
// call the gate already let through.
func (g Gate) AnswerChoice(ctx context.Context, said string) (map[string]any, bool) {
	a, ok := g.Tools.(node.ChoiceAnswerer)
	if !ok {
		return nil, false
	}
	return a.AnswerChoice(ctx, said)
}

// TurnListening passes the turn's start and end through (node.TurnWatcher), with which
// commands are on.
func (g Gate) TurnListening(ctx context.Context, on bool) {
	if w, ok := g.Tools.(node.TurnWatcher); ok {
		w.TurnListening(node.WithEnabled(ctx, g.Store.Enabled), on)
	}
}

// The voice loop finds these by type assertion on the toolset it is given: a Gate that dropped
// them silently sent every "switch to X" and "the second one" to the model.
var (
	_ node.Director       = Gate{}
	_ node.ChoiceAnswerer = Gate{}
	_ node.TurnWatcher    = Gate{}
)
