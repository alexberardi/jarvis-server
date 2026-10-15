package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

const devA = "a1b2c3d4e5f60718a1b2c3d4e5f60718a1b2c3d4e5f60718a1b2c3d4e5f60718"

// enrollDesktop is the agent's enrolment: a token minted by tok's user, redeemed with the
// computer's device_id and node_kind (either may be empty: an older agent sends neither), then
// a heartbeat like the agent's.
func (e *env) enrollDesktop(tok, hh, deviceID string) testNode {
	e.t.Helper()
	pt := e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": hh, "room": "desktop"}, bearer(tok)).want(201).json()
	body := map[string]any{"node_id": pt["node_id"], "provisioning_token": pt["token"]}
	if deviceID != "" {
		body["device_id"], body["node_kind"] = deviceID, "desktop"
	}
	reg := e.do("POST", "/api/v0/nodes/register", body, nil).want(201).json()
	n := testNode{id: reg["node_id"].(string), key: reg["node_key"].(string)}
	beat := map[string]any{"version_info": map[string]any{"version": "dev", "install_mode": "desktop-agent"}, "node_kind": "desktop"}
	if deviceID != "" {
		beat["device_id"] = deviceID
	}
	e.do("POST", "/api/v0/admin/nodes/heartbeat", beat, n.h()).want(200)
	return n
}

func (e *env) nodeExists(id string) bool {
	e.t.Helper()
	var n int
	if err := e.d.Read.QueryRow(`SELECT count(*) FROM cc_nodes WHERE node_id = ?`, id).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n == 1
}

func (e *env) storedDeviceID(id string) string {
	e.t.Helper()
	var d *string
	if err := e.d.Read.QueryRow(`SELECT device_id FROM cc_nodes WHERE node_id = ?`, id).Scan(&d); err != nil {
		e.t.Fatal(err)
	}
	if d == nil {
		return ""
	}
	return *d
}

func TestReenrollReplacesTheComputersNode(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	old := e.enrollDesktop(owner, "hh1", devA)
	c := e.dialNode(old)
	c.subscribe("jarvis/nodes/" + old.id + "/#")

	n := e.enrollDesktop(owner, "hh1", devA)
	if e.nodeExists(old.id) || e.auth.active(old.id) {
		t.Fatalf("old node still there (row %v, key active %v)", e.nodeExists(old.id), e.auth.active(old.id))
	}
	if !e.nodeExists(n.id) || !e.auth.active(n.id) || e.storedDeviceID(n.id) != devA {
		t.Fatal("the new node isn't intact")
	}
	// Removed the way Sign out removes a desktop: told "signed-out", never a factory reset.
	pub := c.next()
	if !strings.HasSuffix(pub.TopicName, "/signed-out") {
		t.Fatalf("old node got %s", pub.TopicName)
	}
	e.do("POST", "/api/v0/admin/nodes/heartbeat", nil, old.h()).want(401)
	list := e.do("GET", "/api/v0/admin/nodes?household_id=hh1", nil, bearer(owner)).want(200).list()
	if len(list) != 1 || list[0].(map[string]any)["node_id"] != n.id {
		t.Fatalf("nodes after re-enrolling: %v", list)
	}
	// A third enrolment replaces the second.
	n3 := e.enrollDesktop(owner, "hh1", devA)
	if e.nodeExists(n.id) || !e.nodeExists(n3.id) {
		t.Fatal("third enrolment didn't replace the second")
	}
}

