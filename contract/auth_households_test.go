//go:build contract

package contract

import (
	"net/http"
	"strconv"
	"testing"
)

// jarvis-auth households.py: household CRUD, members, leave, and household-scoped nodes.
// Permission model: _require_membership answers 403 "Not a member of this household" for a
// non-member (including for a household id that doesn't exist), then 403
// "Requires <role> role or higher" for an insufficient role. Membership is checked against the
// household in the path, never the JWT's household_id claim (D5: users have several).

func TestAuthHouseholdsCRUD(t *testing.T) {
	tg := T(t)
	owner := NewUser(t)
	outsider := NewUser(t)
	member := NewUser(t)

	hh := NewHousehold(t, owner, "contract household")
	AddMember(t, owner, hh, member, "member")
	const noSuch = "00000000-0000-4000-8000-000000000000"

	t.Run("create validation", func(t *testing.T) {
		tg.Post(t, Auth, "/households", map[string]string{}, owner.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "name"))
		tg.Post(t, Auth, "/households", map[string]string{"name": "x"}).
			ExpectError(http.StatusUnauthorized, "Not authenticated")
	})

	t.Run("list", func(t *testing.T) {
		list := listOf(t, "/households", owner.H())
		if errs := ArrayOf(householdListItem).Match("$", list); len(errs) > 0 {
			t.Fatalf("shape: %v", errs)
		}
		// Both the solo "My Home" from registration and the new one, each as admin.
		solo, created := findBy(list, "id", owner.HouseholdID), findBy(list, "id", hh)
		if solo == nil || created == nil || solo["role"] != "admin" || created["role"] != "admin" || solo["name"] != "My Home" {
			t.Fatalf("GET /households: want solo + created as admin, got %v", list)
		}
		if m := findBy(listOf(t, "/households", member.H()), "id", hh); m == nil || m["role"] != "member" {
			t.Fatalf("member's GET /households should list %s as member", hh)
		}
		tg.Get(t, Auth, "/households").ExpectError(http.StatusUnauthorized, "Not authenticated")
	})

	t.Run("get", func(t *testing.T) {
		tg.Get(t, Auth, "/households/"+hh, member.H()).
			Expect(http.StatusOK, All(householdShape, Open{"id": Eq(hh), "name": Eq("contract household")}))
		tg.Get(t, Auth, "/households/"+hh, outsider.H()).ExpectError(http.StatusForbidden, "Not a member of this household")
		// A nonexistent household is indistinguishable from someone else's: 403, not 404.
		tg.Get(t, Auth, "/households/"+noSuch, owner.H()).ExpectError(http.StatusForbidden, "Not a member of this household")
	})

	t.Run("update", func(t *testing.T) {
		tg.Do(t, Auth, http.MethodPatch, "/households/"+hh, map[string]string{"name": "contract renamed"}, owner.H()).
			Expect(http.StatusOK, All(householdShape, Open{"name": Eq("contract renamed")}))
		tg.Do(t, Auth, http.MethodPatch, "/households/"+hh, map[string]string{"name": "x"}, member.H()).
			ExpectError(http.StatusForbidden, "Requires admin role or higher")
		tg.Do(t, Auth, http.MethodPatch, "/households/"+hh, map[string]string{"name": "x"}, outsider.H()).
			ExpectError(http.StatusForbidden, "Not a member of this household")
		tg.Do(t, Auth, http.MethodPatch, "/households/"+hh, map[string]string{}, owner.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "name"))
	})

	t.Run("delete", func(t *testing.T) {
		gone := NewHousehold(t, owner, "contract doomed")
		AddMember(t, owner, gone, member, "power_user")
		tg.Do(t, Auth, http.MethodDelete, "/households/"+gone, nil, member.H()).
			ExpectError(http.StatusForbidden, "Requires admin role or higher")
		tg.Do(t, Auth, http.MethodDelete, "/households/"+gone, nil, outsider.H()).
			ExpectError(http.StatusForbidden, "Not a member of this household")
		// An admin may delete a household that still has other members (D5: members own it).
		tg.Do(t, Auth, http.MethodDelete, "/households/"+gone, nil, owner.H()).ExpectStatus(http.StatusNoContent).ExpectEmpty()
		tg.Get(t, Auth, "/households/"+gone, member.H()).ExpectError(http.StatusForbidden, "Not a member of this household")
		if findBy(listOf(t, "/households", member.H()), "id", gone) != nil {
			t.Fatalf("deleted household still listed for its former member")
		}
	})
}

