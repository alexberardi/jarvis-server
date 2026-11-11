package cc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/devicetypes"
	"github.com/alexberardi/jarvis-server/internal/display"
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
	hh := r.PathValue("household_id")
	if err := m.householdAccess(r.Context(), a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	m.controlDevice(w, r, hh, action, data, a.user.ID)
}

// handleNodeControlDevice is the household route's control for a node (a touch display): a
// device of the node's own household (from auth, never the request).
func (m *Module) handleNodeControlDevice(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	action, _ := b.str("action", true)
	data, _ := b.object("data", false)
	confirmed, _ := b.boolean("confirmed")
	if !b.done(w) {
		return
	}
	if n.HouseholdID == "" {
		detail(w, http.StatusNotFound, "Device not found")
		return
	}
	d, err := m.device(r.Context(), n.HouseholdID, r.PathValue("device_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if status, msg := nodeActionGuard(d, action, data, confirmed); status != 0 {
		detail(w, status, msg)
		return
	}
	m.controlDevice(w, r, n.HouseholdID, action, data, 0)
}

// parameterActions take a value in data; the device's manager checks they fit its domain.
var parameterActions = []string{"set_temperature", "set_mode", "set_brightness", "set_color", "set_position", "set_percentage"}

// nodeActionGuard is what a node (a touch display) may ask of a device: an action the device
// advertises (its buttons, its catalogue features where those are actions, or a parameter
// action), access devices only once confirmed, and only value fields in data, never identity
// or routing ones. 0 allows it.
func nodeActionGuard(d *deviceRow, action string, data map[string]any, confirmed bool) (int, string) {
	allowed := slices.Contains(parameterActions, action)
	for _, b := range supportedActions(d.domain, d.protocol.String, d.controllable()) {
		allowed = allowed || b["button_action"] == action
	}
	if t, _ := devicetypes.Lookup(d.domain); t.ControlType == "media" || t.ControlType == "alarm" {
		allowed = allowed || slices.Contains(t.Features, action)
	}
	if !allowed || !d.controllable() {
		return http.StatusBadRequest, "Action is not advertised by this device"
	}
	if (action == "unlock" || strings.HasPrefix(action, "open")) && !confirmed {
		return http.StatusConflict, "Confirm opening this access device"
	}
	for k, v := range data {
		if msg := nodeActionValue(k, v); msg != "" {
			return http.StatusBadRequest, msg
		}
	}
	return 0, ""
}

// asFloat reads a JSON number (request bodies decode them as json.Number).
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	}
	return 0, false
}

func nodeActionValue(key string, value any) string {
	num := func(lo, hi float64) string {
		if v, ok := asFloat(value); !ok || v < lo || v > hi {
			return "Invalid device action value"
		}
		return ""
	}
	switch key {
	case "mode", "hvac_mode":
		if v, ok := value.(string); !ok || v == "" || len(v) > 50 || strings.ContainsAny(v, "\x00\r\n") {
			return "Invalid device mode"
		}
	case "url":
		if v, ok := value.(string); !ok || len(v) > 2048 || !display.WebURL(v) {
			return "Use a direct HTTP(S) media link"
		}
	case "temperature":
		return num(-100, 150)
	case "brightness", "volume", "position", "percentage":
		return num(0, 100)
	case "seconds":
		return num(0, 864000)
	case "color_temp":
		return num(1000, 20000)
	case "rgb":
		c, ok := value.([]any)
		if !ok || len(c) != 3 {
			return "Invalid device action value"
		}
		for _, x := range c {
			if v, ok := asFloat(x); !ok || v < 0 || v > 255 {
				return "Invalid device action value"
			}
		}
	default:
		return fmt.Sprintf("Unsupported action field %q", key)
	}
	return ""
}

func (m *Module) controlDevice(w http.ResponseWriter, r *http.Request, hh, action string, data map[string]any, userID int64) {
	ctx := r.Context()
	d, err := m.device(ctx, hh, r.PathValue("device_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !d.controllable() {
		detail(w, http.StatusBadRequest, "Device is not controllable")
		return
	}
	node, err := m.deviceInputNode(ctx, hh, d, userID, action, data)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if !m.bus.Available() {
		detail(w, http.StatusServiceUnavailable, "MQTT not available")
		return
	}
	result := m.controlDeviceAction(ctx, node, d, action, data)
	prompt, _ := result["input_required"].(map[string]any)
	if action == "pair_start" && (result["success"] == true || getStr(prompt, "session_id", "") != "") {
		bound, err := m.pinDeviceNode(ctx, hh, d, node)
		if err != nil {
			m.writeErr(w, err)
			return
		}
		if bound.nodeID != node.nodeID {
			detail(w, http.StatusConflict, "Device pairing moved to its assigned node; start pairing again")
			return
		}
	}
	if prompt != nil {
		m.deviceInputs.remember(m.now(), d, node.nodeID, userID, prompt)
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// controlDeviceAction publishes an `action` for the node's control_device command and waits
// for its result: the shared half of the app's POST …/devices/{id}/control and the
// control_device server tool (device_tool.go). The answer is the legacy result shape; a
// timeout is success false with errNodeTimeout, never an error (§7.3).
func (m *Module) controlDeviceAction(ctx context.Context, node *nodeRow, d *deviceRow, action string, data map[string]any) map[string]any {
	actx := map[string]any{
		"entity_id": d.entityID, "domain": d.domain, "name": d.name, "protocol": nullable(d.protocol),
		"cloud_id": nullable(d.cloudID), "model": nullable(d.model), "local_ip": nullable(d.localIP),
		"mac_address": nullable(d.mac), "source": d.sourceOr(),
	}
	for k, v := range data {
		// Action values (including pairing answers) cannot replace the registered
		// device identity. Its node and credentials are scoped to this identity.
		if _, identity := actx[k]; !identity {
			actx[k] = v
		}
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
		return map[string]any{
			"success": false, "entity_id": d.entityID, "action": action, "error": errNodeTimeout, "input_required": nil,
		}
	}
	success, _ := laxBool(res["success"])
	return map[string]any{
		"success": success, "entity_id": d.entityID, "action": action,
		"error": strOrNil(res["error"]), "input_required": objOrNil(res["input_required"]),
	}
}

// handleDeviceState asks the node for the device's live state (never stored).
func (m *Module) handleDeviceState(w http.ResponseWriter, r *http.Request, a provAuth) {
	hh := r.PathValue("household_id")
	if err := m.householdAccess(r.Context(), a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	m.deviceState(w, r, hh)
}

// handleNodeDeviceState is the household route's state for a node: a device of the node's own
// household.
func (m *Module) handleNodeDeviceState(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	if n.HouseholdID == "" {
		detail(w, http.StatusNotFound, "Device not found")
		return
	}
	m.deviceState(w, r, n.HouseholdID)
}

func (m *Module) deviceState(w http.ResponseWriter, r *http.Request, hh string) {
	ctx := r.Context()
	d, err := m.device(ctx, hh, r.PathValue("device_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	node, err := m.pickDeviceNode(ctx, hh, d)
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
			"source": d.sourceOr(), "cloud_id": nullable(d.cloudID), "model": nullable(d.model),
			"local_ip": nullable(d.localIP), "mac_address": nullable(d.mac),
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
