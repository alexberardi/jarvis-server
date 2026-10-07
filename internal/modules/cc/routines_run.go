package cc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Run-now and scheduled execution (execute_routine_on_node, routine_scheduler.py). Changes
// from legacy:
//   - the `routine` command carries the full node-native definition under "routine" (D24),
//     and no `trusted` (D4); no user_id is ever sent (D41);
//   - the result arrives through the Bus's in-process slot, not a /tmp file, and the wait is
//     ~60 s (D40 08.Q7); a late result is dropped;
//   - an overlapping run of the same routine is skipped with a note (D40 08.Q10);
//   - no routine_executions row (D40 08.Q11);
//   - scheduling is a platform trigger per routine (D27): no polling loop, no
//     routines.scheduler_enabled gate (D25), and a missed run fires once, late (D26).

// routineCard is _ROUTINE_STATUS_CARD: status → (icon, headline) for the completion card.
var routineCard = map[string][2]string{
	"success": {"✅", "finished"},
	"partial": {"⚠️", "finished with issues"},
	"timeout": {"⏱️", "didn't finish"},
	"failed":  {"⚠️", "couldn't run"},
}

// routineResult is a run's outcome, in the run-now response shape.
type routineResult struct {
	Success bool
	Status  string // success | partial | failed | timeout
	Message any
	Passed  int64
	Failed  int64
	Error   string // set on a publish failure (and an overlap), as legacy's `error` key
}

func (r routineResult) response() map[string]any {
	out := map[string]any{"success": r.Success, "status": r.Status, "message": r.Message, "passed": r.Passed, "failed": r.Failed}
	if r.Error != "" {
		out["error"] = r.Error
	}
	return out
}

// pyIntOr0 is int(x or 0) on a decoded JSON number.
func pyIntOr0(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return int64(f)
		}
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

// runRoutine publishes the routine to the node and waits for its result. notify posts the one
// completion card a detached (scheduled) run owes the household, on every terminal state.
func (m *Module) runRoutine(ctx context.Context, rt *routineRow, nodeID string, notify bool) routineResult {
	res := m.runRoutineOnce(ctx, rt, nodeID)
	if notify {
		m.notifyRoutine(ctx, rt, res)
	}
	return res
}

