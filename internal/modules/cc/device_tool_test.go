package cc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

func TestResolveDevice(t *testing.T) {
	devs := []*deviceRow{
		{entityID: "rgbic_tv_light_bars", name: "Office Light Bars"},
		{entityID: "light.kitchen", name: "Kitchen Light"},
		{entityID: "light.kitchen_counter", name: "Kitchen Counter Light"},
		{entityID: "lock.front", name: "Front Door"},
	}
	cases := []struct {
		entity, name, want, errPart string
	}{
		{"rgbic_tv_light_bars", "", "rgbic_tv_light_bars", ""},
		{"", "office light bars", "rgbic_tv_light_bars", ""},      // case-insensitive exact
		{"", "the office  Light Bars", "rgbic_tv_light_bars", ""}, // whitespace collapsed... (not exact: "the")
		{"", "office light bar", "rgbic_tv_light_bars", ""},       // singular, substring
		{"", "kitchen light", "light.kitchen", ""},                // exact beats the substring sibling
		{"", "kitchen", "", "Several devices match"},              // ambiguous
		{"", "garage door", "", "No device called"},               // unknown
		{"wrong.entity", "front door", "lock.front", ""},          // bad entity falls back to the name
		{"wrong.entity", "", "", "No device with entity id"},      // bad entity, no name
		{"", "", "", "No device named"},                           // nothing given
	}
	for _, c := range cases {
		d, msg := resolveDevice(devs, c.entity, c.name)
		switch {
		case c.want != "" && (d == nil || d.entityID != c.want):
			t.Errorf("resolveDevice(%q, %q) = %v (%s), want %s", c.entity, c.name, d, msg, c.want)
		case c.want == "" && d != nil:
			t.Errorf("resolveDevice(%q, %q) = %s, want none", c.entity, c.name, d.entityID)
		case c.want == "" && !strings.Contains(msg, c.errPart):
			t.Errorf("resolveDevice(%q, %q) message %q, want %q in it", c.entity, c.name, msg, c.errPart)
		}
	}
}

func toolCall(args string) servertools.Call {
	v, err := pyjson.Loads(args)
	if err != nil {
		panic(err)
	}
	return servertools.Call{ID: "c1", Name: "control_device", Args: v.(*pyjson.Object)}
}

// The control_device server tool drives the node as the app's POST …/control does (#512):
// resolve in the registry, publish the action, wait on the result sink.
func TestControlDeviceServerTool(t *testing.T) {
	controlWait = 300 * time.Millisecond
	defer func() { controlWait = 10 * time.Second }()
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"protocols": []string{"govee"}}, n.h()).want(200)
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	e.importDevices(tok,
		map[string]any{"entity_id": "rgbic_tv_light_bars", "name": "Office Light Bars", "domain": "light",
			"source": "direct", "protocol": "govee"},
		map[string]any{"entity_id": "sensor.t", "name": "Office Temperature", "domain": "sensor"},
	)
	c.next() // invalidate_device_cache from the import
	tool := &controlDeviceTool{m: e.m}
	turn := servertools.Turn{ConversationID: "cv1", HouseholdID: "hh1", NodeID: "n1"}
	ctx := context.Background()

	text := func(res any, err error) string {
		if err != nil {
			t.Fatal(err)
		}
		return pyjson.Dumps(res, true)
	}

	// Resolution and shape errors answer the model, never the node.
	if s := text(tool.Execute(ctx, toolCall(`{"device_name": "garage door", "action": "turn_off"}`), turn)); !strings.Contains(s, `"success": false`) || !strings.Contains(s, "No device called 'garage door'") {
		t.Fatal(s)
	}
	if s := text(tool.Execute(ctx, toolCall(`{"device_name": "office temperature", "action": "turn_on"}`), turn)); !strings.Contains(s, "is not controllable") {
		t.Fatal(s)
	}
	if s := text(tool.Execute(ctx, toolCall(`{"device_name": "office light bars"}`), turn)); !strings.Contains(s, "No action given") {
		t.Fatal(s)
	}

	// The happy path: the published action is the app's shape, and the node's answer comes back.
	type out struct {
		s   string
		err error
	}
	ch := make(chan out, 1)
	go func() {
		res, err := tool.Execute(ctx, toolCall(`{"device_name": "office light bar", "action": "set_brightness", "value": "50"}`), turn)
		ch <- out{pyjson.Dumps(res, true), err}
	}()
	verb, d := command(t, c.next())
	actx := d["context"].(map[string]any)
	if verb != "action" || d["command_name"] != "control_device" || d["action_name"] != "set_brightness" ||
		d["reply_request_id"] != d["request_id"] || actx["entity_id"] != "rgbic_tv_light_bars" ||
		actx["protocol"] != "govee" || actx["value"] != "50" || actx["source"] != "direct" {
		t.Fatal(verb, d)
	}
	rid := d["request_id"].(string)
	if e.do("POST", "/api/v0/commands/"+rid+"/verify", nil, n.h()).json()["valid"] != true {
		t.Fatal("verify")
	}
	e.do("POST", "/api/v0/device-control-results/"+rid, map[string]any{"success": true}, n.h()).want(200)
	r := <-ch
	if r.err != nil || !strings.Contains(r.s, `"success": true`) || !strings.Contains(r.s, `"entity_id": "rgbic_tv_light_bars"`) {
		t.Fatal(r.s, r.err)
	}

	// A node failure (or, below, a timeout) is success false with the error, for an honest reply.
	go func() {
		res, err := tool.Execute(ctx, toolCall(`{"entity_id": "rgbic_tv_light_bars", "device_name": "office light bars", "action": "turn_off"}`), turn)
		ch <- out{pyjson.Dumps(res, true), err}
	}()
	_, d = command(t, c.next())
	rid = d["request_id"].(string)
	e.do("POST", "/api/v0/device-control-results/"+rid, map[string]any{"success": false, "error": "bulb offline"}, n.h()).want(200)
	r = <-ch
	if r.err != nil || !strings.Contains(r.s, `"success": false`) || !strings.Contains(r.s, "bulb offline") {
		t.Fatal(r.s, r.err)
	}

	go func() {
		res, err := tool.Execute(ctx, toolCall(`{"device_name": "office light bars", "action": "turn_off"}`), turn)
		ch <- out{pyjson.Dumps(res, true), err}
	}()
	c.next() // the action nobody answers
	r = <-ch
	if r.err != nil || !strings.Contains(r.s, `"success": false`) || !strings.Contains(r.s, errNodeTimeout) {
		t.Fatal(r.s, r.err)
	}
}
