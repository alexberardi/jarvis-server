package builtins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/reminders"
)

// Reminder is reminder (commands/reminder_command.py) and its services.reminder_service: the
// speakers' reminders, one record per reminder in the node's store under "set_reminder", as the
// Python node kept them. Due runs N:agents/reminder_agent.py's check for the Pi's poller.
//
// Deliberate differences from Python (go-pi-node.md): a date's time is placed in the node's
// zone on that date, and a recurring reminder advances on the local clock, so "Monday at 9" and
// "every day at 8" stay at 9 and 8 across a DST change (Python used today's UTC offset, and
// advanced in UTC); the records are read from the store each time rather than cached, so an edit
// or delete from the app is what fires.
type Reminder struct {
	Store *nodesettings.Store
	CC    CC // the phone pushes (nil: none)
	Log   *slog.Logger
	Now   func() time.Time // nil: time.Now
	Loc   *time.Location   // the node's zone; nil: time.Local

	mu sync.Mutex // one change at a time: the poller, the commands and the app read, then write
}

const (
	reminderName    = "reminder"
	reminderStorage = "set_reminder" // historical: the records live under the old command's name
	snoozeWindow    = 5 * time.Minute
	reminderPushKey = "REMINDER_PUSH_NOTIFICATIONS"
)

var recurrences = []string{"daily", "weekdays", "weekly", "biweekly", "monthly"}

func (r *Reminder) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def(reminderName)}
}

func (r *Reminder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reminder) loc() *time.Location {
	if r.Loc != nil {
		return r.Loc
	}
	return time.Local
}

func (r *Reminder) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// reminderData is ReminderData, its fields in asdict order (the stored record's).
type reminderData struct {
	ReminderID      string  `json:"reminder_id"`
	Text            string  `json:"text"`
	DueAt           string  `json:"due_at"`
	CreatedAt       string  `json:"created_at"`
	Recurrence      *string `json:"recurrence"`
	Announced       bool    `json:"announced"`
	SnoozeUntil     *string `json:"snooze_until"`
	AnnounceCount   int     `json:"announce_count"`
	LastAnnouncedAt *string `json:"last_announced_at"`
	UserID          *int64  `json:"user_id"`
}

// reminderOf is ReminderData.from_dict; false for a record it would skip.
func reminderOf(o nodesettings.Object) (reminderData, bool) {
	for _, k := range []string{"reminder_id", "text", "due_at", "created_at"} {
		if _, ok := o.Get(k); !ok {
			return reminderData{}, false
		}
	}
	b, _ := json.Marshal(o)
	var d reminderData
	return d, json.Unmarshal(b, &d) == nil
}

func (d reminderData) object() nodesettings.Object {
	b, _ := json.Marshal(d)
	var o nodesettings.Object
	_ = json.Unmarshal(b, &o)
	return o
}

func (d reminderData) visibleTo(user *int64) bool {
	return user == nil || d.UserID == nil || *d.UserID == *user
}

// all is the reminders in the order they were made (the Python service's dict).
func (r *Reminder) all() []reminderData {
	var out []reminderData
	for _, rec := range r.Store.Records(reminderStorage, r.now()) {
		if d, ok := reminderOf(rec.Data); ok {
			out = append(out, d)
		}
	}
	return out
}

func (r *Reminder) save(d reminderData) {
	if err := r.Store.SaveData(reminderStorage, d.ReminderID, d.object(), nil); err != nil {
		r.log().Warn("reminder: not saved", "id", d.ReminderID, "err", err)
	}
}

// newHex is n random bytes in hex (rem_ + uuid4().hex[:8] is 4).
func newHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *Reminder) create(text string, due time.Time, recurrence *string, user *int64) reminderData {
	d := reminderData{ReminderID: "rem_" + newHex(4), Text: text, DueAt: isoformat(due), CreatedAt: pyISO(r.now()),
		Recurrence: recurrence, UserID: user}
	r.save(d)
	r.log().Info("reminder: created", "id", d.ReminderID, "due_at", d.DueAt)
	return d
}

