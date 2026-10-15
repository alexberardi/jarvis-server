package cc

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// The node's registered name (the computer's hostname for a desktop agent) reaches the node
// list and the single-node view, so the apps can tell several computers in one room apart.
// A node registered without one (auth falls back to its id) answers null.
func TestNodeListCarriesRegisteredName(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleOwner)

	e.do("POST", "/api/v0/admin/nodes", map[string]any{"node_id": "desk", "household_id": "hh1", "room": "desktop", "name": "omarchy"}, adminH()).want(200)
	e.createNode("pi", "hh1") // no name: auth stores the node id

	// The provisioning-token path (the agent's sign-in enrolment and the pairing approval).
	tok := e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh1", "room": "desktop", "name": "laptop"}, bearer(owner)).want(201).json()
	e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": tok["node_id"], "provisioning_token": tok["token"]}, nil).want(201)

	want := map[string]any{"desk": "omarchy", "pi": nil, tok["node_id"].(string): "laptop"}
	l := e.do("GET", "/api/v0/admin/nodes", nil, bearer(owner)).want(200).list()
	if len(l) != len(want) {
		t.Fatalf("list: %v", l)
	}
	for _, row := range l {
		r := row.(map[string]any)
		name, ok := r["name"]
		if !ok || name != want[r["node_id"].(string)] {
			t.Fatalf("%v: name %v (present %v), want %v", r["node_id"], name, ok, want[r["node_id"].(string)])
		}
	}
	for id, w := range want {
		if got := e.do("GET", "/api/v0/admin/nodes/"+id, nil, bearer(owner)).want(200).json(); got["name"] != w {
			t.Fatalf("get %s: name %v, want %v", id, got["name"], w)
		}
	}
	// PATCH answers the same shape.
	if p := e.do("PATCH", "/api/v0/admin/nodes/desk", map[string]any{"room": "office"}, adminH()).want(200).json(); p["name"] != "omarchy" {
		t.Fatalf("patch: %v", p)
	}
}

// The primary-node picker's options carry the same name, so the picker can name computers.
func TestSmartHomeConfigNodeName(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.createNode("pi", "hh1")
	e.do("POST", "/api/v0/admin/nodes", map[string]any{"node_id": "desk", "household_id": "hh1", "room": "desktop", "name": "omarchy"}, adminH()).want(200)

	r := e.do("GET", hhPath+"/smart-home/config", nil, bearer(tok)).want(200).json()
	want := map[string]any{"pi": nil, "desk": "omarchy"}
	nodes := r["nodes"].([]any)
	if len(nodes) != len(want) {
		t.Fatal(r)
	}
	for _, x := range nodes {
		o := x.(map[string]any)
		if n, ok := o["name"]; !ok || n != want[o["node_id"].(string)] {
			t.Fatalf("%v: name = %v (present %v)", o["node_id"], n, ok)
		}
	}
}
