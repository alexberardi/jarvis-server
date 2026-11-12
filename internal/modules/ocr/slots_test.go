package ocr

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// TestVisionRunsOnItsSlot transcribes an image per slot and reads the label off the request that
// actually reached the model: ocr.model is what the admin Models page writes, and auto must stay
// on background, where this was hardcoded before the setting existed.
func TestVisionRunsOnItsSlot(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	svc, err := settings.New(d, "ocr", Definitions, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	m := &Module{settings: svc}

	for _, tc := range []struct{ set, want string }{
		{llm.SlotAuto, llm.LabelBackground},
		{llm.LabelLive, llm.LabelLive},
		{llm.LabelBackground, llm.LabelBackground},
		{llm.LabelCloud, llm.LabelCloud},
	} {
		if err := svc.Set(ctx, SettingModel, tc.set, settings.Scope{}); err != nil {
			t.Fatal(err)
		}
		model := &fakeLLM{content: `{"text":"hello"}`}
		v := &LLMVision{LLM: model, SlotFn: m.visionSlot}
		if _, err := v.Recognize(ctx, Image{Data: []byte("not-a-real-png"), ContentType: "image/png"}, Options{}); err != nil {
			t.Logf("recognize returned %v (the label is still what is under test)", err)
		}
		if len(model.reqs) == 0 {
			t.Fatalf("%s: no model call", tc.set)
		}
		if got := model.reqs[0].Label; got != tc.want {
			t.Errorf("ocr.model=%s: request went to %s, want %s", tc.set, got, tc.want)
		}
	}

	// With no slot function the engine keeps its pre-setting default rather than panicking.
	model := &fakeLLM{content: `{"text":"hello"}`}
	v := &LLMVision{LLM: model}
	_, _ = v.Recognize(ctx, Image{Data: []byte("x"), ContentType: "image/png"}, Options{})
	if len(model.reqs) == 0 || model.reqs[0].Label != llm.LabelBackground {
		t.Errorf("no SlotFn: %+v", model.reqs)
	}
}
