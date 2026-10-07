package cc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The attention broker (attention_broker.py, attention_journal.py), D18: the working phase-1
// part only. A proactive notification on /node/push-notification, /node/inbox-item or
// /node/send-link is journaled and walks journal → inbox → push through dedup, the safety
// bypass, budgets and quiet hours (household timezone, D8). The consent/tier gates and their
// tables are dropped. It never drops: the floor is the journal. Any broker error fails open to
// the legacy delivery (AttentionGate ok=false), and with attention.enabled off the routes never
// reach it (nodeplugin.go), so they stay byte-identical.

// HouseholdTimezone resolves a household's IANA zone (quiet hours, the budgets' local day and
// the journal cron, D18). Nil, "" or an unknown zone mean UTC.
type HouseholdTimezone interface {
	HouseholdTimezone(ctx context.Context, householdID string) string
}

const (
	attentionCleanupJob = "cc.attention_cleanup"
	attentionJournalJob = "cc.attention_journal"
	signalSweepJob      = "cc.signal_sweep"
	journalCategory     = "attention_journal"
)

var rungs = []string{"journal", "inbox", "push", "led_invite", "speak"}

func rungIndex(r string) int {
	for i, x := range rungs {
		if x == r {
			return i
		}
	}
	return -1
}

func demote(r string) string {
	i := rungIndex(r) - 1
	if i < 0 {
		i = 0
	}
	return rungs[i]
}

// fallbackDedupeKey is the title hash for producers that set no dedupe_key.
func fallbackDedupeKey(title string) string {
	norm := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(title))), " ")
	h := sha256.Sum256([]byte(norm))
	return "title:" + hex.EncodeToString(h[:])[:32]
}

// attentionBroker implements AttentionGate.
type attentionBroker struct{ m *Module }

func (m *Module) householdLocation(ctx context.Context, hh string) *time.Location {
	if m.HouseholdClock != nil {
		if tz := m.HouseholdClock.HouseholdTimezone(ctx, hh); tz != "" {
			if loc, err := time.LoadLocation(tz); err == nil {
				return loc
			}
		}
	}
	return time.UTC
}

// inQuietHours is _in_quiet_hours: "HH:MM-HH:MM", local, may cross midnight; malformed never
// silences anything.
func inQuietHours(spec string, local time.Time) bool {
	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		return false
	}
	parse := func(s string) (int, bool) {
		hm := strings.Split(strings.TrimSpace(s), ":")
		if len(hm) < 2 {
			return 0, false
		}
		h, e1 := strconv.Atoi(strings.TrimSpace(hm[0]))
		mi, e2 := strconv.Atoi(strings.TrimSpace(hm[1]))
		if e1 != nil || e2 != nil || h < 0 || h > 23 || mi < 0 || mi > 59 {
			return 0, false
		}
		return h*60 + mi, true
	}
	start, ok1 := parse(parts[0])
	end, ok2 := parse(parts[1])
	if !ok1 || !ok2 {
		return false
	}
	now := local.Hour()*60 + local.Minute()
	if start <= end {
		return start <= now && now < end
	}
	return now >= start || now < end
}

func trailEntry(gate, result, detail string) *pyjson.Object {
	o := pyjson.NewObject()
	o.Set("gate", gate)
	o.Set("result", result)
	o.Set("detail", detail)
	return o
}

// Gate is record_and_route. ok=false (fail open) on any storage error.
func (b *attentionBroker) Gate(ctx context.Context, req AttentionRequest) (AttentionDecision, bool) {
	m := b.m
	d, err := b.route(ctx, req)
	if err != nil {
		m.deps.Log.Warn("cc: attention broker failed; legacy delivery", "household", req.HouseholdID, "err", err)
		return AttentionDecision{}, false
	}
	m.ensureJournalTrigger(ctx, req.HouseholdID)
	return d, true
}

