package cc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	lldates "github.com/alexberardi/jarvis-server/internal/modules/llm/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Signal reactions (signal_reaction_registry.py, signal_reaction_bridge.py,
// signal_automation_executor.py). Ingest never waits for them (§7.5): each (reaction, signal)
// becomes a durable queue job (cc.signal_reaction), which bounds load on slow hardware and
// replaces call_soon_threadsafe and both captured main loops. Without a queue (tests) they run
// on a bounded goroutine pool that drops rather than blocks.

const signalReactionJob = "cc.signal_reaction"

// Node round-trip timeouts (doc 10 §5). Variables so tests can shorten them.
var (
	probeTimeout    = 4 * time.Second  // one report_tools probe
	resolveTimeout  = 6 * time.Second  // resolving a node for a command, all probes
	dispatchTimeout = 20 * time.Second // one tool_call
	callbackTimeout = 25 * time.Second // a proposable @callback on the node
	callbackPoll    = 500 * time.Millisecond
	toolsCacheTTL   = 30 * time.Second
	reactionTimeout = 2 * time.Minute
)

const leaveBufferMinutes = 5

// reactionCtx is ReactionContext. Facts is json.dumps(facts) (dict order kept), so it rides a
// queue payload unchanged.
type reactionCtx struct {
	HouseholdID string `json:"household_id"`
	NodeID      string `json:"node_id,omitempty"`
	UserID      *int64 `json:"user_id,omitempty"`
	Kind        string `json:"kind"`
	Facts       string `json:"facts"`
}

func (rc reactionCtx) facts() *pyjson.Object {
	v, err := pyjson.Loads(rc.Facts)
	if o, ok := v.(*pyjson.Object); ok && err == nil {
		return o
	}
	return pyjson.NewObject()
}

// reaction is one registered handler; it returns a status string and never fails ingest.
type reaction struct {
	name  string
	run   func(ctx context.Context, rc reactionCtx) string
	dedup func(rc reactionCtx) string // queue dedup key while a job is live ("" none)
}

// registerReaction is register_signal_reaction: idempotent per (kind, name).
func (m *Module) registerReaction(kind string, r reaction) {
	s := m.sig
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.reactions[kind] {
		if e.name == r.name {
			return
		}
	}
	s.reactions[kind] = append(s.reactions[kind], r)
}

func (m *Module) reactionsFor(kind string) []reaction {
	s := m.sig
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]reaction(nil), s.reactions[kind]...)
}

// registerSignalReactions wires the built-in reactions (main.py:513-516): appt.upcoming →
// leave_by, and every catalog kind → automation, so appt.upcoming fires both (D46).
func (m *Module) registerSignalReactions() {
	m.registerReaction("appt.upcoming", reaction{name: "leave_by", run: m.reactLeaveBy, dedup: func(rc reactionCtx) string {
		return "leave_by:" + rc.HouseholdID + ":" + eventID(rc.facts())
	}})
	for _, k := range signalCatalog {
		m.registerReaction(k.kind, reaction{name: "automation", run: m.reactAutomation, dedup: func(rc reactionCtx) string {
			key, sig := automationIdentity(rc)
			return "automation:" + key + ":" + shortHash(sig)
		}})
	}
}

type reactionJob struct {
	Reaction string      `json:"reaction"`
	Ctx      reactionCtx `json:"ctx"`
}

// dispatchSignal is dispatch_signal_edges minus the cut matcher edge (D17): fan the persisted
// signal out to its kind's reactions. Never blocks or fails the caller.
func (m *Module) dispatchSignal(rc reactionCtx) {
	for _, r := range m.reactionsFor(rc.Kind) {
		if q := m.deps.Queue; q != nil {
			payload, _ := json.Marshal(reactionJob{Reaction: r.name, Ctx: rc})
			opts := queue.Options{MaxAttempts: 1}
			if r.dedup != nil {
				opts.DedupKey = "cc.signal_reaction:" + r.dedup(rc)
			}
			if _, err := q.Enqueue(context.Background(), signalReactionJob, payload, opts); err != nil && !errors.Is(err, queue.ErrDuplicate) {
				m.deps.Log.Warn("cc: signal reaction not scheduled", "reaction", r.name, "kind", rc.Kind, "err", err)
			}
			continue
		}
		select {
		case m.sig.sem <- struct{}{}:
		default:
			m.deps.Log.Warn("cc: signal reaction dropped (busy)", "reaction", r.name, "kind", rc.Kind)
			continue
		}
		m.sig.wg.Add(1)
		go func(r reaction) {
			defer m.sig.wg.Done()
			defer func() { <-m.sig.sem }()
			m.runReaction(context.Background(), r, rc)
		}(r)
	}
}