// pending is get_all_reminders: the user's (and unowned) reminders not yet announced, by due_at.
func (r *Reminder) pending(user *int64) []reminderData {
	var out []reminderData
	for _, d := range r.all() {
		if d.visibleTo(user) && !d.Announced {
			out = append(out, d)
		}
	}
	slices.SortStableFunc(out, func(a, b reminderData) int { return strings.Compare(a.DueAt, b.DueAt) })
	return out
}

// findByText is find_by_text: an exact match (any case), else the first containing it.
func (r *Reminder) findByText(text string, user *int64) (reminderData, bool) {
	want := strings.ToLower(text)
	all := r.all()
	for _, exact := range []bool{true, false} {
		for _, d := range all {
			got := strings.ToLower(d.Text)
			if d.visibleTo(user) && (got == want || !exact && strings.Contains(got, want)) {
				return d, true
			}
		}
	}
	return reminderData{}, false
}

// lastAnnounced is find_most_recently_announced: the latest fired within the snooze window.
func (r *Reminder) lastAnnounced(user *int64) (reminderData, bool) {
	cutoff := r.now().Add(-snoozeWindow)
	var best reminderData
	var bestAt time.Time
	found := false
	for _, d := range r.all() {
		if d.LastAnnouncedAt == nil || !d.visibleTo(user) {
			continue
		}
		at, aware, ok := pyFromISO(*d.LastAnnouncedAt)
		if !ok || !aware || at.Before(cutoff) {
			continue
		}
		// Ties go to the later one: a batch fired by one Due shares a stamp, and Python's last
		// mark_announced read the clock last.
		if !found || !at.Before(bestAt) {
			best, bestAt, found = d, at, true
		}
	}
	return best, found
}

// --- the command ---

func (r *Reminder) Call(_ context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if name != reminderName {
		return nil, false
	}
	args = reminderPostProcess(args)
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch args["action"] {
	case "set":
		return r.set(args, req.UserID), true
	case "list":
		return r.list(args, req.UserID), true
	case "delete":
		return r.remove(args, req.UserID), true
	}
	return r.snooze(args, req.UserID), true
}

// reminderPostProcess is post_process_tool_call: no action is set with a text, else list.
func reminderPostProcess(in map[string]any) map[string]any {
	args := make(map[string]any, len(in))
	for k, v := range in {
		args[k] = v
	}
	if !pyTruthy(args["action"]) {
		args["action"] = "list"
		if pyTruthy(args["text"]) {
			args["action"] = "set"
		}
	}
	return args
}

func (r *Reminder) set(args map[string]any, user *int64) map[string]any {
	text, _ := args["text"].(string)
	if text == "" {
		return finish(map[string]any{"error": "What should I remind you about?", "message": "What should I remind you about?"}, false, true)
	}
	if user == nil {
		return failure("I couldn't tell who's asking, so I can't save a personal reminder. Try again after Jarvis has recognized you.",
			map[string]any{"message": "I'm not sure who's speaking — try training your voice so I can save reminders for you."})
	}
	var keys []string
	if l, ok := args["resolved_datetimes"].([]any); ok {
		for _, k := range l {
			keys = append(keys, pyStr(k))
		}
	}
	clock, _ := args["time"].(string)
	rel, _ := pyInt(args["relative_minutes"])
	var recurrence *string
	if s, ok := args["recurrence"].(string); ok {
		recurrence = &s
	}
	if !hasExplicitTime(keys, clock, rel) {
		q := "What time should I remind you to " + text + "?"
		if recurrence != nil {
			q = "What time should I remind you to " + text + " " + *recurrence + "?"
		}
		return finish(map[string]any{"error": q, "pending_text": text, "pending_recurrence": args["recurrence"],
			"pending_date_keys": args["resolved_datetimes"], "message": q}, false, true)
	}
	due, ok, err := r.resolveDue(keys, clock, rel)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()} // Python's datetime() raised
	}
	if !ok {
		return failure("When should I remind you? Please specify a date, time, or duration.", map[string]any{})
	}
	d := r.create(text, due, recurrence, user)
	human := r.human(d.DueAt)
	msg := "Reminder set: " + text + " — " + human
	if recurrence != nil {
		msg += " (" + *recurrence + ")"
	}
	return finish(map[string]any{"reminder_id": d.ReminderID, "text": text, "due_at": d.DueAt, "due_at_human": human,
		"recurrence": args["recurrence"], "message": msg}, true, false)
}

