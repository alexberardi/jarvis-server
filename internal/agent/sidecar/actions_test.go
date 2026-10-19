package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"
)

// The app's device control with no control_device command: the device's family runs it on the
// device jarvisd describes (the Python node's DiscoveredDevice, passed where the SDK says ip),
// with the action's context as data.
func TestSidecarControlsADeviceThroughItsFamily(t *testing.T) {
	sc, st := startLights(t)
	ctx := map[string]any{"protocol": "fakelight", "entity_id": "light.lamp", "name": "Lamp", "domain": "light",
		"local_ip": "10.0.0.5", "mac_address": "aa:bb:cc:00:00:01", "cloud_id": nil, "model": nil, "brightness": 50}
	res, err := sc.Action(map[string]any{"command_name": "control_device", "action_name": "turn_on", "context": ctx, "enabled": true})
	if err != nil || res["success"] != true || res["error"] != nil {
		t.Fatalf("turn_on = %v, %v", res, err)
	}
	rec, ok := st.Data("fakelight", "controlled", time.Now())
	want := []any{"light.lamp", "Lamp", "light", "10.0.0.5", "aa:bb:cc:00:00:01", json.Number("50")}
	if !ok || rec.Value("action") != "turn_on" || !reflect.DeepEqual(rec.Value("device"), want) {
		t.Fatalf("control saw %v", rec)
	}

	// A family whose control raises is a failed result, not a crashed sidecar.
	res, err = sc.Action(map[string]any{"command_name": "control_device", "action_name": "explode", "context": ctx})
	if err != nil || res["success"] != false || res["error"] != "the bulb popped" {
		t.Fatalf("explode = %v, %v", res, err)
	}
	res, err = sc.Action(map[string]any{"command_name": "control_device", "action_name": "turn_on",
		"context": map[string]any{"protocol": "nope", "entity_id": "light.x"}})
	if err != nil || res["success"] != false || res["error"] != "Device protocol 'nope' not found" {
		t.Fatalf("unknown protocol = %v, %v", res, err)
	}
	res, _ = sc.Action(map[string]any{"command_name": "control_device", "action_name": "turn_on", "context": map[string]any{}})
	if res["error"] != "No protocol specified in device context" {
		t.Fatalf("no protocol = %v", res)
	}
	// Any other command this sidecar doesn't have is not found.
	res, _ = sc.Action(map[string]any{"command_name": "email", "action_name": "send_click", "context": map[string]any{}})
	if res["success"] != false || res["error"] != "Command 'email' not found on this node" {
		t.Fatalf("unknown command = %v", res)
	}
}

// Pairing: pair_start asks for a PIN (input_required) and pair_finish runs on the same event
// loop, so a session the family holds (pyatv's) survives between the two calls, as on N:.
func TestSidecarDevicePairingKeepsItsLoop(t *testing.T) {
	sc, _ := startLights(t)
	ctx := map[string]any{"protocol": "fakelight", "entity_id": "light.pair_me", "domain": "light"}
	res, err := sc.Action(map[string]any{"command_name": "control_device", "action_name": "pair_start", "context": ctx})
	want := map[string]any{"type": "pin", "prompt": "Enter the PIN", "session_id": "s1", "follow_up_action": "pair_finish"}
	if err != nil || res["success"] != false || res["error"] != "PIN needed" || !reflect.DeepEqual(res["input_required"], want) {
		t.Fatalf("pair_start = %v, %v", res, err)
	}
	// A scan in between runs on its own loop and must not end the pairing one.
	if _, err := sc.DeviceCall("device_scan", nil); err != nil {
		t.Fatal(err)
	}
	ctx["pin"] = "1234"
	res, err = sc.Action(map[string]any{"command_name": "control_device", "action_name": "pair_finish", "context": ctx})
	if err != nil || res["success"] != true || res["error"] != nil {
		t.Fatalf("pair_finish = %v, %v", res, err)
	}
}

func startCommands(t *testing.T) *Sidecar {
	t.Helper()
	sc, err := Start(context.Background(), python(t), "testdata/commands", slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })
	return sc
}

