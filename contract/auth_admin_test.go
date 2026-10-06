//go:build contract

package contract

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// jarvis-auth's master-admin-token routes: admin_app_clients.py, admin_nodes.py and
// admin_users.py. Only X-Jarvis-Admin-Token is accepted; app credentials and superuser JWTs
// are not (granting superuser must not be reachable with the app credentials every service
// holds).

// TestAuthAdminTokenRequired: every admin route answers 401 "Unauthorized" without the token,
// with a wrong one, with app credentials and with a superuser JWT. The token is checked before
// the body is validated or the resource looked up.
func TestAuthAdminTokenRequired(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	super := SharedSuperuser(t)
	routes := []struct{ method, path string }{
		{http.MethodPost, "/admin/app-clients"},
		{http.MethodGet, "/admin/app-clients"},
		{http.MethodPost, "/admin/app-clients/x/rotate"},
		{http.MethodPost, "/admin/app-clients/x/revoke"},
		{http.MethodPost, "/admin/nodes"},
		{http.MethodGet, "/admin/nodes"},
		{http.MethodGet, "/admin/nodes/x"},
		{http.MethodDelete, "/admin/nodes/x"},
		{http.MethodPost, "/admin/nodes/x/rotate-key"},
		{http.MethodPost, "/admin/nodes/x/services"},
		{http.MethodDelete, "/admin/nodes/x/services/y"},
		{http.MethodPut, "/admin/users/1/superuser"},
		{http.MethodGet, "/admin/users/1"},
		{http.MethodGet, "/admin/users/by-email/x@example.com"},
	}
	creds := map[string]H{
		"none":      nil,
		"wrong":     {"X-Jarvis-Admin-Token": "contract-wrong-token"},
		"app":       app.H(),
		"superuser": super.H(),
	}
	for _, rt := range routes {
		for name, h := range creds {
			t.Run(rt.method+" "+rt.path+" "+name, func(t *testing.T) {
				tg.Do(t, Auth, rt.method, rt.path, nil, h).ExpectError(http.StatusUnauthorized, "Unauthorized")
			})
		}
	}
}

var appClientCreated = Obj{
	"app_id":          NonEmptyString,
	"name":            String,
	"key":             NonEmptyString,
	"created_at":      TimestampUTC,
	"last_rotated_at": Null,
}

