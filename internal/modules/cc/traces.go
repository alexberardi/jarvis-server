package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Request traces (doc 05 §3.10): the voice pipeline (5b) writes them through RecordTrace; the
// admin trace visualizer reads them. Retention is a fixed 7 days (D11).

const traceRetention = 7 * 24 * time.Hour

// Trace is one request_traces row.
type Trace struct {
	ConversationID   string
	RequestType      string // stt | warmup | voice_command | ...
	Source           string // node | mobile
	NodeID           string
	HouseholdID      string
	UserID           int64 // 0 = unknown speaker
	UserCommand      string
	AssistantMessage string
	Status           string // ok | error
	ErrorMessage     string
	TotalDurationMS  float64
	Spans            any // JSON-encodable span list
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// RecordTrace stores a trace and returns its id. A trace with no source is not stored
// (legacy end_request skipped source "unknown").
func (m *Module) RecordTrace(ctx context.Context, t Trace) (string, error) {
	if t.Source == "" || t.Source == "unknown" {
		return "", nil
	}
	spans, err := json.Marshal(t.Spans)
	if err != nil || t.Spans == nil {
		spans = []byte("[]")
	}
	status := t.Status
	if status == "" {
		status = "ok"
	}
	var user any
	if t.UserID != 0 {
		user = t.UserID
	}
	node := nullIfEmpty(t.NodeID)
	if t.NodeID != "" {
		// The FK would reject a trace for a node that's gone; keep the trace without it.
		if _, err := m.nodeByID(ctx, t.NodeID); err != nil {
			node = nil
		}
	}
	id := uuid4()
	_, err = m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_request_traces (id, conversation_id, request_type, source, node_id,
		household_id, user_id, user_command, assistant_message, status, error_message, total_duration_ms, spans_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, t.ConversationID, t.RequestType, t.Source, node,
		nullIfEmpty(t.HouseholdID), user, nullIfEmpty(t.UserCommand), nullIfEmpty(t.AssistantMessage), status,
		nullIfEmpty(t.ErrorMessage), t.TotalDurationMS, string(spans), dbTime(m.now()))
	return id, err
}

type traceRow struct {
	id, conversationID, requestType, source string
	nodeID, householdID, userCommand        sql.NullString
	assistantMessage, errorMessage          sql.NullString
	status                                  string
	totalMS                                 float64
	spans                                   string
	createdAt                               string
}

const traceCols = `id, conversation_id, request_type, source, node_id, household_id, user_command, assistant_message,
	status, error_message, total_duration_ms, spans_json, created_at`

func scanTrace(s scanner) (*traceRow, error) {
	var t traceRow
	err := s.Scan(&t.id, &t.conversationID, &t.requestType, &t.source, &t.nodeID, &t.householdID, &t.userCommand,
		&t.assistantMessage, &t.status, &t.errorMessage, &t.totalMS, &t.spans, &t.createdAt)
	return &t, err
}

func (t *traceRow) spanList() []any {
	var spans []any
	if json.Unmarshal([]byte(t.spans), &spans) != nil || spans == nil {
		return []any{}
	}
	return spans
}

func (t *traceRow) base() map[string]any {
	return map[string]any{
		"id": t.id, "conversation_id": t.conversationID, "request_type": t.requestType, "source": t.source,
		"node_id": nullable(t.nodeID), "household_id": nullable(t.householdID), "user_command": nullable(t.userCommand),
		"assistant_message": nullable(t.assistantMessage), "status": t.status, "total_duration_ms": t.totalMS,
		"created_at": pyNaive(parseTS(t.createdAt)),
	}
}

// TraceFilter selects traces for ListTraces. Empty strings don't filter.
type TraceFilter struct {
	Limit, Offset                       int
	Status, Source, HouseholdID, NodeID string
}

// ParseTraceFilter reads the trace list's query (limit 1–200, default 50; offset ≥ 0; the four
// equality filters). problem is the legacy validation detail ("query -> limit: …"), or "".
func ParseTraceFilter(q url.Values) (f TraceFilter, problem string) {
	f.Limit = 50
	for _, p := range []struct {
		name     string
		dst      *int
		min, max int
	}{{"limit", &f.Limit, 1, 200}, {"offset", &f.Offset, 0, 1 << 30}} {
		if v := q.Get(p.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return f, "query -> " + p.name + ": Input should be a valid integer, unable to parse string as an integer"
			}
			if n < p.min {
				return f, "query -> " + p.name + ": Input should be greater than or equal to " + strconv.Itoa(p.min)
			}
			if n > p.max {
				return f, "query -> " + p.name + ": Input should be less than or equal to " + strconv.Itoa(p.max)
			}
			*p.dst = n
		}
	}
	f.Status, f.Source, f.HouseholdID, f.NodeID = q.Get("status"), q.Get("source"), q.Get("household_id"), q.Get("node_id")
	return f, ""
}

