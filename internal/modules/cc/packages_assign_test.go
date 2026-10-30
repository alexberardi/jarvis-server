package cc

import (
	"context"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

const assignBase = "/api/v0/households/hh1/package-assignments"

const weatherRepo = "https://github.com/acme/weather.git"

type assignEnv struct {
	*env
	pantry        *fakePantry
	admin, member hdr
}

func newAssignEnv(t *testing.T) *assignEnv {
	t.Helper()
	e := newEnv(t, envOpts{noMQTT: true})
	pantry := newFakePantry(t)
	setPantry(t, e, "hh1", pantry.URL)
	enablePantry(t, e, "hh1", true)
	pantry.set(func() {
		pantry.commands["weather"] = `{"command_name":"weather","github_repo_url":"` + weatherRepo + `","apt_packages":[]}`
		pantry.commands["weather/versions"] = `{"command_name":"weather","versions":[{"version":"1.2.0","git_tag":"v1.2.0"}]}`
	})
	return &assignEnv{env: e, pantry: pantry,
		admin:  bearer(e.auth.addUser(1, "hh1", authn.RoleAdmin)),
		member: bearer(e.auth.addUser(2, "hh1", authn.RoleMember))}
}

// online makes a node reachable (heard from now), as a heartbeat does.
func (a *assignEnv) online(ids ...string) {
	a.t.Helper()
	for _, id := range ids {
		if _, err := a.d.Write.Exec(`UPDATE cc_nodes SET last_seen = ?, contacted = 1 WHERE node_id = ?`, dbTime(a.clock()), id); err != nil {
			a.t.Fatal(err)
		}
	}
}

// statuses maps node id to its status in the weather assignment.
func (a *assignEnv) statuses() map[string]string {
	a.t.Helper()
	out := map[string]string{}
	for _, as := range a.do("GET", assignBase, nil, a.member).want(200).json()["assignments"].([]any) {
		for _, n := range as.(map[string]any)["nodes"].([]any) {
			n := n.(map[string]any)
			out[n["node_id"].(string)] = n["status"].(string)
		}
	}
	return out
}

func (a *assignEnv) requests() int {
	a.t.Helper()
	var n int
	if err := a.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_package_install_requests`).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

func (a *assignEnv) put(body map[string]any, h hdr) *resp {
	return a.do("PUT", assignBase+"/weather", body, h)
}

func sameStatuses(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("statuses %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("statuses %v, want %v", got, want)
		}
	}
}

func TestPackageAssignmentSelectedNodes(t *testing.T) {
	a := newAssignEnv(t)
	n1, _ := a.createNode("n1", "hh1"), a.createNode("n2", "hh1")
	a.createNode("other", "hh2")
	a.online("n1", "n2")
	body := map[string]any{"github_repo_url": weatherRepo, "node_ids": []string{"n1"}}

	// Only a household admin changes it; any member reads it.
	a.put(body, a.member).detail(403, "User has member role, requires admin or higher")
	a.put(map[string]any{"github_repo_url": weatherRepo}, a.admin).want(400) // neither nodes nor all_nodes
	a.put(map[string]any{"github_repo_url": weatherRepo, "node_ids": []string{"other"}}, a.admin).want(404)
	a.put(map[string]any{"github_repo_url": "https://github.com/evil/weather", "node_ids": []string{"n1"}}, a.admin).want(422)
	if a.requests() != 0 {
		t.Fatal("a refused assignment made a request")
	}

	j := a.put(body, a.admin).want(200).json()
	if j["command_name"] != "weather" || j["all_nodes"] != false {
		t.Fatal(j)
	}
	sameStatuses(t, a.statuses(), map[string]string{"n1": "installing"})
	if a.requests() != 1 {
		t.Fatal(a.requests())
	}

	// The node does what it's told: the assignment reports installed, with no second request.
	rid := j["nodes"].([]any)[0].(map[string]any)["request_id"].(string)
	a.do("GET", "/api/v0/nodes/n1/package-install/"+rid+"/verify", nil, n1.h()).want(200)
	a.do("POST", "/api/v0/nodes/n1/package-install/"+rid+"/results", map[string]any{"success": true}, n1.h()).want(200)
	a.put(body, a.admin).want(200)
	sameStatuses(t, a.statuses(), map[string]string{"n1": "installed"})
	if a.requests() != 1 {
		t.Fatal("an unchanged assignment installed again")
	}

	// Moving it to n2 drops n1 from the assignment (n1 keeps the package) and installs on n2.
	a.put(map[string]any{"github_repo_url": weatherRepo, "node_ids": []string{"n2"}}, a.admin).want(200)
	sameStatuses(t, a.statuses(), map[string]string{"n2": "installing"})
}

func TestPackageAssignmentRefusedWhenPantryOff(t *testing.T) {
	a := newAssignEnv(t)
	a.createNode("n1", "hh1")
	a.online("n1")
	enablePantry(t, a.env, "hh1", false)
	a.put(map[string]any{"github_repo_url": weatherRepo, "all_nodes": true}, a.admin).want(403)
	if a.requests() != 0 {
		t.Fatal("installed with the Pantry off")
	}
}

func TestPackageAssignmentAllNodesAndSweep(t *testing.T) {
	a := newAssignEnv(t)
	n1 := a.createNode("n1", "hh1")
	a.createNode("n2", "hh1")
	a.online("n1") // n2 has never connected
	a.put(map[string]any{"github_repo_url": weatherRepo, "all_nodes": true}, a.admin).want(200)
	sameStatuses(t, a.statuses(), map[string]string{"n1": "installing", "n2": "waiting"})

	// n2 comes online and a node is paired later: the hourly pass installs both, n1 stays alone.
	a.createNode("n3", "hh1")
	a.online("n2", "n3")
	if err := a.m.cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	sameStatuses(t, a.statuses(), map[string]string{"n1": "installing", "n2": "installing", "n3": "installing"})
	if a.requests() != 3 {
		t.Fatal(a.requests())
	}

	// A failed install is tried again, but not within the hour and not more than three times in all.
	fail := func() {
		var rid string
		if err := a.d.Read.QueryRow(`SELECT request_id FROM cc_package_assignment_nodes WHERE node_id = 'n1'`).Scan(&rid); err != nil {
			t.Fatal(err)
		}
		a.do("GET", "/api/v0/nodes/n1/package-install/"+rid+"/verify", nil, n1.h()).want(200)
		a.do("POST", "/api/v0/nodes/n1/package-install/"+rid+"/results", map[string]any{"success": false, "error": "pip failed"}, n1.h()).want(200)
	}
	reconcileLater := func(d time.Duration) {
		a.advance(d)
		a.online("n1", "n2", "n3")
		a.m.reconcileAssignments(context.Background(), "")
	}
	fail()
	sameStatuses(t, a.statuses(), map[string]string{"n1": "failed", "n2": "installing", "n3": "installing"})
	reconcileLater(time.Minute)
	if a.requests() != 3 {
		t.Fatalf("retried within the hour: %d requests", a.requests())
	}
	reconcileLater(2 * time.Hour) // attempt 2
	if a.requests() != 6 {        // n1 failed; n2 and n3 never picked theirs up in the hours since
		t.Fatalf("no retry after the hour: %d requests", a.requests())
	}
	fail()
	reconcileLater(2 * time.Hour) // attempt 3
	if a.requests() != 9 {
		t.Fatalf("third attempt: %d requests", a.requests())
	}
	fail()
	reconcileLater(2 * time.Hour)
	if a.requests() != 9 {
		t.Fatalf("a fourth attempt was made: %d requests", a.requests())
	}
	// n2 and n3 never picked theirs up: a request past its deadline counts as failed, and they were retried the same way.
	sameStatuses(t, a.statuses(), map[string]string{"n1": "failed", "n2": "failed", "n3": "failed"})
}

func TestPackageAssignmentChangeAndDelete(t *testing.T) {
	a := newAssignEnv(t)
	n1 := a.createNode("n1", "hh1")
	a.createNode("n2", "hh1")
	a.online("n1", "n2")
	body := map[string]any{"github_repo_url": weatherRepo, "git_tag": "v1.0.0", "all_nodes": true}
	j := a.put(body, a.admin).want(200).json()
	rid := j["nodes"].([]any)[0].(map[string]any)["request_id"].(string)
	a.do("GET", "/api/v0/nodes/n1/package-install/"+rid+"/verify", nil, n1.h()).want(200)
	a.do("POST", "/api/v0/nodes/n1/package-install/"+rid+"/results", map[string]any{"success": true}, n1.h()).want(200)

	// A new version installs again on every node.
	body["git_tag"] = "v1.2.0"
	a.put(body, a.admin).want(200)
	if a.requests() != 4 {
		t.Fatalf("requests %d", a.requests())
	}

	// Deleting keeps the package on the nodes unless asked; ?uninstall asks the nodes that finished.
	a.do("DELETE", assignBase+"/weather", nil, a.member).want(403)
	a.do("DELETE", assignBase+"/weather", nil, a.admin).want(200)
	a.do("DELETE", assignBase+"/weather", nil, a.admin).want(404)
	if a.requests() != 4 {
		t.Fatal("a plain delete asked nodes to uninstall")
	}
	a.put(map[string]any{"github_repo_url": weatherRepo, "node_ids": []string{"n1"}}, a.admin).want(200)
	var rid2 string
	_ = a.d.Read.QueryRow(`SELECT request_id FROM cc_package_assignment_nodes WHERE node_id = 'n1'`).Scan(&rid2)
	a.do("GET", "/api/v0/nodes/n1/package-install/"+rid2+"/verify", nil, n1.h()).want(200)
	a.do("POST", "/api/v0/nodes/n1/package-install/"+rid2+"/results", map[string]any{"success": true}, n1.h()).want(200)
	before := a.requests()
	a.do("DELETE", assignBase+"/weather?uninstall=true", nil, a.admin).want(200)
	if a.requests() != before+1 {
		t.Fatalf("uninstall requests %d -> %d", before, a.requests())
	}
}

func TestPackageAssignmentComputerNodes(t *testing.T) {
	a := newAssignEnv(t)
	a.createNode("desk", "hh1")
	a.createNode("pi", "hh1")
	a.online("desk", "pi")
	if _, err := a.d.Write.Exec(`UPDATE cc_nodes SET node_kind = 'desktop' WHERE node_id = 'desk'`); err != nil {
		t.Fatal(err)
	}
	// A Pi-only package is installed on the Pi and skipped, with a reason, on the computer.
	a.pantry.set(func() {
		a.pantry.commands["weather"] = `{"command_name":"weather","github_repo_url":"` + weatherRepo + `","targets":["pi"]}`
	})
	a.put(map[string]any{"github_repo_url": weatherRepo, "all_nodes": true}, a.admin).want(200)
	sameStatuses(t, a.statuses(), map[string]string{"pi": "installing", "desk": "skipped"})
	if a.requests() != 1 {
		t.Fatal(a.requests())
	}
}