func TestAuthAdminAppClients(t *testing.T) {
	tg := T(t)
	tg.NeedAdmin(t)
	id := fmt.Sprintf("contract-%s-admin", tg.RunID)

	r := tg.Post(t, Auth, "/admin/app-clients", map[string]string{"app_id": id, "name": "contract admin"}, tg.AdminH()).
		Expect(http.StatusCreated, All(appClientCreated, Open{"app_id": Eq(id), "name": Eq("contract admin")}))
	a := &App{ID: id, Key: r.Object()["key"].(string), owned: true}
	t.Cleanup(func() {
		if err := tg.revokeApp(a); err != nil {
			t.Errorf("cleanup app %s: %v", id, err)
		}
	})
	tg.Get(t, Auth, "/internal/app-ping", a.H()).ExpectStatus(http.StatusOK)

	t.Run("create errors", func(t *testing.T) {
		tg.Post(t, Auth, "/admin/app-clients", map[string]string{"app_id": id, "name": "dup"}, tg.AdminH()).
			ExpectError(http.StatusBadRequest, "app_id already exists")
		tg.Post(t, Auth, "/admin/app-clients", map[string]string{"app_id": id + "-x"}, tg.AdminH()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "name"))
	})

	t.Run("list", func(t *testing.T) {
		got := findBy(listOf(t, "/admin/app-clients", tg.AdminH()), "app_id", id)
		if got == nil || got["is_active"] != true || got["last_rotated_at"] != nil {
			t.Fatalf("new client in list: %v", got)
		}
	})

	t.Run("rotate", func(t *testing.T) {
		r := tg.Post(t, Auth, "/admin/app-clients/"+id+"/rotate", nil, tg.AdminH()).
			Expect(http.StatusOK, Obj{"app_id": Eq(id), "key": NonEmptyString, "last_rotated_at": TimestampUTC})
		oldKey := a.Key
		a.Key = r.Object()["key"].(string)
		tg.Get(t, Auth, "/internal/app-ping", H{"X-Jarvis-App-Id": id, "X-Jarvis-App-Key": oldKey}).
			ExpectError(http.StatusUnauthorized, "Invalid app credentials")
		tg.Get(t, Auth, "/internal/app-ping", a.H()).Expect(http.StatusOK, Obj{"app_id": Eq(id), "name": Eq("contract admin")})
		tg.Post(t, Auth, "/admin/app-clients/contract-no-such-app/rotate", nil, tg.AdminH()).
			ExpectError(http.StatusNotFound, "App client not found")
	})

	t.Run("revoke", func(t *testing.T) {
		tg.Post(t, Auth, "/admin/app-clients/"+id+"/revoke", nil, tg.AdminH()).
			Expect(http.StatusOK, Obj{"app_id": Eq(id), "is_active": Eq(false)})
		tg.Get(t, Auth, "/internal/app-ping", a.H()).ExpectError(http.StatusUnauthorized, "Invalid app credentials")
		if got := findBy(listOf(t, "/admin/app-clients", tg.AdminH()), "app_id", id); got == nil || got["is_active"] != false {
			t.Fatalf("revoked client in list: %v", got)
		}
		tg.Post(t, Auth, "/admin/app-clients/contract-no-such-app/revoke", nil, tg.AdminH()).
			ExpectError(http.StatusNotFound, "App client not found")

		// LEGACY-BUG (security, low): rotating a revoked client silently re-activates it, so
		// "rotate" doubles as "un-revoke". Revocation should stick until explicitly undone.
		r := tg.Post(t, Auth, "/admin/app-clients/"+id+"/rotate", nil, tg.AdminH()).ExpectStatus(http.StatusOK)
		a.Key = r.Object()["key"].(string)
		tg.Get(t, Auth, "/internal/app-ping", a.H()).ExpectStatus(http.StatusOK)
	})
}

var nodeDetail = Obj{
	"node_id":               NonEmptyString,
	"name":                  String,
	"household_id":          UUID,
	"registered_by_user_id": NullOr(Int),
	"is_active":             Bool,
	"created_at":            TimestampUTC,
	"updated_at":            TimestampUTC,
	"last_rotated_at":       NullOr(TimestampUTC),
	"services":              ArrayOf(Obj{"service_id": NonEmptyString, "granted_at": TimestampUTC, "granted_by": NullOr(Int)}),
}