func TestAuthHouseholdMembers(t *testing.T) {
	tg := T(t)
	owner := NewUser(t)
	outsider := NewUser(t)
	member := NewUser(t)
	hh := NewHousehold(t, owner, "contract members")
	base := "/households/" + hh + "/members"

	t.Run("add", func(t *testing.T) {
		tg.Post(t, Auth, base, map[string]any{"user_id": member.ID}, owner.H()).
			Expect(http.StatusCreated, All(memberShape, Open{"user_id": Eq(member.ID), "email": Eq(member.Email), "role": Eq("member")}))
		tg.Post(t, Auth, base, map[string]any{"user_id": member.ID}, owner.H()).
			ExpectError(http.StatusBadRequest, "User is already a member of this household")
		tg.Post(t, Auth, base, map[string]any{"user_id": 2147483000}, owner.H()).
			ExpectError(http.StatusNotFound, "User not found")
		tg.Post(t, Auth, base, map[string]any{"user_id": outsider.ID, "role": "owner"}, owner.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "role"))
		tg.Post(t, Auth, base, map[string]any{"user_id": outsider.ID}, member.H()).
			ExpectError(http.StatusForbidden, "Requires admin role or higher")
		tg.Post(t, Auth, base, map[string]any{"user_id": outsider.ID}, outsider.H()).
			ExpectError(http.StatusForbidden, "Not a member of this household")
	})

	t.Run("list", func(t *testing.T) {
		list := listOf(t, base, member.H())
		if errs := ArrayOf(memberShape).Match("$", list); len(errs) > 0 {
			t.Fatalf("shape: %v", errs)
		}
		if o, m := findBy(list, "user_id", owner.ID), findBy(list, "user_id", member.ID); o == nil || m == nil ||
			o["role"] != "admin" || m["role"] != "member" || len(list) != 2 {
			t.Fatalf("members: want owner admin + member, got %v", list)
		}
		tg.Get(t, Auth, base, outsider.H()).ExpectError(http.StatusForbidden, "Not a member of this household")
	})

	t.Run("update role", func(t *testing.T) {
		tg.Do(t, Auth, http.MethodPatch, base+"/"+strconv.Itoa(member.ID), map[string]string{"role": "power_user"}, owner.H()).
			Expect(http.StatusOK, All(memberShape, Open{"user_id": Eq(member.ID), "role": Eq("power_user")}))
		tg.Do(t, Auth, http.MethodPatch, base+"/"+strconv.Itoa(owner.ID), map[string]string{"role": "member"}, owner.H()).
			ExpectError(http.StatusBadRequest, "Cannot demote yourself")
		tg.Do(t, Auth, http.MethodPatch, base+"/"+strconv.Itoa(outsider.ID), map[string]string{"role": "member"}, owner.H()).
			ExpectError(http.StatusNotFound, "Member not found")
		tg.Do(t, Auth, http.MethodPatch, base+"/"+strconv.Itoa(owner.ID), map[string]string{"role": "member"}, member.H()).
			ExpectError(http.StatusForbidden, "Requires admin role or higher")
		tg.Do(t, Auth, http.MethodPatch, base+"/"+strconv.Itoa(member.ID), map[string]string{"role": "boss"}, owner.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "role"))
		// The new role is what the internal role check sees.
		app := SharedApp(t)
		tg.Post(t, Auth, "/internal/validate-household-access",
			map[string]any{"user_id": member.ID, "household_id": hh, "required_role": "power_user"}, app.H()).
			Expect(http.StatusOK, Open{"valid": Eq(true), "role": Eq("power_user")})
	})

	t.Run("leave", func(t *testing.T) {
		// The only admin can't leave while others remain.
		tg.Post(t, Auth, "/households/"+hh+"/leave", nil, owner.H()).
			ExpectError(http.StatusBadRequest, "You're the only admin. Promote another member to admin before leaving.")
		// Nobody can leave their only household.
		tg.Post(t, Auth, "/households/"+outsider.HouseholdID+"/leave", nil, outsider.H()).
			ExpectError(http.StatusBadRequest, "Cannot leave your only household. Join or create another household first.")
		// LEGACY-BUG: leaving a household you're not in is 404, while every other household
		// route answers a non-member with 403 and the same detail.
		tg.Post(t, Auth, "/households/"+hh+"/leave", nil, outsider.H()).
			ExpectError(http.StatusNotFound, "Not a member of this household")

		tg.Post(t, Auth, "/households/"+hh+"/leave", nil, member.H()).
			Expect(http.StatusOK, Obj{"left": Eq(true), "household_id": Eq(hh), "household_deleted": Eq(false)})
		tg.Get(t, Auth, "/households/"+hh, member.H()).ExpectError(http.StatusForbidden, "Not a member of this household")

		// The last member leaving deletes the household.
		solo := NewHousehold(t, owner, "contract last one out")
		tg.Post(t, Auth, "/households/"+solo+"/leave", nil, owner.H()).
			Expect(http.StatusOK, Obj{"left": Eq(true), "household_id": Eq(solo), "household_deleted": Eq(true)})
		if findBy(listOf(t, "/superuser/households", SharedSuperuser(t).H()), "id", solo) != nil {
			t.Fatalf("household %s should be gone after its last member left", solo)
		}
	})

	t.Run("remove", func(t *testing.T) {
		AddMember(t, owner, hh, member, "member")
		tg.Do(t, Auth, http.MethodDelete, base+"/"+strconv.Itoa(owner.ID), nil, owner.H()).
			ExpectError(http.StatusBadRequest, "Cannot kick yourself. Use POST /households/{id}/leave instead.")
		tg.Do(t, Auth, http.MethodDelete, base+"/"+strconv.Itoa(outsider.ID), nil, owner.H()).
			ExpectError(http.StatusNotFound, "Member not found")
		tg.Do(t, Auth, http.MethodDelete, base+"/"+strconv.Itoa(owner.ID), nil, member.H()).
			ExpectError(http.StatusForbidden, "Requires admin role or higher")
		tg.Do(t, Auth, http.MethodDelete, base+"/"+strconv.Itoa(member.ID), nil, owner.H()).ExpectStatus(http.StatusNoContent).ExpectEmpty()
		tg.Get(t, Auth, base, member.H()).ExpectError(http.StatusForbidden, "Not a member of this household")
	})
}

