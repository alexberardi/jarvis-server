// Package actions is a node's side of three command verbs the Python node answers in
// mqtt_tts_listener.py: action (the app's device control and a command's buttons),
// callback (an inbox item's @callback) and toggle_command (the household turning a command
// on or off). The commands and device families themselves are Python and run in the package
// sidecar; this package verifies, fetches and posts, as N:'s handle_action, handle_callback
// and handle_toggle_command do (docs/agent/go-pi-node.md §3.18).
package actions

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The verbs.
const (
	VerbAction   = "action"
	VerbCallback = "callback"
	VerbToggle   = "toggle_command"
)

// CC is command-center as the node reaches it (node.CC).
type CC interface {
	Do(ctx context.Context, method, path string, body, out any) error
	Verify(ctx context.Context, rid string) (bool, error)
}

// Commands is the sidecar's side (sidecar.Live): a command's handle_action, or for
// control_device with no such command the device's family; and a command's @callback. Each
// answers the body the Python node posts.
type Commands interface {
	Action(params map[string]any) (map[string]any, error)
	Callback(params map[string]any) (map[string]any, error)
}

// Handler answers the three verbs.
type Handler struct {
	Commands Commands
	// Enabled and SetEnabled are the household's command switches (nodesettings.Store): a
	// command turned off is not run, as N:'s command discovery leaves it out.
	Enabled    func(name string) bool
	SetEnabled func(name string, on bool) error
	// Say, if set, speaks an action's message when it came from a voice turn (no
	// reply_request_id: nobody is waiting on the result).
	Say func(ctx context.Context, text string) error
	Log *slog.Logger
}

// Verbs are the handlers, given the connected command-center.
func (h *Handler) Verbs() map[string]func(context.Context, CC, map[string]any) {
	return map[string]func(context.Context, CC, map[string]any){
		VerbAction:   h.action,
		VerbCallback: h.callback,
		VerbToggle:   h.toggle,
	}
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func (h *Handler) enabled(name string) bool {
	return h.Enabled == nil || h.Enabled(name)
}

// action is N:'s handle_action. jarvisd never sends `trusted`, and a node verifies every
// action (D48), so there is no unverified path.
func (h *Handler) action(ctx context.Context, cc CC, det map[string]any) {
	rid := str(det, "request_id")
	if rid == "" {
		h.Log.Warn("action: no request_id; ignored")
		return
	}
	if ok, err := cc.Verify(ctx, rid); err != nil || !ok {
		h.Log.Warn("action failed verification; dropped", "request_id", rid, "err", err)
		return
	}
	name, act := str(det, "command_name"), str(det, "action_name")
	if name == "" || act == "" {
		h.Log.Warn("action: no command_name or action_name; ignored", "request_id", rid)
		return
	}
	actx, _ := det["context"].(map[string]any)
	if actx == nil {
		actx = map[string]any{}
	}
	res, err := h.Commands.Action(map[string]any{"command_name": name, "action_name": act, "context": actx,
		"user_id": det["user_id"], "enabled": h.enabled(name)})
	body := map[string]any{"success": false, "error": nil}
	if err != nil {
		body["error"] = err.Error()
	} else {
		body["success"], _ = res["success"].(bool)
		if e, _ := res["error"].(string); e != "" {
			body["error"] = e
		}
		if ir, _ := res["input_required"].(map[string]any); ir != nil {
			body["input_required"] = ir
		}
	}
	h.Log.Info("action", "command", name, "action", act, "protocol", actx["protocol"], "entity_id", actx["entity_id"],
		"success", body["success"], "error", body["error"])
	reply := str(det, "reply_request_id")
	to := reply
	if to == "" {
		to = rid
	}
	if err := cc.Do(ctx, http.MethodPost, "/api/v0/device-control-results/"+url.PathEscape(to), body, nil); err != nil {
		h.Log.Warn("action: result not posted", "request_id", to, "err", err)
	}
	// The result goes first so the app is unblocked; a voice-originated action's message is
	// then spoken, best effort.
	if msg, _ := res["message"].(string); msg != "" && reply == "" && h.Say != nil {
		if err := h.Say(ctx, msg); err != nil {
			h.Log.Warn("action: message not spoken", "command", name, "err", err)
		}
	}
}

// callback is N:'s handle_callback: request_id is the job, and fetching its payload (which
// authenticates this node and checks the job is its own) is the verification.
func (h *Handler) callback(ctx context.Context, cc CC, det map[string]any) {
	job := str(det, "request_id")
	if job == "" {
		h.Log.Warn("callback: no request_id; ignored")
		return
	}
	var p map[string]any
	if err := cc.Do(ctx, http.MethodGet, "/api/v0/callbacks/"+url.PathEscape(job), nil, &p); err != nil || p == nil {
		h.Log.Warn("callback: payload not fetched; dropped", "job", job, "err", err)
		return
	}
	post := func(body map[string]any) {
		if err := cc.Do(ctx, http.MethodPost, "/api/v0/callbacks/"+url.PathEscape(job)+"/result", body, nil); err != nil {
			h.Log.Warn("callback: result not posted", "job", job, "err", err)
		}
	}
	name, cb := str(p, "command_name"), str(p, "callback_name")
	if name == "" || cb == "" {
		h.Log.Warn("callback: no command_name or callback_name", "job", job)
		post(map[string]any{"success": false, "error": "missing command_name or callback_name"})
		return
	}
	data, _ := p["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	conv := str(p, "conversation_id")
	if conv == "" {
		conv = "callback:" + job
	}
	voice := str(p, "voice_command")
	if voice == "" {
		voice = "cb:" + cb
	}
	res, err := h.Commands.Callback(map[string]any{"command_name": name, "callback_name": cb, "data": data,
		"user_id": p["user_id"], "conversation_id": conv, "voice_command": voice, "enabled": h.enabled(name)})
	body := map[string]any{"success": false, "error": nil}
	if err != nil {
		body["error"] = err.Error()
	} else {
		body["success"], _ = res["success"].(bool)
		if e, _ := res["error"].(string); e != "" {
			body["error"] = e
		}
		if cd, ok := res["context_data"].(map[string]any); ok {
			body["context_data"] = cd
		}
	}
	h.Log.Info("callback", "command", name, "callback", cb, "job", job, "success", body["success"], "error", body["error"])
	post(body)
}

// toggle is N:'s handle_toggle_command: jarvisd sends it for control_device when the
// household switches between the built-in and an external device integration.
func (h *Handler) toggle(_ context.Context, _ CC, det map[string]any) {
	name := str(det, "command_name")
	if name == "" {
		h.Log.Warn("toggle_command: no command_name; ignored")
		return
	}
	on := true
	if v, ok := det["enabled"]; ok {
		on = laxBool(v)
	}
	if err := h.SetEnabled(name, on); err != nil {
		h.Log.Warn("toggle_command failed", "command", name, "err", err)
		return
	}
	h.Log.Info("toggle_command", "command", name, "enabled", on)
}

// laxBool is Python's str(v).lower() in ("true", "1", "yes").
func laxBool(v any) bool {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case bool:
		s = strconv.FormatBool(x)
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	default:
		s = fmt.Sprint(x)
	}
	switch strings.ToLower(s) {
	case "true", "1", "yes":
		return true
	}
	return false
}
