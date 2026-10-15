package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// Install plan 1.8: the heartbeat's node_kind and capabilities reach the node list and the
// single-node view (the admin and mobile node APIs), additively.
func TestHeartbeatNodeKindAndCapabilities(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleOwner)
	n := e.createNode("n1", "hh1")
	pi := e.createNode("pi", "hh1")

	// Before any report: null and {}.
	got := e.do("GET", "/api/v0/admin/nodes/n1", nil, bearer(owner)).want(200).json()
	if got["node_kind"] != nil || len(got["capabilities"].(map[string]any)) != 0 {
		t.Fatalf("fresh: %v", got)
	}

	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{
		"version_info": map[string]any{"version": "1.0", "install_mode": "desktop-agent"},
		"node_kind":    "desktop",
		"capabilities": map[string]any{
			"browser_extension": "connected", "voice": true, "n": 3,
			"nested": map[string]any{"x": 1}, "long": strings.Repeat("x", 65), strings.Repeat("k", 65): "v",
		},
	}, n.h()).want(200)
	// A Pi's beat (no new fields) leaves its own row alone.
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"is_busy": false}, pi.h()).want(200)

	want := map[string]any{"browser_extension": "connected", "voice": true, "n": 3.0}
	check := func(row map[string]any) {
		t.Helper()
		caps := row["capabilities"].(map[string]any)
		if row["node_kind"] != "desktop" || len(caps) != len(want) {
			t.Fatalf("desktop row: %v", row)
		}
		for k, v := range want {
			if caps[k] != v {
				t.Fatalf("caps[%s] = %v, want %v (%v)", k, caps[k], v, caps)
			}
		}
	}
	check(e.do("GET", "/api/v0/admin/nodes/n1", nil, bearer(owner)).want(200).json())
	var sawPi bool
	for _, x := range e.do("GET", "/api/v0/admin/nodes", nil, bearer(owner)).want(200).list() {
		row := x.(map[string]any)
		switch row["node_id"] {
		case "n1":
			check(row)
		case "pi":
			sawPi = true
			if row["node_kind"] != nil || len(row["capabilities"].(map[string]any)) != 0 {
				t.Fatalf("pi row: %v", row)
			}
		}
	}
	if !sawPi {
		t.Fatal("pi not listed")
	}

	// The extension disconnects: the next beat replaces the object; a beat without the fields
	// keeps them.
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"capabilities": map[string]any{"browser_extension": "disconnected"}}, n.h()).want(200)
	e.do("POST", "/api/v0/admin/nodes/heartbeat", nil, n.h()).want(200)
	got = e.do("GET", "/api/v0/admin/nodes/n1", nil, bearer(owner)).want(200).json()
	if got["node_kind"] != "desktop" || got["capabilities"].(map[string]any)["browser_extension"] != "disconnected" {
		t.Fatalf("after disconnect: %v", got)
	}

	// Wrong types are a 400, like the other heartbeat fields.
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"node_kind": 1}, n.h()).want(400)
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"capabilities": "connected"}, n.h()).want(400)
}

// Decision 2026-10-15 (primary node on a computer): the smart-home config's node options carry
// node_kind so the primary-node picker can sort Pis first and label computers. An agent that
// only reported its install mode still counts as a desktop; a Pi's is null.
func TestSmartHomeConfigNodeKind(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.createNode("pi", "hh1")
	desk := e.createNode("desk", "hh1")
	old := e.createNode("old", "hh1")
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"node_kind": "desktop"}, desk.h()).want(200)
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{
		"version_info": map[string]any{"version": "0.9", "install_mode": "desktop-agent"},
	}, old.h()).want(200)

	r := e.do("GET", hhPath+"/smart-home/config", nil, bearer(tok)).want(200).json()
	want := map[string]any{"pi": nil, "desk": "desktop", "old": "desktop"}
	nodes := r["nodes"].([]any)
	if len(nodes) != len(want) {
		t.Fatal(r)
	}
	for _, x := range nodes {
		o := x.(map[string]any)
		k, ok := o["node_kind"]
		if !ok || k != want[o["node_id"].(string)] {
			t.Fatalf("%v: node_kind = %v (present %v)", o["node_id"], k, ok)
		}
	}
}
