package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// Errand schedules (docs/cc/08 §3.5, app/services/schedule_service.py): a durable time
// trigger for the errand plan→card loop. A schedule never acts on its own: when it fires,
// errands (doc 09) re-plan its intent and post a plan card the user must approve.
//
// Each active schedule is a one-shot platform trigger at its next_fire_at (D27). Its job
// claims the row with legacy's conditional UPDATE (the serialization point, at most once per
// occurrence), then re-arms a recurring schedule at its next occurrence strictly after now,
// so missed occurrences collapse into one late fire (D26). cc_schedules.next_fire_at stays
// the source of truth the list and the mobile screen read. Terminal rows are purged after 30
// days, and 'paused' and 'title' are gone (D40 08.Q12).
//
// Producers and readers live in doc 09 (schedule_errand, list_scheduled_errands, the Cancel
// card callback); this file is the store, the trigger plumbing, the mobile routes and the
// display vocabulary they share.

const (
	scheduleFireJob       = "cc.schedule_fire"
	schedulePurgeJob      = "cc.schedule_purge"
	scheduleTriggerPrefix = "cc.schedule:"
	scheduleTerminalTTL   = 30 * 24 * time.Hour

	// The Cancel buttons on the "scheduled errands" card (server-callback pair).
	ScheduleCallbackCommand = "schedule"
	ScheduleCancelCallback  = "cancel_schedule"
	scheduleListCategory    = "schedule"
	scheduleListCardMax     = 8
)

// ScheduledErrand is a fired schedule handed to the errand hook (draft_errand_plan_detached).
type ScheduledErrand struct {
	ScheduleID  string
	HouseholdID string
	NodeID      string // "" when unknown
	UserID      *int64
	Intent      string
	Timezone    string
}

// NewSchedule is create_schedule's input. FireAt is the first fire; Recurrence is the JSON
// spec ({"type":"interval","interval_seconds":N} | {"type":"cron","cron":"m h dom mon dow"})
// or "" for a one-shot.
type NewSchedule struct {
	HouseholdID string
	NodeID      string
	UserID      *int64
	Intent      string
	FireAt      time.Time
	Timezone    string
	Recurrence  string
}

func scheduleTrigger(id string) string { return scheduleTriggerPrefix + id }

// CreateSchedule creates an active schedule and arms its trigger. Returns its id (sch_<hex32>).
func (m *Module) CreateSchedule(ctx context.Context, in NewSchedule) (string, error) {
	tz := in.Timezone
	if tz == "" {
		tz = "UTC"
	}
	var rec, node any
	if in.Recurrence != "" {
		rec = in.Recurrence
	}
	if in.NodeID != "" {
		node = in.NodeID
	}
	var user any
	if in.UserID != nil {
		user = *in.UserID
	}
	id, now := "sch_"+randHex(16), dbTime(m.now())
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_schedules (id, household_id, user_id, node_id, intent,
		timezone, next_fire_at, recurrence, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		id, in.HouseholdID, user, node, in.Intent, tz, dbTime(in.FireAt), rec, now, now); err != nil {
		return "", err
	}
	m.armSchedule(ctx, id, in.FireAt)
	m.deps.Log.Info("cc: schedule created", "id", id, "fire_at", in.FireAt, "recurring", in.Recurrence != "")
	return id, nil
}

// armSchedule (re)puts a schedule's one-shot trigger. A failure is logged: Start reconciles.
func (m *Module) armSchedule(ctx context.Context, id string, at time.Time) {
	if m.deps.Scheduler == nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{"schedule_id": id})
	if err := m.deps.Scheduler.Put(ctx, scheduler.Trigger{
		Name: scheduleTrigger(id), Kind: scheduler.KindOnce, JobType: scheduleFireJob,
		Spec: scheduler.Spec{At: at}, Payload: payload,
	}); err != nil {
		m.deps.Log.Error("cc: arm schedule", "id", id, "err", err)
	}
}

