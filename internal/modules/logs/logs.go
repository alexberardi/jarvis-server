// Package logs is the jarvisd logs module (jarvis-logs, legacy port 7702). It replaces
// Loki + Grafana with one SQLite table and keeps the HTTP API: services ship batches with
// app credentials, nodes with X-Node-Id/X-Node-Key, and the admin UI queries and tails.
//
// jarvisd's own log records also land here, through Sink (see internal/platform/logging).
package logs

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/logging"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the module's goose migrations, rooted at the migrations directory.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // unreachable: the directory is embedded at build time
	}
	return sub
}

// ServiceID is the name nodes must be granted access to (node service-access rows).
const ServiceID = "jarvis-logs"

// Settings. The legacy definitions (max_batch_size, query_limit, server.port,
// auth.cache_ttl_seconds) had no reader and are dropped (docs/cc D11). Retention was enforced
// by Loki's config (168h), so the default is 7 days, what users actually had.
var Definitions = []settings.Definition{
	{Key: "logs.retention_days", Category: "logs", Type: settings.Int, Default: int64(7),
		Description: "Number of days to retain logs"},
}

const purgeJob = "logs.purge"

// Module is the logs module.
type Module struct {
	Auth authn.Authority
	// Superuser guards settings writes; app credentials or a superuser JWT may read.
	SettingsRead, SettingsWrite settings.Guard

	deps     module.Deps
	settings *settings.Service
	now      func() time.Time
	// ready is set once Register has run (migrations are done by then): jarvisd's own log
	// shipper starts before the modules and must not write into a module that isn't wired.
	ready atomic.Bool
}

func (m *Module) Name() string      { return "logs" }
func (m *Module) Listener() string  { return pconfig.ListenerLogs }
func (m *Module) Migrations() fs.FS { return Migrations() }

// Settings is the module's settings service (valid after Register), for the admin aggregator.
func (m *Module) Settings() *settings.Service { return m.settings }

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	if m.now == nil {
		m.now = time.Now
	}
	svc, err := settings.New(deps.DB, "logs", Definitions, deps.Log)
	if err != nil {
		panic(err) // static definitions
	}
	m.settings = svc
	if m.SettingsRead != nil && m.SettingsWrite != nil {
		svc.Mount(mux, m.SettingsRead, m.SettingsWrite)
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		// The legacy body reported Loki; the SQLite store is always available, and callers
		// only read status (LEGACY shape kept).
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status":    "healthy",
			"timestamp": pyNaive(m.now()),
			"services":  map[string]any{"loki": "available"},
		})
	})
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "pong"})
	})

	mux.HandleFunc("POST /api/v0/logs", m.app(m.ingestOne))
	mux.HandleFunc("POST /api/v0/logs/batch", m.app(m.ingestBatch))
	mux.HandleFunc("GET /api/v0/logs", m.app(m.query))
	mux.HandleFunc("GET /api/v0/logs/stream", m.app(m.stream))
	mux.HandleFunc("GET /api/v0/services", m.app(m.services))
	mux.HandleFunc("POST /api/v0/node/logs", m.node(m.ingestOne))
	mux.HandleFunc("POST /api/v0/node/logs/batch", m.node(m.ingestBatch))

	if deps.Queue != nil {
		deps.Queue.Register(purgeJob, queue.Handler{Run: m.purge})
	}
	m.ready.Store(true)
}

// Start migrates the settings table and schedules the daily retention purge.
func (m *Module) Start(ctx context.Context) error {
	if err := m.settings.Migrate(ctx); err != nil {
		return err
	}
	if m.deps.Scheduler == nil {
		return nil
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: purgeJob, Kind: scheduler.KindInterval, JobType: purgeJob,
		Spec: scheduler.Spec{Every: 24 * time.Hour, StartNow: true},
	})
}

func (m *Module) purge(ctx context.Context, _ queue.Job) ([]byte, error) {
	days := m.settings.Int(ctx, "logs.retention_days", settings.Scope{})
	if days <= 0 {
		return nil, nil
	}
	cutoff := m.now().Add(-time.Duration(days) * 24 * time.Hour).UnixNano()
	res, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM logs_entries WHERE ts < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	return []byte(strconv.FormatInt(n, 10)), nil
}

