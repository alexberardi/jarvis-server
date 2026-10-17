package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// The signal source (docs/autonomy/PLAN.md §2): a "*" reaction checks every signal against the
// household's active signal watches on its kind. absent_for arms its own KindOnce timer when the
// value moves off the wanted one, and fires when the timer runs out before the value comes back.
const watchAbsentJob = "cc.watch_absent"

func watchAbsentTrigger(id string) string { return watchTrigger(id) + ":absent" }

type watchAbsentPayload struct {
	HouseholdID string `json:"household_id"`
	WatchID     string `json:"watch_id"`
}

// registerWatchSignals adds the watch reaction and the absent timer's job.
func (m *Module) registerWatchSignals() {
	m.registerReaction("*", reaction{name: "watch", run: m.reactWatch, dedup: func(rc reactionCtx) string {
		return "watch:" + rc.HouseholdID + ":" + rc.Kind + ":" + shortHash(rc.Facts)
	}})
	if m.deps.Queue != nil {
		m.deps.Queue.Register(watchAbsentJob, queue.Handler{Run: m.runWatchAbsentJob})
	}
}

// reactWatch evaluates the signal against each active watch on its kind.
func (m *Module) reactWatch(ctx context.Context, rc reactionCtx) string {
	if !m.watchesEnabled(ctx, rc.HouseholdID) {
		return "disabled"
	}
	if strings.HasPrefix(rc.Kind, "watch.") {
		return "skip" // a watch's own signal never feeds a watch
	}
	ws, err := m.watches.ActiveSignalWatches(ctx, rc.HouseholdID, rc.Kind)
	if err != nil {
		m.deps.Log.Warn("cc: signal watches not loaded", "household", rc.HouseholdID, "kind", rc.Kind, "err", err)
		return "error"
	}
	if len(ws) == 0 {
		return "no_watch"
	}
	f := rc.facts()
	now := m.now()
	n := 0
	for _, w := range ws {
		if !signalMatches(w.Source, rc, f) {
			continue
		}
		n++
		value := pyjson.Dumps(pySorted(f), true)
		if w.Condition.Field != "" {
			value = factString(f, w.Condition.Field)
		}
		if w.Condition.Op == watches.OpAbsentFor {
			m.observeAbsent(ctx, w, value, now)
			continue
		}
		m.observeWatch(ctx, w, watchObservation{Value: value, At: now})
	}
	return "observed:" + strconv.Itoa(n)
}

// signalMatches applies the watch's member and facts filters.
func signalMatches(src watches.Source, rc reactionCtx, f *pyjson.Object) bool {
	if src.UserID != nil && (rc.UserID == nil || *rc.UserID != *src.UserID) {
		return false
	}
	for k, v := range src.Facts {
		if factString(f, k) != v {
			return false
		}
	}
	return true
}

// observeAbsent records an absent_for watch's value: the wanted value disarms the timer, any
// other value arms it unless it already runs (the deadline counts from the first miss).
func (m *Module) observeAbsent(ctx context.Context, w watches.Watch, value string, now time.Time) {
	if normValue(value) == normValue(w.Condition.Value) {
		m.disarmWatchAbsent(ctx, w.ID)
		m.recordWatchValue(ctx, w.ID, value)
		return
	}
	if m.watchAbsentArmed(ctx, w.ID) {
		return
	}
	d, err := time.ParseDuration(w.Condition.For)
	if err != nil {
		m.deps.Log.Warn("cc: absent_for watch has a bad duration", "id", w.ID, "for", w.Condition.For)
		return
	}
	m.armWatchAbsent(ctx, w, now.Add(d))
	m.recordWatchValue(ctx, w.ID, value)
}

func (m *Module) recordWatchValue(ctx context.Context, id, value string) {
	if err := m.watches.RecordCheck(ctx, id, value, ""); err != nil {
		m.deps.Log.Warn("cc: watch check not recorded", "id", id, "err", err)
	}
}

func (m *Module) watchAbsentArmed(ctx context.Context, id string) bool {
	if m.deps.Scheduler == nil {
		return false
	}
	st, err := m.deps.Scheduler.Status(ctx, watchAbsentTrigger(id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.deps.Log.Warn("cc: absent timer status", "id", id, "err", err)
	}
	return err == nil && !st.NextFireAt.IsZero()
}

func (m *Module) armWatchAbsent(ctx context.Context, w watches.Watch, at time.Time) {
	if m.deps.Scheduler == nil {
		return
	}
	payload, _ := json.Marshal(watchAbsentPayload{HouseholdID: w.HouseholdID, WatchID: w.ID})
	if err := m.deps.Scheduler.Put(ctx, scheduler.Trigger{
		Name: watchAbsentTrigger(w.ID), Kind: scheduler.KindOnce, JobType: watchAbsentJob,
		Spec: scheduler.Spec{At: at}, Payload: payload,
	}); err != nil {
		m.deps.Log.Error("cc: arm absent timer", "id", w.ID, "err", err)
	}
}

func (m *Module) disarmWatchAbsent(ctx context.Context, id string) {
	if m.deps.Scheduler == nil {
		return
	}
	if err := m.deps.Scheduler.Delete(ctx, watchAbsentTrigger(id)); err != nil {
		m.deps.Log.Error("cc: disarm absent timer", "id", id, "err", err)
	}
}

func (m *Module) runWatchAbsentJob(ctx context.Context, job queue.Job) ([]byte, error) {
	var p watchAbsentPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, queue.Permanent(err)
	}
	return nil, m.runWatchAbsent(ctx, p.HouseholdID, p.WatchID)
}

// runWatchAbsent fires an absent_for watch whose value still isn't the wanted one.
func (m *Module) runWatchAbsent(ctx context.Context, hh, id string) error {
	w, err := m.watches.Get(ctx, hh, id)
	if err != nil || w == nil || w.State != watches.StateActive || w.Condition.Op != watches.OpAbsentFor {
		return err
	}
	if normValue(w.LastValue) == normValue(w.Condition.Value) {
		return nil
	}
	if !m.watchesEnabled(ctx, hh) {
		m.observeWatch(ctx, *w, watchObservation{Err: "watches are turned off for this household", At: m.now()})
		return nil
	}
	c := w.Condition
	m.fireWatch(ctx, *w, "", watchObservation{Value: w.LastValue, At: m.now()},
		c.Field+" hasn't been "+c.Value+" for "+c.For)
	return nil
}