// A command's own action (a response's button): handle_action as the requesting user, its
// message (spoken for a voice turn) and input_required passed back; a raise is a failure.
func TestSidecarRunsACommandsAction(t *testing.T) {
	sc := startCommands(t)
	res, err := sc.Action(map[string]any{"command_name": "shout", "action_name": "again",
		"context": map[string]any{"text": "hi"}, "user_id": 7, "enabled": true})
	if err != nil || res["success"] != true || res["error"] != nil || res["message"] != "HI" {
		t.Fatalf("again = %v, %v", res, err)
	}
	res, _ = sc.Action(map[string]any{"command_name": "shout", "action_name": "ask", "context": map[string]any{}})
	if ir, _ := res["input_required"].(map[string]any); res["success"] != true || ir["prompt"] != "Louder?" {
		t.Fatalf("ask = %v", res)
	}
	res, _ = sc.Action(map[string]any{"command_name": "shout", "action_name": "crash", "context": map[string]any{}})
	if res["success"] != false || res["error"] != "lost my voice" {
		t.Fatalf("crash = %v", res)
	}
	// The SDK's own: cancel_click, and an action the command doesn't have.
	res, _ = sc.Action(map[string]any{"command_name": "shout", "action_name": "cancel_click", "context": map[string]any{}})
	if res["success"] != true || res["message"] != "Cancelled." {
		t.Fatalf("cancel_click = %v", res)
	}
	res, _ = sc.Action(map[string]any{"command_name": "shout", "action_name": "nope", "context": map[string]any{}})
	if res["success"] != false || res["error"] != "Action 'nope' is not supported by shout." {
		t.Fatalf("nope = %v", res)
	}
	// A command the household turned off isn't run.
	res, _ = sc.Action(map[string]any{"command_name": "shout", "action_name": "again", "context": map[string]any{}, "enabled": false})
	if res["success"] != false || res["error"] != "Command 'shout' not found on this node" {
		t.Fatalf("disabled = %v", res)
	}
}

// A command's @callback runs with the job's data as the user, and every failure is a result.
func TestSidecarRunsACallback(t *testing.T) {
	sc := startCommands(t)
	res, err := sc.Callback(map[string]any{"command_name": "shout", "callback_name": "echo", "data": map[string]any{"n": 1},
		"user_id": 7, "voice_command": "cb:echo", "conversation_id": "callback:job-1", "enabled": true})
	want := map[string]any{"echo": map[string]any{"n": float64(1)}, "user_id": float64(7), "voice_command": "cb:echo",
		"conversation_id": "callback:job-1"}
	if err != nil || res["success"] != true || res["error"] != nil || !reflect.DeepEqual(res["context_data"], want) {
		t.Fatalf("echo = %v, %v", res, err)
	}
	for _, c := range []struct {
		params map[string]any
		error  string
	}{
		{map[string]any{"command_name": "shout", "callback_name": "refuse"}, "not today"},
		{map[string]any{"command_name": "shout", "callback_name": "crash"}, "callback blew up"},
		{map[string]any{"command_name": "shout", "callback_name": "nope"}, "Callback 'nope' not registered on 'shout'"},
		{map[string]any{"command_name": "email", "callback_name": "send"}, "Command 'email' not found on this node"},
		{map[string]any{"command_name": "shout", "callback_name": "echo", "enabled": false}, "Command 'shout' not found on this node"},
	} {
		res, err := sc.Callback(c.params)
		if err != nil || res["success"] != false || res["error"] != c.error {
			t.Fatalf("%v = %v, %v", c.params, res, err)
		}
	}
}

// Without a sidecar (no packages installed) both say so.
func TestLiveActionsWithoutASidecar(t *testing.T) {
	var l Live
	if _, err := l.Action(nil); err != ErrNoPackages {
		t.Fatalf("action = %v", err)
	}
	if _, err := l.Callback(nil); err != ErrNoPackages {
		t.Fatalf("callback = %v", err)
	}
}