// --- auth ---

type ctxKey int

const nodeCtx ctxKey = 0

func (m *Module) app(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, key, ok := authn.AppCreds(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "Missing app credentials")
			return
		}
		_, valid, err := m.Auth.ValidateApp(r.Context(), id, key)
		if err != nil {
			m.deps.Log.Error("logs: app validation failed", "err", err)
			httpx.Error(w, http.StatusBadGateway, "Auth service unavailable: "+err.Error())
			return
		}
		if !valid {
			httpx.Error(w, http.StatusUnauthorized, "Invalid app credentials")
			return
		}
		h(w, r)
	}
}

// node authenticates X-Node-Id / X-Node-Key. Failures are 403 with the auth reason; every
// other service uses 401 for bad node credentials, but nodes and the contract depend on this
// (LEGACY-BUG kept).
func (m *Module) node(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, key := r.Header.Get("X-Node-Id"), r.Header.Get("X-Node-Key")
		if id == "" || key == "" {
			httpx.Error(w, http.StatusUnauthorized, "Missing node credentials")
			return
		}
		v, err := m.Auth.ValidateNode(r.Context(), id, key, ServiceID)
		if err != nil {
			m.deps.Log.Error("logs: node validation failed", "err", err)
			httpx.Error(w, http.StatusForbidden, "Auth service error: "+err.Error())
			return
		}
		if !v.Valid {
			reason := v.Reason
			if reason == "" {
				reason = "Invalid node credentials"
			}
			httpx.Error(w, http.StatusForbidden, reason)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), nodeCtx, v.Node.ID)))
	}
}

// --- ingest ---

var levels = map[string]bool{"DEBUG": true, "INFO": true, "WARNING": true, "ERROR": true, "CRITICAL": true}

type entryIn struct {
	Timestamp *string        `json:"timestamp"`
	Service   *string        `json:"service"`
	Level     *string        `json:"level"`
	Message   *string        `json:"message"`
	Context   map[string]any `json:"context"`
}

type entry struct {
	ts      time.Time
	service string
	level   string
	message string
	context map[string]any
}

// validate mirrors the LogEntry pydantic model; loc is the path prefix ("body" or
// "body", "logs", i).
func (m *Module) validate(in entryIn, loc ...any) (entry, []httpx.FieldError) {
	var errs []httpx.FieldError
	field := func(name, typ, msg string, input any) {
		errs = append(errs, httpx.FieldError{Type: typ, Loc: append(append([]any{}, loc...), name), Msg: msg, Input: input})
	}
	e := entry{ts: m.now()}
	if in.Timestamp != nil {
		t, ok := parseTime(*in.Timestamp)
		if !ok {
			field("timestamp", "datetime_from_date_parsing", "Input should be a valid datetime", *in.Timestamp)
		}
		e.ts = t
	}
	switch {
	case in.Service == nil:
		field("service", "missing", "Field required", nil)
	case len(*in.Service) < 1:
		field("service", "string_too_short", "String should have at least 1 character", *in.Service)
	case len(*in.Service) > 64:
		field("service", "string_too_long", "String should have at most 64 characters", *in.Service)
	default:
		e.service = *in.Service
	}
	switch {
	case in.Level == nil:
		field("level", "missing", "Field required", nil)
	case !levels[*in.Level]:
		field("level", "string_pattern_mismatch", "String should match pattern '^(DEBUG|INFO|WARNING|ERROR|CRITICAL)$'", *in.Level)
	default:
		e.level = *in.Level
	}
	if in.Message == nil {
		field("message", "missing", "Field required", nil)
	} else {
		e.message = *in.Message
	}
	e.context = in.Context
	return e, errs
}

// parseTime accepts ISO-8601 with or without a zone; naive times are UTC (they came from
// datetime.utcnow() in the Python clients).
func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func (m *Module) withNode(r *http.Request, e *entry) {
	nodeID, ok := r.Context().Value(nodeCtx).(string)
	if !ok {
		return
	}
	if e.context == nil {
		e.context = map[string]any{}
	}
	e.context["node_id"] = nodeID
	// LEGACY-BUG kept: jarvis-logs read user_id from a validate-node field that never existed,
	// so it was always null. Nothing reads it; keep the key for shape parity.
	e.context["user_id"] = nil
}

