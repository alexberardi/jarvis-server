//go:build contract

package contract

import (
	"fmt"
	"net/http"
	"testing"
)

// jarvis-auth superuser_views.py (JWT + is_superuser, checked against the DB) and the write
// half of auth's /settings router (superuser JWT only; reads are in settings_test.go).

func TestAuthSuperuserAuth(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	user := SharedUser(t)
	routes := []struct{ method, path string }{
		{http.MethodGet, "/superuser/households"},
		{http.MethodGet, "/superuser/users"},
		{http.MethodGet, "/superuser/nodes"},
		{http.MethodPost, "/superuser/users/1/temp-password"},
		{http.MethodPut, "/settings/auth.algorithm"},
		{http.MethodPost, "/settings/sync-from-env"},
		{http.MethodPost, "/settings/invalidate-cache"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			body := any(nil)
			if rt.method == http.MethodPut {
				body = map[string]any{"value": "HS256"}
			}
			r := tg.Do(t, Auth, rt.method, rt.path, body).ExpectError(http.StatusUnauthorized, "Not authenticated")
			if got := r.Header["Www-Authenticate"]; len(got) != 1 || got[0] != "Bearer" {
				r.Fatalf("WWW-Authenticate: want Bearer, got %v", got)
			}
			// App credentials and the admin token are not accepted here.
			tg.Do(t, Auth, rt.method, rt.path, body, app.H()).ExpectError(http.StatusUnauthorized, "Not authenticated")
			tg.Do(t, Auth, rt.method, rt.path, body, tg.AdminH()).ExpectError(http.StatusUnauthorized, "Not authenticated")
			tg.Do(t, Auth, rt.method, rt.path, body, Bearer("not-a-jwt")).ExpectError(http.StatusUnauthorized, "Could not validate credentials")
			tg.Do(t, Auth, rt.method, rt.path, body, user.H()).ExpectError(http.StatusForbidden, "Superuser access required")
		})
	}
}

func TestAuthSuperuserViews(t *testing.T) {
	super := SharedSuperuser(t)
	user := SharedUser(t)
	node := SharedNode(t)

	t.Run("households", func(t *testing.T) {
		list := listOf(t, "/superuser/households", super.H())
		if errs := NonEmptyArrayOf(householdShape).Match("$", list); len(errs) > 0 {
			t.Fatalf("shape: %v", errs)
		}
		if findBy(list, "id", user.HouseholdID) == nil {
			t.Fatalf("every household is listed; %s is missing", user.HouseholdID)
		}
	})

	t.Run("users", func(t *testing.T) {
		list := listOf(t, "/superuser/users", super.H())
		item := Obj{
			"id":                   Int,
			"email":                NonEmptyString,
			"username":             String,
			"is_active":            Bool,
			"is_superuser":         Bool,
			"must_change_password": Bool,
			"created_at":           TimestampUTC,
			"updated_at":           NullOr(TimestampUTC),
			"households":           ArrayOf(Obj{"household_id": UUID, "household_name": String, "role": householdRole}),
		}
		if errs := NonEmptyArrayOf(item).Match("$", list); len(errs) > 0 {
			t.Fatalf("shape: %v", errs)
		}
		got := findBy(list, "id", user.ID)
		want := Open{
			"email": Eq(user.Email), "is_superuser": Eq(false),
			"households": Eq([]any{map[string]any{"household_id": user.HouseholdID, "household_name": "My Home", "role": "admin"}}),
		}
		if errs := want.Match("$", got); got == nil || len(errs) > 0 {
			t.Fatalf("user %d in /superuser/users: %v %v", user.ID, got, errs)
		}
		if s := findBy(list, "id", super.ID); s == nil || s["is_superuser"] != true {
			t.Fatalf("superuser %d should be listed with is_superuser true: %v", super.ID, s)
		}
	})

	t.Run("nodes", func(t *testing.T) {
		list := listOf(t, "/superuser/nodes", super.H())
		if errs := NonEmptyArrayOf(nodeListItem).Match("$", list); len(errs) > 0 {
			t.Fatalf("shape: %v", errs)
		}
		if got := findBy(list, "node_id", node.ID); got == nil || got["household_id"] != user.HouseholdID {
			t.Fatalf("shared node %s missing from /superuser/nodes: %v", node.ID, got)
		}
	})
}