func (r *Reminder) list(args map[string]any, user *int64) map[string]any {
	all := r.pending(user)
	var shown []reminderData
	today := r.now().In(r.loc())
	for _, d := range all {
		switch args["filter"] {
		case "today":
			if due, _, ok := pyFromISO(d.DueAt); !ok || civilDay(due.In(r.loc())) != civilDay(today) {
				continue
			}
		case "recurring":
			if d.Recurrence == nil {
				continue
			}
		}
		shown = append(shown, d)
	}
	if len(shown) == 0 {
		return finish(map[string]any{"reminders": []any{}, "count": 0, "message": "You don't have any reminders."}, true, false)
	}
	list := make([]any, len(shown))
	for i, d := range shown {
		list[i] = map[string]any{"reminder_id": d.ReminderID, "text": d.Text, "due_at": d.DueAt,
			"due_at_human": r.human(d.DueAt), "recurrence": d.Recurrence}
	}
	return finish(map[string]any{"reminders": list, "count": len(list),
		"message": "You have " + strconv.Itoa(len(list)) + " reminder(s)."}, true, false)
}

func (r *Reminder) remove(args map[string]any, user *int64) map[string]any {
	if args["scope"] == "all" {
		n := 0
		for _, d := range r.all() {
			if d.visibleTo(user) {
				if ok, _ := r.Store.DeleteData(reminderStorage, d.ReminderID); ok {
					n++
				}
			}
		}
		return finish(map[string]any{"deleted_count": n, "message": "Deleted all " + strconv.Itoa(n) + " reminder(s)."}, true, false)
	}
	text, _ := args["text"].(string)
	if text == "" {
		return failure("Which reminder should I cancel? Describe it or say 'delete all'.", map[string]any{})
	}
	d, ok := r.findByText(text, user)
	if !ok {
		return failure("No reminder found matching '"+text+"'.", map[string]any{})
	}
	_, _ = r.Store.DeleteData(reminderStorage, d.ReminderID)
	return finish(map[string]any{"deleted_count": 1, "deleted_text": d.Text, "message": "Cancelled your reminder: " + d.Text}, true, false)
}

func (r *Reminder) snooze(args map[string]any, user *int64) map[string]any {
	minutes, _ := pyInt(args["minutes"])
	if minutes == 0 {
		minutes = 10
	}
	var d reminderData
	var ok bool
	if text, _ := args["text"].(string); text != "" {
		d, ok = r.findByText(text, user)
	} else {
		d, ok = r.lastAnnounced(user)
	}
	if !ok {
		return failure("No recently announced reminder to snooze.", map[string]any{})
	}
	until := pyISO(r.now().Add(time.Duration(minutes) * time.Minute))
	d.SnoozeUntil, d.Announced = &until, false
	r.save(d)
	return finish(map[string]any{"reminder_id": d.ReminderID, "text": d.Text, "snoozed_until": until,
		"snoozed_until_human": r.human(until), "message": fmt.Sprintf("Snoozed '%s' for %d minutes.", d.Text, minutes)}, true, false)
}

// --- dates ---

