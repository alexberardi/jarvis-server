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
		pkgs := map[string]nodesettings.Command{}
		for _, t := range tools {
			if sc, ok := t.(*sidecar.Live); ok {
				for _, c := range sc.Commands() {
					pkgs[c.Name] = c
				}
			}
		}
		var out []nodesettings.Command
		for _, d := range tools.Available(ctx) {
			c, ok := pkgs[d.Name]
			if !ok {
				desc, _ := d.Command["description"].(string)
				c = nodesettings.Command{Name: d.Name, Description: desc}
			}
			out = append(out, c)
		}
		return out
	}
}

// dataCommands are the commands whose records the app's "Stored data" tab browses: the
// sidecar's packages (the agent's built-in tools keep no JarvisStorage records).
func dataCommands(tools node.Multi) nodesettings.DataCommands {
	for _, t := range tools {
		if sc, ok := t.(*sidecar.Live); ok {
			return sc
		}
	}
	return nil
}

// wireNodeSettings makes the runner a participant in the K2 settings protocol (DESIGN §16):
// the K2 relay, settings snapshots and config pushes over MQTT nudges, the backstop on every
// connect, and needs_k2 in the heartbeat until a key is stored. It also answers the app's
// command-data browser (§16, "Stored data"). nodeConfig, when set, is the snapshot's node_config
// (the Pi profile's piNodeConfig); nil is a computer's.
func wireNodeSettings(r *node.Runner, st *nodesettings.Store, tools node.Multi, nodeConfig func() map[string]any, log *slog.Logger) {
	h := &nodesettings.Handler{NodeID: r.Creds.NodeID, Store: st, Commands: settingsCommands(tools), NodeConfig: nodeConfig, Log: log,
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
	cd := &nodesettings.CommandData{Store: st, Commands: dataCommands(tools), Log: log}
	r.Requests = map[string]func(context.Context, string, []byte) (string, []byte){nodesettings.TopicCommandData: cd.Handle}
	r.NeedsK2 = func() bool { return !st.HasK2() }
}