func (m *Module) ingestOne(w http.ResponseWriter, r *http.Request) {
	var in entryIn
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	e, errs := m.validate(in, "body")
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	m.withNode(r, &e)
	if err := m.store(r.Context(), []entry{e}); err != nil {
		m.deps.Log.Error("logs: store", "err", err)
		httpx.Error(w, http.StatusBadGateway, "Failed to push log to Loki")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) ingestBatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Logs *[]entryIn `json:"logs"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if in.Logs == nil {
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"body", "logs"}, Msg: "Field required"})
		return
	}
	var all []entry
	var errs []httpx.FieldError
	for i, raw := range *in.Logs {
		e, fe := m.validate(raw, "body", "logs", i)
		errs = append(errs, fe...)
		m.withNode(r, &e)
		all = append(all, e)
	}
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	if len(all) > 0 {
		if err := m.store(r.Context(), all); err != nil {
			m.deps.Log.Error("logs: store", "err", err)
			httpx.Error(w, http.StatusBadGateway, "Failed to push logs to Loki")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) store(ctx context.Context, es []entry) error {
	return m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO logs_entries (ts, service, level, message, context) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range es {
			var ctxJSON any
			if e.context != nil {
				b, err := json.Marshal(e.context)
				if err != nil {
					return err
				}
				ctxJSON = string(b)
			}
			if _, err := stmt.ExecContext(ctx, e.ts.UnixNano(), e.service, e.level, e.message, ctxJSON); err != nil {
				return err
			}
		}
		return nil
	})
}

// Sink returns a logging.Sink that stores jarvisd's own records under service "jarvisd" (or the
// record's "module" attribute).
func (m *Module) Sink() logging.Sink { return sink{m} }

type sink struct{ m *Module }

func (s sink) Write(ctx context.Context, batch []logging.Record) error {
	if !s.m.ready.Load() {
		return logging.ErrNotReady
	}
	es := make([]entry, 0, len(batch))
	for _, r := range batch {
		lvl := "INFO"
		switch {
		case r.Level >= 8:
			lvl = "ERROR"
		case r.Level >= 4:
			lvl = "WARNING"
		case r.Level < 0:
			lvl = "DEBUG"
		}
		var c map[string]any
		if len(r.Attrs) > 0 {
			c = r.Attrs
		}
		es = append(es, entry{ts: r.Time.UTC(), service: r.Source, level: lvl, message: r.Message, context: c})
	}
	return s.m.store(ctx, es)
}

// --- query ---

type logOut struct {
	Timestamp string         `json:"timestamp"`
	Service   string         `json:"service"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Context   map[string]any `json:"context"`
	id        int64
}

// pyNaive renders a UTC time like Python's naive isoformat(): microseconds, omitted when zero.
func pyNaive(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05")
	}
	return t.Format("2006-01-02T15:04:05.000000")
}

type filter struct {
	service, level string
	search         *regexp.Regexp
	since, until   time.Time
	afterID        int64
	limit          int
	ascending      bool
}

