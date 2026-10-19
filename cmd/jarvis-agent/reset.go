package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/packages"
	"github.com/alexberardi/jarvis-server/internal/agent/reset"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// rebootPi restarts the Pi (install-pi.sh's sudoers allows it). A var: the tests replace it.
var rebootPi = func() error {
	if out, err := exec.Command("sudo", "-n", "systemctl", "reboot").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// wirePiTasks answers jarvisd's factory reset (wiping the state dir, dir), keeps the household's
// routines in the routine command's data (on routines/sync and on every connect), and answers
// the planners' context requests from the sidecar's commands: the Pi profile's share of the
// node topics (go-pi-node.md §1.2). A confirmed reset seals st and tm (the built-ins' timers,
// nil for none) before the wipe, so neither writes its file back before the reboot. quit ends
// the run when a reset can't reboot. Call it after
// wireNodeSettings and wirePackages (it adds to their Topics, Requests and OnMQTTConnect).
func wirePiTasks(r *node.Runner, st *nodesettings.Store, dir string, tools node.Multi, pm *packages.Manager, tm *timers.Service, quit func(), log *slog.Logger) {
	if r.Topics == nil {
		r.Topics = map[string]func(context.Context, *node.CC, []byte){}
	}
	if r.Requests == nil {
		r.Requests = map[string]func(context.Context, string, []byte) (string, []byte){}
	}
	fr := &reset.Handler{NodeID: r.Creds.NodeID, Log: log, Reboot: func() error { return rebootPi() }, Exit: quit,
		Wipe: func(context.Context) ([]string, error) { return reset.Wipe(dir) },
		Freeze: func() {
			st.Seal()
			if tm != nil {
				tm.Seal()
			}
		}}
	r.Topics[reset.Topic] = func(ctx context.Context, cc *node.CC, payload []byte) { fr.Handle(ctx, cc, payload) }

	rt := &nodesettings.Routines{NodeID: r.Creds.NodeID, Store: st, Protected: pantryRoutines(pm), Log: log}
	r.Topics[nodesettings.TopicRoutinesSync] = func(ctx context.Context, cc *node.CC, payload []byte) { rt.Handle(ctx, cc, payload) }
	backstop := r.OnMQTTConnect
	r.OnMQTTConnect = func(ctx context.Context, cc *node.CC) {
		if backstop != nil {
			backstop(ctx, cc)
		}
		_, _ = rt.Pull(ctx, cc) // N: pulls at boot; a reconnect also catches a nudge missed meanwhile
	}

	cx := &nodesettings.Context{Store: st, Log: log}
	if sc := liveSidecar(tools); sc != nil {
		cx.Providers = sc
	}
	r.Requests[nodesettings.TopicContext] = cx.Handle
}

// liveSidecar is the toolset's sidecar (the built-in commands and the Pantry packages), or nil.
func liveSidecar(tools node.Multi) *sidecar.Live {
	for _, t := range tools {
		if sc, ok := t.(*sidecar.Live); ok {
			return sc
		}
	}
	return nil
}

// pantryRoutines are the routines the installed packages ship (their routine components): a
// routines pull never removes them, as N: never prunes its custom_routines.
func pantryRoutines(pm *packages.Manager) func() []string {
	return func() []string {
		if pm == nil {
			return nil
		}
		var out []string
		for _, in := range pm.List() {
			for _, c := range in.Manifest.Components {
				if c.Type == "routine" {
					out = append(out, c.Name)
				}
			}
		}
		return out
	}
}
