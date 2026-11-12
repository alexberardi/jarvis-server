package phone

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

// TestCallSlots: phone_calls.model and phone_calls.planning_model choose the slot a call's live
// turns and its planning run on. Unset (auto) keeps each on the label it was hardcoded to before
// the settings existed: live for the call itself, background for planning and follow-ups.
func TestCallSlots(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	svc, err := settings.New(d, "cc", Definitions(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	s := &Service{Settings: svc}

	if got := s.modelSlot(ctx); got != llm.LabelLive {
		t.Errorf("auto call slot = %s, want %s", got, llm.LabelLive)
	}
	if got := s.planningSlot(ctx); got != llm.LabelBackground {
		t.Errorf("auto planning slot = %s, want %s", got, llm.LabelBackground)
	}

	for _, slot := range []string{llm.LabelLive, llm.LabelBackground, llm.LabelCloud, llm.SlotAuto} {
		if err := svc.Set(ctx, SettingModel, slot, settings.Scope{}); err != nil {
			t.Fatal(err)
		}
		if err := svc.Set(ctx, SettingPlanningModel, slot, settings.Scope{}); err != nil {
			t.Fatal(err)
		}
		wantCall, wantPlan := slot, slot
		if slot == llm.SlotAuto { // each feature's own default
			wantCall, wantPlan = llm.LabelLive, llm.LabelBackground
		}
		if got := s.modelSlot(ctx); got != wantCall {
			t.Errorf("%s: call slot = %s, want %s", slot, got, wantCall)
		}
		if got := s.planningSlot(ctx); got != wantPlan {
			t.Errorf("%s: planning slot = %s, want %s", slot, got, wantPlan)
		}
	}

	// The two are independent: a cloud planner does not put the live call on cloud.
	if err := svc.Set(ctx, SettingModel, llm.SlotAuto, settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Set(ctx, SettingPlanningModel, llm.LabelCloud, settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	if call, plan := s.modelSlot(ctx), s.planningSlot(ctx); call != llm.LabelLive || plan != llm.LabelCloud {
		t.Errorf("independent: call %s, planning %s", call, plan)
	}

	// A free-text model name never reaches the store.
	for _, bad := range []string{"gpt-4o", "LIVE", ""} {
		if err := svc.Set(ctx, SettingModel, bad, settings.Scope{}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
