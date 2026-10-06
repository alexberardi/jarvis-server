//go:build contract

package contract

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

// jarvis-auth auth.py beyond register/login/refresh/me (auth_test.go): logout, change-password,
// switch-household, DELETE /auth/me, first-run setup, register's household routing, and the
// failed-login lockout.

func TestAuthLogout(t *testing.T) {
	tg := T(t)
	u := NewUser(t)
	first := u.RefreshToken // device 1: the registration session

	// Device 2: a second, independent session (its own refresh family).
	r := tg.Post(t, Auth, "/auth/login", map[string]string{"email": u.Email, "password": u.Password}).
		Expect(http.StatusOK, tokenResponse)
	second := r.Object()["refresh_token"].(string)

	// Logout revokes only the presented token's family.
	tg.Post(t, Auth, "/auth/logout", map[string]string{"refresh_token": first}).ExpectStatus(http.StatusNoContent).ExpectEmpty()
	tg.Post(t, Auth, "/auth/refresh", map[string]string{"refresh_token": first}).ExpectError(http.StatusUnauthorized, "Invalid refresh token")
	r = tg.Post(t, Auth, "/auth/refresh", map[string]string{"refresh_token": second}).Expect(http.StatusOK, tokenResponse)
	second = r.Object()["refresh_token"].(string)

	// all_devices revokes every family. A third session proves it's not just the presented one.
	third := tg.Post(t, Auth, "/auth/login", map[string]string{"email": u.Email, "password": u.Password}).
		ExpectStatus(http.StatusOK).Object()["refresh_token"].(string)
	tg.Post(t, Auth, "/auth/logout", map[string]any{"refresh_token": second, "all_devices": true}).ExpectStatus(http.StatusNoContent)
	tg.Post(t, Auth, "/auth/refresh", map[string]string{"refresh_token": third}).ExpectError(http.StatusUnauthorized, "Invalid refresh token")

	// LEGACY-BUG (security, low): logout revokes refresh tokens only. The access token stays
	// valid until it expires (default 30 min); there is no jti denylist.
	tg.Get(t, Auth, "/auth/me", u.H()).ExpectStatus(http.StatusOK)

	// Unknown or already-revoked tokens: still 204, so nothing is revealed. No Bearer needed.
	tg.Post(t, Auth, "/auth/logout", map[string]string{"refresh_token": "contract-not-a-token"}).ExpectStatus(http.StatusNoContent).ExpectEmpty()
	tg.Post(t, Auth, "/auth/logout", map[string]string{"refresh_token": first}).ExpectStatus(http.StatusNoContent)
	tg.Post(t, Auth, "/auth/logout", map[string]string{}).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "refresh_token"))
}

func TestAuthChangePassword(t *testing.T) {
	tg := T(t)
	u := NewUser(t)
	oldPassword, oldRefresh, oldAccess := u.Password, u.RefreshToken, u.AccessToken
	newPassword := "contract-" + randHex(8)
	body := func(cur, next string) map[string]string {
		return map[string]string{"current_password": cur, "new_password": next}
	}

	tg.Post(t, Auth, "/auth/change-password", body("wrong-password", newPassword), u.H()).
		ExpectError(http.StatusUnauthorized, "Incorrect password")
	tg.Post(t, Auth, "/auth/change-password", body(oldPassword, oldPassword), u.H()).
		ExpectError(http.StatusBadRequest, "New password must be different from the current password")
	tg.Post(t, Auth, "/auth/change-password", body(oldPassword, "short"), u.H()).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "new_password"))
	tg.Post(t, Auth, "/auth/change-password", body(oldPassword, newPassword)).
		ExpectError(http.StatusUnauthorized, "Not authenticated")

	r := tg.Post(t, Auth, "/auth/change-password", body(oldPassword, newPassword), u.H()).
		Expect(http.StatusOK, All(tokenResponse, Open{"must_change_password": Eq(false)}))
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	r.Decode(&out)
	u.Password, u.AccessToken, u.RefreshToken = newPassword, out.AccessToken, out.RefreshToken
	checkAccessToken(t, tg, u.AccessToken, u)

	// Every earlier session is revoked; the returned pair is the one to adopt.
	tg.Post(t, Auth, "/auth/refresh", map[string]string{"refresh_token": oldRefresh}).ExpectError(http.StatusUnauthorized, "Invalid refresh token")
	tg.Post(t, Auth, "/auth/login", map[string]string{"email": u.Email, "password": oldPassword}).
		ExpectError(http.StatusUnauthorized, "Invalid email or password")
	tg.Get(t, Auth, "/auth/me", u.H()).ExpectStatus(http.StatusOK)
	// LEGACY-BUG (security, low): a password change leaves earlier access tokens valid until
	// they expire.
	tg.Get(t, Auth, "/auth/me", Bearer(oldAccess)).ExpectStatus(http.StatusOK)
}

