package cc

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Phase 5c: the Signal Bus (docs/cc/10). A Signal is a short-lived household fact ("Alex is
// home", "dentist at 3pm"), upserted on (household, source_key). Producers are POST /signals
// (node agents, app-to-app), the phone (POST /mobile/presence) and voice (a speaker identified
// in the conversation, D2/D3). Ingest fans out to deterministic reactions (signals_reactions.go):
// leave-by and the user-authored automations. The proactive situation matcher is cut (D17).
//
// Shared-file hooks (kept tiny): registerSignals (Register), startSignals (Start),
// signalDefinitions (Definitions), purgeSignals (PurgeUser), noteVoicePresence (turn identity).

// Setting keys owned by 5c signals/proposals/attention (D11: only keys something reads).
const (
	settingSignalsEnabled    = "signals.enabled"
	settingSignalAutomations = "signals.automations"
	settingAmbientContext    = "ambient_context.enabled"
	settingProposalsEnabled  = "proposals.enabled"

	settingAttnPushBudget     = "attention.daily_push_budget"
	settingAttnInboxBudget    = "attention.daily_inbox_budget"
	settingAttnSourceCap      = "attention.source_daily_cap"
	settingAttnDedupeHours    = "attention.dedupe_window_hours"
	settingAttnQuietHours     = "attention.quiet_hours"
	settingAttnSafety         = "attention.safety_categories"
	settingAttnJournalTTL     = "attention.journal_ttl_days"
	settingAttnJournalEnabled = "attention.journal_card_enabled"
	settingAttnJournalCron    = "attention.journal_card_cron"
)

// Constants a port must carry (doc 10 §5).
const (
	signalRatePerMin     = 60
	mobilePresenceTTL    = 4 * time.Hour
	voicePresenceTTL     = 900 * time.Second
	maxSignalTTLSeconds  = 604800
	maxInstructionLength = 500
)

// signalDefinitions are the settings of signals, proposals and attention (legacy
// settings_definitions.py; proposals.proactive_enabled and attention.timezone are dropped by
// D17/D18; attention.enabled is declared by 5b's voiceDefinitions).
func signalDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingSignalsEnabled, Category: "signals", Type: settings.Bool, Default: true,
			Description: "Enable the Signal Bus ingress + reactive rendering for this household"},
		{Key: settingSignalAutomations, Category: "signals", Type: settings.String, Default: "{}",
			Description: "Per-household signal automations as a JSON object mapping a signal kind to " +
				"{\"instruction\": str, \"enabled\": bool, \"delivery\": \"automatic\"|\"notification\"} — the " +
				"user's free-text instruction for what should happen when that signal fires (interpreted at " +
				"fire time). Edited from mobile via the signal-automations endpoint; the authorable kinds " +
				"come from the static signal catalog."},
		{Key: settingAmbientContext, Category: "memory", Type: settings.Bool, Default: false,
			Description: "Inject an always-on ambient situational block (current time, weather, today's " +
				"calendar, live household Signals) into the voice prompt, and record voice presence from an " +
				"identified speaker. Opt-in."},
		{Key: settingProposalsEnabled, Category: "proposals", Type: settings.Bool, Default: false,
			Description: "Master toggle for agent-proposed action cards. Gates the proposable-action " +
				"dispatcher and the leave-by reaction: when off (default), a tapped 'agent proposes a " +
				"command' card (e.g. an email agent's 'Add to calendar?') is refused server-side. " +
				"Fail-closed — any settings error disables it — because it lets background agents " +
				"originate real writes."},
		{Key: settingAttnPushBudget, Category: "attention", Type: settings.Int, Default: int64(8),
			Description: "Max broker-routed push notifications per household per local day (exhausted budget demotes to inbox)"},
		{Key: settingAttnInboxBudget, Category: "attention", Type: settings.Int, Default: int64(30),
			Description: "Max broker-routed inbox cards per household per local day (exhausted budget demotes to journal)"},
		{Key: settingAttnSourceCap, Category: "attention", Type: settings.Int, Default: int64(4),
			Description: "Max delivered items per source per household per local day"},
		{Key: settingAttnDedupeHours, Category: "attention", Type: settings.Int, Default: int64(24),
			Description: "Window in which a repeated (source, dedupe_key) is journaled as a duplicate instead of re-delivered"},
		{Key: settingAttnQuietHours, Category: "attention", Type: settings.String, Default: "22:00-07:00",
			Description: "Household-local window (HH:MM-HH:MM, household timezone) during which push demotes to " +
				"inbox; safety categories exempt"},
		{Key: settingAttnSafety, Category: "attention", Type: settings.String, Default: "medication,reminder,security,safety",
			Description: "Comma-separated categories that bypass budgets and quiet hours (dedup still applies " +
				"only with an explicit dedupe key)"},
		{Key: settingAttnJournalTTL, Category: "attention", Type: settings.Int, Default: int64(30),
			Description: "Days to retain attention events/deliveries before cleanup (per household)"},
		{Key: settingAttnJournalEnabled, Category: "attention", Type: settings.Bool, Default: true,
			Description: "Post the daily attention-journal inbox card (delivered + withheld summary)"},
		{Key: settingAttnJournalCron, Category: "attention", Type: settings.String, Default: "0 21 * * *",
			Description: "Cron (household timezone) for the daily attention-journal card"},
	}
}

