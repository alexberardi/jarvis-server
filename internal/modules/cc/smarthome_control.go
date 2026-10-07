package cc

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Device control and state (smart_home.py:1098-1430): a synchronous relay from mobile to the
// node that drives the device's protocol. The node answers on /device-control-results/{rid}
// and /device-state-results/{rid} (the shared, node-bound result sink in commands.go), which
// replaces legacy's /tmp polling.

// Waits (vars for tests): pairing does a device scan + SRP handshake, so 20 s (§7.4).
var (
	controlWait     = 10 * time.Second
	controlPairWait = 20 * time.Second
	stateWait       = 10 * time.Second
)

const errNodeTimeout = "Timed out waiting for node response"

// awaitNode opens the result slot for rid, runs publish, and waits up to d. ok=false on
// timeout or a failed publish (both answer 200 with the timeout error, as legacy).
func (m *Module) awaitNode(ctx context.Context, nodeID, rid string, d time.Duration, publish func() error) (map[string]any, bool) {
	m.bus.Expect(rid, nodeID)
	defer m.bus.Drop(rid)
	if err := publish(); err != nil {
		m.deps.Log.Warn("cc: device request not delivered", "node", nodeID, "err", err)
		return nil, false
	}
	wctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	raw, err := m.bus.Await(wctx, rid)
	if err != nil {
		return nil, false
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out, true
}

func strOrNil(v any) any {
	if s, ok := v.(string); ok {
		return s
	}
	return nil
}

func objOrNil(v any) any {
	if o, ok := v.(map[string]any); ok {
		return o
	}
	return nil
}

// handleControlDevice publishes an `action` for the node's control_device command and waits
// for its result. No `trusted` flag (D4/D7): the Bus records the rid so the node's
// POST /commands/{rid}/verify succeeds (§7.2).
func (m *Module) handleControlDevice(w http.ResponseWriter, r *http.Request, a provAuth) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	action, _ := b.str("action", true)
	data, _ := b.object("data", false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	d, err := m.device(ctx, hh, r.PathValue("device_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !d.controllable() {
		detail(w, http.StatusBadRequest, "Device is not controllable")
		return
	}
	node, err := m.pickNode(ctx, hh, d.protocol.String)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !m.bus.Available() {
		detail(w, http.StatusServiceUnavailable, "MQTT not available")
		return
	}
	actx := map[string]any{
		"entity_id": d.entityID, "domain": d.domain, "name": d.name, "protocol": nullable(d.protocol),
		"cloud_id": nullable(d.cloudID), "model": nullable(d.model), "local_ip": nullable(d.localIP),
		"mac_address": nullable(d.mac), "source": d.sourceOr(),
	}
	for k, v := range data {
		actx[k] = v
	}
	wait := controlWait
	if strings.HasPrefix(action, "pair") {
		wait = controlPairWait
	}
	rid := uuid4()
	m.deps.Log.Info("cc: device control", "action", action, "node", node.nodeID, "device", d.entityID)
	res, ok := m.awaitNode(ctx, node.nodeID, rid, wait, func() error {
		m.bus.CommandWithID(node.nodeID, "action", map[string]any{
			"command_name": "control_device", "action_name": action, "context": actx, "reply_request_id": rid,
		}, rid)
		return nil
	})
	if !ok {
		// A timeout is 200 with success false, not a 5xx (§7.3).
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false, "entity_id": d.entityID, "action": action, "error": errNodeTimeout, "input_required": nil,
		})
		return
	}
	success, _ := laxBool(res["success"])
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"success": success, "entity_id": d.entityID, "action": action,
		"error": strOrNil(res["error"]), "input_required": objOrNil(res["input_required"]),
	})
}

// handleDeviceState asks the node for the device's live state (never stored).
func (m *Module) handleDeviceState(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	d, err := m.device(ctx, hh, r.PathValue("device_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	node, err := m.pickNode(ctx, hh, d.protocol.String)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !m.bus.Available() {
		detail(w, http.StatusServiceUnavailable, "MQTT not available")
		return
	}
	domain := d.domain
	if domain == "" {
		if i := strings.Index(d.entityID, "."); i >= 0 {
			domain = d.entityID[:i]
		}
	}
	rid := uuid4()
	res, ok := m.awaitNode(ctx, node.nodeID, rid, stateWait, func() error {
		return m.bus.Publish(node.nodeID, "device-state", map[string]any{
			"request_id": rid, "entity_id": d.entityID, "domain": domain, "protocol": nullable(d.protocol),
			"source": d.sourceOr(), "cloud_id": nullable(d.cloudID), "local_ip": nullable(d.localIP),
			"mac_address": nullable(d.mac),
		})
	})
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"entity_id": d.entityID, "domain": domain, "state": nil, "ui_hints": nil, "error": errNodeTimeout,
		})
		return
	}
	entity, isStr := res["entity_id"].(string)
	if !isStr {
		entity = d.entityID
	}
	if s, isStr := res["domain"].(string); isStr {
		domain = s
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"entity_id": entity, "domain": domain, "state": objOrNil(res["state"]),
		"ui_hints": objOrNil(res["ui_hints"]), "error": strOrNil(res["error"]),
	})
}
