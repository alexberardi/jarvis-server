package main

import (
	"context"
	"encoding/json"
	"fmt"
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
			c.Parameters, _ = d.Command["parameters"].([]any)
			for _, t := range tools {
				if f, ok := t.(fastPather); ok {
					c.FastPaths = append(c.FastPaths, f.FastPaths(d.Name)...)
				}
				if s, ok := t.(secretsDeclarer); ok && !fromSidecar(pkgs, d.Name) {
					c.Secrets = append(c.Secrets, s.Secrets(d.Name)...)
				}
			}
			out = append(out, c)
		}
		return out
	}
}

// secretsDeclarer is a Go built-in with settings of its own (the shopping export's store).
type secretsDeclarer interface {
	Secrets(command string) []nodesettings.SecretSpec
}

// fromSidecar reports whether the sidecar has the command (its secrets came with it).
func fromSidecar(pkgs map[string]nodesettings.Command, name string) bool {
	_, ok := pkgs[name]
	return ok
}

// fastPather is a toolset with fast-path patterns for some of its commands (the routine
// built-in's trigger phrases).
type fastPather interface {
	FastPaths(command string) []nodesettings.FastPath
}

// dataCommands are the commands whose records the app's "Stored data" tab browses: the
// sidecar's packages and the Go built-ins that keep records (the shopping list), in the
// toolset's order. One source is returned as is.
func dataCommands(tools node.Multi) nodesettings.DataCommands {
	var all mergedData
	for _, t := range tools {
		if d, ok := t.(nodesettings.DataCommands); ok {
			all = append(all, d)
		}
	}
	switch len(all) {
	case 0:
		return nil
	case 1:
		return all[0]
	}
	return all
}

// mergedData is several sources of browsable commands, each command answered by the first
// that lists it.
type mergedData []nodesettings.DataCommands

func (m mergedData) DataCommands() []nodesettings.DataCommand {
	var out []nodesettings.DataCommand
	seen := map[string]bool{}
	for _, d := range m {
		for _, c := range d.DataCommands() {
			if !seen[c.Name] {
				seen[c.Name] = true
				out = append(out, c)
			}
		}
	}
	return out
}

func (m mergedData) owner(command string) nodesettings.DataCommands {
	for _, d := range m {
		for _, c := range d.DataCommands() {
			if c.Name == command {
				return d
			}
		}
	}
	return nil
}

func (m mergedData) Summaries(ctx context.Context, command string, records []nodesettings.Object) ([]map[string]any, error) {
	if d := m.owner(command); d != nil {
		return d.Summaries(ctx, command, records)
	}
	return nil, fmt.Errorf("no command %q", command)
}

func (m mergedData) Create(ctx context.Context, command string, values nodesettings.Object, uid json.Number) (string, json.RawMessage, error) {
	if d := m.owner(command); d != nil {
		return d.Create(ctx, command, values, uid)
	}
	return "", nil, fmt.Errorf("no command %q", command)
}

func (m mergedData) UpdateRecord(command, key string, patch nodesettings.Object, visible func(nodesettings.Object) bool) (nodesettings.Object, bool, error) {
	if u, ok := m.owner(command).(nodesettings.RecordUpdater); ok {
		return u.UpdateRecord(command, key, patch, visible)
	}
	return nodesettings.Object{}, false, nil
}

// wireNodeSettings makes the runner a participant in the K2 settings protocol (DESIGN §16):
// the K2 relay, settings snapshots and config pushes over MQTT nudges, the backstop on every
// connect, and needs_k2 in the heartbeat until a key is stored. It also answers the app's
// command-data browser (§16, "Stored data"). nodeConfig, when set, is the snapshot's node_config
// (the Pi profile's piNodeConfig); nil is a computer's. devs, when set, are the snapshot's device
// families and managers (a Pi's sidecar); nil reports none, as a computer has none. secrets,
// when set, runs after the app pushes secrets (a Pi describes its families again).
func wireNodeSettings(r *node.Runner, st *nodesettings.Store, tools node.Multi, nodeConfig func() map[string]any,
	devs func(context.Context) nodesettings.Devices, secrets func(), log *slog.Logger) {
	h := &nodesettings.Handler{NodeID: r.Creds.NodeID, Store: st, Commands: settingsCommands(tools), NodeConfig: nodeConfig, Log: log,
		Devices: devs, SecretsChanged: secrets, KeyChanged: r.BeatNow} // the app sees needs_k2: false within seconds
	var once sync.Once
	bind := func(cc *node.CC) *nodesettings.Handler {
		once.Do(func() { h.API = cc }) // one command-center client per run
		return h
	}
	r.Topics = map[string]func(context.Context, *node.CC, []byte){}
	for _, sub := range []string{nodesettings.TopicK2Provision, nodesettings.TopicSettingsRequest, nodesettings.TopicConfigPush, nodesettings.TopicPackageSecrets} {
		r.Topics[sub] = func(ctx context.Context, cc *node.CC, payload []byte) { bind(cc).Handle(ctx, sub, payload) }
	}
	r.OnMQTTConnect = func(ctx context.Context, cc *node.CC) { bind(cc).Backstop(ctx) }
	cd := &nodesettings.CommandData{Store: st, Commands: dataCommands(tools), Log: log}
	r.Requests = map[string]func(context.Context, string, []byte) (string, []byte){nodesettings.TopicCommandData: cd.Handle}
	r.NeedsK2 = func() bool { return !st.HasK2() }
}
