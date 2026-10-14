package cc

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// pairDesktop registers a node through a provisioning token minted by tok's user (the pairing
// approver or the member signing in) and makes it say it is a desktop agent.
func (e *env) pairDesktop(tok, hh string) testNode {
	e.t.Helper()
	pt := e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": hh, "room": "desktop"}, bearer(tok)).want(201).json()
	reg := e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": pt["node_id"], "provisioning_token": pt["token"]}, nil).want(201).json()
	n := testNode{id: reg["node_id"].(string), key: reg["node_key"].(string)}
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{
		"version_info": map[string]any{"version": "dev", "install_mode": "desktop-agent"}, "node_kind": "desktop",
	}, n.h()).want(200)
	return n
}

func TestRegisterRecordsTheOwner(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(7, "hh1", authn.RoleMember)
	n := e.pairDesktop(member, "hh1")
	got := e.do("GET", "/api/v0/admin/nodes/"+n.id, nil, bearer(member)).want(200).json()
	if got["owner_user_id"] != 7.0 {
		t.Fatalf("owner_user_id = %v, want 7", got["owner_user_id"])
	}
	// The admin key's node has no owner.
	e.createNode("pi1", "hh1")
	if got := e.do("GET", "/api/v0/admin/nodes/pi1", nil, bearer(member)).want(200).json(); got["owner_user_id"] != nil {
		t.Fatalf("admin-key node owner = %v", got["owner_user_id"])
	}
}

func TestSignOutWho(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	other := e.auth.addUser(2, "hh1", authn.RoleMember)
	power := e.auth.addUser(3, "hh1", authn.RolePowerUser)
	admin := e.auth.addUser(4, "hh1", authn.RoleAdmin)
	outsider := e.auth.addUser(5, "hh2", authn.RoleAdmin)

	n := e.pairDesktop(owner, "hh1")
	path := "/api/v0/nodes/" + n.id + "/sign-out"
	e.do("POST", path, nil, nil).want(401)
	e.do("POST", path, nil, bearer(outsider)).detail(403, "User is not a member of this household")
	e.do("POST", path, nil, bearer(other)).detail(403, errSignOutRole)
	e.do("POST", path, nil, bearer(power)).detail(403, errSignOutRole)
	e.do("POST", "/api/v0/nodes/nosuch/sign-out", nil, bearer(owner)).detail(404, "Node not found")
	if r := e.do("POST", path, nil, bearer(owner)).want(200).json(); r["node_id"] != n.id {
		t.Fatal(r)
	}
	if e.auth.active(n.id) {
		t.Fatal("credentials not revoked")
	}
	e.do("GET", "/api/v0/admin/nodes/"+n.id, nil, bearer(admin)).detail(404, "Node not found")
	e.do("POST", "/api/v0/admin/nodes/heartbeat", nil, n.h()).want(401)

	// A household admin may sign out someone else's device.
	n2 := e.pairDesktop(owner, "hh1")
	e.do("POST", "/api/v0/nodes/"+n2.id+"/sign-out", nil, bearer(admin)).want(200)

	// An owner who left the household can't.
	n3 := e.pairDesktop(other, "hh1")
	e.auth.mu.Lock()
	delete(e.auth.roles["hh1"], 2)
	e.auth.mu.Unlock()
	e.do("POST", "/api/v0/nodes/"+n3.id+"/sign-out", nil, bearer(other)).detail(403, "User is not a member of this household")
}

func TestSignOutDesktopGetsNoFactoryReset(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleAdmin)
	n := e.pairDesktop(owner, "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/" + n.id + "/#")

	e.do("POST", "/api/v0/admin/nodes/"+n.id+"/factory-reset", nil, bearer(owner)).detail(400, errDesktopNoReset)
	e.do("POST", "/api/v0/nodes/"+n.id+"/sign-out", nil, bearer(owner)).want(200)
	pk := c.next()
	p := payload(t, pk)
	if pk.TopicName != "jarvis/nodes/"+n.id+"/signed-out" || len(p) != 1 || p["node_id"] != n.id {
		t.Fatalf("got %s %v, want only signed-out", pk.TopicName, p)
	}
}

func TestDeleteDesktopGetsNoFactoryReset(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleAdmin)
	n := e.pairDesktop(owner, "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/" + n.id + "/#")
	e.do("DELETE", "/api/v0/admin/nodes/"+n.id, nil, bearer(owner)).want(200)
	if pk := c.next(); pk.TopicName != "jarvis/nodes/"+n.id+"/signed-out" {
		t.Fatalf("got %s, want signed-out", pk.TopicName)
	}
}

func TestSignOutPiKeepsFactoryReset(t *testing.T) {
	e := newEnv(t)
	admin := e.auth.addUser(1, "hh1", authn.RoleAdmin)
	n := e.createNode("pi1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/pi1/#")
	e.do("POST", "/api/v0/nodes/pi1/sign-out", nil, bearer(admin)).want(200)
	pk := c.next()
	if p := payload(t, pk); pk.TopicName != "jarvis/nodes/pi1/factory-reset" || p["request_id"] == nil {
		t.Fatalf("got %s %v, want the factory reset", pk.TopicName, p)
	}
	if e.auth.active("pi1") {
		t.Fatal("not revoked")
	}
}

func TestSelfSignOut(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.pairDesktop(owner, "hh1")
	e.do("POST", "/api/v0/node/sign-out", nil, nil).want(400)
	e.do("POST", "/api/v0/node/sign-out", nil, n.h()).want(200)
	if e.auth.active(n.id) {
		t.Fatal("not revoked")
	}
	e.do("POST", "/api/v0/node/sign-out", nil, n.h()).want(401)
	e.do("GET", "/api/v0/admin/nodes/"+n.id, nil, bearer(owner)).detail(404, "Node not found")
}
