package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// Live is the sidecar in service, as one toolset that never changes identity: installing,
// removing or reverting a package starts a new sidecar and swaps it in, so the tool list, the
// settings snapshot and the Stored data tab follow without restarting the agent. Empty (no
// packages) it offers nothing.
type Live struct {
	mu     sync.RWMutex
	sc     *Sidecar
	onSwap func()
}

// OnSwap runs f after every Swap (the Pi beats at once: the new sidecar's device families are
// its heartbeat's protocols).
func (l *Live) OnSwap(f func()) {
	l.mu.Lock()
	l.onSwap = f
	l.mu.Unlock()
}

// ErrNoPackages is the Stored data tab asking about a command while no sidecar runs.
var ErrNoPackages = errors.New("no packages are installed on this computer")

// Current is the sidecar in service, or nil.
func (l *Live) Current() *Sidecar {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.sc
}

// Swap puts sc (nil for none) in service and closes the one before. A call still running on
// the old one fails with "sidecar exited", as a crash would.
func (l *Live) Swap(sc *Sidecar) {
	l.mu.Lock()
	old := l.sc
	l.sc = sc
	f := l.onSwap
	l.mu.Unlock()
	if old != nil && old != sc {
		_ = old.Close()
	}
	if f != nil {
		f()
	}
}

// Close ends the sidecar in service.
func (l *Live) Close() error {
	l.Swap(nil)
	return nil
}

func (l *Live) Available(ctx context.Context) []node.ToolDef {
	if sc := l.Current(); sc != nil {
		return sc.Available(ctx)
	}
	return nil
}

func (l *Live) Call(ctx context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if sc := l.Current(); sc != nil {
		return sc.Call(ctx, name, args, req)
	}
	return nil, false
}

// Commands are the packages' commands with their secrets, for the settings snapshot.
func (l *Live) Commands(uid *int64) []nodesettings.Command {
	if sc := l.Current(); sc != nil {
		return sc.Commands(uid)
	}
	return nil
}

func (l *Live) DataCommands() []nodesettings.DataCommand {
	if sc := l.Current(); sc != nil {
		return sc.DataCommands()
	}
	return nil
}

func (l *Live) Summaries(ctx context.Context, command string, records []nodesettings.Object) ([]map[string]any, error) {
	if sc := l.Current(); sc != nil {
		return sc.Summaries(ctx, command, records)
	}
	return nil, ErrNoPackages
}

func (l *Live) Create(ctx context.Context, command string, values nodesettings.Object, uid json.Number) (string, json.RawMessage, error) {
	if sc := l.Current(); sc != nil {
		return sc.Create(ctx, command, values, uid)
	}
	return "", nil, ErrNoPackages
}

func (l *Live) ContextCommands() []nodesettings.ContextCommand {
	if sc := l.Current(); sc != nil {
		return sc.ContextCommands()
	}
	return nil
}

func (l *Live) ContextQuery(ctx context.Context, command, operation string, params json.RawMessage, uid *int64) (nodesettings.Object, error) {
	if sc := l.Current(); sc != nil {
		return sc.ContextQuery(ctx, command, operation, params, uid)
	}
	return nodesettings.Object{}, ErrNoPackages
}

func (l *Live) RemindersDue(ctx context.Context) ([]DueReminder, int, error) {
	if sc := l.Current(); sc != nil {
		return sc.RemindersDue(ctx)
	}
	return nil, 0, ErrNoPackages
}

// Agents are the background agents of the sidecar in service; none without one.
func (l *Live) Agents() []AgentDef {
	if sc := l.Current(); sc != nil {
		return sc.Agents()
	}
	return nil
}

// RunAgent is Sidecar.RunAgent on the sidecar in service.
func (l *Live) RunAgent(ctx context.Context, name string) ([]json.RawMessage, error) {
	if sc := l.Current(); sc != nil {
		return sc.RunAgent(ctx, name)
	}
	return nil, ErrNoPackages
}

// Failed are the installed packages whose commands didn't load.
func (l *Live) Failed() map[string]string {
	if sc := l.Current(); sc != nil {
		return sc.Failed()
	}
	return nil
}

// Devices are the device families and managers, for the settings snapshot: described again
// (the last description if that fails), so is_configured follows the secrets set since.
func (l *Live) Devices(context.Context) nodesettings.Devices {
	if sc := l.Current(); sc != nil {
		_ = sc.RefreshDevices()
		return sc.Devices()
	}
	return nodesettings.Devices{}
}

// RefreshDevices is Sidecar.RefreshDevices on the sidecar in service (nothing without one).
func (l *Live) RefreshDevices() error {
	if sc := l.Current(); sc != nil {
		return sc.RefreshDevices()
	}
	return nil
}

// Protocols are the configured device families (the heartbeat's protocols); none without a
// sidecar.
func (l *Live) Protocols() []string {
	if sc := l.Current(); sc != nil {
		return sc.Protocols()
	}
	return []string{}
}

// DeviceCall is Sidecar.DeviceCall on the sidecar in service.
func (l *Live) DeviceCall(method string, params map[string]any) (map[string]any, error) {
	if sc := l.Current(); sc != nil {
		return sc.DeviceCall(method, params)
	}
	return nil, ErrNoPackages
}

// StoreAuth is Sidecar.StoreAuth on the sidecar in service.
func (l *Live) StoreAuth(provider string, values map[string]any, uid any) (string, error) {
	if sc := l.Current(); sc != nil {
		return sc.StoreAuth(provider, values, uid)
	}
	return "", ErrNoPackages
}
