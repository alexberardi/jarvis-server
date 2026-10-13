package auth

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestUserNamesPreferDisplayName: the in-process name resolution (speaker and member names
// for the model's prompt) answers the display name when set, the username otherwise; a blank
// or whitespace-only stored display name counts as unset.
func TestUserNamesPreferDisplayName(t *testing.T) {
	e := newEnv(t)
	named, plain, blank := e.register(), e.register(), e.register()
	e.expect(200, "", "PATCH", "/auth/me", map[string]any{"display_name": "Sam"}, bearerH(named.access))
	// The API clears blanks itself; the database may still hold one (legacy import, hand edits).
	if _, err := e.db.Write.Exec(`UPDATE auth_users SET display_name = '   ' WHERE id = ?`, blank.id); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.UserNames(context.Background(), []int64{named.id, plain.id, blank.id, 999999})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{
		named.id: "Sam",
		plain.id: strings.SplitN(plain.email, "@", 2)[0],
		blank.id: strings.SplitN(blank.email, "@", 2)[0],
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("id %d: got %q, want %q (all %v)", id, got[id], w, got)
		}
	}
}

// TestUsersBatchDisplayNames: /internal/users/batch keeps "users" as id → username (the
// legacy contract) and adds "display_names": id → display name, null when unset.
func TestUsersBatchDisplayNames(t *testing.T) {
	e := newEnv(t)
	named, plain := e.register(), e.register()
	e.expect(200, "", "PATCH", "/auth/me", map[string]any{"display_name": "Sam"}, bearerH(named.access))
	m := e.expect(200, "", "GET", fmt.Sprintf("/internal/users/batch?user_ids=%d&user_ids=%d&user_ids=999999", named.id, plain.id), nil, e.appH())
	if keysOf(m) != "display_names,users" {
		t.Fatal(m)
	}
	users, dn := m["users"].(map[string]any), m["display_names"].(map[string]any)
	nk, pk := fmt.Sprint(named.id), fmt.Sprint(plain.id)
	if users[nk] != strings.SplitN(named.email, "@", 2)[0] || users[pk] != strings.SplitN(plain.email, "@", 2)[0] || len(users) != 2 {
		t.Fatalf("users %v", users)
	}
	if v, ok := dn[pk]; dn[nk] != "Sam" || !ok || v != nil || len(dn) != 2 {
		t.Fatalf("display_names %v", dn)
	}
}
