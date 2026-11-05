package cc

import (
	"context"
	"testing"
	"time"
)

// A call plan's context comes from every live node in the household: each command offering
// call_context is asked by name (a node's own "first provider" would answer for one only), the
// calendar's availability from whichever node has it, and a node that never answers is skipped.
func TestNodeContextGathersFromEveryNode(t *testing.T) {
	e := newEnv(t)
	desk, pi := e.createNode("desk", "hh1"), e.createNode("pi", "hh1")
	e.createNode("silent", "hh1")
	e.respondingNode(e.createNode("other", "hh2"), "context").set(func(string, map[string]any) any {
		t.Error("asked another household's node")
		return nil
	})
	e.respondingNode(desk, "context").set(func(op string, p map[string]any) any {
		if op == "operations" {
			return map[string]any{"ok": true, "providers": []any{map[string]any{"command_name": "get_calendar_events",
				"operations": []any{map[string]any{"name": "availability"}}}}}
		}
		if p["operation"] == "availability" {
			return map[string]any{"ok": true, "command_name": "get_calendar_events",
				"data": map[string]any{"free": []any{"Thu 2-5pm"}, "busy": []any{"Tue 9am-5pm (Work)"}}}
		}
		return map[string]any{"ok": false, "error": "no_provider"}
	})
	e.respondingNode(pi, "context").set(func(op string, p map[string]any) any {
		if op == "operations" {
			return map[string]any{"ok": true, "providers": []any{
				map[string]any{"command_name": "dental_plan", "operations": []any{map[string]any{"name": "call_context"}}},
				map[string]any{"command_name": "shoprite", "operations": []any{map[string]any{"name": "call_context"}}}}}
		}
		if p["operation"] == "availability" {
			return map[string]any{"ok": false, "error": "no installed command provides 'availability'"}
		}
		val := map[string]any{"dental_plan": "DX-77120", "shoprite": "SR-1"}[p["command"].(string)]
		return map[string]any{"ok": true, "command_name": p["command"],
			"data": map[string]any{"fields": []any{map[string]any{"key": p["command"].(string) + "_id", "label": "ID", "value": val}}}}
	})
	nodeContextTimeout = 2 * time.Second
	t.Cleanup(func() { nodeContextTimeout = 5 * time.Second })
	nc := nodeContext{e.m}
	uid := int64(7)

	ans, err := nc.Availability(context.Background(), "hh1", &uid, time.Now(), time.Now().AddDate(0, 0, 7))
	if err != nil || !ans.OK || ans.Free[0] != "Thu 2-5pm" || ans.Source != "get_calendar_events" {
		t.Fatalf("availability: %+v %v", ans, err)
	}
	fields := nc.CallContext(context.Background(), "hh1", &uid, "Bright Smiles", "book a cleaning")
	got := map[string]string{}
	for _, f := range fields {
		got[f.Source] = f.Value
	}
	if got["dental_plan"] != "DX-77120" || got["shoprite"] != "SR-1" || len(fields) != 2 {
		t.Fatalf("call context: %+v", fields)
	}
}