func (m *Module) disarmSchedule(ctx context.Context, id string) {
	if m.deps.Scheduler == nil {
		return
	}
	if err := m.deps.Scheduler.Delete(ctx, scheduleTrigger(id)); err != nil {
		m.deps.Log.Error("cc: disarm schedule", "id", id, "err", err)
	}
}

// zoneOrUTC is legacy's _tz: an unknown zone is UTC, never a crash.
func zoneOrUTC(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}

// recurrenceSpec is a schedule's recurrence JSON.
type recurrenceSpec struct {
	Type            string `json:"type"`
	IntervalSeconds any    `json:"interval_seconds"`
	Cron            string `json:"cron"`
}

func parseRecurrence(rec string) (recurrenceSpec, bool) {
	var s recurrenceSpec
	if rec == "" {
		return s, false
	}
	dec := json.NewDecoder(strings.NewReader(rec))
	dec.UseNumber()
	if err := dec.Decode(&s); err != nil {
		return s, false
	}
	return s, true
}

func (s recurrenceSpec) seconds() int64 { return pyIntOr0(s.IntervalSeconds) }

// nextScheduleFire is compute_next_fire: the next occurrence strictly after `after`, or
// false for a one-shot or an unparseable spec (which then degrades to a one-shot, §8.14).
func nextScheduleFire(rec string, after time.Time, tz string) (time.Time, bool) {
	s, ok := parseRecurrence(rec)
	if !ok {
		return time.Time{}, false
	}
	switch s.Type {
	case "interval":
		secs := s.seconds()
		if secs <= 0 {
			return time.Time{}, false
		}
		return after.Add(time.Duration(secs) * time.Second), true
	case "cron":
		if s.Cron == "" {
			return time.Time{}, false
		}
		c, err := scheduler.ParseCron(s.Cron)
		if err != nil {
			return time.Time{}, false
		}
		n, ok := c.Next(after.In(zoneOrUTC(tz)))
		return n.UTC(), ok
	}
	return time.Time{}, false
}

type scheduleRow struct {
	id, householdID, intent, timezone, nextFireAt, state string
	userID                                               sql.NullInt64
	nodeID, recurrence, lastFiredAt                      sql.NullString
}

const scheduleCols = `id, household_id, user_id, node_id, intent, timezone, next_fire_at, recurrence, state, last_fired_at`

func scanSchedule(s scanner) (*scheduleRow, error) {
	var r scheduleRow
	if err := s.Scan(&r.id, &r.householdID, &r.userID, &r.nodeID, &r.intent, &r.timezone, &r.nextFireAt,
		&r.recurrence, &r.state, &r.lastFiredAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// runScheduleFire is the schedule trigger's job (one row of fire_due_schedules). The claim is
// the serialization point: recurring → move next_fire_at forward and stay active; one-shot (or
// a spec that stopped parsing) → done. A lost claim is a no-op.
func (m *Module) runScheduleFire(ctx context.Context, job queue.Job) ([]byte, error) {
	f, err := scheduler.DecodeFire(job.Payload)
	if err != nil {
		return nil, queue.Permanent(err)
	}
	var p struct {
		ScheduleID string `json:"schedule_id"`
	}
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return nil, queue.Permanent(err)
	}
	row, err := scanSchedule(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM cc_schedules WHERE id = ?`, p.ScheduleID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // purged, cancelled-and-purged, or the user was deleted (D20)
	}
	if err != nil {
		return nil, err
	}
	if row.state != "active" {
		return nil, nil
	}
	claimed, err := m.claimSchedule(ctx, row, m.now())
	if err != nil || !claimed {
		return nil, err
	}
	s := ScheduledErrand{ScheduleID: row.id, HouseholdID: row.householdID, NodeID: row.nodeID.String,
		Intent: row.intent, Timezone: row.timezone}
	if row.userID.Valid {
		uid := row.userID.Int64
		s.UserID = &uid
	}
	if m.rt.scheduleFire == nil {
		m.deps.Log.Warn("cc: schedule fired but errands are not available; dropped", "id", row.id)
		return nil, nil
	}
	// The hook posts its own failure card; an error here is only logged (at most once).
	if err := m.rt.scheduleFire(ctx, s); err != nil {
		m.deps.Log.Error("cc: scheduled errand failed to draft", "id", row.id, "err", err)
	}
	return nil, nil
}

// claimSchedule is _claim_and_rearm / _claim_one_shot.
func (m *Module) claimSchedule(ctx context.Context, row *scheduleRow, now time.Time) (bool, error) {
	ts := dbTime(now)
	next, recurring := nextScheduleFire(row.recurrence.String, now, row.timezone)
	var res sql.Result
	var err error
	if recurring {
		res, err = m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_schedules SET next_fire_at = ?, last_fired_at = ?, updated_at = ?
			WHERE id = ? AND state = 'active' AND next_fire_at <= ?`, dbTime(next), ts, ts, row.id, ts)
	} else {
		res, err = m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_schedules SET state = 'done', last_fired_at = ?, updated_at = ?
			WHERE id = ? AND state = 'active'`, ts, ts, row.id)
	}
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if recurring {
		m.armSchedule(ctx, row.id, next)
	}
	return true, nil
}

// reconcileScheduleTriggers re-arms every active schedule whose trigger is missing or out of
// step with its row (an import, or a crash between the claim and the re-arm). A next_fire_at
// in the past fires once, late (D26).
func (m *Module) reconcileScheduleTriggers(ctx context.Context) error {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT id, next_fire_at FROM cc_schedules WHERE state = 'active'`)
	if err != nil {
		return err
	}
	type due struct {
		id string
		at time.Time
	}
	var list []due
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return err
		}
		list = append(list, due{id, parseTS(at)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		st, err := m.deps.Scheduler.Status(ctx, scheduleTrigger(d.id))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && st.NextFireAt.UnixMilli() == d.at.UnixMilli() {
			continue
		}
		m.armSchedule(ctx, d.id, d.at)
	}
	return nil
}