func (b *attentionBroker) route(ctx context.Context, req AttentionRequest) (AttentionDecision, error) {
	m := b.m
	hh := req.HouseholdID
	sc := settings.Scope{HouseholdID: hh}
	title := truncRunes(strings.TrimSpace(req.Title), 500)
	if title == "" {
		title = "(untitled)"
	}
	category := truncRunes(strings.TrimSpace(req.Category), 50)
	if category == "" {
		category = "general"
	}
	source := req.Source
	if source == "" {
		source = category
	}
	source = truncRunes(strings.TrimSpace(source), 100)
	explicit := req.DedupeKey != ""
	key := req.DedupeKey
	if !explicit {
		key = fallbackDedupeKey(title)
	}
	now := m.now()
	loc := m.householdLocation(ctx, hh)
	local := now.In(loc)
	dayStart := dbTime(time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc))
	safety := false
	for _, c := range strings.Split(m.settings.String(ctx, settingAttnSafety, sc), ",") {
		if strings.TrimSpace(strings.ToLower(c)) == strings.ToLower(category) && strings.TrimSpace(c) != "" {
			safety = true
		}
	}
	rung := req.RequestedRung
	if rungIndex(rung) < 0 {
		rung = "push"
	}
	if rungIndex(rung) > rungIndex("push") {
		rung = "push" // the phase-1 ladder tops out at push
	}

	tx, err := m.deps.DB.Write.BeginTx(ctx, nil)
	if err != nil {
		return AttentionDecision{}, err
	}
	defer tx.Rollback()
	eventID := uuid4()
	var payload any
	if len(req.Payload) > 0 {
		payload = pyjson.Dumps(toPy(req.Payload), true)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cc_attention_events (id, household_id, source, category, title, summary,
		dedupe_key, target_user_id, origin_node_id, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, hh, source, category, title, req.Summary, key, req.TargetUserID, nullIfBlank(req.OriginNodeID), payload, dbTime(now)); err != nil {
		return AttentionDecision{}, err
	}

	var trail []any
	withheld := ""
	// Gate 0, dedup: never for a forced alert, and for a safety-class alert only on an explicit
	// key (the title hash must never silence a recurring dose, the 2026-07-19 Keppra incident).
	var dup string
	windowH := m.settings.Int(ctx, settingAttnDedupeHours, sc)
	if !req.Force && (explicit || !safety) {
		err := tx.QueryRowContext(ctx, `SELECT id FROM cc_attention_events WHERE household_id = ? AND source = ?
			AND dedupe_key = ? AND created_at >= ? AND id != ? LIMIT 1`,
			hh, source, key, dbTime(now.Add(-time.Duration(windowH)*time.Hour)), eventID).Scan(&dup)
		if err != nil && err != sql.ErrNoRows {
			return AttentionDecision{}, err
		}
	}
	count := func(q string, args ...any) (int64, error) {
		var n int64
		err := tx.QueryRowContext(ctx, q, args...).Scan(&n)
		return n, err
	}
	switch {
	case req.Force:
		trail = append(trail, trailEntry("force", "bypass", "forced delivery — all gates skipped"))
	case dup != "":
		trail = append(trail, trailEntry("dedupe", "duplicate", fmt.Sprintf("of event %s within %dh", dup, windowH)))
		rung, withheld = "journal", "dedupe"
	case safety:
		d := fmt.Sprintf("category '%s' is safety-class", category)
		if !explicit {
			d += " — never deduped without an explicit key"
		}
		trail = append(trail, trailEntry("safety_class", "bypass", d))
	default:
		// Gate 3, budgets: an exhausted budget demotes one rung, never drops.
		capN := m.settings.Int(ctx, settingAttnSourceCap, sc)
		today, err := count(`SELECT COUNT(*) FROM cc_attention_deliveries d JOIN cc_attention_events e ON d.event_id = e.id
			WHERE d.household_id = ? AND e.source = ? AND d.rung != 'journal' AND d.created_at >= ?`, hh, source, dayStart)
		if err != nil {
			return AttentionDecision{}, err
		}
		if today >= capN {
			trail = append(trail, trailEntry("budget", "withheld", fmt.Sprintf("source cap %d/day reached", capN)))
			rung, withheld = "journal", "budget"
		} else {
			budgetKey := map[string]string{"push": settingAttnPushBudget, "inbox": settingAttnInboxBudget}
			for {
				k, ok := budgetKey[rung]
				if !ok {
					break
				}
				budget := m.settings.Int(ctx, k, sc)
				used, err := count(`SELECT COUNT(*) FROM cc_attention_deliveries WHERE household_id = ? AND rung = ? AND created_at >= ?`,
					hh, rung, dayStart)
				if err != nil {
					return AttentionDecision{}, err
				}
				if used < budget {
					trail = append(trail, trailEntry("budget", "pass", fmt.Sprintf("%s %d/%d", rung, used, budget)))
					break
				}
				next := demote(rung)
				trail = append(trail, trailEntry("budget", "demoted", fmt.Sprintf("%s budget %d/day exhausted -> %s", rung, budget, next)))
				rung = next
			}
			if rung == "journal" {
				withheld = "budget"
			}
		}
		// Gate 4, quiet hours (household timezone): push demotes to inbox.
		if withheld == "" && rung == "push" {
			quiet := m.settings.String(ctx, settingAttnQuietHours, sc)
			if quiet != "" && inQuietHours(quiet, local) {
				trail = append(trail, trailEntry("context", "demoted", "quiet hours "+quiet+" -> inbox"))
				rung = "inbox"
			} else {
				trail = append(trail, trailEntry("context", "pass", "outside quiet hours"))
			}
		}
	}
	if trail == nil {
		trail = []any{}
	}
	deliveryID := uuid4()
	var outcome any
	if withheld == "dedupe" {
		outcome = "duplicate"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cc_attention_deliveries (id, event_id, household_id, rung, gate_trail_json,
		withheld_by, outcome, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		deliveryID, eventID, hh, rung, pyjson.Dumps(trail, true), nullIfBlank(withheld), outcome, dbTime(now)); err != nil {
		return AttentionDecision{}, err
	}
	if err := tx.Commit(); err != nil {
		return AttentionDecision{}, err
	}
	return AttentionDecision{Deliver: rung != "journal", Rung: rung, WithheldBy: withheld, DeliveryID: deliveryID}, nil
}

// Outcome is mark_outcome.
func (b *attentionBroker) Outcome(ctx context.Context, deliveryID, outcome, inboxItemID string) {
	if deliveryID == "" {
		return
	}
	if _, err := b.m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_attention_deliveries SET outcome = ?,
		inbox_item_id = COALESCE(?, inbox_item_id) WHERE id = ?`, outcome, nullIfBlank(inboxItemID), deliveryID); err != nil {
		b.m.deps.Log.Warn("cc: attention outcome not stamped", "delivery", deliveryID, "err", err)
	}
}

// --- the journal card (attention_journal.py) ---

// composeJournal is compose_journal_card over the last 24 h: (summary, markdown body), or
// ok=false with no activity.
func (m *Module) composeJournal(ctx context.Context, hh string) (string, string, bool, error) {
	hours := 24
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT d.rung, d.withheld_by, d.gate_trail_json, e.title, e.source
		FROM cc_attention_deliveries d JOIN cc_attention_events e ON d.event_id = e.id
		WHERE d.household_id = ? AND d.created_at >= ? ORDER BY d.created_at DESC`,
		hh, dbTime(m.now().Add(-time.Duration(hours)*time.Hour)))
	if err != nil {
		return "", "", false, err
	}
	defer rows.Close()
	type row struct{ rung, withheld, trail, title, source string }
	var delivered, held []row
	for rows.Next() {
		var r row
		var wb sql.NullString
		if err := rows.Scan(&r.rung, &wb, &r.trail, &r.title, &r.source); err != nil {
			return "", "", false, err
		}
		r.withheld = wb.String
		if r.rung != "journal" {
			delivered = append(delivered, r)
		} else {
			held = append(held, r)
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", false, err
	}
	if len(delivered)+len(held) == 0 {
		return "", "", false, nil
	}
	var lines []string
	if len(delivered) > 0 {
		lines = append(lines, fmt.Sprintf("**Delivered (%d)**", len(delivered)))
		for i, r := range delivered {
			if i == 20 {
				break
			}
			lines = append(lines, fmt.Sprintf("- %s — %s → %s", r.title, r.source, r.rung))
		}
		if len(delivered) > 20 {
			lines = append(lines, fmt.Sprintf("- …and %d more", len(delivered)-20))
		}
		lines = append(lines, "")
	}
	if len(held) > 0 {
		counts, order := map[string]int{}, []string{}
		for _, r := range held {
			g := r.withheld
			if g == "" {
				g = "unknown"
			}
			if counts[g] == 0 {
				order = append(order, g)
			}
			counts[g]++
		}
		sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] }) // most_common
		parts := make([]string, len(order))
		for i, g := range order {
			parts[i] = fmt.Sprintf("%d %s", counts[g], g)
		}
		lines = append(lines, fmt.Sprintf("**Withheld (%d)** — %s", len(held), strings.Join(parts, ", ")))
		for i, r := range held {
			if i == 20 {
				break
			}
			reason := r.withheld
			if reason == "" {
				reason = "unknown"
			}
			det := trailDetail(r.trail, reason)
			if det != "" {
				det = ": " + det
			}
			lines = append(lines, fmt.Sprintf("- %s — %s (%s%s)", r.title, r.source, reason, det))
		}
		if len(held) > 20 {
			lines = append(lines, fmt.Sprintf("- …and %d more", len(held)-20))
		}
	}
	summary := fmt.Sprintf("Delivered %d, withheld %d in the last %dh", len(delivered), len(held), hours)
	return summary, strings.Join(lines, "\n"), true, nil
}