func TestAuthSwitchHousehold(t *testing.T) {
	tg := T(t)
	u := NewUser(t)
	other := SharedUser(t)
	second := NewHousehold(t, u, "contract second home")

	if c := jwtClaims(t, u.AccessToken); c["household_id"] != u.HouseholdID {
		t.Fatalf("registration token household_id: want %s, got %v", u.HouseholdID, c["household_id"])
	}
	r := tg.Post(t, Auth, "/auth/switch-household", map[string]string{"household_id": second}, u.H()).
		Expect(http.StatusOK, Obj{"access_token": NonEmptyString, "household_id": Eq(second)})
	tok := r.Object()["access_token"].(string)
	switched := *u
	switched.HouseholdID = second
	checkAccessToken(t, tg, tok, &switched)
	if c, orig := jwtClaims(t, tok), jwtClaims(t, u.AccessToken); c["jti"] == orig["jti"] {
		t.Fatalf("switch-household should mint a new token (same jti %v)", c["jti"])
	}
	tg.Get(t, Auth, "/auth/me", Bearer(tok)).Expect(http.StatusOK, All(userOut, Open{"id": Eq(u.ID)}))
	// And back again.
	tg.Post(t, Auth, "/auth/switch-household", map[string]string{"household_id": u.HouseholdID}, Bearer(tok)).
		Expect(http.StatusOK, Obj{"access_token": NonEmptyString, "household_id": Eq(u.HouseholdID)})

	tg.Post(t, Auth, "/auth/switch-household", map[string]string{"household_id": other.HouseholdID}, u.H()).
		ExpectError(http.StatusForbidden, "Not a member of this household")
	tg.Post(t, Auth, "/auth/switch-household", map[string]string{"household_id": "00000000-0000-4000-8000-000000000000"}, u.H()).
		ExpectError(http.StatusForbidden, "Not a member of this household")
	tg.Post(t, Auth, "/auth/switch-household", map[string]string{"household_id": second}).
		ExpectError(http.StatusUnauthorized, "Not authenticated")
	tg.Post(t, Auth, "/auth/switch-household", map[string]string{}, u.H()).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "household_id"))
}

// TestAuthDeleteMe drives DELETE /auth/me on a throwaway user through both 409 guards, then
// the real deletion. Deletion fans out DELETE /api/v0/me/data to command-center and
// notifications with the user's token (a 5xx there aborts with 502).
func TestAuthDeleteMe(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	u := DisposableUser(t)
	other := NewUser(t)
	del := func(body any, hs ...H) *Resp { return tg.Do(t, Auth, http.MethodDelete, "/auth/me", body, hs...) }

	del(map[string]string{"password": "wrong-password"}, u.H()).ExpectError(http.StatusUnauthorized, "Incorrect password")
	del(map[string]string{}, u.H()).ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "password"))
	del(map[string]string{"password": u.Password}).ExpectError(http.StatusUnauthorized, "Not authenticated")

	// Guard 1: an active node registered by the user.
	n := NewHouseholdNode(t, u, u.HouseholdID, app.ID)
	del(map[string]string{"password": u.Password}, u.H()).
		ExpectError(http.StatusConflict, "Cannot delete account with nodes registered to it")
	if err := tg.deactivateNode(n); err != nil {
		t.Fatal(err)
	}

	// Guard 2: sole admin of a household that has other members.
	shared := NewHousehold(t, u, "contract shared")
	AddMember(t, u, shared, other, "member")
	del(map[string]string{"password": u.Password}, u.H()).
		ExpectError(http.StatusConflict, "Cannot delete your account while you are the only admin of a household with other members. Make another member an admin first.")
	// Another admin lifts the guard; the shared household then survives the deletion.
	tg.Do(t, Auth, http.MethodPatch, "/households/"+shared+"/members/"+strconv.Itoa(other.ID), map[string]string{"role": "admin"}, u.H()).
		ExpectStatus(http.StatusOK)

	del(map[string]string{"password": u.Password}, u.H()).ExpectStatus(http.StatusNoContent).ExpectEmpty()

	tg.Get(t, Auth, "/auth/me", u.H()).ExpectError(http.StatusUnauthorized, "Could not validate credentials")
	tg.Post(t, Auth, "/auth/refresh", map[string]string{"refresh_token": u.RefreshToken}).ExpectError(http.StatusUnauthorized, "Invalid refresh token")
	tg.Post(t, Auth, "/auth/login", map[string]string{"email": u.Email, "password": u.Password}).
		ExpectError(http.StatusUnauthorized, "Invalid email or password")
	tg.Get(t, Auth, "/admin/users/by-email/"+url.PathEscape(u.Email), tg.AdminH()).
		ExpectError(http.StatusNotFound, "User with email "+u.Email+" not found")
	// The solo household went with the user, cascading its nodes; the shared one stayed.
	tg.Get(t, Auth, "/admin/nodes/"+n.ID, tg.AdminH()).ExpectError(http.StatusNotFound, "Node not found")
	members := listOf(t, "/households/"+shared+"/members", other.H())
	if len(members) != 1 || findBy(members, "user_id", other.ID) == nil {
		t.Fatalf("shared household after deletion: want only %d, got %v", other.ID, members)
	}
	// Hand the shared household's cleanup to the surviving admin.
	tg.Do(t, Auth, http.MethodDelete, "/households/"+shared, nil, other.H()).ExpectStatus(http.StatusNoContent)
}

