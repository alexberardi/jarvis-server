package logs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// In-process reads for the admin Logs page (AD9, inventory §6.2 #7): a filtered query with a
// keyset cursor, the sources seen, and a live tail. The legacy app-credential routes keep
// their own shapes; these serve the admin BFF only.

// Levels in increasing severity.
var Levels = []string{"DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"}

// Filter selects entries. Zero fields don't filter.
type Filter struct {
	Service string   // the service (module) that logged, e.g. "jarvisd", "jarvis-node"
	NodeID  string   // entries a node shipped (context.node_id)
	Levels  []string // exactly these levels
	// MinLevel keeps this level and everything more severe.
	MinLevel string
	Since    time.Time
	Until    time.Time
	// Text is a case-insensitive substring of the message or the context JSON.
	Text string
	// Limit caps a page (Query); 0 is DefaultLimit, more than MaxLimit is MaxLimit.
	Limit int
	// Cursor continues a Query from the previous page's NextCursor.
	Cursor string
}

const (
	DefaultLimit = 200
	MaxLimit     = 1000
)

// ErrBadCursor is a cursor Query did not issue.
var ErrBadCursor = errors.New("logs: invalid cursor")

// Entry is one stored log line.
type Entry struct {
	ID        int64          `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Service   string         `json:"service"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Context   map[string]any `json:"context"`
	// NodeID is context.node_id, lifted out for the node filter and column ("" for services).
	NodeID string `json:"node_id,omitempty"`
	ts     int64
}

// ValidLevel reports whether l is a stored level name.
func ValidLevel(l string) bool { return slices.Contains(Levels, l) }

// where builds the shared WHERE clause.
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(c string, a ...any) {
		conds = append(conds, c)
		args = append(args, a...)
	}
	if f.Service != "" {
		add("service = ?", f.Service)
	}
	if f.NodeID != "" {
		add("json_extract(context, '$.node_id') = ?", f.NodeID)
	}
	levels := f.Levels
	if f.MinLevel != "" {
		if i := slices.Index(Levels, f.MinLevel); i >= 0 {
			severe := Levels[i:]
			if len(levels) == 0 {
				levels = severe
			} else {
				levels = slices.DeleteFunc(slices.Clone(levels), func(l string) bool { return !slices.Contains(severe, l) })
				if len(levels) == 0 {
					levels = []string{"-"} // disjoint filters match nothing
				}
			}
		}
	}
	if len(levels) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(levels)), ",")
		a := make([]any, len(levels))
		for i, l := range levels {
			a[i] = l
		}
		add("level IN ("+ph+")", a...)
	}
	if !f.Since.IsZero() {
		add("ts >= ?", f.Since.UnixNano())
	}
	if !f.Until.IsZero() {
		add("ts <= ?", f.Until.UnixNano())
	}
	if t := strings.TrimSpace(f.Text); t != "" {
		add("instr(lower(message || ' ' || COALESCE(context, '')), lower(?)) > 0", t)
	}
	if len(conds) == 0 {
		return "1", nil
	}
	return strings.Join(conds, " AND "), args
}

func scanEntries(rows *sql.Rows) ([]Entry, error) {
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var c sql.NullString
		if err := rows.Scan(&e.ID, &e.ts, &e.Service, &e.Level, &e.Message, &c); err != nil {
			return nil, err
		}
		e.Timestamp = time.Unix(0, e.ts).UTC()
		if c.Valid {
			_ = json.Unmarshal([]byte(c.String), &e.Context)
			if n, ok := e.Context["node_id"].(string); ok {
				e.NodeID = n
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Query returns one page, newest first, and the cursor for the next (older) page ("" when
// this page is the last).
func (m *Module) Query(ctx context.Context, f Filter) ([]Entry, string, error) {
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	where, args := f.where()
	if f.Cursor != "" {
		ts, id, ok := parseCursor(f.Cursor)
		if !ok {
			return nil, "", ErrBadCursor
		}
		where += " AND (ts < ? OR (ts = ? AND id < ?))"
		args = append(args, ts, ts, id)
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT id, ts, service, level, message, context FROM logs_entries WHERE `+where+
			` ORDER BY ts DESC, id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	out, err := scanEntries(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = fmt.Sprintf("%d_%d", last.ts, last.ID)
	}
	return out, next, nil
}

func parseCursor(c string) (ts, id int64, ok bool) {
	a, b, found := strings.Cut(c, "_")
	if !found {
		return 0, 0, false
	}
	ts, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseInt(b, 10, 64)
	return ts, id, err1 == nil && err2 == nil && id > 0
}

// LatestID is the newest entry's id (0 when empty): a tail starts after it.
func (m *Module) LatestID(ctx context.Context) (int64, error) {
	var id int64
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM logs_entries`).Scan(&id)
	return id, err
}

// After returns up to limit entries matching f (Since/Until/Cursor/Limit ignored) with an id
// above afterID, oldest first: the tail's next batch.
func (m *Module) After(ctx context.Context, f Filter, afterID int64, limit int) ([]Entry, error) {
	f.Since, f.Until, f.Cursor = time.Time{}, time.Time{}, ""
	where, args := f.where()
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT id, ts, service, level, message, context FROM logs_entries WHERE `+where+
			` AND id > ? ORDER BY id ASC LIMIT ?`, append(args, afterID, limit)...)
	if err != nil {
		return nil, err
	}
	return scanEntries(rows)
}

// Sources lists the services and the nodes that logged since t, sorted.
func (m *Module) Sources(ctx context.Context, since time.Time) (services, nodes []string, err error) {
	read := func(q string) ([]string, error) {
		rows, err := m.deps.DB.Read.QueryContext(ctx, q, since.UnixNano())
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	}
	if services, err = read(`SELECT DISTINCT service FROM logs_entries WHERE ts >= ? ORDER BY service`); err != nil {
		return nil, nil, err
	}
	nodes, err = read(`SELECT DISTINCT json_extract(context, '$.node_id') AS n FROM logs_entries
		WHERE ts >= ? AND n IS NOT NULL AND n != '' ORDER BY n`)
	if err != nil {
		return nil, nil, err
	}
	return services, nodes, nil
}