func trailDetail(trail, gate string) string {
	v, err := pyjson.Loads(trail)
	if err != nil {
		return ""
	}
	l, _ := v.([]any)
	for _, e := range l {
		if o, ok := e.(*pyjson.Object); ok {
			if g, _ := o.Get("gate"); g == gate {
				d, _ := o.Get("detail")
				s, _ := d.(string)
				return s
			}
		}
	}
	return ""
}

// postJournalCard is post_journal_card: a household card, never pushed (the journal never
// spends the attention it accounts for).
func (m *Module) postJournalCard(ctx context.Context, hh string) (string, error) {
	summary, body, ok, err := m.composeJournal(ctx, hh)
	if err != nil || !ok {
		return "", err
	}
	return m.postInboxItem(ctx, hh, nil, "Attention journal — "+m.now().UTC().Format("2006-01-02"), summary, body,
		journalCategory, nil, false, "household"), nil
}

// journalSpec is the household's journal trigger: its cron in the household timezone.
func (m *Module) journalSpec(ctx context.Context, hh string) scheduler.Spec {
	cron := m.settings.String(ctx, settingAttnJournalCron, settings.Scope{HouseholdID: hh})
	if _, err := scheduler.ParseCron(cron); err != nil {
		cron = "0 21 * * *"
	}
	return scheduler.Spec{Cron: cron, TZ: m.householdLocation(ctx, hh).String()}
}

