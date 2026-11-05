package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// NodeContext is what a call scenario's household commands share with the call plan, answered
// by a stand-in node over the sandbox's MQTT as a real node's context handler would
// (internal/agent/nodesettings/context.go): the calendar's availability, and each command's
// call_context fields.
type NodeContext struct {
	// Availability is the calendar's free and busy windows ("Thu 2-5pm"), as
	// jarvis-cmd-calendar's availability operation returns them.
	Availability *struct {
		Free []string `json:"free"`
		Busy []string `json:"busy"`
	} `json:"availability,omitempty"`
	// CallContext is each command's call_context fields (key, label, value, tier).
	CallContext map[string][]map[string]string `json:"call_context,omitempty"`
}

const calendarCommand = "get_calendar_events"

// contextNode connects as the sandbox's seeded node and answers context requests from spec
// until stop is called.
func contextNode(ctx context.Context, env map[string]string, spec NodeContext) (stop func(), err error) {
	id, key := env["SBX_NODE_ID"], env["SBX_NODE_KEY"]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env["SBX_CC"]+"/api/v0/node/mqtt-credentials", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", id+":"+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var cred struct {
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&cred) != nil {
		return nil, fmt.Errorf("mqtt credentials: %s", resp.Status)
	}
	opts := mqtt.NewClientOptions().AddBroker("tcp://" + env["SBX_MQTT"]).
		SetClientID("jarvis-node-" + id).SetCleanSession(true).SetConnectTimeout(10 * time.Second)
	if cred.Username != nil && cred.Password != nil {
		opts.SetUsername(*cred.Username).SetPassword(*cred.Password)
	}
	c := mqtt.NewClient(opts)
	if t := c.Connect(); !t.WaitTimeout(10*time.Second) || t.Error() != nil {
		return nil, fmt.Errorf("mqtt connect: %v", t.Error())
	}
	prefix := "jarvis/nodes/" + id + "/context/"
	t := c.Subscribe(prefix+"+", 1, func(c mqtt.Client, m mqtt.Message) {
		var p map[string]any
		if json.Unmarshal(m.Payload(), &p) != nil {
			return
		}
		cid, _ := p["correlation_id"].(string)
		out, _ := json.Marshal(spec.answer(strings.TrimPrefix(m.Topic(), prefix), p))
		c.Publish(m.Topic()+"/response/"+cid, 1, false, out)
	})
	if !t.WaitTimeout(10*time.Second) || t.Error() != nil {
		c.Disconnect(0)
		return nil, fmt.Errorf("mqtt subscribe: %v", t.Error())
	}
	return func() { c.Disconnect(250) }, nil
}

// answer is the context handler's reply to op ("operations" or "query").
func (spec NodeContext) answer(op string, p map[string]any) map[string]any {
	op2 := func(name string) map[string]any {
		return map[string]any{"name": name, "description": name, "params_schema": map[string]any{}}
	}
	if op == "operations" {
		providers := []any{}
		if spec.Availability != nil {
			providers = append(providers, map[string]any{"command_name": calendarCommand, "operations": []any{op2("availability")}})
		}
		for cmd := range spec.CallContext {
			providers = append(providers, map[string]any{"command_name": cmd, "operations": []any{op2("call_context")}})
		}
		return map[string]any{"ok": true, "providers": providers}
	}
	cmd, _ := p["command"].(string)
	switch p["operation"] {
	case "availability":
		if spec.Availability != nil && (cmd == "" || cmd == calendarCommand) {
			return map[string]any{"ok": true, "command_name": calendarCommand, "operation": "availability",
				"data": map[string]any{"free": spec.Availability.Free, "busy": spec.Availability.Busy}}
		}
	case "call_context":
		if fields, ok := spec.CallContext[cmd]; ok {
			return map[string]any{"ok": true, "command_name": cmd, "operation": "call_context",
				"data": map[string]any{"fields": fields}}
		}
	}
	return map[string]any{"ok": false, "error": fmt.Sprintf("no installed command provides '%v'", p["operation"]), "code": "no_provider"}
}
