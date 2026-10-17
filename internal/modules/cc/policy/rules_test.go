package policy

import (
	"context"
	"database/sql"
	"testing"
)

func TestPutRuleAndLookup(t *testing.T) {
	_, s, _ := storeEnv(t)
	ctx := context.Background()
	uid := int64(7)
	got, err := s.PutRule(ctx, Rule{HouseholdID: "hh-1", ScopeKind: ScopeHousehold, Tool: " quick_search ", Effect: Allow, Note: "fine", UpdatedBy: &uid})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 || got.Tool != "quick_search" || got.Note != "fine" || got.UpdatedBy == nil || *got.UpdatedBy != 7 {
		t.Fatalf("put: %+v", got)
	}
	r, err := s.Rule(ctx, "hh-1", "household", "", "quick_search")
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.ID != got.ID || r.Effect != Allow || r.Note != "fine" || r.UpdatedBy == nil || *r.UpdatedBy != 7 {
		t.Fatalf("lookup: %+v", r)
	}
	none, err := s.Rule(ctx, "hh-1", "household", "", "make_phone_call")
	if err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Fatalf("other tool: %+v", none)
	}
	other, err := s.Rule(ctx, "hh-2", "household", "", "quick_search")
	if err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Fatalf("other household: %+v", other)
	}
}

func TestPutRuleUpserts(t *testing.T) {
	_, s, _ := storeEnv(t)
	ctx := context.Background()
	uid := int64(7)
	first, err := s.PutRule(ctx, Rule{HouseholdID: "hh-1", ScopeKind: ScopeHousehold, Tool: "quick_search", Effect: Allow, UpdatedBy: &uid})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PutRule(ctx, Rule{HouseholdID: "hh-1", ScopeKind: ScopeHousehold, Tool: "quick_search", Effect: Block, Note: "not fine"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("upsert changed the id: %d -> %d", first.ID, second.ID)
	}
	r, err := s.Rule(ctx, "hh-1", "household", "", "quick_search")
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Effect != Block || r.Note != "not fine" || r.UpdatedBy != nil {
		t.Fatalf("after upsert: %+v", r)
	}
	rules, err := s.ListRules(ctx, "hh-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("rows: %d, want 1", len(rules))
	}
}

func TestPutRuleValidation(t *testing.T) {
	_, s, _ := storeEnv(t)
	ctx := context.Background()
	for _, r := range []Rule{
		{HouseholdID: "hh-1", ScopeKind: ScopeHousehold, Tool: "  ", Effect: Allow},
		{HouseholdID: "hh-1", ScopeKind: ScopeHousehold, Tool: "quick_search", Effect: "maybe"},
		{HouseholdID: "hh-1", ScopeKind: ScopeGoal, Tool: "quick_search", Effect: Allow},
		{HouseholdID: "hh-1", ScopeKind: ScopeHousehold, ScopeID: "x", Tool: "quick_search", Effect: Allow},
		{HouseholdID: "hh-1", ScopeKind: "bogus", Tool: "quick_search", Effect: Allow},
	} {
		if _, err := s.PutRule(ctx, r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

func TestListAndDeleteRules(t *testing.T) {
	_, s, _ := storeEnv(t)
	ctx := context.Background()
	uid := int64(7)
	household, err := s.PutRule(ctx, Rule{HouseholdID: "hh-1", ScopeKind: ScopeHousehold, Tool: "notify", Effect: Ask, UpdatedBy: &uid})
	if err != nil {
		t.Fatal(err)
	}
	watch, err := s.PutRule(ctx, Rule{HouseholdID: "hh-1", ScopeKind: ScopeWatch, ScopeID: "wt_1", Tool: "notify", Effect: Allow, UpdatedBy: &uid})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListRules(ctx, "hh-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != household.ID || got[1].ID != watch.ID {
		t.Fatalf("order: %+v", got)
	}
	ok, err := s.DeleteRule(ctx, "hh-2", household.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("deleted another household's rule")
	}
	ok, err = s.DeleteRule(ctx, "hh-1", household.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("delete reported false for an existing rule")
	}
	left, err := s.ListRules(ctx, "hh-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].ID != watch.ID {
		t.Fatalf("after delete: %+v", left)
	}
}

func TestDecideWithStore(t *testing.T) {
	_, s, _ := storeEnv(t)
	ctx := context.Background()
	uid := int64(7)
	if _, err := s.PutRule(ctx, Rule{HouseholdID: "hh-1", ScopeKind: ScopeHousehold, Tool: "quick_search", Effect: Block, UpdatedBy: &uid}); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Rules: s}
	v := p.Decide(ctx, "hh-1", Action{Tool: "quick_search"})
	if v.Effect != Block || v.Rule == nil || v.Rule.ID == 0 || v.Scope != ScopeHousehold {
		t.Fatalf("verdict: %+v", v)
	}
}

func TestRulesPurgeUser(t *testing.T) {
	d, s, _ := storeEnv(t)
	ctx := context.Background()
	uid := int64(7)
	if _, err := s.PutRule(ctx, Rule{HouseholdID: "hh-1", ScopeKind: ScopeHousehold, Tool: "quick_search", Effect: Allow, UpdatedBy: &uid}); err != nil {
		t.Fatal(err)
	}
	err := d.Tx(ctx, func(tx *sql.Tx) error { return s.PurgeUser(ctx, tx, uid) })
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Rule(ctx, "hh-1", "household", "", "quick_search")
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.UpdatedBy != nil {
		t.Fatalf("updated_by not purged: %+v", r)
	}
}