// The date keys of reminder_service: a day offset, a default hour, tomorrow at an hour.
var (
	dateKeyOffsets      = map[string]int{"today": 0, "tomorrow": 1, "day_after_tomorrow": 2, "yesterday": -1}
	dateKeyDefaultHours = map[string]int{"morning": 7, "tonight": 20, "tomorrow_morning": 7, "tomorrow_afternoon": 14,
		"tomorrow_evening": 19, "tomorrow_night": 21, "yesterday_morning": 7, "yesterday_afternoon": 14,
		"yesterday_evening": 19, "last_night": 21}
	tomorrowTimeKeys = map[string]int{"tomorrow_morning": 7, "tomorrow_afternoon": 14, "tomorrow_evening": 19, "tomorrow_night": 21}
	weekdayNames     = map[string]int{"monday": 0, "tuesday": 1, "wednesday": 2, "thursday": 3, "friday": 4, "saturday": 5, "sunday": 6}
)

// hasExplicitTime is has_explicit_time: a clock time, minutes from now, or a key with an hour.
func hasExplicitTime(keys []string, clock string, rel int) bool {
	if clock != "" || rel > 0 {
		return true
	}
	return slices.ContainsFunc(keys, func(k string) bool { _, ok := dateKeyDefaultHours[strings.ToLower(strings.TrimSpace(k))]; return ok })
}

// pyWeekday is datetime.weekday(): Monday 0.
func pyWeekday(t time.Time) int { return (int(t.Weekday()) + 6) % 7 }

// resolveDue is resolve_due_at: date keys (with the clock time, or the key's hour, else 9),
// else the clock time today or tomorrow if past, else minutes from now; false for none. An
// out-of-range hour or minute is the error Python's datetime raised.
func (r *Reminder) resolveDue(keys []string, clock string, rel int) (time.Time, bool, error) {
	loc := r.loc()
	now := r.now().In(loc)
	hour, minute, hasHour := 0, 0, false
	if clock != "" {
		parts := strings.Split(strings.TrimSpace(clock), ":")
		if h, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
			hour, hasHour = h, true // as in Python, set before the minute fails to parse
			if len(parts) > 1 {
				minute, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
			}
		}
	}
	inRange := func(h int) error {
		if h < 0 || h > 23 {
			return errors.New("hour must be in 0..23") // Python 3.12's text (the Pi's); 3.14 adds ", not N"
		}
		if minute < 0 || minute > 59 {
			return errors.New("minute must be in 0..59")
		}
		return nil
	}
	if len(keys) > 0 {
		day := now
		for _, k := range keys {
			k = strings.ToLower(strings.TrimSpace(k))
			if off, ok := dateKeyOffsets[k]; ok {
				day = now.AddDate(0, 0, off)
			} else if h, ok := tomorrowTimeKeys[k]; ok {
				day = now.AddDate(0, 0, 1)
				if !hasHour {
					hour, hasHour = h, true
				}
			} else if k == "this_weekend" {
				day = now.AddDate(0, 0, (5-pyWeekday(now)+7)%7)
			} else if wd, ok := weekdayNames[strings.TrimPrefix(k, "next_")]; ok && strings.HasPrefix(k, "next_") {
				ahead := (wd - pyWeekday(now) + 7) % 7
				if ahead == 0 {
					ahead = 7
				}
				day = now.AddDate(0, 0, ahead)
			} else if h, ok := dateKeyDefaultHours[k]; ok && !hasHour {
				hour, hasHour = h, true
			}
		}
		if !hasHour {
			hour = 9 // the caller asked for a time already
		}
		if err := inRange(hour); err != nil {
			return time.Time{}, false, err
		}
		y, m, d := day.Date()
		return time.Date(y, m, d, hour, minute, 0, 0, loc).UTC(), true, nil
	}
	if hasHour {
		if err := inRange(hour); err != nil {
			return time.Time{}, false, err
		}
		y, m, d := now.Date()
		due := time.Date(y, m, d, hour, minute, 0, 0, loc)
		if !due.After(now) {
			due = time.Date(y, m, d+1, hour, minute, 0, 0, loc)
		}
		return due.UTC(), true, nil
	}
	if rel > 0 {
		return r.now().UTC().Add(time.Duration(rel) * time.Minute), true, nil
	}
	return time.Time{}, false, nil
}

