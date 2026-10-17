package cc

import (
	"context"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
)

// Wiring for the autonomy activity log (docs/autonomy/PLAN.md §0.3): the store and the policy
// that decides background actions. Nothing is gated on it yet; P1 adds the rules and the calls.

// registerAutonomy builds the activity-log store and the (rule-less) policy.
func (m *Module) registerAutonomy() {
	m.autonomy = &policy.Store{DB: m.deps.DB, Now: m.now}
	m.policy = &policy.Policy{}
}

// recordAutonomy appends one autonomy event; a failure to log is a warning, not an error to the
// caller (the log is the audit surface, not the action path).
func (m *Module) recordAutonomy(ctx context.Context, e policy.Event) {
	if err := m.autonomy.Record(ctx, e); err != nil {
		m.deps.Log.Warn("cc: autonomy event not recorded", "source", e.Source, "err", err)
	}
}
