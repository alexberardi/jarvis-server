//go:build contract

package contract

import (
	"net/http"
	"testing"
)

// Command-center node routes not covered elsewhere (docs/cc/05 §2.1, §3.1, §3.8): the
// provisioning-token flow (mobile mints, the node redeems), admin PATCH and the mobile node
// list, the release passthrough, and node update tasks (request → heartbeat dispatch → node
// refusal / user cancel).

// provisionedNode wraps a node created through /nodes/register so the standard CC cleanup
// (owner DELETE, auth deactivate as backstop) removes it.
func provisionedNode(t *testing.T, owner *User, id, key, room string) *CCNode {
	t.Helper()
	tg := T(t)
	n := &CCNode{
		Node:  Node{ID: id, Key: key, HouseholdID: owner.HouseholdID, Services: []string{"jarvis-logs"}},
		Owner: owner,
		Room:  room,
	}
	t.Cleanup(func() {
		if err := tg.deleteCCNode(n); err != nil {
			t.Errorf("cleanup provisioned node %s: %v", id, err)
		}
	})
	return n
}

var provisioningTokenShape = Obj{
	"token":      Regexp(`^prov_[A-Za-z0-9_-]{43}$`),
	"node_id":    NonEmptyString,
	"expires_at": TimestampNaive, // LEGACY-BUG: naive (datetime.utcnow()), as elsewhere in CC.
	"expires_in": Eq(600),
}

var nodeCreateShape = Obj{
	"node_id":    NonEmptyString,
	"room":       String,
	"user":       Eq("default"),
	"voice_mode": Eq("brief"),
	"node_key":   NonEmptyString,
}

