package cc

import (
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// 01-no-cli-plan 1.7: desktop agents follow this server's release. Their heartbeats carry
// server_version, and an update request for one targets this jarvisd's own version, with its v.
func TestDesktopAgentFollowsTheServer(t *testing.T) {
	e := newEnv(t)
	e.m.Version = "v0.2.0"
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.pairDesktop(owner, "hh1") // reports version "dev"
	beat := func(version string) map[string]any {
		return e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{
			"version_info": map[string]any{"version": version, "install_mode": "desktop-agent"}, "node_kind": "desktop",
		}, n.h()).want(200).json()
	}
	if r := beat("dev"); r["server_version"] != "v0.2.0" || r["pending_update"] != nil {
		t.Fatalf("desktop heartbeat: %v", r)
	}
	// A Pi's answer is unchanged (strict contract shape).
	pi := e.createNode("pi1", "hh1")
	if r := e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"is_busy": false}, pi.h()).want(200).json(); len(r) != 1 {
		t.Fatalf("pi heartbeat: %v", r)
	}

	path := "/api/v0/nodes/" + n.id + "/update"
	// "latest" is this server's version, not the Pi node's GitHub release (no egress, no gate).
	task := e.do("POST", path, nil, bearer(owner)).want(200).json()
	if task["target_version"] != "v0.2.0" || task["state"] != "pending" {
		t.Fatal(task)
	}
	tid := task["id"].(string)
	r := beat("dev")
	if pu, _ := r["pending_update"].(map[string]any); pu == nil || pu["task_id"] != tid || pu["target_version"] != "v0.2.0" {
		t.Fatalf("dispatch: %v", r)
	}
	// The agent comes back on the target: success, whatever the v.
	e.advance(5 * time.Second)
	beat("v0.2.0")
	if s := e.do("GET", "/api/v0/tasks/"+tid, nil, bearer(owner)).json(); s["state"] != "success" {
		t.Fatal(s)
	}
	// Not newer than what it runs: refused up front.
	e.do("POST", path, map[string]any{"target_version": "0.2.0"}, bearer(owner)).detail(400,
		"Node is already on version v0.2.0; target v0.2.0 is not newer.")
	e.do("POST", path, map[string]any{"target_version": "v0.1.9"}, bearer(owner)).want(400)
	e.do("POST", path, map[string]any{"target_version": "nightly"}, bearer(owner)).detail(400, "nightly is not a release version.")
	// An explicit newer version is kept, with a v.
	if task := e.do("POST", path, map[string]any{"target_version": "0.3.0-rc1"}, bearer(owner)).want(200).json(); task["target_version"] != "v0.3.0-rc1" {
		t.Fatal(task)
	}
}

func TestDesktopAgentUpdateFromADevServer(t *testing.T) {
	e := newEnv(t)
	e.m.Version = "dev"
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.pairDesktop(owner, "hh1")
	e.do("POST", "/api/v0/nodes/"+n.id+"/update", nil, bearer(owner)).detail(503,
		"This server is a development build (dev): there is no release for its desktop agents to follow.")
}