func (m *Module) runReaction(ctx context.Context, r reaction, rc reactionCtx) string {
	ctx, cancel := context.WithTimeout(ctx, reactionTimeout)
	defer cancel()
	var status string
	func() {
		defer func() {
			if p := recover(); p != nil {
				m.deps.Log.Warn("cc: signal reaction panicked", "reaction", r.name, "kind", rc.Kind, "panic", p)
				status = "error"
			}
		}()
		status = r.run(ctx, rc)
	}()
	m.deps.Log.Info("cc: signal reaction", "reaction", r.name, "kind", rc.Kind, "household", rc.HouseholdID, "status", status)
	return status
}

func (m *Module) runReactionJob(ctx context.Context, job queue.Job) ([]byte, error) {
	var j reactionJob
	if err := json.Unmarshal(job.Payload, &j); err != nil {
		return nil, queue.Permanent(err)
	}
	for _, r := range m.reactionsFor(j.Ctx.Kind) {
		if r.name == j.Reaction {
			return []byte(m.runReaction(ctx, r, j.Ctx)), nil
		}
	}
	return nil, nil
}

// --- reaction claims (cc_reaction_claims, D40 Q5) ---

// claim takes (household, key) unless a live claim holds it; pending claims are short-lived
// so a crashed run frees the key.
func (m *Module) claim(ctx context.Context, hh, key, value, outcome string, ttl time.Duration) (bool, error) {
	now := m.now()
	res, err := m.deps.DB.Write.ExecContext(ctx, `
		INSERT INTO cc_reaction_claims (household_id, claim_key, value, outcome, claimed_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (household_id, claim_key) DO UPDATE SET value = excluded.value, outcome = excluded.outcome,
			claimed_at = excluded.claimed_at, expires_at = excluded.expires_at
		WHERE cc_reaction_claims.expires_at <= ?`,
		hh, key, value, outcome, dbTime(now), dbTime(now.Add(ttl)), dbTime(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// settleClaim latches a claim on a terminal outcome until expires.
func (m *Module) settleClaim(ctx context.Context, hh, key, outcome string, expires time.Time) {
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_reaction_claims SET outcome = ?, expires_at = ?
		WHERE household_id = ? AND claim_key = ?`, outcome, dbTime(expires), hh, key); err != nil {
		m.deps.Log.Warn("cc: claim settle failed", "key", key, "err", err)
	}
}

func (m *Module) releaseClaim(ctx context.Context, hh, key string) {
	if _, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_reaction_claims WHERE household_id = ? AND claim_key = ?`, hh, key); err != nil {
		m.deps.Log.Warn("cc: claim release failed", "key", key, "err", err)
	}
}

// claimValue returns a live claim's value.
func (m *Module) claimValue(ctx context.Context, hh, key string) (string, bool) {
	var v sql.NullString
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT value FROM cc_reaction_claims
		WHERE household_id = ? AND claim_key = ? AND expires_at > ?`, hh, key, dbTime(m.now())).Scan(&v)
	if err != nil {
		return "", false
	}
	return v.String, true
}

func (m *Module) setClaimValue(ctx context.Context, hh, key, value string, ttl time.Duration) {
	now := m.now()
	if _, err := m.deps.DB.Write.ExecContext(ctx, `
		INSERT INTO cc_reaction_claims (household_id, claim_key, value, outcome, claimed_at, expires_at)
		VALUES (?, ?, ?, 'handled', ?, ?)
		ON CONFLICT (household_id, claim_key) DO UPDATE SET value = excluded.value, outcome = excluded.outcome,
			claimed_at = excluded.claimed_at, expires_at = excluded.expires_at`,
		hh, key, value, dbTime(now), dbTime(now.Add(ttl))); err != nil {
		m.deps.Log.Warn("cc: claim write failed", "key", key, "err", err)
	}
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// --- node round trips (node_tools._request_tools_from_node, dispatch_node_command) ---

type toolsEntry struct {
	report  *toolsReport
	expires time.Time
}

// toolsReport is a node's report_tools answer: {client_tools, available_commands, ...}.
type toolsReport struct {
	raw      string
	Commands []struct {
		CommandName       string           `json:"command_name"`
		ProposableActions []map[string]any `json:"proposable_actions"`
	} `json:"available_commands"`
}

var errNodeSilent = errors.New("cc: node did not report its tools")

// nodeTools asks a node for its tools (report_tools, reply on /mobile/node-tool-reports/{rid}),
// with a 30 s per-node cache (doc 10 §11: every directed signal, leave-by, automation and
// confirm used to re-probe).
func (m *Module) nodeTools(ctx context.Context, nodeID string, timeout time.Duration) (*toolsReport, error) {
	s := m.sig
	now := m.now()
	s.mu.Lock()
	if e, ok := s.tools[nodeID]; ok && now.Before(e.expires) {
		s.mu.Unlock()
		return e.report, nil
	}
	s.mu.Unlock()
	raw, err := m.reportTools(ctx, nodeID, timeout)
	if err != nil {
		return nil, err
	}
	rep := &toolsReport{raw: string(raw)}
	if err := json.Unmarshal(raw, rep); err != nil {
		return nil, errNodeSilent
	}
	s.mu.Lock()
	s.tools[nodeID] = toolsEntry{report: rep, expires: m.now().Add(toolsCacheTTL)}
	s.mu.Unlock()
	return rep, nil
}

// commandNames are the commands the report advertises.
func (r *toolsReport) commandNames() map[string]bool {
	out := map[string]bool{}
	for _, c := range r.Commands {
		if c.CommandName != "" {
			out[c.CommandName] = true
		}
	}
	return out
}

// proposable is capability_registry.resolve_proposable_action: the advertised action for
// (command, callback), or nil (the authoritative opt-in gate).
func (r *toolsReport) proposable(command, callback string) map[string]any {
	for _, c := range r.Commands {
		if c.CommandName != command {
			continue
		}
		for _, a := range c.ProposableActions {
			if cb, _ := a["callback"].(string); cb == callback {
				return a
			}
		}
	}
	return nil
}

// firstProposable is the first advertised action of command (any callback).
func (r *toolsReport) firstProposable(command string) map[string]any {
	for _, c := range r.Commands {
		if c.CommandName == command && len(c.ProposableActions) > 0 {
			return c.ProposableActions[0]
		}
	}
	return nil
}

// clientTools returns the report's client_tools as ordered objects, Jarvis extensions stripped.
func (r *toolsReport) clientTools() []prompts.Tool {
	v, err := pyjson.Loads(r.raw)
	o, ok := v.(*pyjson.Object)
	if err != nil || !ok {
		return nil
	}
	lv, _ := o.Get("client_tools")
	l, _ := lv.([]any)
	var tools []prompts.Tool
	for _, e := range l {
		// A photo tool can't run from a signal: there is no photo (§9).
		if t, ok := e.(*pyjson.Object); ok && !servertools.IsPhotoTool(t) {
			tools = append(tools, t)
		}
	}
	return prompts.StripJarvisExtensions(tools)
}

// dispatchNodeCommand runs one command on a node headlessly (tool_call; the node answers on
// /device-control-results/{rid}) and returns its output dict. Publish failures and timeouts
// come back as a synthetic failure, never an error. No `trusted` flag (D4/D7).
func (m *Module) dispatchNodeCommand(ctx context.Context, nodeID, command string, args map[string]any, userID *int64, timeout time.Duration) map[string]any {
	if !m.bus.Available() {
		return map[string]any{"success": false, "error": "could not dispatch to node: " + ErrNoBroker.Error()}
	}
	if args == nil {
		args = map[string]any{}
	}
	key := uuid4()
	details := map[string]any{"command_name": command, "arguments": args, "tool_call_id": key, "reply_request_id": key}
	if userID != nil {
		details["user_id"] = *userID
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, raw, err := m.bus.CommandAwait(cctx, nodeID, "tool_call", details, key)
	if err != nil {
		return map[string]any{"success": false, "error": "the node didn't respond in time", "timeout": true}
	}
	var res map[string]any
	if err := json.Unmarshal(raw, &res); err != nil {
		return map[string]any{"success": false, "error": "malformed node result"}
	}
	out, present := res["output"]
	if !present {
		return res
	}
	if om, ok := out.(map[string]any); ok {
		return om
	}
	return map[string]any{"success": true, "result": out}
}

// dispatchOK is _dispatch_ok.
func dispatchOK(out map[string]any) bool {
	if out == nil {
		return false
	}
	if s, present := out["success"]; present && !pyTruthyValue(s) {
		return false
	}
	return !pyTruthyValue(out["timeout"])
}

// householdNodesByRecency is _household_node_ids_by_recency: active nodes heard from, most
// recent first, then the rest.
func (m *Module) householdNodesByRecency(ctx context.Context, hh string) []string {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT node_id, is_active, last_seen FROM cc_nodes
		WHERE household_id = ? ORDER BY last_seen DESC`, hh)
	if err != nil {
		m.deps.Log.Warn("cc: could not list household nodes", "err", err)
		return nil
	}
	defer rows.Close()
	var preferred, rest []string
	for rows.Next() {
		var id string
		var active bool
		var seen sql.NullString
		if err := rows.Scan(&id, &active, &seen); err != nil {
			return nil
		}
		if active && seen.Valid {
			preferred = append(preferred, id)
		} else {
			rest = append(rest, id)
		}
	}
	return append(preferred, rest...)
}

// resolveNodeForCommand is resolve_household_node_for_command: probe every household node
// concurrently (4 s each, 6 s overall) and take the first, in preference order, that
// advertises the command (and callback when given) as proposable.
func (m *Module) resolveNodeForCommand(ctx context.Context, hh, command, callback string) string {
	ids := m.householdNodesByRecency(ctx, hh)
	if len(ids) == 0 {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	flags := make([]bool, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			rep, err := m.nodeTools(cctx, id, probeTimeout)
			if err != nil {
				return
			}
			if callback != "" {
				flags[i] = rep.proposable(command, callback) != nil
			} else {
				flags[i] = rep.firstProposable(command) != nil
			}
		}(i, id)
	}
	wg.Wait()
	if cctx.Err() != nil && ctx.Err() == nil {
		// The overall cap cut some probes off; legacy then refused the whole resolution.
		for _, f := range flags {
			if f {
				goto pick
			}
		}
		m.deps.Log.Warn("cc: node resolution timed out", "command", command)
		return ""
	}
pick:
	for i, f := range flags {
		if f {
			return ids[i]
		}
	}
	return ""
}

// --- leave-by (signal_reaction_bridge.py) ---

func eventID(f *pyjson.Object) string {
	for _, k := range []string{"event_id", "id", "start_iso", "start"} {
		if v, ok := f.Get(k); ok && pyTruthyValue(v) {
			return pyjson.Str(v)
		}
	}
	return ""
}

func factString(f *pyjson.Object, keys ...string) string {
	for _, k := range keys {
		if v, ok := f.Get(k); ok && pyTruthyValue(v) {
			if s, isStr := v.(string); isStr {
				return s
			}
			return pyjson.Str(v)
		}
	}
	return ""
}

// parseISOAware is _parse_iso: ISO-8601 to an aware time; naive values are UTC.
func parseISOAware(s string) (time.Time, bool) {
	s = strings.Replace(s, "Z", "+00:00", 1)
	for _, f := range []string{"2006-01-02T15:04:05.999999999-07:00", "2006-01-02T15:04-07:00", "2006-01-02 15:04:05.999999999-07:00"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}
	for _, f := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// pyIsoformat is datetime.isoformat() for an aware time ("+00:00", microseconds when nonzero).
func pyIsoformat(t time.Time) string {
	layout := "2006-01-02T15:04:05"
	if t.Nanosecond()/1000 != 0 {
		layout += ".000000"
	}
	return t.Format(layout + "-07:00")
}

// reactLeaveBy is react_to_appt_upcoming. Statuses: disabled, no_node, no_location, no_start,
// no_user, suppressed, duplicate, no_drive_time, no_route, bad_start, departure_passed,
// not_advertised, proposed, error. D40 Q5: the per-event claim is in SQLite and latches only
// on terminal outcomes (proposed, departure_passed), so a transient failure retries on the
// calendar agent's next re-emit (§8.3).
func (m *Module) reactLeaveBy(ctx context.Context, rc reactionCtx) string {
	hh, f := rc.HouseholdID, rc.facts()
	if !m.proposalsEnabled(ctx, hh) {
		return "disabled"
	}
	if rc.NodeID == "" {
		return "no_node"
	}
	location := factString(f, "location")
	if location == "" {
		return "no_location"
	}
	startISO := factString(f, "start_iso", "start")
	if startISO == "" {
		return "no_start"
	}
	if rc.UserID == nil {
		return "no_user"
	}
	event := eventID(f)
	if m.suppressed(ctx, hh, rc.UserID, "reminder", leaveBySource) {
		return "suppressed"
	}
	key := "leave_by:" + event
	got, err := m.claim(ctx, hh, key, event, "pending", 2*time.Minute)
	if err != nil {
		return "error"
	}
	if !got {
		return "duplicate"
	}
	status, start := m.leaveBy(ctx, rc, f, location, startISO, event)
	switch status {
	case "proposed", "departure_passed":
		exp := m.now().Add(7 * 24 * time.Hour)
		if !start.IsZero() && start.Add(24*time.Hour).After(m.now()) {
			exp = start.Add(24 * time.Hour)
		}
		m.settleClaim(ctx, hh, key, status, exp)
	default:
		m.releaseClaim(ctx, hh, key)
	}
	return status
}

const leaveBySource = "leaveby"

func (m *Module) leaveBy(ctx context.Context, rc reactionCtx, f *pyjson.Object, location, startISO, event string) (string, time.Time) {
	hh, nodeID := rc.HouseholdID, rc.NodeID
	rep, err := m.nodeTools(ctx, nodeID, probeTimeout)
	if err != nil || !rep.commandNames()["get_drive_time"] {
		m.deps.Log.Info("cc: leave-by: node lacks get_drive_time", "node", nodeID)
		return "no_drive_time", time.Time{}
	}
	res := m.dispatchNodeCommand(ctx, nodeID, "get_drive_time",
		map[string]any{"destination": location, "resolution": "strict"}, rc.UserID, dispatchTimeout)
	if !pyTruthyValue(res["success"]) {
		m.deps.Log.Info("cc: leave-by: get_drive_time no route", "reason", res["reason"], "error", res["error"])
		return "no_route", time.Time{}
	}
	drive, ok := intOf(res["duration_minutes"])
	if !ok {
		return "no_route", time.Time{}
	}
	address, _ := res["destination"].(string)
	if address == "" {
		address = location
	}
	durationText, _ := res["duration_text"].(string)
	if durationText == "" {
		durationText = fmt.Sprintf("%d min", drive)
	}
	start, ok := parseISOAware(startISO)
	if !ok {
		return "bad_start", time.Time{}
	}
	now := m.now()
	due := start.Add(-time.Duration(drive+leaveBufferMinutes) * time.Minute)
	if !due.After(now) {
		return "departure_passed", start
	}
	dueISO := pyIsoformat(due)
	title := factString(f, "title")
	if title == "" {
		title = "your appointment"
	}
	startDisplay := factString(f, "start_display")

	// leave_by.suggested: observability + render only, never fanned out (§7.6).
	ttl := start.Sub(now) + 300*time.Second
	if ttl < 300*time.Second {
		ttl = 300 * time.Second
	}
	lf := pyjson.NewObject()
	lf.Set("title", title)
	lf.Set("due_at_iso", dueISO)
	lf.Set("drive_minutes", big.NewInt(int64(drive)))
	lf.Set("address", address)
	lf.Set("event_id", event)
	summary := "Leave-by reminder suggested for " + title
	if _, err := m.saveSignal(ctx, newSignal{HouseholdID: hh, SourceKey: "leaveby:" + event, Kind: "leave_by.suggested",
		Summary: &summary, Facts: lf, UserID: rc.UserID, NodeID: nodeID, TTL: time.Duration(ttl.Seconds()) * time.Second,
		SourceAgent: "leave_by_bridge"}); err != nil {
		m.deps.Log.Debug("cc: leave-by: save_signal failed", "err", err)
	}

	target := m.resolveNodeForCommand(ctx, hh, "reminder", "set_at")
	if target == "" {
		m.deps.Log.Info("cc: leave-by: no household node advertises reminder.set_at")
		return "not_advertised", start
	}
	rt, err := m.nodeTools(ctx, target, probeTimeout)
	if err != nil {
		return "not_advertised", start
	}
	action := rt.proposable("reminder", "set_at")
	if action == nil {
		return "not_advertised", start
	}
	idem := "leaveby:" + event
	when := "soon"
	if startDisplay != "" {
		when = "at " + startDisplay
	}
	params := pyjson.NewObject()
	params.Set("text", "Leave for "+title)
	params.Set("due_at_iso", dueISO)
	params.Set("idempotency_key", idem)
	ok = m.emitProposalCard(ctx, proposalCard{
		HouseholdID: hh, NodeID: target, Command: "reminder", Callback: "set_at", Params: params, IdempotencyKey: idem,
		CardTitle:    anyStrOr(action["card_title"], "Set a reminder to leave on time?"),
		ConfirmLabel: anyStrOr(action["confirm_label"], "Set reminder"),
		DismissLabel: anyStrOr(action["dismiss_label"], "Dismiss"),
		BlastTier:    anyStrOr(action["blast_tier"], "reversible"),
		Summary:      title + " " + when + " — " + durationText + " drive from home",
		Body: title + " starts " + when + " at " + address + ". It's about a " + durationText +
			" drive, so I can remind you when it's time to leave.",
		UserID: rc.UserID, Source: leaveBySource, Descriptor: "leave-by reminders for calendar events",
	})
	if !ok {
		return "not_advertised", start
	}
	return "proposed", start
}

func anyStrOr(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

// intOf is int(x) for a decoded JSON number or numeric string.
func intOf(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n), true
		}
		if f, err := x.Float64(); err == nil {
			return int(f), true
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n, true
		}
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// --- signal automations (signal_automation_executor.py) ---

const (
	automationCommand = "jarvis.signal_automation"
	automationMaxTok  = 512
	presenceDedupTTL  = 30 * 24 * time.Hour
	eventDedupTTL     = 7 * 24 * time.Hour
	automationSystem  = "You carry out a household's standing automation rule. When a household " +
		"event happens, do exactly what the user asked by calling ONE of the " +
		"available tools with correct arguments. If no available tool fits the " +
		"instruction, call no tool. Never invent tools or arguments."
)

// automationIdentity is _dedup_identity: presence.left/seen share one key whose value is the
// state (leave→arrive→leave acts three times, a heartbeat re-assert is skipped, §7.7); other
// kinds key on the event identity.
func automationIdentity(rc reactionCtx) (string, string) {
	f := rc.facts()
	uid := "None"
	if rc.UserID != nil {
		uid = strconv.FormatInt(*rc.UserID, 10)
	}
	if rc.Kind == "presence.left" || rc.Kind == "presence.seen" {
		state := factString(f, "state")
		if state == "" {
			state = rc.Kind
		}
		return uid + ":presence", state
	}
	if id := factString(f, "event_id", "id"); id != "" {
		return uid + ":" + rc.Kind, id
	}
	return uid + ":" + rc.Kind, pyjson.Dumps(pySorted(f), true)
}

// reactAutomation is react_to_signal_automation. Statuses: no_rule, unchanged, no_tools,
// no_action, ran:<cmd>, failed:<cmd>, confirm:<cmd>, confirm_failed, error.
func (m *Module) reactAutomation(ctx context.Context, rc reactionCtx) string {
	rule := m.enabledRule(ctx, rc.HouseholdID, rc.Kind)
	if rule == nil {
		return "no_rule"
	}
	key, sig := automationIdentity(rc)
	claimKey := "automation:" + key
	if v, ok := m.claimValue(ctx, rc.HouseholdID, claimKey); ok && v == sig {
		return "unchanged"
	}
	nodeID, tools := m.resolveNodeAndTools(ctx, rc.HouseholdID, rc.NodeID)
	if nodeID == "" || len(tools) == 0 {
		return "no_tools"
	}
	label := rc.Kind
	if k, ok := catalogKind(rc.Kind); ok {
		label = k.label
	}
	name, args, err := m.pickAction(ctx, rc.HouseholdID, label, rule.Instruction, rc.facts(), tools)
	if err != nil {
		m.deps.Log.Warn("cc: signal automation inference failed", "household", rc.HouseholdID, "err", err)
		return "error"
	}
	// Latch now (before dispatch): this occurrence is handled even if the model chose nothing.
	ttl := eventDedupTTL
	if strings.HasSuffix(key, ":presence") {
		ttl = presenceDedupTTL
	}
	m.setClaimValue(ctx, rc.HouseholdID, claimKey, sig, ttl)
	if name == "" {
		return "no_action"
	}
	if rule.Delivery == "automatic" {
		// D7: automatic keeps full power, no allowlist; no trusted flag (D4).
		ok := dispatchOK(m.dispatchNodeCommand(ctx, nodeID, name, args, rc.UserID, dispatchTimeout))
		m.deps.Log.Info("cc: signal automation auto-ran", "command", name, "success", ok)
		if ok {
			return "ran:" + name
		}
		return "failed:" + name
	}
	if m.emitAutomationCard(ctx, rc.HouseholdID, rc.UserID, nodeID, name, args, label, rule.Instruction) {
		return "confirm:" + name
	}
	return "confirm_failed"
}

// resolveNodeAndTools is _resolve_node_and_tools: the preferred node, then household nodes by
// recency (sequentially, 4 s each); the first with a non-empty tool menu wins.
func (m *Module) resolveNodeAndTools(ctx context.Context, hh, preferred string) (string, []prompts.Tool) {
	var cands []string
	if preferred != "" {
		cands = append(cands, preferred)
	}
	for _, id := range m.householdNodesByRecency(ctx, hh) {
		if id != preferred {
			cands = append(cands, id)
		}
	}
	for _, id := range cands {
		rep, err := m.nodeTools(ctx, id, probeTimeout)
		if err != nil {
			continue
		}
		if tools := rep.clientTools(); len(tools) > 0 {
			return id, tools
		}
	}
	return "", nil
}

// pickAction is _pick_action: one background-slot tool-call inference (thinking off). An
// empty name means the model called no tool.
func (m *Module) pickAction(ctx context.Context, hh, label, instruction string, facts *pyjson.Object, tools []prompts.Tool) (string, map[string]any, error) {
	if m.LLM == nil {
		return "", nil, errors.New("LLM unavailable")
	}
	user := "Event: " + label + "\n" +
		"Event details: " + pyjson.Dumps(facts, true) + "\n" +
		"The user's instruction for this event: \"" + instruction + "\"\n" +
		"Call the single tool that carries out the instruction, or none if nothing fits."
	temp, maxTok, budget := 0.0, automationMaxTok, 0
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{
		Label: "background", Temperature: &temp, MaxTokens: &maxTok, ReasoningBudget: &budget,
		// The engine copy drops the date-time marker, as on the voice path (A10b R6/R9).
		Tools: llmTools(tools), ToolChoice: json.RawMessage(`"auto"`),
		Messages: []llm.Message{{Role: "system", Content: llm.TextContent(automationSystem)},
			{Role: "user", Content: llm.TextContent(user)}},
	})
	if err != nil {
		return "", nil, err
	}
	if len(resp.ToolCalls) == 0 || resp.ToolCalls[0].Function.Name == "" {
		return "", nil, nil
	}
	fn := resp.ToolCalls[0].Function
	fn.Arguments = m.automationDates(ctx, hh, instruction, tools, fn.Name, fn.Arguments)
	args := map[string]any{}
	if fn.Arguments != "" {
		dec := json.NewDecoder(strings.NewReader(fn.Arguments))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) == nil {
			if o, ok := v.(map[string]any); ok {
				args = o
			}
		}
	}
	return fn.Name, args, nil
}