// signalState is the module's in-process signal plumbing (none of it is durable state:
// dedup and claims live in cc_reaction_claims, D40 Q5).
type signalState struct {
	mu        sync.Mutex
	rate      map[string]rateBucket // (household, principal) -> bucket (M6/D47)
	tools     map[string]toolsEntry // per-node report_tools cache (doc 10 §11)
	journal   map[string]string     // household -> journal trigger spec already put
	reactions map[string][]reaction // kind -> reactions, registration order

	sem chan struct{} // bounds in-process reactions when there is no queue
	wg  sync.WaitGroup
}

type rateBucket struct {
	tokens float64
	last   time.Time
}

func newSignalState() *signalState {
	return &signalState{rate: map[string]rateBucket{}, tools: map[string]toolsEntry{}, journal: map[string]string{},
		reactions: map[string][]reaction{}, sem: make(chan struct{}, 8)}
}

// rateOK is _rate_ok: a 60/min token bucket, keyed (M6/D47) on the household and the
// authenticated principal, never the client-chosen source_agent. In memory: it resets on
// restart, as legacy.
func (m *Module) rateOK(hh, principal string) bool {
	s := m.sig
	key := hh + "\x00" + principal
	now := m.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.rate[key]
	if !ok {
		b = rateBucket{tokens: signalRatePerMin, last: now}
	}
	b.tokens += now.Sub(b.last).Seconds() * (signalRatePerMin / 60.0)
	if b.tokens > signalRatePerMin {
		b.tokens = signalRatePerMin
	}
	b.last = now
	if b.tokens < 1 {
		s.rate[key] = b
		return false
	}
	b.tokens--
	s.rate[key] = b
	return true
}

// signalsEnabled is _signals_enabled: fail OPEN (a settings error reads as the default, true).
func (m *Module) signalsEnabled(ctx context.Context, hh string) bool {
	return m.settings.Bool(ctx, settingSignalsEnabled, settings.Scope{HouseholdID: hh})
}

// proposalsEnabled is _proposals_enabled: fail CLOSED (default false; an error reads false).
func (m *Module) proposalsEnabled(ctx context.Context, hh string) bool {
	v, err := m.settings.Get(ctx, settingProposalsEnabled, settings.Scope{HouseholdID: hh})
	if err != nil {
		return false
	}
	b, _ := v.Value.(bool)
	return b
}

// --- the store (services/signal_service.py) ---

// newSignal is one save_signal call.
type newSignal struct {
	HouseholdID string
	SourceKey   string
	Kind        string
	Subject     *string
	Summary     *string
	Facts       any // a pyjson value (stored as json.dumps text), or nil
	UserID      *int64
	NodeID      string
	Room        *string
	TTL         time.Duration // 0: never expires
	Cacheable   bool
	Salience    *float64
	SourceAgent string
}

// errNotQuantized is CacheableSignalError.
var errNotQuantized = errors.New("cacheable Signal must be quantized/absolute — it may not contain a " +
	"live float, a seconds-precision time, or a relative-time phrase")

var (
	floatRE        = regexp.MustCompile(`\d+\.\d+`)
	secondsTimeRE  = regexp.MustCompile(`\b\d{1,2}:\d{2}:\d{2}\b`)
	relativeTimeRE = regexp.MustCompile(`(?i)\b(ago|just now|right now|moments? ago|minutes?|seconds?|in \d+ min)\b`)
)

// rejectNonQuantized is _reject_non_quantized over summary + json.dumps(facts).
func rejectNonQuantized(summary *string, factsText string) error {
	var parts []string
	if summary != nil && *summary != "" {
		parts = append(parts, *summary)
	}
	if factsText != "" {
		parts = append(parts, factsText)
	}
	blob := strings.Join(parts, " ")
	if floatRE.MatchString(blob) || secondsTimeRE.MatchString(blob) || relativeTimeRE.MatchString(blob) {
		return errNotQuantized
	}
	return nil
}

