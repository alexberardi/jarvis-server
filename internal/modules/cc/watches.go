package cc

import (
	"context"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Watches (docs/autonomy/PLAN.md §2): the store and the household switch. Checks, firing and the
// voice tools come later; nothing reads max_active or min_cadence_minutes yet.
const (
	settingWatchesEnabled    = "watches.enabled"
	settingWatchesMaxActive  = "watches.max_active"
	settingWatchesMinCadence = "watches.min_cadence_minutes"
)

func watchDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingWatchesEnabled, Category: "watches", Type: settings.Bool, Default: false,
			Description: "Let household members ask Jarvis to watch a web page, a search or a signal and get " +
				"told when it changes."},
		{Key: settingWatchesMaxActive, Category: "watches", Type: settings.Int, Default: int64(10),
			Description: "Most active or paused watches per household."},
		{Key: settingWatchesMinCadence, Category: "watches", Type: settings.Int, Default: int64(30),
			Description: "The shortest check interval, in minutes, a web watch may have."},
	}
}

// registerWatches sets up the watches store. It has no routes.
func (m *Module) registerWatches(_ *http.ServeMux) {
	m.watches = &watches.Store{DB: m.deps.DB, Now: m.now}
}

// watchesEnabled reports whether the household has watches on (watches.enabled). Fail closed: a
// settings error reads as off.
func (m *Module) watchesEnabled(ctx context.Context, hh string) bool {
	return m.settings != nil && m.settings.Bool(ctx, settingWatchesEnabled, settings.Scope{HouseholdID: hh})
}