// runSchedulePurge deletes terminal schedules 30 days after they ended (D40 08.Q12).
func (m *Module) runSchedulePurge(ctx context.Context, _ queue.Job) ([]byte, error) {
	res, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_schedules WHERE state IN ('done', 'cancelled') AND updated_at < ?`,
		dbTime(m.now().Add(-scheduleTerminalTTL)))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		m.deps.Log.Info("cc: purged terminal schedules", "count", n)
	}
	return nil, nil
}

// --- list / cancel ---

// listSchedules is list_schedules: the household's live schedules, soonest first, as the
// legacy plain dicts.
func (m *Module) listSchedules(ctx context.Context, hh string) ([]map[string]any, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT `+scheduleCols+` FROM cc_schedules WHERE household_id = ? AND state = 'active' ORDER BY next_fire_at, rowid`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		r, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		var rec any
		if r.recurrence.Valid {
			rec = r.recurrence.String
		}
		out = append(out, map[string]any{
			"id": r.id, "intent": r.intent, "state": r.state, "next_fire_at": naiveTS(r.nextFireAt),
			"recurrence": rec, "timezone": r.timezone, "last_fired_at": naiveTS(r.lastFiredAt.String),
		})
	}
	return out, rows.Err()
}

// cancelSchedule is cancel_schedule: household-scoped in the UPDATE itself, idempotent. True
// iff a live schedule was cancelled.
func (m *Module) cancelSchedule(ctx context.Context, id, hh string) (bool, error) {
	now := dbTime(m.now())
	res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_schedules SET state = 'cancelled', updated_at = ?
		WHERE id = ? AND household_id = ? AND state = 'active'`, now, id, hh)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		m.disarmSchedule(ctx, id)
	}
	return n > 0, nil
}

func (m *Module) scheduleViews(ctx context.Context, hh string) ([]any, error) {
	list, err := m.listSchedules(ctx, hh)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, scheduleView(s))
	}
	return out, nil
}

// handleListSchedules backs the mobile Schedules screen: member of the household.
func (m *Module) handleListSchedules(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx, hh := r.Context(), r.PathValue("household_id")
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	views, err := m.scheduleViews(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"household_id": hh, "schedules": views})
}

// handleCancelSchedule cancels one schedule and returns the fresh list (cancelled=false when
// it was already terminal or belongs to another household).
func (m *Module) handleCancelSchedule(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx, hh, id := r.Context(), r.PathValue("household_id"), r.PathValue("schedule_id")
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	cancelled, err := m.cancelSchedule(ctx, id, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: mobile cancel schedule", "id", id, "household", hh, "user", u.ID, "cancelled", cancelled)
	views, err := m.scheduleViews(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"cancelled": cancelled, "schedules": views})
}

// --- the management card (list_scheduled_errands and the Cancel tap, doc 09) ---

// postScheduleListCard is post_schedule_list_card: the household's live schedules with one
// server-plane Cancel button each (at most 8). Returns the live count (0 posts nothing) and
// the inbox item id.
func (m *Module) postScheduleListCard(ctx context.Context, hh string, userID *int64) (int, string, error) {
	list, err := m.listSchedules(ctx, hh)
	if err != nil || len(list) == 0 {
		return 0, "", err
	}
	n := len(list)
	title := fmt.Sprintf("🗓️ %d scheduled errand%s", n, plural(n))
	summary := fmt.Sprintf("%d errands scheduled", n)
	if n == 1 {
		summary = describeSchedule(list[0])
	}
	target := "household"
	if userID != nil {
		target = "user"
	}
	id := m.postInboxItem(ctx, hh, userID, title, summary, scheduleListBody(list), scheduleListCategory,
		scheduleCardMetadata(list, hh), true, target)
	return n, id, nil
}

// scheduleListBody is _schedule_list_body: the card's markdown.
func scheduleListBody(list []map[string]any) string {
	if len(list) == 0 {
		return "You don't have any errands scheduled right now."
	}
	lines := []string{"Your scheduled errands:", ""}
	for i, s := range list {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, describeSchedule(s)))
	}
	if len(list) > scheduleListCardMax {
		lines = append(lines, "", fmt.Sprintf("_Showing the first %d of %d._", scheduleListCardMax, len(list)))
	}
	lines = append(lines, "", "Tap **Cancel** on any errand to stop it.")
	return strings.Join(lines, "\n")
}

// scheduleCardMetadata is build_schedule_list_card_metadata.
func scheduleCardMetadata(list []map[string]any, hh string) map[string]any {
	shown := list
	if len(shown) > scheduleListCardMax {
		shown = shown[:scheduleListCardMax]
	}
	elems := make([]any, 0, len(shown))
	for _, s := range shown {
		intent, _ := s["intent"].(string)
		if intent == "" {
			intent = "errand"
		}
		if r := []rune(intent); len(r) > 32 {
			intent = string(r[:32])
		}
		elems = append(elems, map[string]any{
			"id": "cancel-schedule-" + s["id"].(string), "label": "Cancel: " + intent,
			"command": ScheduleCallbackCommand, "callback": ScheduleCancelCallback, "target": "server",
			"data": map[string]any{"schedule_id": s["id"]},
		})
	}
	sched := make([]any, len(shown))
	for i, s := range shown {
		sched[i] = s
	}
	return map[string]any{"household_id": hh, "schedules": sched, "interactive_elements": elems}
}

// CancelScheduleTap is _handle_cancel_schedule, the ("schedule", "cancel_schedule") server
// callback: cancel (household-scoped), re-post the list card, and return the tap's own
// context_data. ok=false carries the error the callback reports.
func (m *Module) CancelScheduleTap(ctx context.Context, hh string, userID *int64, data map[string]any) (map[string]any, string, error) {
	id, _ := data["schedule_id"].(string)
	if id == "" {
		return nil, "No schedule to cancel.", nil
	}
	cancelled, err := m.cancelSchedule(ctx, id, hh)
	if err != nil {
		return nil, "", err
	}
	remaining, _, err := m.postScheduleListCard(ctx, hh, userID)
	if err != nil {
		return nil, "", err
	}
	title := "Errand already stopped"
	if cancelled {
		title = "🗓️ Errand cancelled"
	}
	summary := "Done — you have no errands scheduled now."
	if remaining > 0 {
		summary = fmt.Sprintf("Done — you have %d errand%s still scheduled.", remaining, plural(remaining))
	}
	return map[string]any{"inbox": map[string]any{"title": title, "summary": summary,
		"metadata": map[string]any{"household_id": hh}}}, "", nil
}

// --- the display vocabulary (a mobile/voice contract: rendered verbatim, §7.10) ---

var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// cronTimePhrase is " at 9:00 AM" when both fields are concrete, else "".
func cronTimePhrase(minute, hour string) string {
	if !isDigits(minute) || !isDigits(hour) {
		return ""
	}
	h, _ := strconv.Atoi(hour)
	mi, _ := strconv.Atoi(minute)
	suffix := "AM"
	if h >= 12 {
		suffix = "PM"
	}
	h12 := h % 12
	if h12 == 0 {
		h12 = 12
	}
	return fmt.Sprintf(" at %d:%02d %s", h12, mi, suffix)
}

// describeRecurrence is _describe_recurrence ("once" for a one-shot). A multi-day dow reads
// as "every day" (§8.15), kept.
func describeRecurrence(rec any) string {
	s, _ := rec.(string)
	if s == "" {
		return "once"
	}
	spec, ok := parseRecurrence(s)
	if !ok {
		return "repeating"
	}
	switch spec.Type {
	case "interval":
		secs := spec.seconds()
		if secs != 0 && secs%3600 == 0 {
			if h := secs / 3600; h != 1 {
				return fmt.Sprintf("every %d hours", h)
			}
			return "every hour"
		}
		if secs != 0 && secs%60 == 0 {
			if mi := secs / 60; mi != 1 {
				return fmt.Sprintf("every %d minutes", mi)
			}
			return "every minute"
		}
		return "on a repeating interval"
	case "cron":
		parts := strings.Fields(spec.Cron)
		if len(parts) != 5 {
			return "repeating"
		}
		minute, hour, dom, dow := parts[0], parts[1], parts[2], parts[4]
		tod := cronTimePhrase(minute, hour)
		switch {
		case dow == "1-5":
			return "every weekday" + tod
		case isDigits(dow):
			d, _ := strconv.Atoi(dow)
			return "every " + weekdayNames[d%7] + tod
		case isDigits(dom):
			return "monthly on day " + dom + tod
		}
		return "every day" + tod
	}
	return "repeating"
}

// localWhen is _local_when: "Tue Aug 5, 9 AM". Every ":00" is stripped, as legacy's
// .replace(":00", "") does ("10:00" → "10").
func localWhen(iso any, tz any) string {
	s, _ := iso.(string)
	if s == "" {
		return ""
	}
	t := parseTS(s)
	if t.IsZero() {
		return ""
	}
	zone, _ := tz.(string)
	return strings.ReplaceAll(t.In(zoneOrUTC(zone)).Format("Mon Jan 2, 3:04 PM"), ":00", "")
}

// describeSchedule is describe_schedule: "check the weather · every day at 9:00 AM · next Tue
// Aug 5, 9 AM".
func describeSchedule(s map[string]any) string {
	intent, _ := s["intent"].(string)
	intent = strings.TrimSpace(intent)
	if intent == "" {
		intent = "an errand"
	}
	bits := []string{intent, describeRecurrence(s["recurrence"])}
	if next := localWhen(s["next_fire_at"], s["timezone"]); next != "" {
		bits = append(bits, "next "+next)
	}
	return strings.Join(bits, " · ")
}

// scheduleView is schedule_view: the stored fields plus the display strings mobile renders.
func scheduleView(s map[string]any) map[string]any {
	v := make(map[string]any, len(s)+4)
	for k, x := range s {
		v[k] = x
	}
	rec, _ := s["recurrence"].(string)
	v["is_recurring"] = rec != ""
	v["cadence"] = describeRecurrence(s["recurrence"])
	v["next_local"] = localWhen(s["next_fire_at"], s["timezone"])
	v["description"] = describeSchedule(s)
	return v
}
