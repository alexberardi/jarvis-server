package llm

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The cloud label is reached only by a caller naming it inside jarvisd (decisions/
// 2026-10-22-cloud-model-slot.md): unset, the call fails without touching any engine (no
// fallback to live); set, it goes to the cloud endpoint alone. The public API can't pick it.
func TestCloudLabelRouting(t *testing.T) {
	e := setup(t)
	s := e.m.Service()

	_, err := s.Chat(e.ctx, ChatRequest{Label: LabelCloud, Messages: []Message{msg("user", "hi")}})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusServiceUnavailable {
		t.Fatalf("unset cloud: %v", err)
	}
	if n := len(e.engine.requests()); n != 0 {
		t.Fatalf("unset cloud sent %d requests to the local engine", n)
	}

	cloud := newFakeEngine()
	t.Cleanup(cloud.close)
	e.res.mu.Lock()
	e.res.eps[LabelCloud] = Endpoint{BaseURL: cloud.srv.URL, Model: "gpt-cloud", APIKey: "sk-cloud", Remote: true, Vision: true}
	e.res.mu.Unlock()
	resp, err := s.Chat(e.ctx, ChatRequest{Label: LabelCloud, Priority: PriorityBackground, Messages: []Message{msg("user", "hi")}})
	if err != nil || resp.Content != "OK" {
		t.Fatalf("cloud chat %+v %v", resp, err)
	}
	if b := cloud.last(); b == nil || b["model"] != "gpt-cloud" {
		t.Fatalf("cloud payload %v", b)
	}
	if n := len(e.engine.requests()); n != 0 {
		t.Fatalf("cloud call reached the local engine (%d requests)", n)
	}

	// "cloud" as the public API's model name is live, as any unknown name is.
	before := len(cloud.requests())
	r := e.post("/v1/chat/completions", chat("hi", map[string]any{"model": "cloud"}))
	if r.json(t)["model"] != LabelLive || len(cloud.requests()) != before {
		t.Fatalf("public model=cloud: %s, cloud requests %d", r.body, len(cloud.requests())-before)
	}
	if b := e.engine.last(); b == nil || b["model"] != "qwen-live.gguf" {
		t.Fatalf("public model=cloud payload %v", b)
	}
}

// SlotSetting is the per-feature slot choice: auto, live, background or cloud.
func TestSlotSetting(t *testing.T) {
	d := SlotSetting("computer.model", "computer", "Which model drives computer tasks.")
	if d.Key != "computer.model" || d.Type != settings.String || d.Default != SlotAuto ||
		!slices.Equal(d.Options, []any{SlotAuto, LabelLive, LabelBackground, LabelCloud}) {
		t.Fatalf("%+v", d)
	}
	for _, ok := range []any{"auto", "live", "background", "cloud"} {
		if err := d.Validate(ok); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, bad := range []any{"", "gpt-4o", "Cloud", int64(1)} {
		if err := d.Validate(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
