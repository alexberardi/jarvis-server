//go:build contract

package contract

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// jarvis-auth internal.py beyond app-ping and validate-node (auth_test.go): app-to-app node
// lifecycle, household/node ownership checks and the user batch lookup. All take app
// credentials (X-Jarvis-App-Id/Key) and answer 401 "Missing app credentials" without them.

func TestAuthInternalAppAuth(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	routes := []struct{ method, path string }{
		{http.MethodPost, "/internal/nodes/register"},
		{http.MethodPost, "/internal/nodes/x/services"},
		{http.MethodDelete, "/internal/nodes/x/services/y"},
		{http.MethodDelete, "/internal/nodes/x"},
		{http.MethodPost, "/internal/validate-household-access"},
		{http.MethodPost, "/internal/validate-node-household"},
		{http.MethodGet, "/internal/users/batch?user_ids=1"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			tg.Do(t, Auth, rt.method, rt.path, nil).ExpectError(http.StatusUnauthorized, "Missing app credentials")
			tg.Do(t, Auth, rt.method, rt.path, nil, H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong"}).
				ExpectError(http.StatusUnauthorized, "Invalid app credentials")
			// A user JWT is not an app credential.
			tg.Do(t, Auth, rt.method, rt.path, nil, SharedSuperuser(t).H()).
				ExpectError(http.StatusUnauthorized, "Missing app credentials")
		})
	}
}

func TestAuthInternalNodes(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	u := NewUser(t)
	id := fmt.Sprintf("contract-%s-node-int", tg.RunID)

	r := tg.Post(t, Auth, "/internal/nodes/register", map[string]any{
		"node_id": id, "household_id": u.HouseholdID, "name": "contract internal node",
		"registered_by_user_id": u.ID, "services": []string{"jarvis-logs"},
	}, app.H()).Expect(http.StatusCreated, Obj{"node_id": Eq(id), "node_key": NonEmptyString})
	n := &Node{ID: id, Key: r.Object()["node_key"].(string), HouseholdID: u.HouseholdID}
	t.Cleanup(func() {
		if err := tg.deactivateNode(n); err != nil {
			t.Errorf("cleanup node: %v", err)
		}
	})
	// The calling app is granted automatically, alongside the listed services.
	expectNodeValidity(t, app, n, n.Key, app.ID, "")
	expectNodeValidity(t, app, n, n.Key, "jarvis-logs", "")
	tg.Get(t, Auth, "/admin/nodes/"+id, tg.AdminH()).Expect(http.StatusOK, Open{"registered_by_user_id": Eq(u.ID)})

	t.Run("register errors", func(t *testing.T) {
		body := func(nodeID, hh string, by any) map[string]any {
			return map[string]any{"node_id": nodeID, "household_id": hh, "name": "x", "registered_by_user_id": by}
		}
		tg.Post(t, Auth, "/internal/nodes/register", body(id, u.HouseholdID, nil), app.H()).
			ExpectError(http.StatusBadRequest, "node_id already exists")
		tg.Post(t, Auth, "/internal/nodes/register", body(id+"-x", "00000000-0000-4000-8000-000000000000", nil), app.H()).
			ExpectError(http.StatusNotFound, "Household not found")
		tg.Post(t, Auth, "/internal/nodes/register", body(id+"-x", u.HouseholdID, 2147483000), app.H()).
			ExpectError(http.StatusNotFound, "User not found")
		tg.Post(t, Auth, "/internal/nodes/register", map[string]any{"node_id": id + "-x", "household_id": u.HouseholdID}, app.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "name"))
	})

	t.Run("services", func(t *testing.T) {
		svc := "contract-" + tg.RunID + "-isvc"
		tg.Post(t, Auth, "/internal/nodes/"+id+"/services", map[string]string{"service_id": svc}, app.H()).
			Expect(http.StatusCreated, Obj{"node_id": Eq(id), "service_id": Eq(svc), "granted_at": TimestampUTC})
		expectNodeValidity(t, app, n, n.Key, svc, "")
		tg.Post(t, Auth, "/internal/nodes/"+id+"/services", map[string]string{"service_id": svc}, app.H()).
			ExpectError(http.StatusBadRequest, "Node already has access to this service")
		tg.Post(t, Auth, "/internal/nodes/contract-no-such-node/services", map[string]string{"service_id": svc}, app.H()).
			ExpectError(http.StatusNotFound, "Node not found")
		tg.Do(t, Auth, http.MethodDelete, "/internal/nodes/"+id+"/services/"+svc, nil, app.H()).ExpectStatus(http.StatusNoContent).ExpectEmpty()
		expectNodeValidity(t, app, n, n.Key, svc, "Node is not authorized to access service '"+svc+"'")
		tg.Do(t, Auth, http.MethodDelete, "/internal/nodes/"+id+"/services/"+svc, nil, app.H()).
			ExpectError(http.StatusNotFound, "Service access not found")
	})

	t.Run("validate node household", func(t *testing.T) {
		ok := Obj{"valid": Eq(true), "node_id": Eq(id), "household_id": Eq(u.HouseholdID), "reason": Null}
		bad := func(reason string) Matcher {
			return Obj{"valid": Eq(false), "node_id": Null, "household_id": Null, "reason": Eq(reason)}
		}
		post := func(nodeID, hh string) *Resp {
			return tg.Post(t, Auth, "/internal/validate-node-household", map[string]string{"node_id": nodeID, "household_id": hh}, app.H())
		}
		post(id, u.HouseholdID).Expect(http.StatusOK, ok)
		post(id, SharedUser(t).HouseholdID).Expect(http.StatusOK, bad("Node does not belong to this household"))
		post("contract-no-such-node", u.HouseholdID).Expect(http.StatusOK, bad("Node not found"))
		tg.Post(t, Auth, "/internal/validate-node-household", map[string]string{"node_id": id}, app.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "household_id"))
	})

	t.Run("deactivate", func(t *testing.T) {
		tg.Do(t, Auth, http.MethodDelete, "/internal/nodes/"+id, nil, app.H()).Expect(http.StatusOK, Obj{"node_id": Eq(id), "is_active": Eq(false)})
		expectNodeValidity(t, app, n, n.Key, app.ID, "Node is inactive")
		tg.Do(t, Auth, http.MethodDelete, "/internal/nodes/contract-no-such-node", nil, app.H()).ExpectError(http.StatusNotFound, "Node not found")
	})
}

