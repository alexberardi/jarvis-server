package builtins

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/devices"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

var controlActions = []string{"turn_on", "turn_off", "play", "pause", "volume_up", "volume_down", "next", "previous",
	"lock", "unlock", "set_temperature", "set_mode", "set_brightness", "set_color"}

var controlDeviceSpec = node.Spec{
	Name: "control_device",
	Description: "Control a smart home device: turn on/off, play, pause, volume, lock, unlock. MUST be used for any " +
		"request to turn on, turn off, play, pause, or control a physical device (TV, light, speaker, lock, " +
		"thermostat). Common actions: turn_on, turn_off, play, pause, volume_up, volume_down, next, previous, lock, unlock.",
	Params: []node.Param{
		{Name: "device_name", Type: "string", Required: true, OptionsSource: "devices",
			Description: "Name of the device to control (e.g. 'Apple TV', 'living room light')."},
		{Name: "action", Type: "string", Required: true, Enum: controlActions,
			Description: "Action to perform: turn_on, turn_off, play, pause, volume_up, volume_down, next, previous, lock, unlock, set_temperature, set_mode, set_brightness, set_color."},
		{Name: "entity_id", Type: "string", OptionsSource: "entities",
			Description: "Entity ID from the device list (e.g. 'light.office'). Pass if shown in context."},
		{Name: "value", Type: "string",
			Description: "Value for the action. Examples: temperature in degrees for set_temperature (e.g. '69'), mode name for set_mode (e.g. 'heat', 'cool', 'off'), brightness percentage for set_brightness (e.g. '50')."},
		{Name: "color", Type: "string",
			Description: "Color for set_color: a color name (e.g. 'green', 'blue', 'warm white', 'purple') or an 'r,g,b' triple. Required when action is set_color."},
	},
	Keywords: []string{"turn on", "turn off", "power on", "power off", "play", "pause", "stop", "volume", "next", "previous",
		"lock", "unlock", "device", "tv", "light", "speaker", "switch", "thermostat", "dim", "brighten", "color"},
	Examples: []node.Example{
		{VoiceCommand: "Turn on the Apple TV", Params: map[string]any{"device_name": "Apple TV", "action": "turn_on"}, Primary: true},
		{VoiceCommand: "Turn off the office light", Params: map[string]any{"device_name": "office light", "action": "turn_off"}},
		{VoiceCommand: "Set the thermostat to 72 degrees", Params: map[string]any{"device_name": "thermostat", "action": "set_temperature", "value": "72"}},
		{VoiceCommand: "Set the Nest to heat mode", Params: map[string]any{"device_name": "Nest Thermostat", "action": "set_mode", "value": "heat"}},
		{VoiceCommand: "Turn the office light green", Params: map[string]any{"device_name": "office light", "action": "set_color", "color": "green"}},
		{VoiceCommand: "Turn up the volume on the speaker", Params: map[string]any{"device_name": "speaker", "action": "volume_up"}},
	},
}

// Families is the sidecar's device side (sidecar.Live): the configured families, and an
// action run by one.
type Families interface {
	Protocols() []string
	Action(params map[string]any) (map[string]any, error)
}

// ControlDevice is control_device on this node's own device families, for the household's
// direct devices (commands/control_device_command.py). It is offered only while a direct device
// uses a family configured here: otherwise jarvisd's server-side control_device, which picks
// the node that has the family (#512), keeps the turn.
type ControlDevice struct {
	Devices  *devices.Registry
	Families Families
	CC       CC
}

func (t *ControlDevice) Available(context.Context) []node.ToolDef {
	protocols := t.Families.Protocols()
	for _, d := range t.Devices.Cached() {
		if slices.Contains(protocols, d.Protocol) {
			return []node.ToolDef{controlDeviceSpec.Render()}
		}
	}
	return nil
}

