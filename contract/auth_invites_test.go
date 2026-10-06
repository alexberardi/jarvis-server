//go:build contract

package contract

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// jarvis-auth invites.py: household invite codes, the public validate probe, joining with a
// code, and registering with one (auth.py /auth/register invite_code).

func TestAuthInvites(t *testing.T) {
	tg := T(t)
	owner := NewUser(t)
	outsider := NewUser(t)
	joiner := NewUser(t)
	hh := owner.HouseholdID // owner's solo "My Home", where they are admin
	base := "/households/" + hh + "/invites"

	r := tg.Post(t, Auth, base, map[string]any{}, owner.H()).Expect(http.StatusCreated, All(inviteShape, Open{
		"household_id": Eq(hh), "default_role": Eq("member"), "max_uses": Null, "use_count": Eq(0), "revoked": Eq(false),
	}))
	code := r.Object()["code"].(string)

	t.Run("create errors", func(t *testing.T) {
		tg.Post(t, Auth, base, map[string]any{"default_role": "admin"}, owner.H()).
			ExpectError(http.StatusBadRequest, "Invite codes cannot assign admin role")
		tg.Post(t, Auth, base, map[string]any{"expires_in_days": 0}, owner.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "expires_in_days"))
		tg.Post(t, Auth, base, map[string]any{"expires_in_days": 91}, owner.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "expires_in_days"))
		tg.Post(t, Auth, base, map[string]any{}, outsider.H()).ExpectError(http.StatusForbidden, "Not a member of this household")
		tg.Post(t, Auth, base, map[string]any{}).ExpectError(http.StatusUnauthorized, "Not authenticated")
	})

	t.Run("validate", func(t *testing.T) {
		// Public, unauthenticated, and case-insensitive.
		tg.Get(t, Auth, "/invites/"+strings.ToLower(code)+"/validate").
			Expect(http.StatusOK, Obj{"valid": Eq(true), "household_name": Eq("My Home")})
		tg.Get(t, Auth, "/invites/ZZZZZZZZ/validate").Expect(http.StatusOK, Obj{"valid": Eq(false), "household_name": Null})
	})

	t.Run("join", func(t *testing.T) {
		tg.Post(t, Auth, "/households/join", map[string]string{"invite_code": strings.ToLower(code)}, joiner.H()).
			Expect(http.StatusOK, Obj{"household_id": Eq(hh), "household_name": Eq("My Home"), "role": Eq("member")})
		tg.Post(t, Auth, "/households/join", map[string]string{"invite_code": code}, joiner.H()).
			ExpectError(http.StatusBadRequest, "Already a member of this household")
		tg.Post(t, Auth, "/households/join", map[string]string{"invite_code": "ZZZZZZZZ"}, outsider.H()).
			ExpectError(http.StatusBadRequest, "Invalid or expired invite code")
		tg.Post(t, Auth, "/households/join", map[string]string{"invite_code": code}).
			ExpectError(http.StatusUnauthorized, "Not authenticated")
		tg.Post(t, Auth, "/households/join", map[string]string{}, outsider.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "invite_code"))
		// Joining adds a household; the user's solo one stays.
		list := listOf(t, "/households", joiner.H())
		if m := findBy(list, "id", hh); m == nil || m["role"] != "member" || findBy(list, "id", joiner.HouseholdID) == nil || len(list) != 2 {
			t.Fatalf("joiner's households: want solo + %s as member, got %v", hh, list)
		}
		// The invite's use_count went up.
		if inv := findBy(listOf(t, base, owner.H()), "code", code); inv == nil || mustFloat(inv["use_count"]) != 1 {
			t.Fatalf("use_count after one join: %v", inv)
		}
	})

	t.Run("member cannot invite", func(t *testing.T) {
		tg.Post(t, Auth, base, map[string]any{}, joiner.H()).
			ExpectError(http.StatusForbidden, "Requires power_user role or higher")
	})

	t.Run("register with code", func(t *testing.T) {
		one := tg.Post(t, Auth, base, map[string]any{"default_role": "power_user", "max_uses": 1}, owner.H()).
			Expect(http.StatusCreated, All(inviteShape, Open{"default_role": Eq("power_user"), "max_uses": Eq(1)})).
			Object()["code"].(string)

		u := RegisterUser(t, map[string]string{"invite_code": strings.ToLower(one)})
		if u.HouseholdID != hh {
			t.Fatalf("register with invite: household_id want %s, got %s", hh, u.HouseholdID)
		}
		// No solo household is created: the invite's household is the only one, at its role.
		list := listOf(t, "/households", u.H())
		if len(list) != 1 || findBy(list, "id", hh)["role"] != "power_user" {
			t.Fatalf("invited user's households: %v", list)
		}
		// max_uses reached: the code is now invalid everywhere.
		tg.Get(t, Auth, "/invites/"+one+"/validate").Expect(http.StatusOK, Obj{"valid": Eq(false), "household_name": Null})
		tg.Post(t, Auth, "/households/join", map[string]string{"invite_code": one}, outsider.H()).
			ExpectError(http.StatusBadRequest, "Invalid or expired invite code")
		tg.Post(t, Auth, "/auth/register", map[string]string{
			"email": "contract-" + tg.RunID + "-used@example.com", "password": "contract-password", "invite_code": one,
		}).ExpectError(http.StatusBadRequest, "Invalid or expired invite code")

		// LEGACY-BUG: an admin can kick a member out of their ONLY household, leaving them
		// with none, though /leave refuses exactly that ("Cannot leave your only household").
		tg.Do(t, Auth, http.MethodDelete, "/households/"+hh+"/members/"+strconv.Itoa(u.ID), nil, owner.H()).
			ExpectStatus(http.StatusNoContent)
		if list := listOf(t, "/households", u.H()); len(list) != 0 {
			t.Fatalf("kicked user's households: want [], got %v", list)
		}
	})

	t.Run("list and revoke", func(t *testing.T) {
		list := listOf(t, base, joiner.H()) // any member may list
		if errs := ArrayOf(inviteShape).Match("$", list); len(errs) > 0 {
			t.Fatalf("shape: %v", errs)
		}
		inv := findBy(list, "code", code)
		if inv == nil {
			t.Fatalf("invite %s not listed: %v", code, list)
		}
		id := strconv.Itoa(int(mustFloat(inv["id"])))
		tg.Get(t, Auth, base, outsider.H()).ExpectError(http.StatusForbidden, "Not a member of this household")

		tg.Do(t, Auth, http.MethodDelete, base+"/"+id, nil, joiner.H()).
			ExpectError(http.StatusForbidden, "Requires power_user role or higher")
		tg.Do(t, Auth, http.MethodDelete, "/households/"+outsider.HouseholdID+"/invites/"+id, nil, outsider.H()).
			ExpectError(http.StatusNotFound, "Invite not found")
		tg.Do(t, Auth, http.MethodDelete, base+"/"+id, nil, owner.H()).ExpectStatus(http.StatusNoContent).ExpectEmpty()
		// Revoking is idempotent, and a revoked invite is hidden and invalid.
		tg.Do(t, Auth, http.MethodDelete, base+"/"+id, nil, owner.H()).ExpectStatus(http.StatusNoContent)
		if findBy(listOf(t, base, owner.H()), "code", code) != nil {
			t.Fatalf("revoked invite still listed")
		}
		tg.Get(t, Auth, "/invites/"+code+"/validate").Expect(http.StatusOK, Obj{"valid": Eq(false), "household_name": Null})
		tg.Do(t, Auth, http.MethodDelete, base+"/2147483000", nil, owner.H()).ExpectError(http.StatusNotFound, "Invite not found")
	})
}