// saveSignal is save_signal: one atomic UPSERT on (household_id, source_key), which removes
// the legacy is_active filter and the first-insert race (§8.15). Every column is overwritten;
// a re-emit without a TTL makes the row non-expiring (§7.2).
func (m *Module) saveSignal(ctx context.Context, s newSignal) (int64, error) {
	factsText := ""
	if s.Facts != nil {
		factsText = pyjson.Dumps(s.Facts, true)
	}
	if s.Cacheable {
		if err := rejectNonQuantized(s.Summary, factsText); err != nil {
			return 0, err
		}
	}
	now := m.now()
	var expires any
	if s.TTL > 0 {
		expires = dbTime(now.Add(s.TTL))
	}
	var facts any
	if s.Facts != nil {
		facts = factsText
	}
	var id int64
	err := m.deps.DB.Write.QueryRowContext(ctx, `
		INSERT INTO cc_signals (household_id, user_id, node_id, room, kind, subject, source_key, summary, facts,
		                        source_agent, cacheable, salience, observed_at, expires_at, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT (household_id, source_key) DO UPDATE SET
			user_id = excluded.user_id, node_id = excluded.node_id, room = excluded.room, kind = excluded.kind,
			subject = excluded.subject, summary = excluded.summary, facts = excluded.facts,
			source_agent = excluded.source_agent, cacheable = excluded.cacheable, salience = excluded.salience,
			observed_at = excluded.observed_at, expires_at = excluded.expires_at, is_active = 1,
			updated_at = excluded.updated_at
		RETURNING id`,
		s.HouseholdID, s.UserID, nullIfBlank(s.NodeID), s.Room, s.Kind, s.Subject, s.SourceKey, s.Summary, facts,
		nullIfBlank(s.SourceAgent), s.Cacheable, s.Salience, dbTime(now), expires, dbTime(now), dbTime(now)).Scan(&id)
	return id, err
}

// cleanupSignals is cleanup_expired (expired rows are hard-deleted; NULL never expires), plus
// the expired reaction claims (D40 Q5).
func (m *Module) cleanupSignals(ctx context.Context) error {
	now := dbTime(m.now())
	res, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_signals WHERE expires_at IS NOT NULL AND expires_at <= ?`, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		m.deps.Log.Info("cc: cleaned up expired signals", "count", n)
	}
	_, err = m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_reaction_claims WHERE expires_at <= ?`, now)
	return err
}

// liveSignal is one unexpired row as the renderer and the catalog annotation read it.
type liveSignal struct {
	kind, subject, summary string
}