func TestAuthSetup(t *testing.T) {
	tg := T(t)
	tg.Need(t, Auth)
	needs := tg.Get(t, Auth, "/auth/setup-status").ExpectStatus(http.StatusOK).Object()["needs_setup"]
	if needs != false {
		// Running setup would mint the target's first superuser; never do that from here.
		t.Skip("target has no superuser yet; not exercising first-run setup")
	}
	body := map[string]string{"email": "contract-" + tg.RunID + "-setup@example.com", "password": "contract-password"}
	tg.Post(t, Auth, "/auth/setup", body).ExpectError(http.StatusConflict, "Setup already completed")
	tg.Post(t, Auth, "/auth/setup", map[string]string{"email": "not-an-email", "password": "contract-password"}).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "email"))
}

func TestAuthRegisterHousehold(t *testing.T) {
	tg := T(t)
	owner := NewUser(t)

	t.Run("unknown household", func(t *testing.T) {
		tg.Post(t, Auth, "/auth/register",
			map[string]string{"email": "contract-" + tg.RunID + "-nohh@example.com", "password": "contract-password"},
			H{"X-Household-Id": "00000000-0000-4000-8000-000000000000"}).
			ExpectError(http.StatusBadRequest, "Household not found")
	})
	t.Run("bad invite", func(t *testing.T) {
		tg.Post(t, Auth, "/auth/register", map[string]string{
			"email": "contract-" + tg.RunID + "-badinv@example.com", "password": "contract-password", "invite_code": "ZZZZZZZZ",
		}).ExpectError(http.StatusBadRequest, "Invalid or expired invite code")
	})
	t.Run("bad email", func(t *testing.T) {
		tg.Post(t, Auth, "/auth/register", map[string]string{"email": "not-an-email", "password": "contract-password"}).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "email"))
	})
	t.Run("X-Household-Id joins without an invite", func(t *testing.T) {
		// LEGACY-BUG (security, high): an unauthenticated caller who knows a household's id
		// can register straight into it as a member with the X-Household-Id header. No invite,
		// no approval. That breaks D5's "block outsiders" line.
		if Jarvisd() {
			// Fixed (D4/D5): joining an existing household needs a valid invite.
			tg := T(t)
			tg.Post(t, Auth, "/auth/register", map[string]any{
				"email": "contract-" + tg.RunID + "-intruder@example.com", "password": "Sup3r-secret-pw",
			}, H{"X-Household-Id": owner.HouseholdID}).
				ExpectError(http.StatusBadRequest, "Invalid or expired invite code")
			return
		}
		u := RegisterUser(t, nil, H{"X-Household-Id": owner.HouseholdID})
		if u.HouseholdID != owner.HouseholdID {
			t.Fatalf("household_id: want %s, got %s", owner.HouseholdID, u.HouseholdID)
		}
		m := findBy(listOf(t, "/households/"+owner.HouseholdID+"/members", owner.H()), "user_id", u.ID)
		if m == nil || m["role"] != "member" {
			t.Fatalf("intruder should be listed as a member, got %v", m)
		}
	})
}

// TestAuthLoginLockout: repeated wrong passwords for one (email, IP) lock that pair out with a
// long Retry-After. An unregistered email is used, so no real account is affected.
func TestAuthLoginLockout(t *testing.T) {
	tg := T(t)
	tg.Need(t, Auth)
	email := "contract-" + tg.RunID + "-lockout@example.com"
	for i := 1; i <= 20; i++ {
		r := tg.Post(t, Auth, "/auth/login", map[string]string{"email": email, "password": "wrong-password"})
		if r.Status == http.StatusUnauthorized {
			r.ExpectDetail("Invalid email or password")
			continue
		}
		r.ExpectError(http.StatusTooManyRequests, "Too many failed login attempts. Please try again later.")
		ra, err := strconv.Atoi(http.Header(r.Header).Get("Retry-After"))
		if err != nil || ra <= 60 {
			r.Fatalf("lockout Retry-After: want seconds > 60, got %q", http.Header(r.Header).Get("Retry-After"))
		}
		if i == 1 {
			r.Fatalf("locked out on the first attempt")
		}
		t.Logf("locked out after %d failures (Retry-After %ds)", i-1, ra)
		return
	}
	t.Fatalf("no lockout after 20 failed logins")
}
