package settings

import (
	"context"
	"errors"
	"testing"
)

// HouseholdValues lists only household-scope rows with a value, coerced and secret-masked.
func TestHouseholdValues(t *testing.T) {
	s := newService(t, nil)
	ctx := context.Background()
	const k = "memory.pinned_max_chars"
	for _, w := range []struct {
		v  any
		sc Scope
	}{
		{int64(100), Scope{}},                                 // system: not a household value
		{int64(200), Scope{HouseholdID: "hh-b"}},              // listed
		{int64(300), Scope{HouseholdID: "hh-a"}},              // listed, sorts first
		{int64(400), Scope{HouseholdID: "hh-a", NodeID: "n"}}, // node scope: not listed
		{int64(500), Scope{UserID: 7}},                        // user scope: not listed
		{nil, Scope{HouseholdID: "hh-c"}},                     // cleared: not listed
	} {
		if err := s.Set(ctx, k, w.v, w.sc); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.HouseholdValues(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].HouseholdID != "hh-a" || got[0].Value != int64(300) ||
		got[1].HouseholdID != "hh-b" || got[1].Value != int64(200) || got[0].UpdatedAt == "" {
		t.Fatalf("household values: %+v", got)
	}

	const secret = "phone_calls.call_context"
	if err := s.Set(ctx, secret, map[string]any{"a": 1}, Scope{HouseholdID: "hh-a"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.HouseholdValues(ctx, secret); len(got) != 1 || got[0].Value != "********" {
		t.Fatalf("secret not masked: %+v", got)
	}
	if _, err := s.HouseholdValues(ctx, "nope"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key: %v", err)
	}
}

// Delete removes exactly one scope's row; the cascade then reaches the next level.
func TestDeleteExactScope(t *testing.T) {
	s := newService(t, nil)
	ctx := context.Background()
	const k = "memory.enabled"
	hh := Scope{HouseholdID: "hh"}
	s.Set(ctx, k, false, Scope{})
	s.Set(ctx, k, true, hh)
	s.Set(ctx, k, true, Scope{HouseholdID: "hh", NodeID: "n"})

	removed, err := s.Delete(ctx, k, hh)
	if err != nil || !removed {
		t.Fatalf("delete: %v %v", removed, err)
	}
	if v, _ := s.Get(ctx, k, hh); v.Value != false {
		t.Fatalf("household now reads %v, want the system value", v.Value)
	}
	if v, _ := s.Get(ctx, k, Scope{HouseholdID: "hh", NodeID: "n"}); v.Value != true {
		t.Fatalf("the node row went too: %v", v.Value)
	}
	if v, _ := s.Get(ctx, k, Scope{}); v.Value != false || !v.FromDB {
		t.Fatalf("the system row went too: %+v", v)
	}
	if removed, err := s.Delete(ctx, k, hh); err != nil || removed {
		t.Fatalf("second delete: %v %v", removed, err)
	}
	if _, err := s.Delete(ctx, "nope", hh); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key: %v", err)
	}
}
