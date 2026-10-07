package cc

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// The admin gateway reads traces in process (A3): the same rows and shapes as the admin-key
// route, with the query parsed the same way.
func TestTracesInProcess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.createNode("n1", "hh1")
	id, err := e.m.RecordTrace(ctx, Trace{ConversationID: "c1", RequestType: "voice_command", Source: "node", NodeID: "n1",
		HouseholdID: "hh1", TotalDurationMS: 3, Spans: []map[string]any{{"name": "stt"}, {"name": "llm"}}})
	if err != nil {
		t.Fatal(err)
	}
	e.m.RecordTrace(ctx, Trace{ConversationID: "c2", RequestType: "stt", Source: "mobile", Status: "error", TotalDurationMS: 1})

	f, problem := ParseTraceFilter(url.Values{"household_id": {"hh1"}})
	if problem != "" || f.Limit != 50 || f.HouseholdID != "hh1" {
		t.Fatalf("parse: %+v %q", f, problem)
	}
	traces, total, err := e.m.ListTraces(ctx, f)
	if err != nil || total != 1 || len(traces) != 1 || traces[0]["id"] != id || traces[0]["span_count"] != 2 {
		t.Fatalf("list: %v %d %v", traces, total, err)
	}
	if _, total, _ := e.m.ListTraces(ctx, TraceFilter{}); total != 2 {
		t.Fatalf("unfiltered total %d", total)
	}
	for _, bad := range []string{"limit=0", "limit=201", "offset=-1", "limit=x"} {
		q, _ := url.ParseQuery(bad)
		if _, p := ParseTraceFilter(q); p == "" {
			t.Errorf("%s accepted", bad)
		}
	}
	got, found, err := e.m.GetTrace(ctx, id)
	if err != nil || !found || len(got["spans"].([]any)) != 2 {
		t.Fatalf("get: %v %v %v", got, found, err)
	}
	if _, found, err := e.m.GetTrace(ctx, "nope"); found || err != nil {
		t.Fatalf("missing trace: %v %v", found, err)
	}
}

// AD4: the provider comes from the setting when set, else from the live model; an override
// must name a registered provider, and "" clears it.
func TestPromptProviderStatus(t *testing.T) {
	derived := ""
	e := newEnv(t, envOpts{configure: func(m *Module) {
		m.DefaultPromptProvider = func(context.Context) string { return derived }
	}})
	ctx := context.Background()
	names := prompts.Names()
	if len(names) < 2 {
		t.Fatalf("need two providers, have %v", names)
	}

	st := e.m.PromptProvider(ctx)
	if st.Effective != "" || st.Source != "" || st.Valid || len(st.Options) != len(names) {
		t.Fatalf("nothing set: %+v", st)
	}
	derived = names[0]
	if st := e.m.PromptProvider(ctx); st.Effective != names[0] || st.Source != PromptSourceModel || !st.Valid || st.Value != "" {
		t.Fatalf("from model: %+v", st)
	}
	if err := e.m.SetPromptProvider(ctx, names[1]); err != nil {
		t.Fatal(err)
	}
	if st := e.m.PromptProvider(ctx); st.Effective != names[1] || st.Source != PromptSourceSetting || st.Derived != names[0] {
		t.Fatalf("override: %+v", st)
	}
	if p, err := e.m.promptProvider(ctx); err != nil || p.Name() != names[1] {
		t.Fatalf("voice turn uses %v %v", p, err)
	}
	if err := e.m.SetPromptProvider(ctx, "Llama2"); !errors.Is(err, ErrUnknownPromptProvider) {
		t.Fatalf("unknown provider: %v", err)
	}
	if err := e.m.SetPromptProvider(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if st := e.m.PromptProvider(ctx); st.Source != PromptSourceModel || st.Effective != names[0] {
		t.Fatalf("cleared: %+v", st)
	}
}