func TestReenrollNeverCrossesHouseholdsOrOwners(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleAdmin)
	e.auth.addUser(1, "hh2", authn.RoleMember) // the same member also belongs to hh2
	other := e.auth.addUser(2, "hh1", authn.RoleAdmin)
	outsider := e.auth.addUser(3, "hh3", authn.RoleAdmin)

	mine := e.enrollDesktop(owner, "hh1", devA)
	// Same computer id in another of the owner's households: a separate node, both stay.
	inHH2 := e.enrollDesktop(owner, "hh2", devA)
	// Another member (even a household admin) sending the same id can't evict the owner's node.
	theirs := e.enrollDesktop(other, "hh1", devA)
	// Nor can someone in another household.
	outside := e.enrollDesktop(outsider, "hh3", devA)
	for _, n := range []testNode{mine, inHH2, theirs, outside} {
		if !e.nodeExists(n.id) || !e.auth.active(n.id) {
			t.Fatalf("node %s was removed", n.id)
		}
	}
}

func TestReenrollLeavesPisAndOwnerlessNodes(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)

	// A Pi the same member paired, which (somehow) reports the same device_id but no node_kind.
	pt := e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh1"}, bearer(owner)).want(201).json()
	reg := e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": pt["node_id"], "provisioning_token": pt["token"], "device_id": devA}, nil).want(201).json()
	pi := testNode{id: reg["node_id"].(string), key: reg["node_key"].(string)}
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"version_info": map[string]any{"version": "1", "install_mode": "tarball"}, "device_id": devA}, pi.h()).want(200)
	if e.storedDeviceID(pi.id) != devA {
		t.Fatal("device_id not stored")
	}

	// A desktop added with the admin key has no owner, so nothing it registers retires anything,
	// and no registration retires it.
	apt := e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh1"}, adminH()).want(201).json()
	areg := e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": apt["node_id"], "provisioning_token": apt["token"], "device_id": devA, "node_kind": "desktop"}, nil).want(201).json()
	ownerless := areg["node_id"].(string)

	n := e.enrollDesktop(owner, "hh1", devA)
	for _, id := range []string{pi.id, ownerless, n.id} {
		if !e.nodeExists(id) {
			t.Fatalf("node %s was removed", id)
		}
	}

	// A registration that doesn't say it is a desktop retires nothing, even with the id.
	pt = e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh1"}, bearer(owner)).want(201).json()
	e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": pt["node_id"], "provisioning_token": pt["token"], "device_id": devA}, nil).want(201)
	if !e.nodeExists(n.id) {
		t.Fatal("a non-desktop registration retired the desktop")
	}
}

func TestReenrollLegacyAgent(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	// An agent that predates device_id: every enrolment adds a node, as before.
	a := e.enrollDesktop(owner, "hh1", "")
	b := e.enrollDesktop(owner, "hh1", "")
	if !e.nodeExists(a.id) || !e.nodeExists(b.id) || e.storedDeviceID(a.id) != "" {
		t.Fatal("legacy enrolments changed")
	}
	// An upgraded agent reports its id in heartbeats, so the next enrolment replaces that node
	// (and only that one: b never reported an id).
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"node_kind": "desktop", "device_id": devA}, a.h()).want(200)
	n := e.enrollDesktop(owner, "hh1", devA)
	if e.nodeExists(a.id) || !e.nodeExists(b.id) || !e.nodeExists(n.id) {
		t.Fatalf("after the upgrade: a=%v b=%v new=%v", e.nodeExists(a.id), e.nodeExists(b.id), e.nodeExists(n.id))
	}
}

func TestDeviceIDValidation(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.enrollDesktop(owner, "hh1", devA)
	// Malformed ids are ignored, never stored or used to clear the one on file.
	for _, bad := range []string{"short", strings.Repeat("a", 129), "has spaces in it ok", "../../etc/passwd/xx"} {
		e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"device_id": bad}, n.h()).want(200)
	}
	if e.storedDeviceID(n.id) != devA {
		t.Fatalf("device_id = %q", e.storedDeviceID(n.id))
	}
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"device_id": 5}, n.h()).want(400) // CC's validation status
	// The response never shows it.
	got := e.do("GET", "/api/v0/admin/nodes/"+n.id, nil, bearer(owner)).want(200).json()
	if _, has := got["device_id"]; has {
		t.Fatal("device_id is in the node response")
	}
}
