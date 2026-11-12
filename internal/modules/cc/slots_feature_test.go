package cc

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// setSlot stores a feature's slot choice system-wide, as the admin Models page does.
func setSlot(t *testing.T, m *Module, key, slot string) {
	t.Helper()
	if err := m.Settings().Set(context.Background(), key, slot, settings.Scope{}); err != nil {
		t.Fatal(err)
	}
}

// slotCases is auto plus each slot a feature can be moved to. auto must land on the label the
// code hardcoded before the setting existed, so an upgrade changes nothing.
func slotCases(auto string) []struct{ set, want string } {
	return []struct{ set, want string }{
		{llm.SlotAuto, auto},
		{llm.LabelLive, llm.LabelLive},
		{llm.LabelBackground, llm.LabelBackground},
		{llm.LabelCloud, llm.LabelCloud},
	}
}

// TestWatchesRunOnTheirSlot drives a real watch check per slot and reads the label off the
// request that reached the model: watches.model is what the admin page writes.
func TestWatchesRunOnTheirSlot(t *testing.T) {
	for _, tc := range slotCases(llm.LabelBackground) {
		t.Run(tc.set, func(t *testing.T) {
			we := newWebWatchEnv(t)
			setSlot(t, we.m, SettingWatchesModel, tc.set)
			w := we.createURL()
			we.check(w.ID)
			if len(we.model.reqs) == 0 {
				t.Fatal("the watch made no model call")
			}
			if got := we.model.reqs[0].Label; got != tc.want {
				t.Errorf("watches.model=%s: request went to %s, want %s", tc.set, got, tc.want)
			}
		})
	}
}