// automationDates resolves the chosen call's date-time parameters the way the voice engine
// does (A10b R9): ISO values in resolved_datetimes are mapped back to keys, then every
// date-time parameter is filled or resolved from date keys in the household's zone, with the
// keys named in the rule's instruction ("remind me tomorrow") as the turn's keys. Without it a
// dated client tool got whatever the model wrote (an invented ISO date, or a raw key).
func (m *Module) automationDates(ctx context.Context, hh, instruction string, tools []prompts.Tool, name, args string) string {
	props := dates.SchemaProperties(tools, name)
	if props == nil {
		return args
	}
	dctx := dates.New(m.now(), m.householdTimezone(ctx, hh))
	if _, fixed := dctx.FixISODates([]string{args}); len(fixed) == 1 {
		args = fixed[0]
	}
	if out, changed := dctx.InjectDates(args, props, lldates.Extract(instruction)); changed {
		args = out
	}
	return args
}

// pySorted turns pyjson objects into maps so pyjson.Dumps writes sorted keys
// (json.dumps(sort_keys=True)).
func pySorted(v any) any {
	switch x := v.(type) {
	case *pyjson.Object:
		out := make(map[string]any, x.Len())
		for _, k := range x.Keys() {
			e, _ := x.Get(k)
			out[k] = pySorted(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = pySorted(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = pySorted(e)
		}
		return out
	}
	return v
}
