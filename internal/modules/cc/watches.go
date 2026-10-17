package cc

import (
	"context"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Watches (docs/autonomy/PLAN.md §2): the store, the household switch, firing (watches_fire.go)
// and the hourly expiry sweep. Checks and the voice tools come later; nothing reads max_active or
// min_cadence_minutes yet.
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

// registerWatches sets up the watches store and the expiry sweep's job. It has no routes.
func (m *Module) registerWatches(_ *http.ServeMux) {
	m.watches = &watches.Store{DB: m.deps.DB, Now: m.now}
	if m.deps.Queue != nil {
		m.deps.Queue.Register(watchSweepJob, queue.Handler{Run: func(ctx context.Context, _ queue.Job) ([]byte, error) {
			return nil, m.sweepWatches(ctx)
		}})
	}
}

// startWatches schedules the expiry sweep (D27: a trigger, not a loop).
func (m *Module) startWatches(ctx context.Context) error {
	if m.deps.Scheduler == nil {
		return nil
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: watchSweepJob, Kind: scheduler.KindInterval, JobType: watchSweepJob,
		Spec: scheduler.Spec{Every: time.Hour},
	})
}

// watchesEnabled reports whether the household has watches on (watches.enabled). Fail closed: a
// settings error reads as off.
func (m *Module) watchesEnabled(ctx context.Context, hh string) bool {
	return m.settings != nil && m.settings.Bool(ctx, settingWatchesEnabled, settings.Scope{HouseholdID: hh})
}