func TestCCProvisioning(t *testing.T) {
	tg := T(t)
	tg.NeedCCAdmin(t)
	u := NewUser(t)
	const tokPath = "/api/v0/provisioning/token"
	const regPath = "/api/v0/nodes/register"

	t.Run("auth", func(t *testing.T) {
		body := map[string]any{"household_id": u.HouseholdID}
		tg.Post(t, CommandCenter, tokPath, body).ExpectError(http.StatusUnauthorized, "Authentication required")
		// The admin key is tried first; a wrong one is final even with a valid JWT.
		tg.Post(t, CommandCenter, tokPath, body, H{"X-API-Key": "wrong-" + randHex(4)}, u.H()).
			ExpectError(http.StatusUnauthorized, "Invalid API key")
		tg.Post(t, CommandCenter, tokPath, body, Bearer("not.a.jwt")).
			ExpectError(http.StatusUnauthorized, "Invalid or expired JWT")
		tg.Post(t, CommandCenter, tokPath, map[string]any{}, CCAdminH()).
			Expect(http.StatusBadRequest, ccValidation("body -> household_id: "))
	})

	t.Run("jwt_token_and_register", func(t *testing.T) {
		tok := tg.Post(t, CommandCenter, tokPath, map[string]any{
			"household_id": u.HouseholdID, "room": "contract-token-room", "name": "contract provisioned",
		}, u.H()).Expect(http.StatusCreated, provisioningTokenShape).Object()
		nid, raw := tok["node_id"].(string), tok["token"].(string)
		if errs := UUID.Match("node_id", nid); len(errs) > 0 {
			t.Fatalf("minted node_id: %v", errs)
		}

		tg.Post(t, CommandCenter, regPath, map[string]any{"node_id": nid, "provisioning_token": raw + "x"}).
			ExpectError(http.StatusUnauthorized, "Invalid or expired provisioning token")
		tg.Post(t, CommandCenter, regPath, map[string]any{"node_id": "contract-other", "provisioning_token": raw}).
			ExpectError(http.StatusUnauthorized, "Invalid or expired provisioning token")

		// Room precedence: body > token > "default". No body room here: the token's.
		reg := tg.Post(t, CommandCenter, regPath, map[string]any{"node_id": nid, "provisioning_token": raw}).
			Expect(http.StatusCreated, nodeCreateShape).Object()
		n := provisionedNode(t, u, nid, reg["node_key"].(string), "contract-token-room")
		if reg["node_id"] != nid || reg["room"] != "contract-token-room" {
			t.Fatalf("register: %v", reg)
		}
		// The key works at once, and the node lands in the token's household.
		tg.Get(t, CommandCenter, "/api/v0/node/mqtt-credentials", n.APIKeyH()).ExpectStatus(http.StatusOK)
		got := tg.Get(t, CommandCenter, "/api/v0/admin/nodes/"+nid, u.H()).Expect(http.StatusOK, nodeResponseShape()).Object()
		if got["household_id"] != u.HouseholdID || got["room"] != "contract-token-room" {
			t.Fatalf("registered node: %v", got)
		}

		// One-time: the token is consumed.
		tg.Post(t, CommandCenter, regPath, map[string]any{"node_id": nid, "provisioning_token": raw}).
			ExpectError(http.StatusUnauthorized, "Invalid or expired provisioning token")
		// A refresh for a registered node is refused.
		tg.Post(t, CommandCenter, tokPath, map[string]any{"household_id": u.HouseholdID, "node_id": nid}, u.H()).
			ExpectError(http.StatusBadRequest, "Node already registered")
	})

	t.Run("admin_token_refresh_and_body_room", func(t *testing.T) {
		first := tg.Post(t, CommandCenter, tokPath, map[string]any{"household_id": u.HouseholdID}, CCAdminH()).
			Expect(http.StatusCreated, provisioningTokenShape).Object()
		nid := first["node_id"].(string)
		// Refresh: same node id, and the earlier unconsumed token is invalidated.
		second := tg.Post(t, CommandCenter, tokPath, map[string]any{"household_id": u.HouseholdID, "node_id": nid, "room": "token-room"}, CCAdminH()).
			Expect(http.StatusCreated, provisioningTokenShape).Object()
		if second["node_id"] != nid || second["token"] == first["token"] {
			t.Fatalf("refresh: %v after %v", second, first)
		}
		tg.Post(t, CommandCenter, regPath, map[string]any{"node_id": nid, "provisioning_token": first["token"]}).
			ExpectError(http.StatusUnauthorized, "Invalid or expired provisioning token")

		reg := tg.Post(t, CommandCenter, regPath, map[string]any{
			"node_id": nid, "provisioning_token": second["token"], "room": "body-room",
		}).Expect(http.StatusCreated, nodeCreateShape).Object()
		provisionedNode(t, u, nid, reg["node_key"].(string), "body-room")
		if reg["room"] != "body-room" {
			t.Fatalf("body room should win: %v", reg)
		}
	})

	t.Run("default_room", func(t *testing.T) {
		tok := tg.Post(t, CommandCenter, tokPath, map[string]any{"household_id": u.HouseholdID}, u.H()).
			Expect(http.StatusCreated, provisioningTokenShape).Object()
		nid := tok["node_id"].(string)
		reg := tg.Post(t, CommandCenter, regPath, map[string]any{"node_id": nid, "provisioning_token": tok["token"]}).
			Expect(http.StatusCreated, nodeCreateShape).Object()
		provisionedNode(t, u, nid, reg["node_key"].(string), "default")
		if reg["room"] != "default" {
			t.Fatalf("no room anywhere should be \"default\": %v", reg)
		}
	})

	t.Run("foreign_household", func(t *testing.T) {
		stranger := NewUser(t)
		body := map[string]any{"household_id": u.HouseholdID}
		// LEGACY-BUG: create_provisioning_token never calls require_household_access, so any
		// signed-in user can mint a token for any household (doc 05 §3.1, §8). D4/D5: Go
		// checks the caller is a member of the TARGET household.
		if Jarvisd() {
			tg.Post(t, CommandCenter, tokPath, body, stranger.H()).
				ExpectError(http.StatusForbidden, "User is not a member of this household")
			return
		}
		// The minted token is never redeemed; the hourly cleanup removes it.
		tg.Post(t, CommandCenter, tokPath, body, stranger.H()).Expect(http.StatusCreated, provisioningTokenShape)
	})
}

