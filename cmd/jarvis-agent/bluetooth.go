package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/alexberardi/jarvis-server/internal/agent/bluetooth"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/packages"
)

// openBluetooth is the Pi's BlueZ (a var: the tests replace it).
var openBluetooth = bluetooth.Open

// wireBluetooth answers the app's Bluetooth requests on a Pi (the six bluetooth-* topics;
// internal/agent/bluetooth), and starts the pairing agent and the reconnect loop. The saved
// devices are in dir (next to node.json). Without BlueZ the node runs without Bluetooth, as the
// Python node does without bluetoothctl. Call it after wireNodeSettings (which sets Topics).
// The returned func closes the bus connection.
func wireBluetooth(ctx context.Context, r *node.Runner, dir string, log *slog.Logger) func() {
	bus, closeBus, err := openBluetooth(log)
	if err != nil {
		log.Warn("bluetooth: BlueZ not reachable; no Bluetooth on this node", "err", err)
		return func() {}
	}
	st, err := bluetooth.OpenStore(filepath.Join(dir, bluetooth.StoreFile))
	bt := &bluetooth.Node{Bus: bus, Pulse: bluetooth.Pactl{}, Store: st, Log: log}
	// A music player's Pulse stream can die when the output moves to or from a speaker (#478).
	sys := packages.NewSystem(log)
	bt.RestartPlayers = func(ctx context.Context) error { return sys.RestartUnits(ctx, packages.MusicPlayers...) }
	if err != nil {
		log.Warn("bluetooth: saved devices not readable; nothing is reconnected or saved", "err", err)
		bt.Store = nil
	}
	if r.Topics == nil {
		r.Topics = map[string]func(context.Context, *node.CC, []byte){}
	}
	for _, topic := range bluetooth.Topics {
		r.Topics[topic] = func(ctx context.Context, cc *node.CC, payload []byte) { bt.Handle(ctx, cc, topic, payload) }
	}
	bt.Start(ctx)
	return closeBus
}