func (m *Module) liveSignals(ctx context.Context, hh string) ([]liveSignal, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `
		SELECT kind, COALESCE(subject, ''), COALESCE(summary, '') FROM cc_signals
		WHERE household_id = ? AND is_active = 1 AND (expires_at IS NULL OR expires_at > ?)`, hh, dbTime(m.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []liveSignal
	for rows.Next() {
		var s liveSignal
		if err := rows.Scan(&s.kind, &s.subject, &s.summary); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SignalContext is render_signal_block over the household's live Signals: the summaries, one
// per line, stable-sorted by (kind, subject), blanks skipped. "" when nothing renders. It is
// the Signal part of the ambient bundle (docs/cc/01 §3, 03 §3): the ambient assembler appends
// it when ambient_context.enabled; errors fail open to "".
func (m *Module) SignalContext(ctx context.Context, hh string) string {
	sigs, err := m.liveSignals(ctx, hh)
	if err != nil {
		m.deps.Log.Warn("cc: signal render failed", "household", hh, "err", err)
		return ""
	}
	return renderSignalBlock(sigs)
}

func renderSignalBlock(sigs []liveSignal) string {
	var items []liveSignal
	for _, s := range sigs {
		if strings.TrimSpace(s.summary) != "" {
			items = append(items, s)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].kind != items[j].kind {
			return items[i].kind < items[j].kind
		}
		return items[i].subject < items[j].subject
	})
	lines := make([]string, len(items))
	for i, s := range items {
		lines[i] = strings.TrimSpace(s.summary)
	}
	return strings.Join(lines, "\n")
}

// --- presence (record_presence, record_voice_presence) ---

// recordPresence is the canonical presence writer: home → presence.seen, anything else →
// presence.left, one upserting row per person (source_key presence:{uid}).
func (m *Module) recordPresence(ctx context.Context, hh string, userID int64, state, sourceAgent, nodeID string,
	room, name *string, ttl time.Duration, summary *string) (int64, string, error) {
	home := state == "home"
	kind := "presence.left"
	if home {
		kind = "presence.seen"
	}
	if summary == nil {
		who := "Someone"
		if name != nil && *name != "" {
			who = *name
		}
		s := who + " is away"
		if home {
			where := ""
			if room != nil && *room != "" {
				where = " in the " + *room
			}
			s = who + " is home" + where
		}
		summary = &s
	}
	facts := pyjson.NewObject()
	facts.Set("user_id", big.NewInt(userID))
	facts.Set("state", state)
	if room != nil {
		facts.Set("room", *room)
	} else {
		facts.Set("room", nil)
	}
	subject := "user:" + strconv.FormatInt(userID, 10)
	uid := userID
	id, err := m.saveSignal(ctx, newSignal{HouseholdID: hh, SourceKey: "presence:" + strconv.FormatInt(userID, 10), Kind: kind,
		Subject: &subject, Summary: summary, Facts: facts, UserID: &uid, NodeID: nodeID, Room: room, TTL: ttl,
		SourceAgent: sourceAgent})
	return id, kind, err
}

// noteVoicePresence is record_voice_presence for a speaker identified in this conversation
// (D2/D3: never a sticky fallback). Gated on ambient_context.enabled and, per D40 Q7, on
// signals.enabled. It never fans out (automations must not fire every time you speak) and
// never shortens a live phone "home" row's TTL: it then only refreshes observed_at. Best
// effort: errors are logged.
func (m *Module) noteVoicePresence(ctx context.Context, hh string, userID int64, nodeID, speakerName string) {
	if m.sig == nil || hh == "" || userID == 0 {
		return
	}
	sc := settings.Scope{HouseholdID: hh}
	if !m.settings.Bool(ctx, settingAmbientContext, sc) || !m.signalsEnabled(ctx, hh) {
		return
	}
	now := m.now()
	var kind string
	var expires sql.NullString
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT kind, expires_at FROM cc_signals WHERE household_id = ? AND source_key = ?`,
		hh, "presence:"+strconv.FormatInt(userID, 10)).Scan(&kind, &expires)
	if err == nil && kind == "presence.seen" && (!expires.Valid || parseTS(expires.String).After(now.Add(voicePresenceTTL))) {
		if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_signals SET observed_at = ?, updated_at = ?
			WHERE household_id = ? AND source_key = ?`, dbTime(now), dbTime(now), hh, "presence:"+strconv.FormatInt(userID, 10)); err != nil {
			m.deps.Log.Debug("cc: voice presence refresh skipped", "err", err)
		}
		return
	}
	who := speakerName
	if who == "" {
		who = "Someone"
	}
	where := ""
	if nodeID != "" {
		where = " at the " + nodeID + " node"
	}
	summary := who + " was recently heard" + where
	var name *string
	if speakerName != "" {
		name = &speakerName
	}
	if _, _, err := m.recordPresence(ctx, hh, userID, "home", "voice", nodeID, nil, name, voicePresenceTTL, &summary); err != nil {
		m.deps.Log.Debug("cc: voice presence emit skipped", "err", err)
	}
}

// --- POST /signals (api/signals.py) ---

// signalAuth is SignalsAuthContext: a node (household from auth) or an app (any household,
// kept by M6/D47; app credentials are admin-issued infrastructure).
type signalAuth struct {
	node        bool
	householdID string
	nodeID      string
	appID       string
	members     []int64
}

func (a signalAuth) principal() string {
	if a.node {
		return "node:" + a.nodeID
	}
	return "app:" + a.appID
}

// authSignals is _verify_signals_auth: a valid node key, else app-to-app credentials (a bad
// node key falls through), else 401.
func (m *Module) authSignals(w http.ResponseWriter, r *http.Request) (signalAuth, bool) {
	ctx := r.Context()
	if key := r.Header.Get("X-API-Key"); key != "" {
		if id, k, ok := authn.NodeKey(key); ok {
			v, err := m.Auth.ValidateNode(ctx, id, k, m.serviceID())
			if err == nil && v.Valid {
				if row, err := m.nodeByID(ctx, id); err == nil {
					m.touchLastSeen(ctx, row)
					return signalAuth{node: true, householdID: v.Node.HouseholdID, nodeID: id, members: v.HouseholdMemberIDs}, true
				}
			}
		}
	}
	appID, appKey := r.Header.Get("X-Jarvis-App-Id"), r.Header.Get("X-Jarvis-App-Key")
	if appID != "" && appKey != "" {
		app, ok, err := m.Auth.ValidateApp(ctx, appID, appKey)
		if err != nil {
			m.deps.Log.Error("cc: app auth unavailable", "err", err)
			detail(w, http.StatusBadGateway, "Auth service unavailable")
			return signalAuth{}, false
		}
		if ok {
			return signalAuth{appID: app.ID}, true
		}
		detail(w, http.StatusUnauthorized, "Invalid app credentials")
		return signalAuth{}, false
	}
	detail(w, http.StatusUnauthorized, "Authentication required (X-Api-Key or X-Jarvis-App-Id/Key)")
	return signalAuth{}, false
}