func TestCCAdminNodesPatchAndList(t *testing.T) {
	tg := T(t)
	u := NewUser(t)
	n := NewCCNode(t, u)
	path := "/api/v0/admin/nodes/" + n.ID

	t.Run("patch", func(t *testing.T) {
		got := tg.Do(t, CommandCenter, http.MethodPatch, path, map[string]any{"room": "contract-kitchen", "voice_mode": "verbose"}, CCAdminH()).
			Expect(http.StatusOK, nodeResponseShape()).Object()
		if got["node_id"] != n.ID || got["room"] != "contract-kitchen" || got["voice_mode"] != "verbose" ||
			got["user"] != "default" || got["household_id"] != u.HouseholdID {
			t.Fatalf("patched node: %v", got)
		}
		// Unset fields are untouched.
		got = tg.Do(t, CommandCenter, http.MethodPatch, path, map[string]any{"user": "contract-user"}, CCAdminH()).
			Expect(http.StatusOK, nodeResponseShape()).Object()
		if got["room"] != "contract-kitchen" || got["user"] != "contract-user" {
			t.Fatalf("partial patch: %v", got)
		}
		got = tg.Get(t, CommandCenter, path, u.H()).Expect(http.StatusOK, nodeResponseShape()).Object()
		if got["room"] != "contract-kitchen" || got["voice_mode"] != "verbose" || got["user"] != "contract-user" {
			t.Fatalf("patch not persisted: %v", got)
		}
		tg.Do(t, CommandCenter, http.MethodPatch, "/api/v0/admin/nodes/contract-nosuch-"+randHex(3), map[string]any{"room": "x"}, CCAdminH()).
			ExpectError(http.StatusNotFound, "Node not found")
		tg.Do(t, CommandCenter, http.MethodPatch, path, map[string]any{"room": "x"}, H{"X-API-Key": "wrong-admin-key"}).
			ExpectError(http.StatusUnauthorized, "Invalid Admin API Key")
	})

	t.Run("list", func(t *testing.T) {
		has := func(r *Resp) bool {
			for _, v := range r.JSON().([]any) {
				if v.(map[string]any)["node_id"] == n.ID {
					return true
				}
			}
			return false
		}
		for _, q := range []string{"", "?household_id=" + u.HouseholdID, "?household_id=" + u.HouseholdID + "&include_inactive=true"} {
			r := tg.Get(t, CommandCenter, "/api/v0/admin/nodes"+q, u.H()).Expect(http.StatusOK, ArrayOf(nodeResponseShape()))
			if !has(r) {
				r.Fatalf("list%s is missing node %s", q, n.ID)
			}
		}
		// Scoped to the caller's households: a stranger doesn't see the node, and naming the
		// household is a 403.
		stranger := NewUser(t)
		if r := tg.Get(t, CommandCenter, "/api/v0/admin/nodes", stranger.H()).Expect(http.StatusOK, ArrayOf(nodeResponseShape())); has(r) {
			r.Fatalf("a non-member sees node %s", n.ID)
		}
		tg.Get(t, CommandCenter, "/api/v0/admin/nodes?household_id="+u.HouseholdID, stranger.H()).
			ExpectError(http.StatusForbidden, "User is not a member of this household")
		tg.Get(t, CommandCenter, "/api/v0/admin/nodes").
			ExpectError(http.StatusUnauthorized, "Missing or invalid Authorization header")
	})
}

var latestReleaseShape = Obj{"tag": NonEmptyString, "version": NonEmptyString, "published_at": NullOr(String)}

func TestCCReleasesLatest(t *testing.T) {
	tg := T(t)
	tg.Need(t, CommandCenter)
	// Unauthenticated passthrough to node-setup's latest GitHub release; JSON null when the
	// check is disabled (updates.allow_check defaults off) or GitHub is unreachable.
	tg.Get(t, CommandCenter, "/api/v0/releases/latest").Expect(http.StatusOK, NullOr(latestReleaseShape))
}

var nodeTaskShape = Obj{
	"id":             UUID,
	"node_id":        NonEmptyString,
	"kind":           Eq("update"),
	"target_version": NullOr(String),
	"state":          OneOf("pending", "dispatched", "in_progress", "success", "failed"),
	"error_message":  NullOr(String),
	// LEGACY-BUG: naive timestamps.
	"created_at":  TimestampNaive,
	"updated_at":  TimestampNaive,
	"finished_at": NullOr(TimestampNaive),
}