// ListTraces returns one page of traces, newest first, in the admin list shape (each with
// span_count, without spans), and the total matching f. The admin gateway calls it in process;
// /api/v0/admin/traces serves it to the legacy admin key.
func (m *Module) ListTraces(ctx context.Context, f TraceFilter) ([]map[string]any, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	where, args := " WHERE 1=1", []any{}
	for _, c := range []struct{ col, v string }{
		{"status", f.Status}, {"source", f.Source}, {"household_id", f.HouseholdID}, {"node_id", f.NodeID},
	} {
		if c.v != "" {
			where += " AND " + c.col + " = ?"
			args = append(args, c.v)
		}
	}
	var total int
	if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_request_traces`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+traceCols+` FROM cc_request_traces`+where+
		` ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	traces := []map[string]any{}
	for rows.Next() {
		t, err := scanTrace(rows)
		if err != nil {
			return nil, 0, err
		}
		out := t.base()
		out["span_count"] = len(t.spanList())
		traces = append(traces, out)
	}
	return traces, total, rows.Err()
}

// GetTrace returns one trace with its spans and error_message; found is false for an unknown id.
func (m *Module) GetTrace(ctx context.Context, id string) (trace map[string]any, found bool, err error) {
	t, err := scanTrace(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+traceCols+` FROM cc_request_traces WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	out := t.base()
	out["error_message"] = nullable(t.errorMessage)
	out["spans"] = t.spanList()
	return out, true, nil
}

func (m *Module) handleListTraces(w http.ResponseWriter, r *http.Request) {
	f, problem := ParseTraceFilter(r.URL.Query())
	if problem != "" {
		validationError(w, problem)
		return
	}
	traces, total, err := m.ListTraces(r.Context(), f)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"traces": traces, "total": total})
}

func (m *Module) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	t, found, err := m.GetTrace(r.Context(), r.PathValue("trace_id"))
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !found {
		detail(w, http.StatusNotFound, "Trace not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

// runCleanup is the hourly loop: provisioning tokens expired or consumed over 24 h ago,
// settings requests (and their snapshots) older than 24 h (D40 05.Q11), traces past 7 days.
func (m *Module) runCleanup(ctx context.Context, _ queue.Job) ([]byte, error) {
	return nil, m.cleanup(ctx)
}

func (m *Module) cleanup(ctx context.Context) error {
	now := m.now()
	day := dbTime(now.Add(-24 * time.Hour))
	for _, s := range []struct {
		q    string
		args []any
	}{
		{`DELETE FROM cc_provisioning_tokens WHERE expires_at < ? OR (consumed_at IS NOT NULL AND consumed_at < ?)`, []any{day, day}},
		{`DELETE FROM cc_settings_requests WHERE created_at < ?`, []any{day}},
		{`DELETE FROM cc_request_traces WHERE created_at < ?`, []any{dbTime(now.Add(-traceRetention))}},
	} {
		if _, err := m.deps.DB.Write.ExecContext(ctx, s.q, s.args...); err != nil {
			return err
		}
	}
	// 5c subsystems' retention rides the same hourly trigger (D27).
	for _, sweep := range []func(context.Context, time.Time) error{m.cleanupPackages, m.cleanupTestInstalls, m.cleanupSmartHome} {
		if err := sweep(ctx, now); err != nil {
			return err
		}
	}
	return nil
}

// --- spans (latency_logger.RequestTiming, to_spans, to_trace_summary) ---

// Span names match legacy's so a legacy and a jarvisd trace of the same turn line up
// (docs/cc/01-voice-pipeline.md "Trace spans" lists them). jarvisd-only: warmup_inference,
// speaker_resolve and server_tool_<name>.

type traceSpan struct {
	name, service, status string
	start, end            time.Duration
	meta                  map[string]any
}

// reqTrace collects one request's spans. It rides the request context (withTrace) so the
// pipeline adds spans without threading it through every call; a nil *reqTrace records
// nothing.
type reqTrace struct {
	t0     time.Time
	status string
	errMsg string // set by fail: the request answered but its turn failed
	mu     sync.Mutex
	list   []traceSpan
}

// fail marks the request as failed although its route answered normally (a turn whose LLM
// call failed still returns 202 stop_reason "error" to the node).
func (t *reqTrace) fail(msg string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.status, t.errMsg = "error", msg
	t.mu.Unlock()
}

// failure is what fail recorded ("" when the request didn't fail).
func (t *reqTrace) failure() (string, bool) {
	if t == nil {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.errMsg, t.status == "error"
}

func newReqTrace() *reqTrace { return &reqTrace{t0: time.Now(), status: "ok"} }

type traceCtxKey struct{}

func withTrace(ctx context.Context, t *reqTrace) context.Context {
	return context.WithValue(ctx, traceCtxKey{}, t)
}

// traceFrom is the context's trace, or nil.
func traceFrom(ctx context.Context) *reqTrace {
	t, _ := ctx.Value(traceCtxKey{}).(*reqTrace)
	return t
}

// since is the offset from the request start (a span boundary).
func (t *reqTrace) since() time.Duration {
	if t == nil {
		return 0
	}
	return time.Since(t.t0)
}

// span records a finished span between two offsets (record_span); a non-nil error marks it
// "error".
func (t *reqTrace) span(name, service string, start, end time.Duration, err error, meta map[string]any) {
	if t == nil {
		return
	}
	s := traceSpan{name: name, service: service, status: "ok", start: start, end: end, meta: meta}
	if err != nil {
		s.status = "error"
	}
	t.mu.Lock()
	t.list = append(t.list, s)
	t.mu.Unlock()
}

// measure opens a span; the returned func closes it (measure()).
func (t *reqTrace) measure(name, service string, meta map[string]any) func(error) {
	if t == nil {
		return func(error) {}
	}
	start := t.since()
	return func(err error) { t.span(name, service, start, t.since(), err, meta) }
}

// checkpoint records a zero-length span now.
func (t *reqTrace) checkpoint(name string) {
	now := t.since()
	t.span(name, "cc", now, now, nil, nil)
}

func ms(d time.Duration) float64 { return math.Round(float64(d.Microseconds())/100) / 10 }

func (t *reqTrace) totalMS() float64 { return ms(time.Since(t.t0)) }

func (t *reqTrace) sorted() []traceSpan {
	t.mu.Lock()
	out := append([]traceSpan(nil), t.list...)
	t.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

// spans is to_spans (stored on the request trace). Nil-safe.
func (t *reqTrace) spans() []any {
	if t == nil {
		return nil
	}
	var out []any
	for _, s := range t.sorted() {
		meta := s.meta
		if meta == nil {
			meta = map[string]any{}
		}
		out = append(out, map[string]any{"name": s.name, "service": s.service, "start_ms": ms(s.start),
			"end_ms": ms(s.end), "duration_ms": ms(s.end - s.start), "status": s.status, "metadata": meta})
	}
	return out
}

// summary is to_trace_summary: leaf spans (no other non-zero span inside them), merged into
// consecutive same-service hops.
func (t *reqTrace) summary() *pyjson.Object {
	all := t.sorted()
	var nonzero []traceSpan
	for _, s := range all {
		if ms(s.end-s.start) > 0 {
			nonzero = append(nonzero, s)
		}
	}
	type hop struct {
		service, status string
		ms              float64
		steps           []any
	}
	var hops []*hop
	for i, s := range nonzero {
		parent := false
		for j, o := range nonzero {
			if i != j && s.start <= o.start && s.end >= o.end {
				parent = true
				break
			}
		}
		if parent {
			continue
		}
		d := ms(s.end - s.start)
		if n := len(hops); n > 0 && hops[n-1].service == s.service {
			h := hops[n-1]
			h.ms = math.Round((h.ms+d)*10) / 10
			h.steps = append(h.steps, s.name)
			if s.status == "error" {
				h.status = "error"
			}
			continue
		}
		hops = append(hops, &hop{service: s.service, status: s.status, ms: d, steps: []any{s.name}})
	}
	out := make([]any, 0, len(hops))
	for _, h := range hops {
		out = append(out, servertools.Obj("service", h.service, "duration_ms", h.ms, "status", h.status, "steps", h.steps))
	}
	return servertools.Obj("total_duration_ms", t.totalMS(), "span_count", len(all), "status", t.status, "service_hops", out)
}