func TestAuthInternalHouseholdAccess(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	owner := NewUser(t)
	member := NewUser(t)
	hh := NewHousehold(t, owner, "contract access")
	AddMember(t, owner, hh, member, "member")

	post := func(uid int, household, role string) *Resp {
		return tg.Post(t, Auth, "/internal/validate-household-access",
			map[string]any{"user_id": uid, "household_id": household, "required_role": role}, app.H())
	}
	ok := func(uid int, role string) Matcher {
		return Obj{"valid": Eq(true), "user_id": Eq(uid), "household_id": Eq(hh), "role": Eq(role), "reason": Null}
	}
	bad := func(reason string) Matcher {
		return Obj{"valid": Eq(false), "user_id": Null, "household_id": Null, "role": Null, "reason": Eq(reason)}
	}
	post(owner.ID, hh, "admin").Expect(http.StatusOK, ok(owner.ID, "admin"))
	post(member.ID, hh, "member").Expect(http.StatusOK, ok(member.ID, "member"))
	post(member.ID, hh, "power_user").Expect(http.StatusOK, bad("User has member role, requires power_user or higher"))
	post(member.ID, hh, "admin").Expect(http.StatusOK, bad("User has member role, requires admin or higher"))
	// Membership is checked in the household asked about, not the user's "current" one (D5).
	post(member.ID, member.HouseholdID, "admin").Expect(http.StatusOK, Open{"valid": Eq(true), "household_id": Eq(member.HouseholdID)})
	post(member.ID, owner.HouseholdID, "member").Expect(http.StatusOK, bad("User is not a member of this household"))
	post(member.ID, hh, "owner").ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "required_role"))
}

func TestAuthInternalUsersBatch(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	u := SharedUser(t)
	username := strings.SplitN(u.Email, "@", 2)[0]

	tg.Get(t, Auth, fmt.Sprintf("/internal/users/batch?user_ids=%d&user_ids=2147483000", u.ID), app.H()).
		Expect(http.StatusOK, Obj{"users": Obj{fmt.Sprint(u.ID): Eq(username)}})
	tg.Get(t, Auth, "/internal/users/batch?user_ids=2147483000", app.H()).Expect(http.StatusOK, Obj{"users": Obj{}})
	tg.Get(t, Auth, "/internal/users/batch", app.H()).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("query", "user_ids"))
	tg.Get(t, Auth, "/internal/users/batch?user_ids=abc", app.H()).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("query", "user_ids", 0))

	var q []string
	for i := 1; i <= 101; i++ {
		q = append(q, fmt.Sprintf("user_ids=%d", 2147483000+i))
	}
	tg.Get(t, Auth, "/internal/users/batch?"+strings.Join(q, "&"), app.H()).
		ExpectError(http.StatusBadRequest, "Maximum 100 user IDs per request")
}
