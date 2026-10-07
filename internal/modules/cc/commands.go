package cc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// actionWait: /nodes/{id}/actions waits 10 s for the node's result (§5; a var for tests).
var actionWait = 10 * time.Second

// handleNodeAction forwards a user action (a card button) to the node and waits for its
// result. The published details carry no `trusted` (D4/D7): the node verifies the action via
// POST /commands/{rid}/verify, which the Bus answers (D48).
func (m *Module) handleNodeAction(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	cmd, _ := b.str("command_name", true)
	action, _ := b.str("action_name", true)
	actx, _ := b.object("context", false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("node_id")
	if _, err := m.requireNodeAccess(ctx, u, id, authn.RoleMember, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	if actx == nil {
		actx = map[string]any{}
	}
	wctx, cancel := context.WithTimeout(ctx, actionWait)
	defer cancel()
	rid, res, err := m.bus.CommandAwait(wctx, id, "action", map[string]any{
		"command_name": cmd, "action_name": action, "context": actx, "user_id": u.ID,
	}, "")
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status": "timeout", "request_id": rid, "success": nil, "error": "Node did not respond in time",
		})
		return
	}
	var out map[string]any
	_ = json.Unmarshal(res, &out)
	success, present := out["success"]
	if !present {
		success = true
	}
	resp := map[string]any{"status": "completed", "request_id": rid, "success": success, "error": out["error"]}
	// M10: input_required passes through as an optional field.
	if v, ok := out["input_required"]; ok && v != nil {
		resp["input_required"] = v
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) handleNodeConfig(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	settingsMap, _ := b.object("settings", true)
	for k, v := range settingsMap {
		switch v.(type) {
		case json.Number, string, bool:
		default:
			*b.errs = append(*b.errs, "body -> settings -> "+k+": Input should be a valid integer, number, string or boolean")
		}
	}
	restart := true
	if v, ok := b.boolean("restart"); ok {
		restart = v
	}
	if !b.done(w) {
		return
	}
	if _, err := m.requireNodeAccess(r.Context(), u, r.PathValue("node_id"), authn.RoleMember, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	rid := m.bus.Command(r.PathValue("node_id"), "update_node_config", map[string]any{"settings": settingsMap, "restart": restart})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "sent", "request_id": rid})
}

func (m *Module) handleLEDPreview(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	pattern, _ := b.str("pattern", true)
	dur := 3.0
	if v, ok := b.number("duration_seconds"); ok {
		dur = v
	}
	if !b.done(w) {
		return
	}
	if _, err := m.requireNodeAccess(r.Context(), u, r.PathValue("node_id"), authn.RoleMember, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	rid := m.bus.Command(r.PathValue("node_id"), "preview_led_pattern", map[string]any{"pattern": pattern, "duration_seconds": dur})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "sent", "request_id": rid})
}

func (m *Module) handleVerifyCommand(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"valid": m.bus.VerifyCommand(r.PathValue("request_id"), n.ID)})
}

// readRawObject reads a JSON object body (dict) as raw bytes.
func readRawObject(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBody))
	if err != nil {
		detail(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return nil, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		validationError(w, "body: Field required")
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			validationError(w, "body -> 0: JSON decode error")
		} else {
			validationError(w, "body: Input should be a valid dictionary")
		}
		return nil, false
	}
	return json.RawMessage(raw), true
}

// deliver hands a node's posted result to the waiting caller, logging what got dropped.
func (m *Module) deliver(rid, nodeID string, raw json.RawMessage) DeliverResult {
	res := m.bus.Deliver(rid, nodeID, raw)
	switch res {
	case WrongNode:
		m.deps.Log.Warn("cc: result refused: request issued to another node", "request_id", rid, "node", nodeID)
	case NoSlot, AlreadyFilled:
		m.deps.Log.Debug("cc: late or unknown result dropped", "request_id", rid, "node", nodeID)
	}
	return res
}

// handleResult is the shared node result sink (/device-control-results, /device-state-results,
// /mobile/node-tool-reports, /mobile/voice-profile-results). It replaces the /tmp rendezvous
// files with the Bus's in-process slots (§3.6). D4: node auth, and the rid must have been
// issued to this node; a result for a rid issued to another node is a 403. A late or unknown
// rid is acknowledged and dropped, as legacy acknowledged every post.
func (m *Module) handleResult(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	raw, ok := readRawObject(w, r)
	if !ok {
		return
	}
	if m.deliver(r.PathValue("request_id"), n.ID, raw) == WrongNode {
		detail(w, http.StatusForbidden, "Request does not belong to this node")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
