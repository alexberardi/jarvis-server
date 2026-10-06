//go:build contract

package contract

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Helpers and shapes for the jarvis-auth route tests (auth_*_test.go). Every fixture here is
// tagged contract-<runid> and cleaned up with t.Cleanup. Cleanups run LIFO, so create users
// first, then the households/nodes they own: the household or node is then removed before its
// owner is deleted (an active node registered by a user, or a shared household whose only
// admin is the user, blocks DELETE /auth/me with a 409).

// --- shapes (pydantic response models in jarvis_auth/app/schemas) ---

var householdRole = OneOf("member", "power_user", "admin")

// householdCreated is HouseholdCreateResponse.
var householdCreated = Obj{"id": UUID, "name": String, "created_at": TimestampUTC}

// householdShape is HouseholdResponse (GET/PATCH /households/{id}).
var householdShape = Obj{"id": UUID, "name": String, "created_at": TimestampUTC, "updated_at": TimestampUTC}

// householdListItem is HouseholdListItem (GET /households).
var householdListItem = Obj{"id": UUID, "name": String, "role": householdRole, "created_at": TimestampUTC}

// memberShape is MemberResponse / MemberListItem.
var memberShape = Obj{
	"user_id":    Int,
	"username":   String,
	"email":      NonEmptyString,
	"role":       householdRole,
	"created_at": TimestampUTC,
}

// nodeCreated is NodeCreateResponse (admin and household node registration).
var nodeCreated = Obj{
	"node_id":               NonEmptyString,
	"name":                  String,
	"household_id":          UUID,
	"registered_by_user_id": NullOr(Int),
	"node_key":              NonEmptyString,
	"created_at":            TimestampUTC,
	"services":              ArrayOf(String),
}

// nodeListItem is NodeListItem (admin, household and superuser node lists).
var nodeListItem = Obj{
	"node_id":               NonEmptyString,
	"name":                  String,
	"household_id":          UUID,
	"registered_by_user_id": NullOr(Int),
	"is_active":             Bool,
	"created_at":            TimestampUTC,
	"updated_at":            TimestampUTC,
	"last_rotated_at":       NullOr(TimestampUTC),
	"services":              ArrayOf(String),
}

// inviteShape is InviteResponse. Codes are 8 chars from A-Z2-9 minus 0/O/1/I (the source
// comment says L is excluded too; it is not).
var inviteShape = Obj{
	"id":           Int,
	"household_id": UUID,
	"code":         Regexp(`^[A-HJ-NP-Z2-9]{8}$`),
	"default_role": householdRole,
	"max_uses":     NullOr(Int),
	"use_count":    Int,
	"expires_at":   TimestampUTC,
	"revoked":      Bool,
	"created_at":   TimestampUTC,
}

// --- helpers ---

// jwtClaims decodes a JWT's payload without verifying it.
func jwtClaims(t testing.TB, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWS: %q", tok)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("JWT payload: %v", err)
	}
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("JWT payload JSON: %v", err)
	}
	return c
}

// findBy returns the first element of a JSON array whose key equals want.
func findBy(list []any, key string, want any) map[string]any {
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		v := m[key]
		if f, ok := want.(int); ok {
			if v != nil && mustFloat(v) == float64(f) {
				return m
			}
			continue
		}
		if v == want {
			return m
		}
	}
	return nil
}

// RegisterUser registers a user with an arbitrary body and headers (invite codes,
// X-Household-Id), expecting 201. Cleanup deletes it unless the test already did.
func RegisterUser(t testing.TB, extra map[string]string, hs ...H) *User {
	t.Helper()
	tg := T(t)
	tg.NeedAdmin(t)
	userSeq.Lock()
	userSeq.n++
	n := userSeq.n
	userSeq.Unlock()
	u := &User{
		Email:    fmt.Sprintf("contract-%s-%d@example.com", tg.RunID, n),
		Password: "contract-" + randHex(8),
	}
	body := map[string]string{"email": u.Email, "password": u.Password}
	for k, v := range extra {
		body[k] = v
	}
	r := tg.Post(t, Auth, "/auth/register", body, hs...).Expect(http.StatusCreated, registerResponse)
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		HouseholdID  string `json:"household_id"`
		User         struct {
			ID int `json:"id"`
		} `json:"user"`
	}
	r.Decode(&out)
	u.ID, u.AccessToken, u.RefreshToken, u.HouseholdID = out.User.ID, out.AccessToken, out.RefreshToken, out.HouseholdID
	t.Cleanup(func() { cleanupUser(t, tg, u) })
	return u
}

