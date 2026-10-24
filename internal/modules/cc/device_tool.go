package cc

import (
	"context"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// control_device as a server tool (#512, decision 2026-10-23-device-tool-server-side.md):
// chat and voice device control for conversations whose node offers no control_device client
// tool of its own — the Go Pi node (packages.PiBuiltins skips the Python builtin) and the
// desktop agents. Offered exactly when the conversation's device list renders
// (prompts.ToolGates.Devices over the agents payload, after warmup's cc_devices fallback); a
// node that reports its own control_device keeps it (warmup drops the server copy, so the
// Python node's resolver still runs on the node). Execute resolves the device in the
// household registry and drives the chosen node exactly as the app's
// POST …/devices/{id}/control does (smarthome_control.go).

// controlDeviceDefinition mirrors jarvis-node-setup's control_device command schema (the
// golden node_dev toolset), without the node-side options_source markers.
const controlDeviceDefinition = `{"type": "function", "function": {"name": "control_device", "description": "Control a smart-home device: lights, TV, speakers, locks, thermostat (on, off, play, volume, lock, temperature, colour).", "parameters": {"type": "object", "properties": {"device_name": {"type": "string", "description": "Friendly name, e.g. 'front door', 'kitchen light'"}, "action": {"type": "string", "enum": ["turn_on", "turn_off", "play", "pause", "volume_up", "volume_down", "next", "previous", "lock", "unlock", "set_temperature", "set_mode", "set_brightness", "set_color"]}, "entity_id": {"type": "string", "description": "Entity id, e.g. lock.front_door, if known"}, "value": {"type": "string", "description": "Number or mode for set_* actions"}, "color": {"type": "string", "description": "For set_color: name or 'r,g,b'"}}, "required": ["device_name", "action"]}}}`

type controlDeviceTool struct{ m *Module }

func (t *controlDeviceTool) Name() string { return "control_device" }

func (t *controlDeviceTool) Definition() *pyjson.Object {
	v, err := pyjson.Loads(controlDeviceDefinition)
	if err != nil {
		panic(err)
	}
	return v.(*pyjson.Object)
}

// deviceToolResult is the legacy result shape in a stable key order for the model.
func deviceToolResult(success bool, entityID, action string, errMsg, inputRequired any) *pyjson.Object {
	return servertools.Obj("success", success, "entity_id", entityID, "action", action,
		"error", errMsg, "input_required", inputRequired)
}

func deviceToolError(entityID, action, msg string) *pyjson.Object {
	var eid, act any
	if entityID != "" {
		eid = entityID
	}
	if action != "" {
		act = action
	}
	return servertools.Obj("success", false, "entity_id", eid, "action", act, "error", msg, "input_required", nil)
}

func (t *controlDeviceTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	action := call.Str("action")
	if action == "" {
		return deviceToolError("", "", "No action given. Pass one of the action enum values."), nil
	}
	devs, err := t.m.queryDevices(ctx, `d.household_id = ? AND COALESCE(d.is_active, 1) = 1`, turn.HouseholdID)
	if err != nil {
		return nil, err
	}
	d, msg := resolveDevice(devs, call.Str("entity_id"), call.Str("device_name"))
	if d == nil {
		return deviceToolError("", action, msg), nil
	}
	if !d.controllable() {
		return deviceToolError(d.entityID, action, "'"+d.name+"' is not controllable."), nil
	}
	node, err := t.m.pickNode(ctx, turn.HouseholdID, d.protocol.String)
	if err != nil {
		return deviceToolError(d.entityID, action, err.Error()), nil
	}
	if !t.m.bus.Available() {
		return deviceToolError(d.entityID, action, "MQTT not available"), nil
	}
	data := map[string]any{}
	if v := argText(call, "value"); v != "" {
		data["value"] = v
	}
	if c := call.Str("color"); c != "" {
		data["color"] = c
	}
	res := t.m.controlDeviceAction(ctx, node, d, action, data)
	success, _ := res["success"].(bool)
	return deviceToolResult(success, d.entityID, action, res["error"], res["input_required"]), nil
}

// resolveDevice finds the device a model call means among the household's active devices: the
// exact entity_id when given, else the device name, case-insensitively, then as a unique
// substring either way ("office light bar" finds "Office Light Bars"). The message, on nil,
// says what went wrong in words the model can speak.
func resolveDevice(devs []*deviceRow, entityID, name string) (*deviceRow, string) {
	if entityID != "" {
		for _, d := range devs {
			if d.entityID == entityID {
				return d, ""
			}
		}
	}
	n := normName(name)
	if n == "" {
		if entityID != "" {
			return nil, "No device with entity id '" + entityID + "'."
		}
		return nil, "No device named. Pass device_name or entity_id."
	}
	var exact, part []*deviceRow
	for _, d := range devs {
		dn := normName(d.name)
		switch {
		case dn == n:
			exact = append(exact, d)
		case dn != "" && (strings.Contains(dn, n) || strings.Contains(n, dn)):
			part = append(part, d)
		}
	}
	if len(exact) == 1 {
		return exact[0], ""
	}
	if len(exact) == 0 && len(part) == 1 {
		return part[0], ""
	}
	cands := append(exact, part...)
	if len(cands) == 0 {
		return nil, "No device called '" + name + "' here."
	}
	names := make([]string, 0, 5)
	for _, d := range cands {
		if len(names) == 5 {
			break
		}
		names = append(names, d.name)
	}
	return nil, "Several devices match '" + name + "': " + strings.Join(names, ", ") + ". Which one?"
}

// normName lowercases and collapses whitespace for name matching.
func normName(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// agentsHaveDevices reports whether the agents payload lists anything the prompt's device
// sections render (home_assistant wins over device_agent, as the renderers read it): the
// Devices gate, so control_device is offered exactly when the prompt tells the model to call
// it. After warmup's registry fallback the payload also stands for cc_devices.
func agentsHaveDevices(agents *pyjson.Object) bool {
	if agents == nil {
		return false
	}
	var ha *pyjson.Object
	if v, ok := agents.Get("home_assistant"); ok {
		ha, _ = v.(*pyjson.Object)
	} else if v, ok := agents.Get("device_agent"); ok {
		ha, _ = v.(*pyjson.Object)
	}
	if ha == nil {
		return false
	}
	for _, k := range []string{"light_controls", "device_controls"} {
		if v, ok := ha.Get(k); ok {
			if o, _ := v.(*pyjson.Object); o != nil && o.Len() > 0 {
				return true
			}
		}
	}
	return false
}

// registryAgents is the `agents` payload a node would have sent, built from cc_devices in the
// built-in device_agent shape the prompt's device sections and get_ha_entities read (nil with
// no devices). State is "unknown": the registry stores none (live state stays a node question,
// smarthome_control.go).
func (m *Module) registryAgents(ctx context.Context, hh string) *pyjson.Object {
	devs, err := m.queryDevices(ctx, `d.household_id = ? AND COALESCE(d.is_active, 1) = 1`, hh)
	if err != nil {
		m.deps.Log.Warn("cc: device registry read failed", "household", hh, "err", err)
		return nil
	}
	if len(devs) == 0 {
		return nil
	}
	controls := pyjson.NewObject()
	for _, d := range devs {
		e := servertools.Obj("entity_id", d.entityID, "name", d.name, "state", "unknown")
		if d.roomName.Valid && d.roomName.String != "" {
			e.Set("area", d.roomName.String)
		}
		cur, _ := controls.Get(d.domain)
		l, _ := cur.([]any)
		controls.Set(d.domain, append(l, any(e)))
	}
	return servertools.Obj("device_agent", servertools.Obj("device_controls", controls))
}