// readBodyPy is readBody that also returns the body decoded by pyjson (dict order, Python
// numbers), for the values whose json.dumps bytes matter (facts, idempotency keys).
func readBodyPy(w http.ResponseWriter, r *http.Request) (*body, *pyjson.Object, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBody))
	if err != nil {
		detail(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return nil, nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	b, _, ok := readBody(w, r, false)
	if !ok {
		return nil, nil, false
	}
	pv, err := pyjson.Loads(string(raw))
	po, _ := pv.(*pyjson.Object)
	if err != nil || po == nil {
		po = pyjson.NewObject()
	}
	return b, po, true
}

// maxLen fails a string longer than n characters (pydantic max_length counts code points).
func (b *body) maxLen(name, s string, n int) {
	if len([]rune(s)) > n {
		b.fail(name, fmt.Sprintf("String should have at most %d characters", n))
	}
}

// ttlField reads an optional ttl_seconds: int, 0 < v <= 604800.
func (b *body) ttlField(name string) (int64, bool) {
	v, ok := b.integer(name, false)
	if !ok {
		return 0, false
	}
	if v <= 0 {
		b.fail(name, "Input should be greater than 0")
		return 0, false
	}
	if v > maxSignalTTLSeconds {
		b.fail(name, fmt.Sprintf("Input should be less than or equal to %d", maxSignalTTLSeconds))
		return 0, false
	}
	return v, true
}

// signalIn is the validated SignalPostRequest.
type signalIn struct {
	householdID string
	kind        string
	sourceKey   string
	subject     *string
	summary     *string
	scope       map[string]any
	ttl         int64
	cacheable   bool
	salience    *float64
	sourceAgent string
	command     string
	data        any // pyjson value of body.data, or nil
}

func parseSignalPost(b *body, py *pyjson.Object) (signalIn, bool) {
	var in signalIn
	if s, ok := b.str("household_id", false); ok {
		in.householdID = s
	}
	in.sourceAgent = "external"
	sv, present := b.m["signal"]
	switch sm, isObj := sv.(map[string]any); {
	case !present:
		b.fail("signal", "Field required")
	case !isObj:
		b.fail("signal", "Input should be a valid dictionary or instance of SignalIn")
	default:
		sb := &body{m: sm, loc: b.loc + " -> signal", errs: b.errs}
		if s, ok := sb.str("kind", true); ok {
			sb.maxLen("kind", s, 255)
			in.kind = s
		}
		if s, ok := sb.str("source_key", true); ok {
			sb.maxLen("source_key", s, 512)
			in.sourceKey = s
		}
		if s, ok := sb.optStrPtr("subject"); ok && s != nil {
			sb.maxLen("subject", *s, 255)
			in.subject = s
		}
		if s, ok := sb.optStrPtr("summary"); ok && s != nil {
			sb.maxLen("summary", *s, 2000)
			in.summary = s
		}
		if sc, ok := sb.object("scope", false); ok {
			in.scope = sc
		}
		if v, ok := sb.ttlField("ttl_seconds"); ok {
			in.ttl = v
		}
		if v, ok := sb.boolean("cacheable"); ok {
			in.cacheable = v
		}
		if v, ok := sb.number("salience"); ok {
			in.salience = &v
		}
		if s, ok := sb.str("source_agent", false); ok {
			sb.maxLen("source_agent", s, 255)
			in.sourceAgent = s
		}
	}
	if s, ok := b.str("command", false); ok {
		in.command = s
	}
	if _, ok := b.object("data", false); ok {
		in.data, _ = py.Get("data")
	}
	return in, len(*b.errs) == 0
}

// scopeInt reads scope.user_id leniently (an int or a numeric string).
func scopeInt(v any) *int64 {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return &n
		}
	default:
		if s := fmt.Sprint(x); s != "" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return &n
			}
		}
	}
	return nil
}

