package recipes

import (
	"context"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// TestResolveCaller covers RD7's write household: the token's while still a member, else the
// first membership, else none.
func TestResolveCaller(t *testing.T) {
	hh := &fakeHouseholds{m: map[int64][]string{1: {"A", "B"}, 2: {"B"}}}
	m := &Module{Households: hh}
	for _, c := range []struct {
		id          int64
		claim, want string
		households  []string
	}{
		{1, "B", "B", []string{"A", "B"}},
		{1, "", "A", []string{"A", "B"}},
		{1, "Z", "A", []string{"A", "B"}}, // stale claim
		{2, "A", "B", []string{"B"}},
		{3, "A", "", nil}, // no memberships: private rows only
	} {
		got, err := m.resolve(context.Background(), authn.User{ID: c.id, HouseholdID: c.claim})
		if err != nil {
			t.Fatal(err)
		}
		if got.writeHousehold != c.want || !slices.Equal(got.households, c.households) {
			t.Errorf("user %d claim %q: write %q households %v", c.id, c.claim, got.writeHousehold, got.households)
		}
	}
	// Without a membership source the claim is trusted (tests and tools).
	got, _ := (&Module{}).resolve(context.Background(), authn.User{ID: 1, HouseholdID: "A"})
	if got.writeHousehold != "A" || !slices.Equal(got.households, []string{"A"}) {
		t.Errorf("no lister: %+v", got)
	}
}

// TestVisiblePredicate checks the one scoping predicate against rows: owner, member, a user in
// two households (the union), an outsider, and pre-household rows.
func TestVisiblePredicate(t *testing.T) {
	e := setup(t)
	e.exec(t, `INSERT INTO recipes_recipes (id, user_id, household_id, title) VALUES
		(1, '1', 'A', 'a1'), (2, '2', 'B', 'b2'), (3, '1', NULL, 'mine-old'), (4, '2', NULL, 'theirs-old'), (5, '3', 'C', 'c3')`)
	ids := func(c caller) []int64 {
		pred, args := c.visible("r")
		rows, err := e.d.Read.Query(`SELECT r.id FROM recipes_recipes r WHERE `+pred+` ORDER BY r.id`, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			out = append(out, id)
		}
		return out
	}
	for _, c := range []struct {
		name string
		c    caller
		want []int64
	}{
		{"owner of A", caller{ID: 1, households: []string{"A"}}, []int64{1, 3}},
		{"member of A and B", caller{ID: 2, households: []string{"B", "A"}}, []int64{1, 2, 4}},
		{"outsider", caller{ID: 3, households: []string{"C"}}, []int64{5}},
		{"no household", caller{ID: 1}, []int64{3}},
		{"stranger", caller{ID: 9, households: []string{"Z"}}, nil},
	} {
		if got := ids(c.c); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// authorOnly ignores households.
	pred, args := caller{ID: 1, households: []string{"A"}}.authorOnly("")
	var n int
	if err := e.d.Read.QueryRow(`SELECT COUNT(*) FROM recipes_recipes WHERE `+pred, args...).Scan(&n); err != nil || n != 2 {
		t.Fatalf("authorOnly: %d %v", n, err)
	}
}