func TestAuthAdminNodes(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	u := NewUser(t)
	id := fmt.Sprintf("contract-%s-node-adm", tg.RunID)

	r := tg.Post(t, Auth, "/admin/nodes", map[string]any{
		"node_id": id, "household_id": u.HouseholdID, "name": "contract admin node",
		"registered_by_user_id": u.ID, "services": []string{app.ID},
	}, tg.AdminH()).Expect(http.StatusCreated, All(nodeCreated, Open{
		"node_id": Eq(id), "household_id": Eq(u.HouseholdID), "registered_by_user_id": Eq(u.ID),
	}))
	n := &Node{ID: id, Key: r.Object()["node_key"].(string), HouseholdID: u.HouseholdID}
	t.Cleanup(func() {
		if err := tg.deactivateNode(n); err != nil {
			t.Errorf("cleanup node: %v", err)
		}
	})
	expectNodeValidity(t, app, n, n.Key, app.ID, "")

	t.Run("create errors", func(t *testing.T) {
		body := func(nodeID, hh string, by any) map[string]any {
			return map[string]any{"node_id": nodeID, "household_id": hh, "name": "x", "registered_by_user_id": by}
		}
		tg.Post(t, Auth, "/admin/nodes", body(id, u.HouseholdID, nil), tg.AdminH()).ExpectError(http.StatusBadRequest, "node_id already exists")
		tg.Post(t, Auth, "/admin/nodes", body(id+"-x", "00000000-0000-4000-8000-000000000000", nil), tg.AdminH()).
			ExpectError(http.StatusNotFound, "Household not found")
		tg.Post(t, Auth, "/admin/nodes", body(id+"-x", u.HouseholdID, 2147483000), tg.AdminH()).
			ExpectError(http.StatusNotFound, "User not found")
		tg.Post(t, Auth, "/admin/nodes", map[string]any{"node_id": id + "-x", "name": "x"}, tg.AdminH()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "household_id"))
	})

	t.Run("list and get", func(t *testing.T) {
		list := listOf(t, "/admin/nodes", tg.AdminH())
		got := findBy(list, "node_id", id)
		if got == nil {
			t.Fatalf("node %s not in /admin/nodes", id)
		}
		if errs := nodeListItem.Match("$", got); len(errs) > 0 {
			t.Fatalf("list item shape: %v", errs)
		}
		tg.Get(t, Auth, "/admin/nodes/"+id, tg.AdminH()).Expect(http.StatusOK, All(nodeDetail, Open{
			"node_id": Eq(id), "is_active": Eq(true), "last_rotated_at": Null, "registered_by_user_id": Eq(u.ID),
			"services": ArrayOf(Open{"service_id": Eq(app.ID), "granted_by": Eq(u.ID)}),
		}))
		tg.Get(t, Auth, "/admin/nodes/contract-no-such-node", tg.AdminH()).ExpectError(http.StatusNotFound, "Node not found")
	})

	t.Run("rotate key", func(t *testing.T) {
		r := tg.Post(t, Auth, "/admin/nodes/"+id+"/rotate-key", nil, tg.AdminH()).
			Expect(http.StatusOK, Obj{"node_id": Eq(id), "node_key": NonEmptyString, "last_rotated_at": TimestampUTC})
		oldKey := n.Key
		n.Key = r.Object()["node_key"].(string)
		expectNodeValidity(t, app, n, oldKey, app.ID, "Invalid node credentials")
		expectNodeValidity(t, app, n, n.Key, app.ID, "")
		tg.Get(t, Auth, "/admin/nodes/"+id, tg.AdminH()).Expect(http.StatusOK, Open{"last_rotated_at": TimestampUTC})
		tg.Post(t, Auth, "/admin/nodes/contract-no-such-node/rotate-key", nil, tg.AdminH()).ExpectError(http.StatusNotFound, "Node not found")
	})

	t.Run("services", func(t *testing.T) {
		svc := "contract-" + tg.RunID + "-svc"
		tg.Post(t, Auth, "/admin/nodes/"+id+"/services", map[string]string{"service_id": svc}, tg.AdminH()).
			Expect(http.StatusCreated, Obj{"node_id": Eq(id), "service_id": Eq(svc), "granted_at": TimestampUTC})
		expectNodeValidity(t, app, n, n.Key, svc, "")
		tg.Post(t, Auth, "/admin/nodes/"+id+"/services", map[string]string{"service_id": svc}, tg.AdminH()).
			ExpectError(http.StatusBadRequest, "Node already has access to this service")
		tg.Post(t, Auth, "/admin/nodes/contract-no-such-node/services", map[string]string{"service_id": svc}, tg.AdminH()).
			ExpectError(http.StatusNotFound, "Node not found")
		tg.Post(t, Auth, "/admin/nodes/"+id+"/services", map[string]string{}, tg.AdminH()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "service_id"))

		tg.Do(t, Auth, http.MethodDelete, "/admin/nodes/"+id+"/services/"+svc, nil, tg.AdminH()).ExpectStatus(http.StatusNoContent).ExpectEmpty()
		expectNodeValidity(t, app, n, n.Key, svc, "Node is not authorized to access service '"+svc+"'")
		tg.Do(t, Auth, http.MethodDelete, "/admin/nodes/"+id+"/services/"+svc, nil, tg.AdminH()).
			ExpectError(http.StatusNotFound, "Service access not found")
	})

	t.Run("deactivate", func(t *testing.T) {
		tg.Do(t, Auth, http.MethodDelete, "/admin/nodes/"+id, nil, tg.AdminH()).Expect(http.StatusOK, Obj{"node_id": Eq(id), "is_active": Eq(false)})
		expectNodeValidity(t, app, n, n.Key, app.ID, "Node is inactive")
		tg.Get(t, Auth, "/admin/nodes/"+id, tg.AdminH()).Expect(http.StatusOK, Open{"is_active": Eq(false)})
		tg.Do(t, Auth, http.MethodDelete, "/admin/nodes/contract-no-such-node", nil, tg.AdminH()).ExpectError(http.StatusNotFound, "Node not found")
	})
}