func TestCCNodeUpdateTasks(t *testing.T) {
	tg := T(t)
	u := NewUser(t)
	n := NewCCNode(t, u)
	other := NewCCNode(t, u)
	stranger := NewUser(t)
	update := "/api/v0/nodes/" + n.ID + "/update"

	// An explicit version is used as-is (leading v stripped), with no GitHub egress.
	task := tg.Post(t, CommandCenter, update, map[string]any{"target_version": "v99.0.0"}, u.H()).
		Expect(http.StatusOK, nodeTaskShape).Object()
	tid := task["id"].(string)
	if task["node_id"] != n.ID || task["target_version"] != "99.0.0" || task["state"] != "pending" ||
		task["error_message"] != nil || task["finished_at"] != nil {
		t.Fatalf("new task: %v", task)
	}
	// One open update per node: 409 with a structured detail.
	tg.Post(t, CommandCenter, update, map[string]any{"target_version": "v99.0.1"}, u.H()).Expect(http.StatusConflict, Obj{
		"detail": Obj{"message": Eq("An update is already queued for this node."), "task_id": Eq(tid), "state": Eq("pending")},
	})
	tg.Post(t, CommandCenter, "/api/v0/nodes/contract-nosuch-"+randHex(3)+"/update", map[string]any{"target_version": "v1.0.0"}, u.H()).
		ExpectError(http.StatusNotFound, "Node not found")
	tg.Post(t, CommandCenter, update, map[string]any{"target_version": "v99.0.0"}, stranger.H()).ExpectStatus(http.StatusForbidden)

	tg.Get(t, CommandCenter, "/api/v0/tasks/"+tid, u.H()).Expect(http.StatusOK, with(nodeTaskShape, "id", Eq(tid), "state", Eq("pending")))
	tg.Get(t, CommandCenter, "/api/v0/tasks/"+tid, stranger.H()).ExpectStatus(http.StatusForbidden)
	tg.Get(t, CommandCenter, "/api/v0/tasks/00000000-0000-4000-8000-000000000000", u.H()).ExpectError(http.StatusNotFound, "Task not found")

	// The heartbeat is the dispatch channel: a non-busy node gets pending_update once, and the
	// task moves to dispatched.
	tg.Post(t, CommandCenter, "/api/v0/admin/nodes/heartbeat", map[string]any{"is_busy": false}, n.APIKeyH()).Expect(http.StatusOK, Obj{
		"status": Eq("ok"), "pending_update": Obj{"task_id": Eq(tid), "target_version": Eq("99.0.0")},
	})
	tg.Post(t, CommandCenter, "/api/v0/admin/nodes/heartbeat", map[string]any{"is_busy": false}, n.APIKeyH()).
		Expect(http.StatusOK, Obj{"status": Eq("ok")})
	tg.Get(t, CommandCenter, "/api/v0/tasks/"+tid, u.H()).Expect(http.StatusOK, with(nodeTaskShape, "state", Eq("dispatched")))

	// The node may only fail its own update tasks, and only with "failed".
	status := "/api/v0/nodes/tasks/" + tid + "/status"
	tg.Post(t, CommandCenter, status, map[string]any{"state": "failed"}, other.APIKeyH()).ExpectError(http.StatusNotFound, "Task not found")
	tg.Post(t, CommandCenter, status, map[string]any{"state": "success"}, n.APIKeyH()).
		Expect(http.StatusBadRequest, ccValidation("body -> state: "))
	tg.Post(t, CommandCenter, status, map[string]any{"state": "failed", "error_message": "contract: updates disabled"}, n.APIKeyH()).
		Expect(http.StatusOK, with(nodeTaskShape, "id", Eq(tid), "state", Eq("failed"),
			"error_message", Eq("contract: updates disabled"), "finished_at", TimestampNaive))
	tg.Post(t, CommandCenter, status, map[string]any{"state": "failed"}, n.APIKeyH()).
		ExpectError(http.StatusConflict, "Task is already failed")

	// A user cancel marks the open task failed with a fixed message.
	t2 := tg.Post(t, CommandCenter, update, map[string]any{"target_version": "99.0.2"}, u.H()).
		Expect(http.StatusOK, with(nodeTaskShape, "target_version", Eq("99.0.2"), "state", Eq("pending"))).Object()
	tid2 := t2["id"].(string)
	tg.Post(t, CommandCenter, "/api/v0/nodes/"+other.ID+"/tasks/"+tid2+"/cancel", nil, u.H()).ExpectError(http.StatusNotFound, "Task not found")
	tg.Post(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/tasks/"+tid2+"/cancel", nil, u.H()).
		Expect(http.StatusOK, with(nodeTaskShape, "id", Eq(tid2), "state", Eq("failed"),
			"error_message", Eq("Cancelled by user"), "finished_at", TimestampNaive))
	tg.Post(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/tasks/"+tid2+"/cancel", nil, u.H()).
		ExpectError(http.StatusConflict, "Task is already failed")

	// History, newest first, limit clamped to 1–100.
	list := tg.Get(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/tasks", u.H()).Expect(http.StatusOK, ArrayOf(nodeTaskShape)).JSON().([]any)
	if len(list) != 2 || list[0].(map[string]any)["id"] != tid2 || list[1].(map[string]any)["id"] != tid {
		t.Fatalf("task history: %v", list)
	}
	list = tg.Get(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/tasks?limit=1", u.H()).Expect(http.StatusOK, ArrayOf(nodeTaskShape)).JSON().([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != tid2 {
		t.Fatalf("limit=1: %v", list)
	}
	if got := tg.Get(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/tasks?limit=0", u.H()).Expect(http.StatusOK, ArrayOf(nodeTaskShape)).JSON().([]any); len(got) != 1 {
		t.Fatalf("limit=0 should clamp to 1: %v", got)
	}
	tg.Get(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/tasks", stranger.H()).ExpectStatus(http.StatusForbidden)
	tg.Get(t, CommandCenter, "/api/v0/nodes/contract-nosuch-"+randHex(3)+"/tasks", u.H()).ExpectError(http.StatusNotFound, "Node not found")
}