// fetch returns up to f.limit entries, newest first (or oldest first if ascending). search is
// LogQL's `|~` line filter: an RE2 regexp over the stored line "message | {context json}",
// which Go's regexp implements exactly.
func (m *Module) fetch(ctx context.Context, f filter) ([]logOut, error) {
	where := []string{"ts >= ?", "ts <= ?"}
	args := []any{f.since.UnixNano(), f.until.UnixNano()}
	if f.service != "" {
		where = append(where, "service = ?")
		args = append(args, f.service)
	}
	if f.level != "" {
		where = append(where, "level = ?")
		args = append(args, f.level)
	}
	if f.afterID > 0 {
		where = append(where, "id > ?")
		args = append(args, f.afterID)
	}
	order := "ts DESC, id DESC"
	if f.ascending {
		order = "ts ASC, id ASC"
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT id, ts, service, level, message, context FROM logs_entries WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []logOut{}
	for rows.Next() && len(out) < f.limit {
		var o logOut
		var ts int64
		var c sql.NullString
		if err := rows.Scan(&o.id, &ts, &o.Service, &o.Level, &o.Message, &c); err != nil {
			return nil, err
		}
		line := o.Message
		if c.Valid {
			line += " | " + c.String
			_ = json.Unmarshal([]byte(c.String), &o.Context)
		}
		if f.search != nil && !f.search.MatchString(line) {
			continue
		}
		o.Timestamp = pyNaive(time.Unix(0, ts))
		out = append(out, o)
	}
	return out, rows.Err()
}

func (m *Module) parseFilter(w http.ResponseWriter, r *http.Request) (filter, bool) {
	q := r.URL.Query()
	f := filter{service: q.Get("service"), level: q.Get("level"), limit: 100}
	var errs []httpx.FieldError
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		switch {
		case err != nil:
			errs = append(errs, httpx.FieldError{Type: "int_parsing", Loc: []any{"query", "limit"}, Msg: "Input should be a valid integer, unable to parse string as an integer", Input: s})
		case n < 1:
			errs = append(errs, httpx.FieldError{Type: "greater_than_equal", Loc: []any{"query", "limit"}, Msg: "Input should be greater than or equal to 1", Input: s})
		case n > 1000:
			errs = append(errs, httpx.FieldError{Type: "less_than_equal", Loc: []any{"query", "limit"}, Msg: "Input should be less than or equal to 1000", Input: s})
		default:
			f.limit = n
		}
	}
	f.until = m.now()
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"since", &f.since}, {"until", &f.until}} {
		if s := q.Get(p.name); s != "" {
			t, ok := parseTime(s)
			if !ok {
				errs = append(errs, httpx.FieldError{Type: "datetime_from_date_parsing", Loc: []any{"query", p.name}, Msg: "Input should be a valid datetime", Input: s})
				continue
			}
			*p.dst = t
		}
	}
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return f, false
	}
	if f.since.IsZero() {
		f.since = f.until.Add(-time.Hour) // Loki query_range default window in the legacy client
	}
	if s := q.Get("search"); s != "" {
		re, err := regexp.Compile(s)
		if err != nil {
			// Loki rejected a bad regexp and the legacy client returned []; keep that.
			f.limit = 0
		} else {
			f.search = re
		}
	}
	return f, true
}

func (m *Module) query(w http.ResponseWriter, r *http.Request) {
	f, ok := m.parseFilter(w, r)
	if !ok {
		return
	}
	out := []logOut{}
	if f.limit > 0 {
		var err error
		if out, err = m.fetch(r.Context(), f); err != nil {
			m.deps.Log.Error("logs: query", "err", err)
			out = []logOut{} // the legacy client returned [] on any backend failure
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// stream is the SSE tail: every second, entries newer than the last one sent, oldest first.
// Fix (D8): the legacy poll used an inclusive time bound and re-sent the newest entry every
// second; this tracks the last id instead.
func (m *Module) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	q := r.URL.Query()
	// Take the high-water mark before answering, so nothing logged after the client sees the
	// headers can be missed.
	var lastID int64
	m.deps.DB.Read.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(id), 0) FROM logs_entries`).Scan(&lastID)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
		logs, err := m.fetch(r.Context(), filter{
			service: q.Get("service"), level: q.Get("level"),
			since: time.Unix(0, 0), until: m.now().Add(time.Hour),
			afterID: lastID, limit: 50, ascending: true,
		})
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				m.deps.Log.Error("logs: stream", "err", err)
			}
			continue
		}
		for _, l := range logs {
			b, _ := json.Marshal(l)
			fmt.Fprintf(w, "data: %s\n\n", b)
			lastID = max(lastID, l.id)
		}
		if len(logs) > 0 {
			flusher.Flush()
		}
	}
}

// services lists services that logged in the last hour (the legacy query's window).
func (m *Module) services(w http.ResponseWriter, r *http.Request) {
	rows, err := m.deps.DB.Read.QueryContext(r.Context(),
		`SELECT DISTINCT service FROM logs_entries WHERE ts >= ? ORDER BY service`, m.now().Add(-time.Hour).UnixNano())
	out := []string{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var s string
			if rows.Scan(&s) == nil {
				out = append(out, s)
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