// next is _next_occurrence, on the node's clock: the same local time a day, week, two weeks,
// weekday or month (day capped at 28, as in Python) later, in due's own zone.
func (r *Reminder) next(due time.Time, recurrence string) time.Time {
	l := due.In(r.loc())
	switch recurrence {
	case "daily":
		l = l.AddDate(0, 0, 1)
	case "weekly":
		l = l.AddDate(0, 0, 7)
	case "biweekly":
		l = l.AddDate(0, 0, 14)
	case "weekdays":
		l = l.AddDate(0, 0, 1)
		for pyWeekday(l) >= 5 {
			l = l.AddDate(0, 0, 1)
		}
	case "monthly":
		y, m := l.Year(), l.Month()+1
		if m > 12 {
			y, m = y+1, 1
		}
		l = time.Date(y, m, min(l.Day(), 28), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), l.Location())
	}
	return l.In(due.Location())
}

// human is format_due_at_human: "today at 3:00 PM", "tomorrow at …", "Friday at …" within the
// week, else "Oct 28 at …"; a value it can't read comes back as is.
func (r *Reminder) human(iso string) string {
	due, _, ok := pyFromISO(iso) // a naive value is UTC
	if !ok {
		return iso
	}
	due, now := due.In(r.loc()), r.now().In(r.loc())
	at := " at " + due.Format("3:04 PM")
	switch days := civilDay(due) - civilDay(now); {
	case days == 0:
		return "today" + at
	case days == 1:
		return "tomorrow" + at
	case days > 0 && days <= 6:
		return due.Format("Monday") + at
	}
	return due.Format("Jan 2") + at
}

// civilDay numbers a time's calendar date in its own zone.
func civilDay(t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// pyFromISO is datetime.fromisoformat for the forms reminders hold: a date, or a date and time
// (T or a space; minutes, seconds and a fraction optional) with an optional Z or ±HH:MM. A naive
// value comes back as UTC wall time with aware false.
func pyFromISO(s string) (t time.Time, aware, ok bool) {
	if len(s) > 10 && s[10] == ' ' {
		s = s[:10] + "T" + s[11:]
	}
	for _, l := range []string{"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, true, true
		}
	}
	for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02T15", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, false, true
		}
	}
	return time.Time{}, false, false
}

// --- the poller's check ---

// Due is one run of reminders_due: drop the one-shot reminders fired past the snooze window,
// mark the due ones announced (a recurring one moves to its next time) and push each to its
// owner's phone when the reminder's push setting is on. pending is how many were not yet
// announced before.
func (r *Reminder) Due(ctx context.Context) ([]reminders.Due, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for _, d := range r.all() {
		if !d.Announced || d.Recurrence != nil || d.LastAnnouncedAt == nil {
			continue
		}
		if at, aware, ok := pyFromISO(*d.LastAnnouncedAt); ok && aware && at.Before(now.Add(-snoozeWindow)) {
			_, _ = r.Store.DeleteData(reminderStorage, d.ReminderID)
		}
	}
	pending := r.pending(nil)
	if len(pending) == 0 {
		return nil, 0, nil
	}
	push := slices.Contains([]string{"true", "1", "yes", "on"}, strings.ToLower(r.Store.Secret(reminderPushKey, "integration", nil)))
	var due []reminders.Due
	for _, d := range r.all() {
		if d.Announced || !isPast(d.DueAt, now) || d.SnoozeUntil != nil && !isPast(*d.SnoozeUntil, now) {
			continue
		}
		d.AnnounceCount++
		last := pyISO(now)
		d.LastAnnouncedAt = &last
		if d.Recurrence != nil {
			if at, _, ok := pyFromISO(d.DueAt); ok {
				d.DueAt = isoformat(r.next(at, *d.Recurrence))
			}
			d.SnoozeUntil = nil
		} else {
			d.Announced = true
		}
		r.save(d)
		if push {
			r.push(ctx, d)
		}
		due = append(due, reminders.Due{ID: d.ReminderID, Text: d.Text, UserID: d.UserID})
	}
	return due, len(pending), nil
}