// DisposableUser is NewUser whose cleanup tolerates the test having deleted the user itself.
func DisposableUser(t testing.TB) *User {
	t.Helper()
	tg := T(t)
	tg.NeedAdmin(t)
	u, err := tg.registerUser()
	if err != nil {
		t.Fatalf("fixture user: %v", err)
	}
	t.Cleanup(func() { cleanupUser(t, tg, u) })
	return u
}

func cleanupUser(t testing.TB, tg *Target, u *User) {
	r, err := tg.do(Auth, http.MethodGet, "/admin/users/by-email/"+url.PathEscape(u.Email), nil, tg.AdminH())
	if err == nil && r.Status == http.StatusNotFound {
		return // the test deleted it
	}
	if err := tg.deleteUser(u); err != nil {
		t.Errorf("cleanup user %s: %v", u.Email, err)
	}
}

// NewHousehold creates a household with owner as admin. Cleanup deletes it as owner; a 403
// (already deleted, or the owner left) or 401 (the test deleted the owner) is fine.
func NewHousehold(t testing.TB, owner *User, name string) string {
	t.Helper()
	tg := T(t)
	r := tg.Post(t, Auth, "/households", map[string]string{"name": name}, owner.H()).
		Expect(http.StatusCreated, householdCreated)
	id := r.Object()["id"].(string)
	t.Cleanup(func() {
		r, err := tg.do(Auth, http.MethodDelete, "/households/"+id, nil, owner.H())
		if err != nil {
			t.Errorf("cleanup household %s: %v", id, err)
			return
		}
		if r.Status != http.StatusNoContent && r.Status != http.StatusForbidden && r.Status != http.StatusUnauthorized {
			t.Errorf("cleanup household %s: %s", id, r.describe())
		}
	})
	return id
}

// AddMember has admin add m to household hh with role.
func AddMember(t testing.TB, admin *User, hh string, m *User, role string) {
	t.Helper()
	T(t).Post(t, Auth, "/households/"+hh+"/members", map[string]any{"user_id": m.ID, "role": role}, admin.H()).
		Expect(http.StatusCreated, memberShape)
}

// NewHouseholdNode registers a node in hh through the user-facing route (as by), so the node
// is registered_by that user. Cleanup deactivates it (404 after a household cascade is fine).
func NewHouseholdNode(t testing.TB, by *User, hh string, services ...string) *Node {
	t.Helper()
	tg := T(t)
	if services == nil {
		services = []string{}
	}
	id := fmt.Sprintf("contract-%s-node-%s", tg.RunID, randHex(3))
	r := tg.Post(t, Auth, "/households/"+hh+"/nodes", map[string]any{
		"node_id": id, "household_id": hh, "name": "contract node", "services": services,
	}, by.H()).Expect(http.StatusCreated, nodeCreated)
	n := &Node{ID: id, Key: r.Object()["node_key"].(string), HouseholdID: hh, Services: services}
	t.Cleanup(func() {
		if err := tg.deactivateNode(n); err != nil {
			t.Errorf("cleanup node %s: %v", n.ID, err)
		}
	})
	return n
}

// validateNode calls POST /internal/validate-node and returns the decoded body.
func validateNode(t testing.TB, app *App, nodeID, key, service string) map[string]any {
	t.Helper()
	return T(t).Post(t, Auth, "/internal/validate-node",
		map[string]string{"node_id": nodeID, "node_key": key, "service_id": service}, app.H()).
		ExpectStatus(http.StatusOK).Object()
}

// expectNodeValidity fails unless validate-node said valid (or, with reason != "", invalid for that reason).
func expectNodeValidity(t testing.TB, app *App, n *Node, key, service, reason string) {
	t.Helper()
	got := validateNode(t, app, n.ID, key, service)
	if reason == "" {
		if got["valid"] != true {
			t.Fatalf("validate-node %s: want valid, got %v", n.ID, got)
		}
		return
	}
	if got["valid"] != false || got["reason"] != reason {
		t.Fatalf("validate-node %s: want invalid %q, got %v", n.ID, reason, got)
	}
}

// listOf GETs path and returns the JSON array.
func listOf(t testing.TB, path string, hs ...H) []any {
	t.Helper()
	r := T(t).Get(t, Auth, path, hs...).ExpectStatus(http.StatusOK)
	a, ok := r.JSON().([]any)
	if !ok {
		r.Fatalf("want a JSON array")
	}
	return a
}
