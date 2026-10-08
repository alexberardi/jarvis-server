package auth

import (
	"context"
	"testing"
)

func TestReadDirectory(t *testing.T) {
	e := newEnv(t)
	a, b := e.register(), e.register()
	// b joins a's household through an invite.
	inv := e.expect(201, "", "POST", "/households/"+a.household+"/invites", map[string]any{}, bearerH(a.access))
	e.expect(200, "", "POST", "/households/join", map[string]any{"invite_code": inv["code"]}, bearerH(b.access))
	// An inactive account is left out (it cannot sign in, so nothing should be imported to it).
	c := e.register()
	if _, err := e.db.Write.Exec(`UPDATE auth_users SET is_active = 0 WHERE id = ?`, c.id); err != nil {
		t.Fatal(err)
	}

	d, err := ReadDirectory(context.Background(), e.db.Read)
	if err != nil {
		t.Fatal(err)
	}
	if d.Emails[a.id] != a.email || d.Emails[b.id] != b.email || d.Emails[c.id] != "" {
		t.Fatalf("emails %v", d.Emails)
	}
	if _, ok := d.Households[a.household]; !ok {
		t.Fatalf("households %v", d.Households)
	}
	var inA []int64
	for _, m := range d.Memberships {
		if m.UserID == c.id {
			t.Fatal("inactive member listed")
		}
		if m.HouseholdID == a.household {
			inA = append(inA, m.UserID)
			if m.UserID == a.id && m.Role != "admin" {
				t.Fatalf("owner role %q", m.Role)
			}
		}
	}
	if len(inA) != 2 || inA[0] != a.id || inA[1] != b.id {
		t.Fatalf("members of a's household, oldest first: %v", inA)
	}
}