func scopeStr(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

func (m *Module) handlePostSignal(w http.ResponseWriter, r *http.Request) {
	a, ok := m.authSignals(w, r)
	if !ok {
		return
	}
	b, py, ok := readBodyPy(w, r)
	if !ok {
		return
	}
	in, valid := parseSignalPost(b, py)
	if !valid {
		b.done(w)
		return
	}
	ctx := r.Context()
	hh := in.householdID
	if hh == "" {
		hh = a.householdID
	}
	if hh == "" {
		detail(w, http.StatusBadRequest, "household_id required (provide in body or use node auth)")
		return
	}
	if a.node && in.householdID != "" && a.householdID != "" && in.householdID != a.householdID {
		detail(w, http.StatusForbidden, "household_id does not match node's household")
		return
	}
	if !m.signalsEnabled(ctx, hh) {
		detail(w, http.StatusConflict, "Signal bus is disabled for this household")
		return
	}
	if !m.rateOK(hh, a.principal()) {
		w.Header().Set("Retry-After", "1")
		detail(w, http.StatusTooManyRequests, "Rate limit exceeded")
		return
	}

	// scope.node_id falls back to the authenticated node. Divergence (security, §2 step 5):
	// a client-supplied node or user outside the household is never stored, so no reaction
	// can target another household's node or user.
	nodeID := ""
	if s := scopeStr(in.scope["node_id"]); s != nil {
		if m.nodeInHousehold(ctx, *s, hh) {
			nodeID = *s
		} else {
			m.deps.Log.Warn("cc: signal scope.node_id outside the household dropped", "household", hh, "node", *s)
		}
	}
	if nodeID == "" && a.node && a.householdID == hh {
		nodeID = a.nodeID
	}
	userID := scopeInt(in.scope["user_id"])
	if userID != nil && !m.isMember(ctx, a, hh, *userID) {
		m.deps.Log.Warn("cc: signal scope.user_id outside the household dropped", "household", hh, "user", *userID)
		userID = nil
	}
	room := scopeStr(in.scope["room"])

	var ttl time.Duration
	if in.ttl > 0 {
		ttl = time.Duration(in.ttl) * time.Second
	}
	id, err := m.saveSignal(ctx, newSignal{HouseholdID: hh, SourceKey: in.sourceKey, Kind: in.kind, Subject: in.subject,
		Summary: in.summary, Facts: in.data, UserID: userID, NodeID: nodeID, Room: room, TTL: ttl,
		Cacheable: in.cacheable, Salience: in.salience, SourceAgent: in.sourceAgent})
	if errors.Is(err, errNotQuantized) {
		detail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}

	proposed := false
	if in.command != "" {
		proposed = m.emitDirectedProposal(ctx, hh, nodeID, in.command, in.data, in.sourceKey, userID)
	}
	m.dispatchSignal(reactionCtx{HouseholdID: hh, NodeID: nodeID, UserID: userID, Kind: in.kind, Facts: factsText(in.data)})

	mode := "open"
	if in.command != "" {
		mode = "directed"
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"signal_id": id, "mode": mode, "proposed": proposed})
}

// factsText is json.dumps(facts or {}).
func factsText(v any) string {
	if v == nil {
		return "{}"
	}
	return pyjson.Dumps(v, true)
}

// isMember reports whether userID belongs to hh: from the node's validated member list, or the
// auth module for an app caller.
func (m *Module) isMember(ctx context.Context, a signalAuth, hh string, userID int64) bool {
	if a.node && a.householdID == hh && a.members != nil {
		return containsID(a.members, userID)
	}
	_, member, err := m.Auth.HouseholdRole(ctx, userID, hh)
	return err == nil && member
}