// isPast is a stored time at or before now (a naive one as UTC).
func isPast(iso string, now time.Time) bool {
	t, _, ok := pyFromISO(iso)
	return ok && !t.After(now)
}

const pushTimeout = 10 * time.Second

func (r *Reminder) push(ctx context.Context, d reminderData) {
	var cc *node.CC
	if r.CC != nil {
		cc = r.CC()
	}
	if cc == nil {
		r.log().Warn("reminder: push not sent: not connected", "id", d.ReminderID)
		return
	}
	body := map[string]any{"title": "Reminder", "body": d.Text, "priority": "high", "category": "reminder"}
	if d.UserID != nil {
		body["user_id"], body["target_type"] = *d.UserID, "user"
	}
	if err := cc.WithTimeout(pushTimeout).Do(ctx, http.MethodPost, "/api/v0/node/push-notification", body, nil); err != nil {
		r.log().Warn("reminder: push failed", "id", d.ReminderID, "err", err)
	}
}

// --- set_at, the proposable action's callback ---

// Callback is the set_at @callback: a reminder at an absolute time from a confirmed card, for
// the recognised speaker. handled is false for another command's callback.
func (r *Reminder) Callback(params map[string]any) (map[string]any, bool) {
	if params["command_name"] != reminderName {
		return nil, false
	}
	if enabled, ok := params["enabled"].(bool); ok && !enabled {
		return map[string]any{"success": false, "error": "Command '" + reminderName + "' not found on this node"}, true
	}
	if params["callback_name"] != "set_at" {
		return map[string]any{"success": false, "error": "Callback '" + pyStr(params["callback_name"]) + "' not registered on '" + reminderName + "'"}, true
	}
	if params["user_id"] == nil {
		return map[string]any{"success": false, "error": "Unknown speaker — cannot save a personal reminder.",
			"context_data": map[string]any{"error": "unknown_speaker", "set": false, "message": "I'm not sure who this reminder is for."}}, true
	}
	uid, err := pyInt(params["user_id"])
	if err != nil {
		return map[string]any{"success": false, "error": "bad user_id"}, true
	}
	user := int64(uid)
	data, _ := params["data"].(map[string]any)
	text := pyStr(data["text"])
	raw, _ := data["due_at_iso"].(string)
	due, _, ok := pyFromISO(raw) // a naive time is UTC
	if !pyTruthy(data["text"]) || !ok {
		return map[string]any{"success": false, "error": "set_at requires text and a valid absolute due_at_iso.",
			"context_data": map[string]any{"error": "invalid_params", "set": false, "message": "I need what to remind you about and when."}}, true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.create(text, due, nil, &user)
	human := r.human(d.DueAt)
	return map[string]any{"success": true, "error": nil, "context_data": map[string]any{"reminder_id": d.ReminderID, "text": text,
		"due_at": d.DueAt, "due_at_human": human, "set": true, "message": "Reminder set: " + text + " — " + human}}, true
}

// --- fast paths: the deterministic list, delete-all and snooze shapes ---

var (
	reminderListRe = regexp.MustCompile(`(?i)^\s*(?:what\s+reminders?\s+do\s+i\s+have|list\s+(?:my\s+)?reminders?` +
		`|show\s+(?:me\s+)?(?:my\s+)?reminders?|do\s+i\s+have\s+any\s+(?:upcoming\s+)?reminders?|any\s+reminders?(?:\s+(?:for|today))?)\s*[?.!]*$`)
	reminderTodayRe = regexp.MustCompile(`(?i)^\s*(?:(?:any|what)\s+reminders?\s+(?:for|do\s+i\s+have)\s+today` +
		`|reminders?\s+(?:for\s+)?today|do\s+i\s+have\s+(?:any\s+)?reminders?\s+today)\s*[?.!]*$`)
	reminderRecurringRe = regexp.MustCompile(`(?i)^\s*(?:show\s+(?:me\s+)?my\s+recurring\s+reminders?|(?:list\s+)?(?:my\s+)?recurring\s+reminders?)\s*[?.!]*$`)
	reminderDeleteAllRe = regexp.MustCompile(`(?i)^\s*(?:delete|cancel|clear|remove)\s+all\s+(?:my\s+|of\s+my\s+)?reminders?\s*[?.!]*$`)
	snoozeBareRe        = regexp.MustCompile(`(?i)^\s*snooze(?:\s+(?:that|it|this|the\s+reminder))?\s*[?.!]*$`)
	snoozeForRe         = regexp.MustCompile(`(?i)^\s*snooze(?:\s+(?:that|it|this))?\s+for\s+(\d+)\s+(minute|minutes|min|mins|hour|hours|hr|hrs)\s*[?.!]*$`)
)

func reminderArgs(a map[string]any) func([]string) (map[string]any, bool) {
	return func([]string) (map[string]any, bool) { return a, true }
}

var reminderPatterns = []pattern{
	regexPattern("reminder.list_today", "Bypass LLM for 'any reminders for today' / 'reminders for today'", "any reminders for today",
		reminderTodayRe, reminderArgs(map[string]any{"action": "list", "filter": "today"})),
	regexPattern("reminder.list_recurring", "Bypass LLM for 'show my recurring reminders' / 'list recurring reminders'", "show my recurring reminders",
		reminderRecurringRe, reminderArgs(map[string]any{"action": "list", "filter": "recurring"})),
	regexPattern("reminder.list", "Bypass LLM for 'list my reminders' / 'what reminders do I have'", "what reminders do I have",
		reminderListRe, reminderArgs(map[string]any{"action": "list"})),
	regexPattern("reminder.delete_all", "Bypass LLM for 'delete all my reminders' / 'clear all reminders'", "delete all my reminders",
		reminderDeleteAllRe, reminderArgs(map[string]any{"action": "delete", "scope": "all"})),
	regexPattern("reminder.snooze_for", "Bypass LLM for 'snooze for N minutes/hours'", "snooze for 15 minutes",
		snoozeForRe, func(m []string) (map[string]any, bool) {
			n, _ := strconv.Atoi(m[1])
			if u := strings.ToLower(m[2]); strings.HasPrefix(u, "hour") || strings.HasPrefix(u, "hr") {
				n *= 60
			}
			return map[string]any{"action": "snooze", "minutes": n}, true
		}),
	regexPattern("reminder.snooze", "Bypass LLM for bare 'snooze' / 'snooze that' (uses default 10 min)", "snooze",
		snoozeBareRe, reminderArgs(map[string]any{"action": "snooze"})),
}

func (r *Reminder) Direct(ctx context.Context, said string) (map[string]any, bool) {
	return directBy(ctx, r.Store, reminderName, reminderPatterns, said, func(a map[string]any) map[string]any {
		out, _ := r.Call(ctx, reminderName, a, node.Request{UserID: node.Speaker(ctx)})
		return out
	})
}

func (r *Reminder) FastPaths(command string) []nodesettings.FastPath {
	if command != reminderName {
		return nil
	}
	return patternList(reminderPatterns)
}

// ReminderMatch is the arguments Direct would run (for comparing with N:'s pre_route).
func ReminderMatch(said string) (Match, bool) {
	for _, p := range reminderPatterns {
		if args, ok := p.match(said); ok {
			return Match{reminderName, args}, true
		}
	}
	return Match{}, false
}

// Secrets is the push setting, for the app's settings.
func (r *Reminder) Secrets(command string) []nodesettings.SecretSpec {
	if command != reminderName {
		return nil
	}
	return []nodesettings.SecretSpec{{Key: reminderPushKey, Scope: "integration",
		Description: "Also send reminders as push notifications to your phone", ValueType: "bool", FriendlyName: "Push Notifications"}}
}

// --- the app's Stored data browser ---

func (r *Reminder) DataCommands() []nodesettings.DataCommand {
	return []nodesettings.DataCommand{browserDef(reminderName)}
}

// Summaries is display_summary: the text, when it's due (and how it repeats), a bell.
func (r *Reminder) Summaries(_ context.Context, _ string, records []nodesettings.Object) ([]map[string]any, error) {
	out := make([]map[string]any, len(records))
	for i, o := range records {
		x := rec{obj: o}
		var subtitle any
		if x.truthy("due_at") {
			subtitle = r.human(pyStr(o.Value("due_at")))
		}
		if rc := x.str("recurrence"); rc != "" {
			if subtitle != nil {
				subtitle = subtitle.(string) + " • " + rc
			} else {
				subtitle = rc
			}
		}
		out[i] = map[string]any{"title": cmpOrStr(x.str("text"), "Reminder"), "subtitle": subtitle, "icon": "bell"}
	}
	return out, nil
}

func (r *Reminder) Create(context.Context, string, nodesettings.Object, json.Number) (string, json.RawMessage, error) {
	return "", nil, noCreate(reminderName)
}

// UpdateRecord is update_reminder for the app's edits: text, due_at and recurrence checked, a
// naive due_at placed in the node's zone, and a new recurrence clearing the firing history so
// the reminder fires again on its new schedule.
func (r *Reminder) UpdateRecord(command, key string, patch nodesettings.Object, visible func(nodesettings.Object) bool) (nodesettings.Object, bool, error) {
	if command != reminderName {
		return nodesettings.Object{}, false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	reject := func(msg string) (nodesettings.Object, bool, error) {
		return nodesettings.Object{}, true, &nodesettings.ErrRejected{Message: msg}
	}
	o, found := r.Store.Data(reminderStorage, key, r.now())
	d, ok := reminderOf(o)
	if !found || !ok || !visible(o) {
		return reject("Reminder " + key + " not found")
	}
	_, hasRec := patch.Get("recurrence")
	newRec := patch.Value("recurrence")
	changed := hasRec && !(newRec == nil && d.Recurrence == nil || d.Recurrence != nil && newRec == *d.Recurrence)
	if _, ok := patch.Get("text"); ok {
		t, isStr := patch.Value("text").(string)
		if !isStr || strings.TrimSpace(t) == "" {
			return reject("text must be a non-empty string")
		}
		d.Text = strings.TrimSpace(t)
	}
	if _, ok := patch.Get("due_at"); ok {
		raw, isStr := patch.Value("due_at").(string)
		if !isStr {
			return reject("due_at must be an ISO 8601 string")
		}
		t, aware, ok := pyFromISO(raw)
		if !ok {
			return reject("due_at is not a valid ISO 8601 timestamp: " + raw)
		}
		if !aware {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), r.loc())
		}
		d.DueAt = isoformat(t)
	}
	if hasRec {
		if newRec == nil {
			d.Recurrence = nil
		} else if s, isStr := newRec.(string); isStr && slices.Contains(recurrences, s) {
			d.Recurrence = &s
		} else {
			return reject("unknown recurrence: " + pyStr(newRec))
		}
	}
	if changed {
		d.Announced, d.AnnounceCount, d.LastAnnouncedAt, d.SnoozeUntil = false, 0, nil, nil
	}
	r.save(d)
	r.log().Info("reminder: updated", "id", key, "fields", patch.Keys(), "recurrence_changed", changed)
	return d.object(), true, nil
}