func TestAuthSuperuserTempPassword(t *testing.T) {
	tg := T(t)
	super := SharedSuperuser(t)
	u := NewUser(t)
	path := fmt.Sprintf("/superuser/users/%d/temp-password", u.ID)
	oldPassword, oldRefresh, oldAccess := u.Password, u.RefreshToken, u.AccessToken
	issued := Obj{"temp_password": NonEmptyString, "expires_at": TimestampUTC, "must_change_password": Eq(true)}

	t.Run("validation", func(t *testing.T) {
		tg.Post(t, Auth, path, map[string]any{"temp_password": "short"}, super.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "temp_password"))
		tg.Post(t, Auth, path, map[string]any{"expires_in_hours": 0}, super.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "expires_in_hours"))
		tg.Post(t, Auth, path, map[string]any{"expires_in_hours": 169}, super.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "expires_in_hours"))
		tg.Post(t, Auth, "/superuser/users/2147483000/temp-password", nil, super.H()).ExpectError(http.StatusNotFound, "User not found")
	})

	// No body: the server generates the password.
	tg.Post(t, Auth, path, nil, super.H()).Expect(http.StatusOK, issued)
	// A chosen one, which is what we log in with.
	temp := "contract-temp-" + randHex(4)
	tg.Post(t, Auth, path, map[string]any{"temp_password": temp, "expires_in_hours": 1}, super.H()).
		Expect(http.StatusOK, All(issued, Open{"temp_password": Eq(temp)}))

	// All sessions are revoked, and the old password is gone.
	tg.Post(t, Auth, "/auth/refresh", map[string]string{"refresh_token": oldRefresh}).ExpectError(http.StatusUnauthorized, "Invalid refresh token")
	tg.Post(t, Auth, "/auth/login", map[string]string{"email": u.Email, "password": oldPassword}).
		ExpectError(http.StatusUnauthorized, "Invalid email or password")
	// LEGACY-BUG (security, low): the reset does not cut off access tokens already issued;
	// they stay valid until they expire.
	tg.Get(t, Auth, "/auth/me", Bearer(oldAccess)).ExpectStatus(http.StatusOK)

	// The temp password logs in, flagged must_change_password at both levels.
	r := tg.Post(t, Auth, "/auth/login", map[string]string{"email": u.Email, "password": temp}).
		Expect(http.StatusOK, All(tokenResponse, Open{"must_change_password": Eq(true), "user": Open{"must_change_password": Eq(true)}}))
	u.Password, u.AccessToken = temp, r.Object()["access_token"].(string)
	tg.Get(t, Auth, "/auth/me", u.H()).Expect(http.StatusOK, Open{"must_change_password": Eq(true)})

	// Changing it clears the flag.
	final := "contract-" + randHex(8)
	r = tg.Post(t, Auth, "/auth/change-password", map[string]string{"current_password": temp, "new_password": final}, u.H()).
		Expect(http.StatusOK, All(tokenResponse, Open{"must_change_password": Eq(false), "user": Open{"must_change_password": Eq(false)}}))
	u.Password, u.AccessToken = final, r.Object()["access_token"].(string)
	tg.Get(t, Auth, "/auth/me", u.H()).Expect(http.StatusOK, Open{"must_change_password": Eq(false)})
}

func TestAuthSettingsWrite(t *testing.T) {
	tg := T(t)
	super := SharedSuperuser(t)
	u := NewUser(t)
	const key = "auth.token.access_expire_minutes"

	tg.Do(t, Auth, http.MethodPut, "/settings/contract.no.such.key", map[string]any{"value": 1}, super.H()).
		ExpectStatus(http.StatusNotFound).ExpectShape(settingNotFound("contract.no.such.key"))
	tg.Do(t, Auth, http.MethodPut, "/settings/"+key, map[string]any{}, super.H()).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "value"))

	// Happy path, scoped to a throwaway user so nothing system-wide changes. The current
	// effective value is written back. DELETE /auth/me removes the user's settings rows.
	cur := tg.Get(t, Auth, "/settings/"+key, super.H()).ExpectStatus(http.StatusOK).Object()["value"]
	tg.Do(t, Auth, http.MethodPut, fmt.Sprintf("/settings/%s?user_id=%d", key, u.ID), map[string]any{"value": cur}, super.H()).
		Expect(http.StatusOK, Obj{"success": Eq(true), "key": Eq(key), "requires_reload": Bool, "message": NullOr(String)})

	tg.Post(t, Auth, "/settings/invalidate-cache", nil, super.H()).Expect(http.StatusOK, Obj{"status": Eq("ok"), "invalidated": Eq("all")})
	tg.Post(t, Auth, "/settings/invalidate-cache?key="+key, nil, super.H()).Expect(http.StatusOK, Obj{"status": Eq("ok"), "invalidated": Eq(key)})
	// POST /settings/sync-from-env's happy path rewrites system settings from the target's env,
	// so only its auth is exercised (TestAuthSuperuserAuth).
}