func TestAuthHouseholdNodes(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	owner := NewUser(t)
	outsider := NewUser(t)
	power := NewUser(t)
	plain := NewUser(t)
	hh := NewHousehold(t, owner, "contract nodes")
	AddMember(t, owner, hh, power, "power_user")
	AddMember(t, owner, hh, plain, "member")
	base := "/households/" + hh + "/nodes"

	// A power user may register; the node is registered_by them and gets the listed grants.
	n := NewHouseholdNode(t, power, hh, app.ID)
	expectNodeValidity(t, app, n, n.Key, app.ID, "")
	members := validateNode(t, app, n.ID, n.Key, app.ID)["household_member_ids"].([]any)
	if len(members) != 3 {
		t.Fatalf("household_member_ids: want the 3 members of %s, got %v", hh, members)
	}

	body := func(id, household string) map[string]any {
		return map[string]any{"node_id": id, "household_id": household, "name": "contract node"}
	}
	t.Run("register errors", func(t *testing.T) {
		tg.Post(t, Auth, base, body("contract-"+tg.RunID+"-nope", hh), plain.H()).
			ExpectError(http.StatusForbidden, "Requires power_user role or higher")
		tg.Post(t, Auth, base, body("contract-"+tg.RunID+"-nope", hh), outsider.H()).
			ExpectError(http.StatusForbidden, "Not a member of this household")
		tg.Post(t, Auth, base, body("contract-"+tg.RunID+"-nope", outsider.HouseholdID), owner.H()).
			ExpectError(http.StatusBadRequest, "Household ID in payload must match URL")
		tg.Post(t, Auth, base, body(n.ID, hh), owner.H()).ExpectError(http.StatusBadRequest, "node_id already exists")
		tg.Post(t, Auth, base, map[string]any{"node_id": "x", "household_id": hh}, owner.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "name"))
	})

	t.Run("list", func(t *testing.T) {
		list := listOf(t, base, plain.H())
		if errs := ArrayOf(nodeListItem).Match("$", list); len(errs) > 0 {
			t.Fatalf("shape: %v", errs)
		}
		got := findBy(list, "node_id", n.ID)
		if got == nil || got["is_active"] != true || mustFloat(got["registered_by_user_id"]) != float64(power.ID) {
			t.Fatalf("household node list: want %s registered by %d, got %v", n.ID, power.ID, list)
		}
		if s := got["services"].([]any); len(s) != 1 || s[0] != app.ID {
			t.Fatalf("services: want [%s], got %v", app.ID, s)
		}
		tg.Get(t, Auth, base, outsider.H()).ExpectError(http.StatusForbidden, "Not a member of this household")
	})
}
