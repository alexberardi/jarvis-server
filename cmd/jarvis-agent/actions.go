package main

import (
	"context"
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/actions"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// wireActions answers jarvisd's action, callback and toggle_command verbs over the sidecar's
// commands and device families (internal/agent/actions): the app's device control and a
// command's buttons, an inbox item's callback, and the household turning a command off. Every
// profile gets them: a computer's packages have buttons and callbacks too, and with no device
// families its control_device answers "not found" at once instead of timing out. say speaks a
// voice-originated action's message (nil: nothing is spoken). A Go built-in's callbacks (the
// to-do export's Save) are its own: tools' callbackers answer theirs before the sidecar.
func wireActions(r *node.Runner, live *sidecar.Live, tools node.Multi, st *nodesettings.Store, say func(context.Context, string) error, log *slog.Logger) {
	if live == nil {
		live = &sidecar.Live{}
	}
	cmds := routedCommands{live: live}
	for _, t := range tools {
		if c, ok := t.(callbacker); ok {
			cmds.own = append(cmds.own, c)
		}
	}
	h := &actions.Handler{Commands: cmds, Enabled: st.Enabled, SetEnabled: st.SetEnabled, Say: say, Log: log}
	verbs := map[string]func(context.Context, *node.CC, map[string]any){}
	for verb, f := range h.Verbs() {
		verbs[verb] = func(ctx context.Context, cc *node.CC, det map[string]any) { f(ctx, cc, det) }
	}
	addVerbs(r, verbs)
}

// callbacker is a Go built-in with @callbacks of its own; handled is false for another
// command's.
type callbacker interface {
	Callback(params map[string]any) (out map[string]any, handled bool)
}

// routedCommands are the sidecar's commands and families, with the Go built-ins' callbacks.
type routedCommands struct {
	live *sidecar.Live
	own  []callbacker
}

func (r routedCommands) Action(params map[string]any) (map[string]any, error) {
	return r.live.Action(params)
}

func (r routedCommands) Callback(params map[string]any) (map[string]any, error) {
	for _, c := range r.own {
		if out, ok := c.Callback(params); ok {
			return out, nil
		}
	}
	return r.live.Callback(params)
}