func (m *Module) nodeInHousehold(ctx context.Context, nodeID, hh string) bool {
	var one int
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT 1 FROM cc_nodes WHERE node_id = ? AND household_id = ?`, nodeID, hh).Scan(&one)
	return err == nil
}

// --- POST /mobile/presence (api/mobile_presence.py) ---

func (m *Module) handleMobilePresence(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	hh, _ := b.str("household_id", true)
	b.maxLen("household_id", hh, 255)
	state := "home"
	if s, ok := b.str("state", false); ok {
		state = s
	}
	room, _ := b.optStrPtr("room")
	if room != nil {
		b.maxLen("room", *room, 255)
	}
	ttl, _ := b.ttlField("ttl_seconds")
	name, _ := b.optStrPtr("name")
	if name != nil {
		b.maxLen("name", *name, 255)
	}
	if !b.done(w) {
		return
	}
	if state != "home" && state != "away" {
		detail(w, http.StatusUnprocessableEntity, "state must be one of ['away', 'home']")
		return
	}
	ctx := r.Context()
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	if !m.signalsEnabled(ctx, hh) {
		detail(w, http.StatusConflict, "Signal bus is disabled for this household")
		return
	}
	if !m.rateOK(hh, "user:"+strconv.FormatInt(u.ID, 10)) {
		detail(w, http.StatusTooManyRequests, "Too many presence updates")
		return
	}
	if name != nil {
		if t := strings.TrimSpace(*name); t != "" {
			name = &t
		} else {
			name = nil
		}
	}
	life := mobilePresenceTTL
	if ttl > 0 {
		life = time.Duration(ttl) * time.Second
	}
	id, kind, err := m.recordPresence(ctx, hh, u.ID, state, "mobile", "", room, name, life, nil)
	if err != nil {
		m.internalError(w, err)
		return
	}
	facts := pyjson.NewObject()
	facts.Set("user_id", big.NewInt(u.ID))
	facts.Set("state", state)
	if room != nil {
		facts.Set("room", *room)
	} else {
		facts.Set("room", nil)
	}
	uid := u.ID
	m.dispatchSignal(reactionCtx{HouseholdID: hh, UserID: &uid, Kind: kind, Facts: pyjson.Dumps(facts, true)})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "signal_id": id, "kind": kind})
}

// --- signal automations (api/mobile_signal_automations.py, signal_automation_store.py) ---

// signalKind is one user-authorable catalog entry (D40 Q8: a static catalog; the stale
// appt.detected listener is cut).
type signalKind struct {
	kind, label, description string
	facts                    [][2]string // ordered (key, meaning)
	example, source          string
}

var signalCatalog = []signalKind{
	{kind: "presence.left", label: "I leave home", description: "Your phone leaves the home area — you've left.",
		facts:   [][2]string{{"user_id", "Who left"}, {"state", "Always 'away'"}, {"room", "Last known room, if any"}},
		example: "Lock the front door", source: "mobile"},
	{kind: "presence.seen", label: "I arrive home", description: "Your phone enters the home area — you're home.",
		facts:   [][2]string{{"user_id", "Who arrived"}, {"state", "Always 'home'"}, {"room", "Room, if known"}},
		example: "Turn on the entryway lights", source: "mobile"},
	{kind: "appt.upcoming", label: "An appointment is coming up",
		description: "A calendar event with a location enters its leave-by window.",
		facts: [][2]string{{"title", "Event title"}, {"location", "Where it is"}, {"start_iso", "Start time (ISO-8601)"},
			{"start_display", "Start time (friendly)"}, {"event_id", "Calendar event id"}},
		example: "Remind me 30 minutes before I have to leave", source: "calendar_alerts"},
}

func catalogKind(kind string) (signalKind, bool) {
	for _, k := range signalCatalog {
		if k.kind == kind {
			return k, true
		}
	}
	return signalKind{}, false
}

// automationRule is one stored rule.
type automationRule struct {
	Instruction string
	Enabled     bool
	Delivery    string
}

func normalizeDelivery(v any) string {
	if s, ok := v.(string); ok && (s == "automatic" || s == "notification") {
		return s
	}
	return "notification"
}

// loadRules is load_rules: the household's map, decoded in order; garbage reads as none.
func (m *Module) loadRules(ctx context.Context, hh string) *pyjson.Object {
	raw := m.settings.String(ctx, settingSignalAutomations, settings.Scope{HouseholdID: hh})
	if raw == "" {
		return pyjson.NewObject()
	}
	v, err := pyjson.Loads(raw)
	o, ok := v.(*pyjson.Object)
	if err != nil || !ok {
		m.deps.Log.Warn("cc: unparseable signals.automations", "household", hh)
		return pyjson.NewObject()
	}
	return o
}

// enabledRule is get_enabled_rule: nil without an enabled, non-blank rule.
func (m *Module) enabledRule(ctx context.Context, hh, kind string) *automationRule {
	v, _ := m.loadRules(ctx, hh).Get(kind)
	o, ok := v.(*pyjson.Object)
	if !ok {
		return nil
	}
	ins, _ := o.Get("instruction")
	s, _ := ins.(string)
	s = strings.TrimSpace(s)
	en, _ := o.Get("enabled")
	if s == "" || !pyTruthyValue(en) {
		return nil
	}
	d, _ := o.Get("delivery")
	return &automationRule{Instruction: s, Enabled: true, Delivery: normalizeDelivery(d)}
}

// pyTruthyValue is Python truthiness for a decoded JSON value.
func pyTruthyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case *big.Int:
		return x.Sign() != 0
	case []any:
		return len(x) > 0
	case *pyjson.Object:
		return x.Len() > 0
	}
	return true
}

func (m *Module) observedKinds(ctx context.Context, hh string) map[string]bool {
	out := map[string]bool{}
	sigs, err := m.liveSignals(ctx, hh)
	if err != nil {
		m.deps.Log.Warn("cc: observed-kinds query failed", "household", hh, "err", err)
		return out
	}
	for _, s := range sigs {
		out[s.kind] = true
	}
	return out
}

func (m *Module) handleListSignalAutomations(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	rules := m.loadRules(ctx, hh)
	observed := m.observedKinds(ctx, hh)
	items := make([]any, 0, len(signalCatalog))
	for _, k := range signalCatalog {
		facts := pyjson.NewObject()
		for _, f := range k.facts {
			facts.Set(f[0], f[1])
		}
		rv, _ := rules.Get(k.kind)
		rule, _ := rv.(*pyjson.Object)
		instruction, enabled, delivery := any(""), false, "notification"
		if rule != nil {
			if v, ok := rule.Get("instruction"); ok {
				instruction = v
			}
			v, _ := rule.Get("enabled")
			enabled = pyTruthyValue(v)
			d, _ := rule.Get("delivery")
			delivery = normalizeDelivery(d)
		}
		items = append(items, map[string]any{
			"kind": k.kind, "label": k.label, "description": k.description, "facts": facts, "example": k.example,
			"source": k.source, "observed": observed[k.kind], "instruction": pyjson.ToJSON(instruction),
			"enabled": enabled, "delivery": delivery,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"household_id": hh, "automations": items})
}

func (m *Module) handlePutSignalAutomation(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	instruction := ""
	if s, ok := b.str("instruction", false); ok {
		b.maxLen("instruction", s, maxInstructionLength)
		instruction = s
	}
	enabled := true
	if v, ok := b.boolean("enabled"); ok {
		enabled = v
	}
	delivery := "notification"
	if s, ok := b.str("delivery", false); ok {
		if s != "automatic" && s != "notification" {
			b.fail("delivery", "Input should be 'automatic' or 'notification'")
		}
		delivery = s
	}
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh, kind := r.PathValue("household_id"), r.PathValue("kind")
	if _, ok := catalogKind(kind); !ok {
		detail(w, http.StatusNotFound, "Unknown or non-authorable signal kind: "+kind)
		return
	}
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleAdmin); err != nil {
		m.writeErr(w, err)
		return
	}
	instruction = strings.TrimSpace(instruction)
	rules := m.loadRules(ctx, hh)
	next := pyjson.NewObject()
	for _, k := range rules.Keys() {
		if k == kind && instruction == "" {
			continue // an empty instruction clears the rule
		}
		v, _ := rules.Get(k)
		next.Set(k, v)
	}
	if instruction != "" {
		rule := pyjson.NewObject()
		rule.Set("instruction", instruction)
		rule.Set("enabled", enabled)
		rule.Set("delivery", delivery)
		next.Set(kind, rule)
	}
	if err := m.settings.Set(ctx, settingSignalAutomations, pyjson.Dumps(next, true), settings.Scope{HouseholdID: hh}); err != nil {
		m.deps.Log.Error("cc: save automation failed", "household", hh, "err", err)
		detail(w, http.StatusInternalServerError, "Failed to save automation")
		return
	}
	m.deps.Log.Info("cc: household set automation", "household", hh, "kind", kind, "enabled", enabled,
		"delivery", delivery, "chars", len([]rune(instruction)))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true, "kind": kind, "instruction": instruction, "enabled": enabled && instruction != "",
		"delivery": delivery, "cleared": instruction == "",
	})
}

// --- account deletion (D20) ---

// purgeSignals hard-deletes the user's signals, suppressions and reaction claims, and
// de-identifies the attention journal rows that named them (deliveries are kept).
func (m *Module) purgeSignals(ctx context.Context, tx *sql.Tx, userID int64) error {
	uid := strconv.FormatInt(userID, 10)
	for _, s := range []struct {
		q    string
		args []any
	}{
		{`DELETE FROM cc_signals WHERE user_id = ? OR source_key = ?`, []any{userID, "presence:" + uid}},
		{`DELETE FROM cc_proposal_suppressions WHERE user_id = ?`, []any{userID}},
		{`DELETE FROM cc_reaction_claims WHERE claim_key LIKE ?`, []any{"automation:" + uid + ":%"}},
		{`UPDATE cc_attention_events SET target_user_id = NULL, payload_json = NULL WHERE target_user_id = ?`, []any{userID}},
	} {
		if _, err := tx.ExecContext(ctx, s.q, s.args...); err != nil {
			return err
		}
	}
	return nil
}
