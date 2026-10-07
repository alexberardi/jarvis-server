package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	logsmod "github.com/alexberardi/jarvis-server/internal/modules/logs"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The Logs page (AD9, inventory §6.2 #7): a filtered, paged query over the log store and a
// live tail, both superuser-only, calling the logs module in process.

// LogStore reads the log store (the logs module).
type LogStore interface {
	Query(ctx context.Context, f logsmod.Filter) ([]logsmod.Entry, string, error)
	After(ctx context.Context, f logsmod.Filter, afterID int64, limit int) ([]logsmod.Entry, error)
	LatestID(ctx context.Context) (int64, error)
	Sources(ctx context.Context, since time.Time) (services, nodes []string, err error)
}

const (
	// defaultLogPoll is how often the tail looks for new entries.
	defaultLogPoll = time.Second
	// logHeartbeat keeps an idle tail's connection (and any proxy) open.
	logHeartbeat = 15 * time.Second
	// tailBatch caps the entries sent per poll; the next poll continues.
	tailBatch = 200
)

// parseLogFilter reads the shared query parameters: service, node_id, level (comma list),
// min_level, since/until (RFC 3339), q (text), limit, cursor. Bad values are 422s.
func parseLogFilter(r *http.Request, paging bool) (logsmod.Filter, []httpx.FieldError) {
	q := r.URL.Query()
	f := logsmod.Filter{
		Service: strings.TrimSpace(q.Get("service")),
		NodeID:  strings.TrimSpace(q.Get("node_id")),
		Text:    q.Get("q"),
	}
	var errs []httpx.FieldError
	bad := func(field, typ, msg string, input any) {
		errs = append(errs, httpx.FieldError{Type: typ, Loc: []any{"query", field}, Msg: msg, Input: input})
	}
	if s := q.Get("level"); s != "" {
		for _, l := range strings.Split(s, ",") {
			l = strings.ToUpper(strings.TrimSpace(l))
			if !logsmod.ValidLevel(l) {
				bad("level", "enum", "Input should be one of "+strings.Join(logsmod.Levels, ", "), s)
				break
			}
			f.Levels = append(f.Levels, l)
		}
	}
	if s := q.Get("min_level"); s != "" {
		l := strings.ToUpper(strings.TrimSpace(s))
		if !logsmod.ValidLevel(l) {
			bad("min_level", "enum", "Input should be one of "+strings.Join(logsmod.Levels, ", "), s)
		}
		f.MinLevel = l
	}
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if s := q.Get(p.name); s != "" {
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				bad(p.name, "datetime_parsing", "Input should be an RFC 3339 datetime", s)
				continue
			}
			*p.dst = t
		}
	}
	if paging {
		if s := q.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > logsmod.MaxLimit {
				bad("limit", "int_range", fmt.Sprintf("Input should be an integer from 1 to %d", logsmod.MaxLimit), s)
			}
			f.Limit = n
		}
		f.Cursor = q.Get("cursor")
	}
	return f, errs
}

// handleLogs is GET /api/logs: {logs: [entry], next_cursor}, newest first. Pass next_cursor
// back as ?cursor= for the next (older) page; null means this was the last.
func (m *Module) handleLogs(w http.ResponseWriter, r *http.Request) {
	if m.Logs == nil {
		unavailable(w, "logs")
		return
	}
	f, errs := parseLogFilter(r, true)
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	entries, next, err := m.Logs.Query(r.Context(), f)
	if errors.Is(err, logsmod.ErrBadCursor) {
		httpx.ValidationError(w, httpx.FieldError{Type: "value_error", Loc: []any{"query", "cursor"}, Msg: "Invalid cursor", Input: f.Cursor})
		return
	}
	if err != nil {
		m.deps.Log.Error("admin: log query failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	var nextCursor any
	if next != "" {
		nextCursor = next
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"logs": entries, "next_cursor": nextCursor})
}

// handleLogSources is GET /api/logs/sources[?since=]: the services and nodes that logged in
// the window (default the last 24 hours), for the filter dropdowns.
func (m *Module) handleLogSources(w http.ResponseWriter, r *http.Request) {
	if m.Logs == nil {
		unavailable(w, "logs")
		return
	}
	since := time.Now().Add(-24 * time.Hour)
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			httpx.ValidationError(w, httpx.FieldError{Type: "datetime_parsing", Loc: []any{"query", "since"}, Msg: "Input should be an RFC 3339 datetime", Input: s})
			return
		}
		since = t
	}
	services, nodes, err := m.Logs.Sources(r.Context(), since)
	if err != nil {
		m.deps.Log.Error("admin: log sources failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"services": services, "nodes": nodes, "since": since.UTC().Format(time.RFC3339)})
}

// handleLogStream is GET /api/logs/stream: a live tail as Server-Sent Events, read with
// fetch() (an EventSource can't send the Authorization header). Each entry is one event,
// `id: <entry id>` and `data: <entry JSON>`; `: ping` comments keep it open. It starts after
// the newest entry, or after ?after= / Last-Event-ID to resume without gaps. Filters as
// /api/logs (since/until/limit/cursor ignored).
func (m *Module) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if m.Logs == nil {
		unavailable(w, "logs")
		return
	}
	f, errs := parseLogFilter(r, false)
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	ctx := r.Context()
	resume := r.URL.Query().Get("after")
	if resume == "" {
		resume = r.Header.Get("Last-Event-ID")
	}
	var last int64
	if resume != "" {
		n, err := strconv.ParseInt(resume, 10, 64)
		if err != nil || n < 0 {
			httpx.ValidationError(w, httpx.FieldError{Type: "int_parsing", Loc: []any{"query", "after"}, Msg: "Input should be a log entry id", Input: resume})
			return
		}
		last = n
	} else {
		// Taken before the headers go out, so nothing logged after the client connects is missed.
		n, err := m.Logs.LatestID(ctx)
		if err != nil {
			m.deps.Log.Error("admin: log tail failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		last = n
	}
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// The first bytes resolve the client's fetch() promptly.
	fmt.Fprintf(w, ": tail after %d\n\n", last)
	if rc.Flush() != nil {
		return
	}
	poll := m.LogPoll
	if poll <= 0 {
		poll = defaultLogPoll
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	quiet := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		// The store has one writer, so every id up to the newest is committed: a short batch
		// means every entry up to high was considered, and the filter's misses are skipped
		// for good instead of rescanned each poll.
		high, err := m.Logs.LatestID(ctx)
		var entries []logsmod.Entry
		if err == nil {
			entries, err = m.Logs.After(ctx, f, last, tailBatch)
		}
		if err != nil {
			if ctx.Err() == nil {
				m.deps.Log.Error("admin: log tail failed", "err", err)
			}
			continue
		}
		for _, e := range entries {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.ID, b)
			last = e.ID
		}
		if len(entries) < tailBatch {
			last = max(last, high)
		}
		switch {
		case len(entries) > 0:
			quiet = time.Now()
		case time.Since(quiet) >= logHeartbeat:
			fmt.Fprint(w, ": ping\n\n")
			quiet = time.Now()
		default:
			continue
		}
		if rc.Flush() != nil {
			return
		}
	}
}
