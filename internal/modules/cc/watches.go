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

// Watches (docs/autonomy/PLAN.md §2): the store, the household switch, firing (watches_fire.go),
// the signal source (watches_signal.go), web checks (watches_web.go), the voice tools
// (watch_tools.go) and the hourly expiry sweep.
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

// registerWatches sets up the watches store, the signal source (watches_signal.go), the web
// check's job and the expiry sweep's job. It has no routes.
func (m *Module) registerWatches(_ *http.ServeMux) {
	m.watches = &watches.Store{DB: m.deps.DB, Now: m.now}
	m.registerWatchSignals()
	if m.deps.Queue != nil {
		m.deps.Queue.Register(watchSweepJob, queue.Handler{Run: func(ctx context.Context, _ queue.Job) ([]byte, error) {
			return nil, m.sweepWatches(ctx)
		}})
		m.deps.Queue.Register(watchCheckJob, queue.Handler{Run: m.runWatchCheck, Concurrency: 1, MaxAttempts: 1,
			Lease: 5 * time.Minute})
	}
}

// startWatches schedules the expiry sweep (D27: a trigger, not a loop) and arms any web watch
// whose next check is missing.
func (m *Module) startWatches(ctx context.Context) error {
	if m.deps.Scheduler == nil {
		return nil
	}
	if err := m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: watchSweepJob, Kind: scheduler.KindInterval, JobType: watchSweepJob,
		Spec: scheduler.Spec{Every: time.Hour},
	}); err != nil {
		return err
	}
	return m.reconcileWatchTriggers(ctx)
}

// watchesEnabled reports whether the household has watches on (watches.enabled). Fail closed: a
// settings error reads as off.
func (m *Module) watchesEnabled(ctx context.Context, hh string) bool {
	return m.settings != nil && m.settings.Bool(ctx, settingWatchesEnabled, settings.Scope{HouseholdID: hh})
}
