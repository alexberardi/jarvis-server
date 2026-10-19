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
// voice-originated action's message (nil: nothing is spoken).
func wireActions(r *node.Runner, live *sidecar.Live, st *nodesettings.Store, say func(context.Context, string) error, log *slog.Logger) {
	if live == nil {
		live = &sidecar.Live{}
	}
	h := &actions.Handler{Commands: live, Enabled: st.Enabled, SetEnabled: st.SetEnabled, Say: say, Log: log}
	verbs := map[string]func(context.Context, *node.CC, map[string]any){}
	for verb, f := range h.Verbs() {
		verbs[verb] = func(ctx context.Context, cc *node.CC, det map[string]any) { f(ctx, cc, det) }
	}
	addVerbs(r, verbs)
}