func (m *Module) runRoutineOnce(ctx context.Context, rt *routineRow, nodeID string) routineResult {
	if !m.rt.acquire(rt.id) {
		note := fmt.Sprintf("'%s' is already running.", rt.name)
		return routineResult{Status: "failed", Message: note, Error: note}
	}
	defer m.rt.release(rt.id)
	if !m.bus.Available() {
		m.deps.Log.Error("cc: run routine: MQTT not available", "routine", rt.slug, "node", nodeID)
		return routineResult{Status: "failed", Error: ErrNoBroker.Error()}
	}
	// The slot opens before the publish so a fast reply can't be missed; reply_request_id is
	// the command's own request id.
	rid := uuid4()
	m.bus.Expect(rid, nodeID)
	defer m.bus.Drop(rid)
	m.bus.CommandWithID(nodeID, "routine", map[string]any{
		"routine_name":     rt.slug,
		"reply_request_id": rid,
		"tool_call_id":     uuid4(),
		"voice_command":    "routine: " + rt.slug,
		"routine":          rt.node(), // D24: the node runs this, not its possibly stale copy
	}, rid)
	m.deps.Log.Info("cc: routine published", "routine", rt.slug, "node", nodeID, "request_id", rid)

	wctx, cancel := context.WithTimeout(ctx, routineWait)
	defer cancel()
	raw, err := m.bus.Await(wctx, rid)
	if err != nil {
		m.deps.Log.Warn("cc: routine result not received", "routine", rt.slug, "node", nodeID, "err", err)
		return routineResult{Status: "timeout"}
	}
	var body struct {
		Output map[string]any `json:"output"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&body)
	out := body.Output
	res := routineResult{Passed: pyIntOr0(out["passed"]), Failed: pyIntOr0(out["failed"]), Message: out["message"]}
	res.Success, _ = laxBool(out["success"])
	switch {
	case !res.Success:
		res.Status = "failed"
	case res.Failed > 0:
		res.Status = "partial"
	default:
		res.Status = "success"
	}
	return res
}

// notifyRoutine is _notify_routine_complete: ONE card with push to the household. Non-fatal.
func (m *Module) notifyRoutine(ctx context.Context, rt *routineRow, res routineResult) {
	card, ok := routineCard[res.Status]
	if !ok {
		card = routineCard["failed"]
	}
	msg := ""
	switch v := res.Message.(type) {
	case string:
		msg = v
	case nil:
	default:
		b, _ := json.Marshal(v)
		msg = string(b)
	}
	var summary string
	switch {
	case (res.Status == "success" || res.Status == "partial") && msg != "":
		summary = msg
	case res.Status == "timeout":
		summary = "The node didn't respond in time."
	case msg != "":
		summary = msg
	case res.Error != "":
		summary = res.Error
	default:
		summary = "The routine did not complete."
	}
	m.postInboxItem(ctx, rt.householdID, nil, fmt.Sprintf("%s '%s' %s", card[0], rt.name, card[1]), summary, summary,
		"routine", map[string]any{"household_id": rt.householdID, "routine_id": rt.id, "status": res.Status}, true, "household")
}

// handleRunRoutineNow runs a routine on body.node_id, else the household's primary node, and
// returns the result synchronously. It never notifies (mobile shows the result inline).
func (m *Module) handleRunRoutineNow(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	nodeID, _ := b.optStrPtr("node_id")
	if !b.done(w) || !m.pathHouseholdAccess(w, r, auth) {
		return
	}
	ctx, hh := r.Context(), r.PathValue("household_id")
	rt, err := m.routineByID(ctx, hh, r.PathValue("routine_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	target := ""
	if nodeID != nil {
		target = *nodeID
	}
	if target == "" {
		target = m.settings.String(ctx, settingPrimaryNode, settings.Scope{HouseholdID: hh})
	}
	if target == "" {
		detail(w, http.StatusBadRequest, "No node specified and no household primary node configured")
		return
	}
	// A routine only ever runs on a node of its own household (D24): the definition goes with it.
	n, err := m.nodeByID(ctx, target)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && n.householdID.String != hh) {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m.runRoutine(ctx, rt, target, false).response())
}

// --- the scheduler side (D27) ---

func routineTrigger(id string) string { return routineTriggerPrefix + id }

// routineTiming is the trigger-relevant state of a routine: its schedule timing, and whether
// both the routine and its schedule are enabled. "" means no trigger.
func (m *Module) routineTiming(rt *routineRow) string {
	s := rt.sched()
	if !rt.enabled || s == nil || !s.Enabled {
		return ""
	}
	return s.timing()
}

func routineTriggerFor(rt *routineRow, s *routineSchedule) (scheduler.Trigger, bool) {
	payload, _ := json.Marshal(map[string]string{"routine_id": rt.id})
	t := scheduler.Trigger{Name: routineTrigger(rt.id), JobType: routineFireJob, Payload: payload}
	switch s.Type {
	case "cron":
		if s.Cron == nil {
			return t, false
		}
		t.Kind, t.Spec = scheduler.KindCron, scheduler.Spec{Cron: *s.Cron, TZ: s.location().String()}
	case "interval":
		if s.IntervalSeconds == nil || *s.IntervalSeconds <= 0 {
			return t, false
		}
		// The first fire is one interval after the routine is scheduled (legacy's baseline).
		t.Kind, t.Spec = scheduler.KindInterval, scheduler.Spec{Every: time.Duration(*s.IntervalSeconds) * time.Second}
	default:
		return t, false
	}
	return t, true
}

// syncRoutineTrigger puts, keeps or deletes a routine's trigger after a save. The trigger (and
// so its next fire) is kept when the timing didn't change and it still exists; force puts it
// regardless. A trigger write failure is logged: Start reconciles on the next boot.
func (m *Module) syncRoutineTrigger(ctx context.Context, rt *routineRow, prevTiming string, force bool) {
	sch := m.deps.Scheduler
	if sch == nil {
		return
	}
	timing := m.routineTiming(rt)
	if timing == "" {
		m.deleteRoutineTrigger(ctx, rt.id)
		return
	}
	if !force && timing == prevTiming {
		if st, err := sch.Status(ctx, routineTrigger(rt.id)); err == nil && !st.NextFireAt.IsZero() {
			return
		}
	}
	t, ok := routineTriggerFor(rt, rt.sched())
	if !ok {
		m.deleteRoutineTrigger(ctx, rt.id)
		return
	}
	if err := sch.Put(ctx, t); err != nil {
		m.deps.Log.Error("cc: put routine trigger", "routine", rt.id, "err", err)
	}
}

func (m *Module) deleteRoutineTrigger(ctx context.Context, id string) {
	if m.deps.Scheduler == nil {
		return
	}
	if err := m.deps.Scheduler.Delete(ctx, routineTrigger(id)); err != nil {
		m.deps.Log.Error("cc: delete routine trigger", "routine", id, "err", err)
	}
}

// reconcileRoutineTriggers creates the trigger of every scheduled routine that lacks one
// (imported rows, a failed trigger write). An existing trigger keeps its next fire.
func (m *Module) reconcileRoutineTriggers(ctx context.Context) error {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+routineCols+` FROM cc_routines WHERE schedule IS NOT NULL`)
	if err != nil {
		return err
	}
	var list []*routineRow
	for rows.Next() {
		rt, err := scanRoutine(rows)
		if err != nil {
			rows.Close()
			return err
		}
		list = append(list, rt)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, rt := range list {
		timing := m.routineTiming(rt)
		if timing == "" {
			m.deleteRoutineTrigger(ctx, rt.id)
			continue
		}
		_, err := m.deps.Scheduler.Status(ctx, routineTrigger(rt.id))
		if errors.Is(err, sql.ErrNoRows) {
			m.syncRoutineTrigger(ctx, rt, "", true)
		} else if err != nil {
			return err
		}
	}
	return nil
}

// runRoutineFire is the routine trigger's job (run_due_routines for one routine). A routine
// that is gone or no longer scheduled drops its trigger. A target node that is missing,
// inactive, of another household or offline gets a "couldn't run" card, and the occurrence
// still counts as fired (respect cadence, don't pile up).
func (m *Module) runRoutineFire(ctx context.Context, job queue.Job) ([]byte, error) {
	f, err := scheduler.DecodeFire(job.Payload)
	if err != nil {
		return nil, queue.Permanent(err)
	}
	var p struct {
		RoutineID string `json:"routine_id"`
	}
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return nil, queue.Permanent(err)
	}
	rt, err := scanRoutine(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+routineCols+` FROM cc_routines WHERE id = ?`, p.RoutineID))
	if errors.Is(err, sql.ErrNoRows) {
		m.deleteRoutineTrigger(ctx, p.RoutineID)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := rt.sched()
	if m.routineTiming(rt) == "" {
		m.deleteRoutineTrigger(ctx, rt.id)
		return nil, nil
	}
	now := m.now()
	// The mobile projection (D27): written in place, so a concurrent PATCH isn't clobbered.
	if _, err := m.deps.DB.Write.ExecContext(ctx,
		`UPDATE cc_routines SET schedule = json_set(schedule, '$.last_fired_at', ?) WHERE id = ? AND schedule IS NOT NULL`,
		pyAware(now), rt.id); err != nil {
		m.deps.Log.Warn("cc: record routine fire", "routine", rt.id, "err", err)
	}
	target := s.target()
	if !m.routineTargetUp(ctx, rt.householdID, target, now) {
		m.deps.Log.Info("cc: scheduled routine skipped: target node unavailable", "routine", rt.slug, "node", target)
		m.notifyRoutine(ctx, rt, routineResult{Status: "failed", Error: "the target node was offline"})
		return nil, nil
	}
	m.deps.Log.Info("cc: firing scheduled routine", "routine", rt.slug, "node", target, "due", f.ScheduledAt)
	res := m.runRoutine(ctx, rt, target, true)
	return json.Marshal(res.response())
}

// routineTargetUp: the node exists in the routine's household, is active and is online.
func (m *Module) routineTargetUp(ctx context.Context, hh, nodeID string, now time.Time) bool {
	if nodeID == "" {
		return false
	}
	n, err := m.nodeByID(ctx, nodeID)
	return err == nil && n.householdID.String == hh && n.isActive && n.online(now)
}

// pyAware renders an aware UTC time like Python's isoformat(): "...+00:00", microseconds
// omitted when zero (schedule.last_fired_at, which mobile parses).
func pyAware(t time.Time) string { return pyNaive(t) + "+00:00" }
