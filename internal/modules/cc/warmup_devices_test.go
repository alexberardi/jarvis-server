package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// goNodeTools is the shape of the Go Pi node's report: control_node, but no control_device
// client tool (packages.PiBuiltins skips the Python builtin, #512).
const goNodeTools = `[
 {"type": "function", "function": {"name": "control_node", "description": "This speaker's own volume or mute. Smart-home devices: control_device.",
  "parameters": {"type": "object", "properties": {"action": {"type": "string", "enum": ["volume_up", "volume_down", "set_volume", "mute", "unmute"]},
  "target_percent": {"type": "integer"}}, "required": ["action"]}},
  "keywords": ["volume", "mute"]}
]`

func reqToolNames(req map[string]any) []string {
	var out []string
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		fn, _ := tm["function"].(map[string]any)
		if n, _ := fn["name"].(string); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func (ve *voiceEnv) importVoiceDevices(devs ...map[string]any) {
	ve.t.Helper()
	tok := ve.auth.addUser(9, voiceHH, "member")
	ve.do("POST", "/api/v0/households/"+voiceHH+"/devices/import",
		map[string]any{"devices": devs}, bearer(tok)).want(201)
}

// #512 through warmup: a node that sends no agents payload and offers no control_device of its
// own (the Go Pi node, the desktop agents) gets the server tool and a device list built from
// the household registry.
func TestWarmupDeviceToolAndRegistryList(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.importVoiceDevices(map[string]any{"entity_id": "rgbic_tv_light_bars", "name": "Office Light Bars",
		"domain": "light", "source": "direct", "protocol": "govee"})
	ve.start("d1", goNodeTools)
	reqs := ve.eng.requests()
	req := reqs[len(reqs)-1]
	names := reqToolNames(req)
	nDevice := 0
	for _, n := range names {
		if n == "control_device" {
			nDevice++
		}
	}
	sys, _ := messagesOf(req)[0]["content"].(string)
	if nDevice != 1 || !strings.Contains(sys, "rgbic_tv_light_bars: Office Light Bars") ||
		!strings.Contains(sys, "Use control_device to control devices.") {
		t.Fatalf("control_device offered %d times (tools %v); prompt:\n%s", nDevice, names, sys)
	}
}

// A node offering its own control_device keeps it: the server copy is dropped, so the node's
// resolver still runs on the node (the Python node's path stays as it was).
func TestWarmupDeviceToolClientWins(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.importVoiceDevices(map[string]any{"entity_id": "rgbic_tv_light_bars", "name": "Office Light Bars",
		"domain": "light", "source": "direct", "protocol": "govee"})
	clientCopy := `[{"type": "function", "function": {"name": "control_device", "description": "NODE OWN COPY",
	 "parameters": {"type": "object", "properties": {"device_name": {"type": "string"}, "action": {"type": "string"}}, "required": ["device_name", "action"]}},
	 "keywords": ["turn on", "turn off"]}]`
	ve.start("d2", clientCopy)
	reqs := ve.eng.requests()
	req := reqs[len(reqs)-1]
	var descs []string
	tools, _ := req["tools"].([]any)
	for _, tl := range tools {
		fn, _ := tl.(map[string]any)["function"].(map[string]any)
		if fn["name"] == "control_device" {
			d, _ := fn["description"].(string)
			descs = append(descs, d)
		}
	}
	if len(descs) != 1 || descs[0] != "NODE OWN COPY" {
		t.Fatalf("control_device definitions %v, want only the node's", descs)
	}
}

// With no devices in the registry, the tool and the list stay out of the conversation.
func TestWarmupNoDevicesNoDeviceTool(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("d3", goNodeTools)
	reqs := ve.eng.requests()
	req := reqs[len(reqs)-1]
	for _, n := range reqToolNames(req) {
		if n == "control_device" {
			t.Fatalf("control_device offered with no devices (tools %v)", reqToolNames(req))
		}
	}
	if sys, _ := messagesOf(req)[0]["content"].(string); strings.Contains(sys, "control_device") {
		t.Fatalf("prompt mentions control_device with no devices:\n%s", sys)
	}
}
