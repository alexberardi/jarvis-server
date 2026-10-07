package logs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seed(t *testing.T, m *Module, es ...entry) {
	t.Helper()
	if err := m.store(context.Background(), es); err != nil {
		t.Fatal(err)
	}
}

func messages(es []Entry) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Message)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestQueryFiltersAndCursor(t *testing.T) {
	m, _ := setup(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	seed(t, m,
		entry{ts: at(1), service: "jarvisd", level: "INFO", message: "started"},
		entry{ts: at(2), service: "jarvisd", level: "ERROR", message: "Engine crashed", context: map[string]any{"engine": "llama"}},
		entry{ts: at(3), service: "jarvis-node", level: "WARNING", message: "mic quiet", context: map[string]any{"node_id": "kitchen"}},
		entry{ts: at(4), service: "jarvis-node", level: "DEBUG", message: "tick", context: map[string]any{"node_id": "office"}},
		entry{ts: at(5), service: "jarvisd", level: "CRITICAL", message: "disk full"},
		// Ingested late with an older timestamp: ordering is by time, not id.
		entry{ts: at(0), service: "jarvisd", level: "INFO", message: "boot"},
	)
	q := func(f Filter) []string {
		t.Helper()
		out, _, err := m.Query(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return messages(out)
	}
	cases := []struct {
		name string
		f    Filter
		want []string
	}{
		{"all, newest first", Filter{}, []string{"disk full", "tick", "mic quiet", "Engine crashed", "started", "boot"}},
		{"service", Filter{Service: "jarvis-node"}, []string{"tick", "mic quiet"}},
		{"node", Filter{NodeID: "kitchen"}, []string{"mic quiet"}},
		{"exact levels", Filter{Levels: []string{"INFO", "DEBUG"}}, []string{"tick", "started", "boot"}},
		{"min level", Filter{MinLevel: "WARNING"}, []string{"disk full", "mic quiet", "Engine crashed"}},
		{"levels and min level intersect", Filter{Levels: []string{"INFO", "ERROR"}, MinLevel: "WARNING"}, []string{"Engine crashed"}},
		{"disjoint levels", Filter{Levels: []string{"DEBUG"}, MinLevel: "ERROR"}, []string{}},
		{"time range", Filter{Since: at(2), Until: at(4)}, []string{"tick", "mic quiet", "Engine crashed"}},
		{"text in message, any case", Filter{Text: "ENGINE"}, []string{"Engine crashed"}},
		{"text in context", Filter{Text: "llama"}, []string{"Engine crashed"}},
		{"text is literal, not a regexp", Filter{Text: "d.sk"}, []string{}},
	}
	for _, c := range cases {
		if got := q(c.f); !eq(got, c.want) {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}

	// Pages of 2 walk the whole set exactly once.
	var all []string
	cursor := ""
	for i := 0; ; i++ {
		page, next, err := m.Query(ctx, Filter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, messages(page)...)
		if next == "" {
			break
		}
		if i > 5 {
			t.Fatal("cursor loops")
		}
		cursor = next
	}
	if !eq(all, []string{"disk full", "tick", "mic quiet", "Engine crashed", "started", "boot"}) {
		t.Fatalf("paged: %v", all)
	}
	if _, _, err := m.Query(ctx, Filter{Cursor: "nope"}); !errors.Is(err, ErrBadCursor) {
		t.Fatalf("bad cursor: %v", err)
	}

	out, _, _ := m.Query(ctx, Filter{NodeID: "kitchen"})
	if out[0].NodeID != "kitchen" || out[0].Timestamp != at(3) || out[0].ID == 0 {
		t.Fatalf("entry: %+v", out[0])
	}
}

func TestAfterAndSources(t *testing.T) {
	m, _ := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seed(t, m, entry{ts: now.Add(-48 * time.Hour), service: "old", level: "INFO", message: "ancient", context: map[string]any{"node_id": "attic"}})
	high, err := m.LatestID(ctx)
	if err != nil || high == 0 {
		t.Fatalf("latest: %d %v", high, err)
	}
	seed(t, m,
		entry{ts: now, service: "jarvisd", level: "INFO", message: "a"},
		entry{ts: now, service: "jarvis-node", level: "ERROR", message: "b", context: map[string]any{"node_id": "kitchen"}},
		entry{ts: now, service: "jarvisd", level: "ERROR", message: "c"},
	)
	got, err := m.After(ctx, Filter{MinLevel: "ERROR"}, high, 50)
	if err != nil || !eq(messages(got), []string{"b", "c"}) {
		t.Fatalf("after: %v %v", messages(got), err)
	}
	if got, _ := m.After(ctx, Filter{}, got[1].ID, 50); len(got) != 0 {
		t.Fatalf("nothing newer: %v", messages(got))
	}
	services, nodes, err := m.Sources(ctx, now.Add(-time.Hour))
	if err != nil || !eq(services, []string{"jarvis-node", "jarvisd"}) || !eq(nodes, []string{"kitchen"}) {
		t.Fatalf("sources: %v %v %v", services, nodes, err)
	}
}