// ensureJournalTrigger keeps one cron trigger per household with broker activity (D27: the
// legacy 60 s tick and its in-memory last_fired become a persisted trigger, so a restart never
// double-posts). It is re-put only when the cron or timezone changed.
func (m *Module) ensureJournalTrigger(ctx context.Context, hh string) {
	if m.deps.Scheduler == nil || hh == "" {
		return
	}
	spec := m.journalSpec(ctx, hh)
	want := spec.Cron + "|" + spec.TZ
	m.sig.mu.Lock()
	have := m.sig.journal[hh]
	m.sig.mu.Unlock()
	if have == want {
		return
	}
	t := scheduler.Trigger{Name: attentionJournalJob + ":" + hh, Kind: scheduler.KindCron, Spec: spec,
		JobType: attentionJournalJob, Payload: []byte(hh)}
	var err error
	if have == "" {
		// First sight since start: keep a persisted schedule (and its missed fire), unless the
		// spec differs, which the next fire re-checks.
		err = m.deps.Scheduler.Ensure(ctx, t)
	} else {
		err = m.deps.Scheduler.Put(ctx, t)
	}
	if err != nil {
		m.deps.Log.Warn("cc: journal trigger not scheduled", "household", hh, "err", err)
		return
	}
	m.sig.mu.Lock()
	m.sig.journal[hh] = want
	m.sig.mu.Unlock()
}

func (m *Module) runJournalJob(ctx context.Context, job queue.Job) ([]byte, error) {
	f, err := scheduler.DecodeFire(job.Payload)
	if err != nil {
		return nil, queue.Permanent(err)
	}
	hh := string(f.Payload)
	sc := settings.Scope{HouseholdID: hh}
	if !m.settings.Bool(ctx, settingAttention, sc) || !m.settings.Bool(ctx, settingAttnJournalEnabled, sc) {
		return nil, nil
	}
	// A cron/timezone change since the trigger was put: re-put it (next fire uses it).
	spec := m.journalSpec(ctx, hh)
	m.sig.mu.Lock()
	m.sig.journal[hh] = ""
	m.sig.mu.Unlock()
	if m.deps.Scheduler != nil {
		_ = m.deps.Scheduler.Put(ctx, scheduler.Trigger{Name: attentionJournalJob + ":" + hh, Kind: scheduler.KindCron,
			Spec: spec, JobType: attentionJournalJob, Payload: []byte(hh)})
		m.sig.mu.Lock()
		m.sig.journal[hh] = spec.Cron + "|" + spec.TZ
		m.sig.mu.Unlock()
	}
	id, err := m.postJournalCard(ctx, hh)
	if err != nil {
		return nil, err
	}
	if id != "" {
		m.deps.Log.Info("cc: attention journal card posted", "household", hh)
	}
	return nil, nil
}

