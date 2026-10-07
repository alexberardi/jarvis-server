package settings

import (
	"context"
	"testing"
)

func TestGetExactNoCascadeNoFallback(t *testing.T) {
	s := newService(t, map[string]string{"JARVIS_LLM_PROXY_API_URL": "http://env:1"})
	ctx := context.Background()
	const k = "llm.proxy.url"
	hh := Scope{HouseholdID: "hh"}
	expect := func(sc Scope, want any, found bool) {
		t.Helper()
		v, ok, err := s.GetExact(ctx, k, sc)
		if err != nil || ok != found || v != want {
			t.Fatalf("scope %+v: got %v found=%v err=%v, want %v found=%v", sc, v, ok, err, want, found)
		}
	}
	// No rows: neither the env fallback nor the default counts.
	expect(Scope{}, nil, false)
	expect(hh, nil, false)
	if err := s.Set(ctx, k, "http://system:1", Scope{}); err != nil {
		t.Fatal(err)
	}
	expect(Scope{}, "http://system:1", true)
	expect(hh, nil, false) // the system row does not cascade down
	if err := s.Set(ctx, k, "http://hh:1", hh); err != nil {
		t.Fatal(err)
	}
	expect(hh, "http://hh:1", true)
	// A cleared (null or empty) row is not a value.
	if err := s.Set(ctx, k, nil, hh); err != nil {
		t.Fatal(err)
	}
	expect(hh, nil, false)
	if err := s.Set(ctx, k, "", Scope{}); err != nil {
		t.Fatal(err)
	}
	expect(Scope{}, nil, false)
	if _, _, err := s.GetExact(ctx, "nope", Scope{}); err != ErrUnknownKey {
		t.Fatalf("unknown key: %v", err)
	}
}