func (t *ControlDevice) Call(ctx context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if name != controlDeviceSpec.Name {
		return nil, false
	}
	str := func(k string) string { s, _ := args[k].(string); return strings.TrimSpace(s) }
	deviceName, action := str("device_name"), str("action")
	fail := func(msg string) map[string]any {
		return map[string]any{"success": false, "error": msg, "device_name": deviceName, "action": action}
	}
	if deviceName == "" || action == "" {
		return fail("Both device_name and action are required."), true
	}
	cc := t.CC()
	if cc == nil {
		return fail("not connected to command-center"), true
	}
	var d devices.Device
	ok := false
	if id := str("entity_id"); id != "" {
		d, ok = t.Devices.Get(ctx, cc, id)
	}
	if !ok {
		// No entity_id, or one the model made up: the name.
		all := t.Devices.Devices(ctx, cc)
		if len(all) == 0 {
			return fail("No devices available. Add devices first."), true
		}
		if d, ok = matchDevice(all, deviceName); !ok {
			out := fail("Device '" + deviceName + "' not found.")
			names := make([]string, 0, len(all))
			for _, d := range all {
				names = append(names, d.Name)
			}
			slices.Sort(names)
			out["available_devices"] = names
			return out, true
		}
	}
	actx := map[string]any{"entity_id": d.EntityID, "name": d.Name, "domain": d.Domain, "protocol": d.Protocol,
		"cloud_id": d.CloudID, "model": d.Model, "local_ip": d.LocalIP, "mac_address": d.MacAddress}
	for k, v := range actionData(action, str("value"), str("color")) {
		actx[k] = v
	}
	// enabled false: the family's control, never a sidecar command named control_device (which,
	// when present and on, served this call before it got here).
	res, err := t.Families.Action(map[string]any{"command_name": "control_device", "action_name": action,
		"context": actx, "user_id": req.UserID, "enabled": false})
	if err != nil {
		return fail(err.Error()), true
	}
	if ok, _ := res["success"].(bool); !ok {
		msg, _ := res["error"].(string)
		if msg == "" {
			msg = "Control failed"
		}
		out := fail(msg)
		if ir := res["input_required"]; ir != nil {
			out["input_required"] = ir
		}
		return out, true
	}
	return map[string]any{"success": true, "device": d.Name, "action": action,
		"message": titleWords(strings.ReplaceAll(action, "_", " ")) + " " + d.Name}, true
}

// actionData is the family's parameters for action, as the Python command builds them. A
// set_color's name goes as color; the sidecar adds its rgb for families that need one.
func actionData(action, value, color string) map[string]any {
	data := map[string]any{}
	if action == "set_color" {
		if spoken := cmp.Or(color, value); spoken != "" {
			data["color"] = spoken
		}
		return data
	}
	if value == "" {
		return data
	}
	key := map[string]string{"set_temperature": "temperature", "set_mode": "mode", "set_brightness": "brightness"}[action]
	data[cmp.Or(key, "value")] = value
	return data
}

// matchDevice is _match_device: the name case-insensitively, then either containing the other,
// then the entity id containing the name. Ties go to the first in name order.
func matchDevice(all []devices.Device, query string) (devices.Device, bool) {
	all = slices.Clone(all)
	slices.SortFunc(all, func(a, b devices.Device) int { return strings.Compare(a.Name, b.Name) })
	q := strings.ToLower(strings.TrimSpace(query))
	tests := []func(d devices.Device) bool{
		func(d devices.Device) bool { return strings.ToLower(d.Name) == q },
		func(d devices.Device) bool {
			n := strings.ToLower(d.Name)
			return n != "" && (strings.Contains(n, q) || strings.Contains(q, n))
		},
		func(d devices.Device) bool { return strings.Contains(strings.ToLower(d.EntityID), q) },
	}
	for _, test := range tests {
		for _, d := range all {
			if test(d) {
				return d, true
			}
		}
	}
	return devices.Device{}, false
}

// titleWords is Python's str.title for the action words.
func titleWords(s string) string {
	w := strings.Fields(s)
	for i, x := range w {
		w[i] = strings.ToUpper(x[:1]) + x[1:]
	}
	return strings.Join(w, " ")
}