var userAdminShape = Obj{"id": Int, "email": NonEmptyString, "username": String, "is_active": Bool, "is_superuser": Bool}

func TestAuthAdminUsers(t *testing.T) {
	tg := T(t)
	u := NewUser(t)
	uid := fmt.Sprint(u.ID)
	want := Open{"id": Eq(u.ID), "email": Eq(u.Email), "is_active": Eq(true), "is_superuser": Eq(false)}

	tg.Get(t, Auth, "/admin/users/"+uid, tg.AdminH()).Expect(http.StatusOK, All(userAdminShape, want))
	tg.Get(t, Auth, "/admin/users/by-email/"+url.PathEscape(u.Email), tg.AdminH()).Expect(http.StatusOK, All(userAdminShape, want))
	tg.Get(t, Auth, "/admin/users/2147483000", tg.AdminH()).ExpectError(http.StatusNotFound, "User with ID 2147483000 not found")
	tg.Get(t, Auth, "/admin/users/by-email/contract-nobody@example.com", tg.AdminH()).
		ExpectError(http.StatusNotFound, "User with email contract-nobody@example.com not found")
	tg.Get(t, Auth, "/admin/users/not-a-number", tg.AdminH()).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("path", "user_id"))

	put := func(on bool) *Resp {
		return tg.Do(t, Auth, http.MethodPut, "/admin/users/"+uid+"/superuser", map[string]bool{"is_superuser": on}, tg.AdminH())
	}
	resp := func(on bool, msg string) Matcher {
		return Obj{"success": Eq(true), "user_id": Eq(u.ID), "email": Eq(u.Email), "is_superuser": Eq(on), "message": Eq(msg)}
	}
	// Demote on the way out, before NewUser's cleanup deletes the user.
	t.Cleanup(func() {
		if err := tg.setSuperuser(u, false); err != nil {
			t.Errorf("demote: %v", err)
		}
	})
	put(true).Expect(http.StatusOK, resp(true, "Superuser access granted for user "+u.Email))
	// The messages are Python f-strings, bools included.
	put(true).Expect(http.StatusOK, resp(true, "User "+u.Email+" already has is_superuser=True"))
	tg.Get(t, Auth, "/admin/users/"+uid, tg.AdminH()).Expect(http.StatusOK, Open{"is_superuser": Eq(true)})

	// The claim is minted at login: the pre-promotion token is still a plain user's.
	tg.Get(t, Auth, "/superuser/households", u.H()).ExpectStatus(http.StatusOK) // DB, not claim, decides here
	if err := tg.login(u); err != nil {
		t.Fatal(err)
	}
	u.Superuser = true
	checkAccessToken(t, tg, u.AccessToken, u)

	put(false).Expect(http.StatusOK, resp(false, "Superuser access revoked for user "+u.Email))
	u.Superuser = false
	// A token still claiming is_superuser is re-checked against the DB.
	tg.Get(t, Auth, "/superuser/households", u.H()).ExpectError(http.StatusForbidden, "Superuser access required")
	tg.Get(t, Auth, "/settings/", u.H()).ExpectError(http.StatusForbidden, "Superuser access required")

	tg.Do(t, Auth, http.MethodPut, "/admin/users/2147483000/superuser", map[string]bool{"is_superuser": true}, tg.AdminH()).
		ExpectError(http.StatusNotFound, "User with ID 2147483000 not found")
	tg.Do(t, Auth, http.MethodPut, "/admin/users/"+uid+"/superuser", map[string]string{}, tg.AdminH()).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "is_superuser"))
}
