package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

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

func (m *Module) handleListTraces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := 50, 0
	for _, p := range []struct {
		name     string
		dst      *int
		min, max int
	}{{"limit", &limit, 1, 200}, {"offset", &offset, 0, 1 << 30}} {
		if v := q.Get(p.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				validationError(w, "query -> "+p.name+": Input should be a valid integer, unable to parse string as an integer")
				return
			}
			if n < p.min {
				validationError(w, "query -> "+p.name+": Input should be greater than or equal to "+strconv.Itoa(p.min))
				return
			}
			if n > p.max {
				validationError(w, "query -> "+p.name+": Input should be less than or equal to "+strconv.Itoa(p.max))
				return
			}
			*p.dst = n
		}
	}
	where, args := " WHERE 1=1", []any{}
	for _, f := range []string{"status", "source", "household_id", "node_id"} {
		if v := q.Get(f); v != "" {
			where += " AND " + f + " = ?"
			args = append(args, v)
		}
	}
	ctx := r.Context()
	var total int
	if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_request_traces`+where, args...).Scan(&total); err != nil {
		m.internalError(w, err)
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+traceCols+` FROM cc_request_traces`+where+
		` ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	traces := []map[string]any{}
	for rows.Next() {
		t, err := scanTrace(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out := t.base()
		out["span_count"] = len(t.spanList())
		traces = append(traces, out)
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"traces": traces, "total": total})
}

func (m *Module) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	t, err := scanTrace(m.deps.DB.Read.QueryRowContext(r.Context(), `SELECT `+traceCols+` FROM cc_request_traces WHERE id = ?`,
		r.PathValue("trace_id")))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Trace not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	out := t.base()
	out["error_message"] = nullable(t.errorMessage)
	out["spans"] = t.spanList()
	httpx.WriteJSON(w, http.StatusOK, out)
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
	return nil
}