// cleanupAttention deletes events older than each household's attention.journal_ttl_days
// (D8: read per household, not globally); deliveries cascade (foreign_keys=ON).
func (m *Module) cleanupAttention(ctx context.Context) error {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT DISTINCT household_id FROM cc_attention_events`)
	if err != nil {
		return err
	}
	var hhs []string
	for rows.Next() {
		var hh string
		if err := rows.Scan(&hh); err != nil {
			rows.Close()
			return err
		}
		hhs = append(hhs, hh)
	}
	rows.Close()
	for _, hh := range hhs {
		days := m.settings.Int(ctx, settingAttnJournalTTL, settings.Scope{HouseholdID: hh})
		if days <= 0 {
			days = 30
		}
		res, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_attention_events WHERE household_id = ? AND created_at < ?`,
			hh, dbTime(m.now().Add(-time.Duration(days)*24*time.Hour)))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			m.deps.Log.Info("cc: attention cleanup removed events", "household", hh, "count", n)
		}
	}
	return nil
}

// --- wiring ---

// registerSignals mounts 5c signals/proposals/attention and registers their jobs.
func (m *Module) registerSignals(mux *http.ServeMux) {
	m.sig = newSignalState()
	if m.Attention == nil {
		m.Attention = &attentionBroker{m: m}
	}
	m.registerSignalReactions()

	const v0 = "/api/v0"
	mux.HandleFunc("POST "+v0+"/signals", m.handlePostSignal)
	mux.HandleFunc("POST "+v0+"/mobile/presence", m.user(m.handleMobilePresence))
	mux.HandleFunc("GET "+v0+"/mobile/household/{household_id}/signal-automations", m.user(m.handleListSignalAutomations))
	mux.HandleFunc("PUT "+v0+"/mobile/household/{household_id}/signal-automations/{kind}", m.user(m.handlePutSignalAutomation))
	mux.HandleFunc("GET "+v0+"/proposals/suppressions", m.node(m.handleSuppressionSignals))
	mux.HandleFunc("GET "+v0+"/mobile/proposal-suppressions", m.user(m.handleListMySuppressions))
	mux.HandleFunc("DELETE "+v0+"/mobile/proposal-suppressions/{suppression_id}", m.user(m.handleDeleteMySuppression))

	if q := m.deps.Queue; q != nil {
		q.Register(signalReactionJob, queue.Handler{Run: m.runReactionJob, Concurrency: 2, MaxAttempts: 1, Lease: 5 * time.Minute})
		q.Register(signalSweepJob, queue.Handler{Run: func(ctx context.Context, _ queue.Job) ([]byte, error) { return nil, m.cleanupSignals(ctx) }})
		q.Register(attentionCleanupJob, queue.Handler{Run: func(ctx context.Context, _ queue.Job) ([]byte, error) { return nil, m.cleanupAttention(ctx) }})
		q.Register(attentionJournalJob, queue.Handler{Run: m.runJournalJob})
	}
}

// startSignals schedules the loops (D27): the signal TTL sweep every 30 min and the attention
// cleanup daily, both first at startup (D18), persisted across restarts.
func (m *Module) startSignals(ctx context.Context) error {
	if m.deps.Scheduler == nil {
		return nil
	}
	for _, t := range []scheduler.Trigger{
		{Name: signalSweepJob, Kind: scheduler.KindInterval, JobType: signalSweepJob, Spec: scheduler.Spec{Every: 30 * time.Minute, StartNow: true}},
		{Name: attentionCleanupJob, Kind: scheduler.KindInterval, JobType: attentionCleanupJob, Spec: scheduler.Spec{Every: 24 * time.Hour, StartNow: true}},
	} {
		if err := m.deps.Scheduler.Ensure(ctx, t); err != nil {
			return err
		}
	}
	return nil
}
