// Package reset is a Pi node's factory reset: what it wipes (Wipe, shared by jarvisd's tracked
// factory-reset task over MQTT and the provisioning API's POST /api/v1/factory-reset) and the
// MQTT handler that runs it (N:scripts/mqtt_tts_listener.py _handle_factory_reset,
// N:provisioning/factory_reset.py). A desktop agent has none: jarvisd refuses to send it one
// (decision 2026-10-14), and signing out is how a computer leaves.
package reset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/alexberardi/jarvis-server/internal/agent/bluetooth"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// Topic is the factory-reset sub-topic of jarvis/nodes/{id}/.
const Topic = "factory-reset"

// wiped is what a reset removes from the state dir (the directory of node.json), in order, with
// the label the provisioning API lists it under (N:'s labels where there is one). node.json goes
// last: it is the node's credentials and its provisioning marker (provisioning.Options.Provisioned),
// so a wipe that fails part-way leaves a node that still knows it is enrolled.
var wiped = []struct {
	path   string
	labels []string
}{
	{nodesettings.K2File, []string{"k2_key"}},
	// Command secrets, command data and the household's on/off switches: N:'s node database.
	{nodesettings.SettingsFile, []string{"node_database"}},
	{"timers.json", []string{"timers"}},
	{bluetooth.StoreFile, []string{"bluetooth_devices"}},
	// Pantry packages (and the versions revert would restore) and the sidecar's list of them.
	// The venv and the built-in commands stay: they are the node's runtime, like the binary.
	{filepath.Join("packages", "pkgs"), []string{"pantry:packages"}},
	{filepath.Join("packages", "commands.json"), []string{"pantry:commands_list"}},
	{"node.json", []string{"provisioning_marker", "config_credentials"}},
}

// removeAll is os.RemoveAll; a var so a test can make one entry fail on every OS (a read-only
// directory stops a removal on Linux, not on Windows).
var removeAll = os.RemoveAll

// Wipe clears a node's state under dir and lists what it removed. It keeps the agent itself,
// its unit, the settings file (agent.json: the node's config, as N: keeps config.json), the
// volume, the self-update records and the packages' Python environment. A file that is not
// there is not an error. Every entry is tried; the error joins the ones that failed.
func Wipe(dir string) ([]string, error) {
	cleared := []string{}
	var errs []error
	for _, w := range wiped {
		p := filepath.Join(dir, w.path)
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := removeAll(p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", w.labels[0], err))
			continue
		}
		cleared = append(cleared, w.labels...)
	}
	return cleared, errors.Join(errs...)
}

// API is what the handler asks command-center (node.CC).
type API interface {
	FactoryResetStatus(ctx context.Context, taskID, token, state, errMsg string) error
	VerifyReset(ctx context.Context, requestID string) error
}

// Handler runs jarvisd's factory-reset requests for one node.
type Handler struct {
	NodeID string
	// Wipe clears the node (Wipe over the state dir) and lists what it cleared.
	Wipe func(context.Context) ([]string, error)
	// Reboot restarts the Pi once the reset is reported. If it fails, Exit runs instead (the
	// agent quits and systemd starts it again, now without credentials), as N: falls back to.
	Reboot func() error
	Exit   func()
	// Freeze, if set, runs once jarvisd has confirmed the reset and before the wipe: it stops
	// what the agent still holds (settings, timers) from writing a file back before the reboot.
	Freeze func()
	Log    *slog.Logger

	busy atomic.Bool // a redelivered message while one runs is dropped
}

type message struct {
	RequestID string `json:"request_id"`
	NodeID    string `json:"node_id"`
	TaskID    string `json:"task_id"`
}

// Handle is one factory-reset message: {request_id, node_id, task_id}. With a task id it reports
// in_progress first, which is also the check that jarvisd sent it (the token is the task's): a
// rejection aborts before anything is wiped. Without one it asks the legacy verify-reset. Then
// it wipes, reports success (or failed with the error) and reboots.
func (h *Handler) Handle(ctx context.Context, api API, payload []byte) {
	var m message
	if err := json.Unmarshal(payload, &m); err != nil {
		h.Log.Warn("factory-reset: not JSON; ignoring", "err", err)
		return
	}
	if m.RequestID == "" || m.NodeID == "" {
		h.Log.Warn("factory-reset: request_id or node_id missing; ignoring")
		return
	}
	if m.NodeID != h.NodeID {
		h.Log.Warn("factory-reset: for another node; ignoring", "node", m.NodeID)
		return
	}
	if !h.busy.CompareAndSwap(false, true) {
		h.Log.Info("factory-reset: one is already running; ignoring this one", "task", m.TaskID)
		return
	}
	defer h.busy.Store(false)
	if m.TaskID == "" {
		h.Log.Warn("factory-reset: no task_id; using the legacy verify-reset")
		if err := api.VerifyReset(ctx, m.RequestID); err != nil {
			h.Log.Warn("factory-reset: rejected by jarvisd's verify-reset; nothing wiped", "err", err)
			return
		}
	} else if err := api.FactoryResetStatus(ctx, m.TaskID, m.RequestID, "in_progress", ""); err != nil {
		h.Log.Warn("factory-reset: jarvisd refused the in_progress report (a spoof or an expired token); nothing wiped",
			"task", m.TaskID, "err", err)
		return
	}
	if h.Freeze != nil {
		h.Freeze()
	}
	h.Log.Info("factory-reset: wiping the node", "task", m.TaskID)
	cleared, err := h.Wipe(ctx)
	if err != nil {
		h.Log.Error("factory-reset: wipe failed", "task", m.TaskID, "cleared", cleared, "err", err)
		if m.TaskID != "" {
			h.report(ctx, api, m, "failed", err.Error())
		}
		return
	}
	h.Log.Info("factory-reset: wiped", "task", m.TaskID, "cleared", cleared)
	// Before the reboot: once it runs there is no chance to. The token and task id were read
	// before the wipe, and the call needs nothing the wipe removed.
	if m.TaskID != "" {
		h.report(ctx, api, m, "success", "")
	}
	h.Log.Info("factory-reset: rebooting")
	if h.Reboot != nil {
		err := h.Reboot()
		if err == nil {
			return
		}
		h.Log.Warn("factory-reset: reboot failed; quitting instead", "err", err)
	}
	if h.Exit != nil {
		h.Exit()
	}
}

// report posts the final state; a failure is logged and never stops the reset (N:'s rule:
// better a wiped node and a task that times out than a node left half reset).
func (h *Handler) report(ctx context.Context, api API, m message, state, errMsg string) {
	if err := api.FactoryResetStatus(ctx, m.TaskID, m.RequestID, state, errMsg); err != nil {
		h.Log.Warn("factory-reset: status report failed", "state", state, "err", err)
	}
}

var _ API = (*node.CC)(nil)
