package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// settingsCommands is what the settings snapshot lists: every tool this node has right now,
// enabled or not, with the sidecar's declared secrets (built-in tools declare none).
func settingsCommands(tools node.Multi) func(ctx context.Context) []nodesettings.Command {
	return func(ctx context.Context) []nodesettings.Command {
		secrets := map[string][]nodesettings.SecretSpec{}
		for _, t := range tools {
			if sc, ok := t.(*sidecar.Sidecar); ok {
				for _, c := range sc.Commands() {
					secrets[c.Name] = c.Secrets
				}
			}
		}
		var out []nodesettings.Command
		for _, d := range tools.Available(ctx) {
			desc, _ := d.Command["description"].(string)
			out = append(out, nodesettings.Command{Name: d.Name, Description: desc, Secrets: secrets[d.Name]})
		}
		return out
	}
}

// wireNodeSettings makes the runner a participant in the K2 settings protocol (DESIGN §16):
// the K2 relay, settings snapshots and config pushes over MQTT nudges, the backstop on every
// connect, and needs_k2 in the heartbeat until a key is stored.
func wireNodeSettings(r *node.Runner, st *nodesettings.Store, tools node.Multi, log *slog.Logger) {
	h := &nodesettings.Handler{NodeID: r.Creds.NodeID, Store: st, Commands: settingsCommands(tools), Log: log,
		KeyChanged: r.BeatNow} // the app sees needs_k2: false within seconds
	var once sync.Once
	bind := func(cc *node.CC) *nodesettings.Handler {
		once.Do(func() { h.API = cc }) // one command-center client per run
		return h
	}
	r.Topics = map[string]func(context.Context, *node.CC, []byte){}
	for _, sub := range []string{nodesettings.TopicK2Provision, nodesettings.TopicSettingsRequest, nodesettings.TopicConfigPush} {
		r.Topics[sub] = func(ctx context.Context, cc *node.CC, payload []byte) { bind(cc).Handle(ctx, sub, payload) }
	}
	r.OnMQTTConnect = func(ctx context.Context, cc *node.CC) { bind(cc).Backstop(ctx) }
	r.NeedsK2 = func() bool { return !st.HasK2() }
}
