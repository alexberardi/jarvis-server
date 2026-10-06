package notifications

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// InboxItem is one inbox row, in the legacy response shape (InboxItemResponse).
type InboxItem struct {
	ID            string         `json:"id"`
	UserID        *int64         `json:"user_id"`
	HouseholdID   string         `json:"household_id"`
	Title         string         `json:"title"`
	Summary       string         `json:"summary"`
	Body          string         `json:"body"`
	Category      string         `json:"category"`
	SourceService string         `json:"source_service"`
	Metadata      map[string]any `json:"metadata"`
	IsRead        bool           `json:"is_read"`
	CreatedAt     string         `json:"created_at"` // naive UTC isoformat, as Python emitted
}

// NewInboxItem is what a producer writes. UserID nil means household-wide (every member sees it).
type NewInboxItem struct {
	HouseholdID   string
	UserID        *int64
	Title         string
	Summary       string
	Body          string
	Category      string
	SourceService string
	// Metadata is stored as JSON. An empty map is stored as NULL (legacy `if metadata`).
	Metadata map[string]any
}

// InboxUpdate changes an item in place; nil fields are left alone.
type InboxUpdate struct {
	Title, Summary, Body, Category *string
	// Metadata replaces the stored metadata when non-nil (an empty map is stored as {}).
	Metadata map[string]any
}

// ErrNotFound is returned by UpdateInboxItem when no item has that id in that household.
var ErrNotFound = errors.New("notifications: item not found")

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTx runs fn in tx when given, otherwise in a new write transaction.
func (m *Module) inTx(ctx context.Context, tx *sql.Tx, fn func(*sql.Tx) error) error {
	if tx != nil {
		return fn(tx)
	}
	return m.deps.DB.Tx(ctx, fn)
}

const inboxCols = `id, user_id, household_id, title, summary, body, category, source_service, metadata_json, is_read, created_at`

func scanInbox(sc interface{ Scan(...any) error }) (InboxItem, error) {
	var it InboxItem
	var uid sql.NullInt64
	var meta sql.NullString
	var created string
	if err := sc.Scan(&it.ID, &uid, &it.HouseholdID, &it.Title, &it.Summary, &it.Body, &it.Category,
		&it.SourceService, &meta, &it.IsRead, &created); err != nil {
		return InboxItem{}, err
	}
	if uid.Valid {
		v := uid.Int64
		it.UserID = &v
	}
	if meta.Valid && meta.String != "" {
		it.Metadata = decodeJSONObject(meta.String)
	}
	it.CreatedAt = pyNaive(parseTS(created))
	return it, nil
}

func getInbox(ctx context.Context, q querier, id string) (InboxItem, error) {
	return scanInbox(q.QueryRowContext(ctx, `SELECT `+inboxCols+` FROM notifications_inbox_items WHERE id = ?`, id))
}

// CreateInboxItem writes an inbox item. With tx non-nil it joins the caller's transaction
// (docs/cc D31); otherwise it commits on its own.
func (m *Module) CreateInboxItem(ctx context.Context, tx *sql.Tx, in NewInboxItem) (InboxItem, error) {
	var meta any
	if len(in.Metadata) > 0 {
		s, err := marshalJSON(in.Metadata)
		if err != nil {
			return InboxItem{}, err
		}
		meta = s
	}
	var uid any
	if in.UserID != nil {
		uid = *in.UserID
	}
	id := newUUID()
	var it InboxItem
	err := m.inTx(ctx, tx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notifications_inbox_items
			(id, user_id, household_id, title, summary, body, category, source_service, metadata_json, is_read, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			id, uid, in.HouseholdID, in.Title, in.Summary, in.Body, in.Category, in.SourceService, meta, ts(m.now())); err != nil {
			return err
		}
		var err error
		it, err = getInbox(ctx, tx, id)
		return err
	})
	if err != nil {
		return InboxItem{}, err
	}
	m.deps.Log.Info("notifications: created inbox item", "id", id, "category", in.Category, "household_id", in.HouseholdID)
	return it, nil
}

// UpdateInboxItem changes an item's content in place (same id), scoped to householdID. It
// returns ErrNotFound when the household has no such item. tx is optional, as for
// CreateInboxItem.
func (m *Module) UpdateInboxItem(ctx context.Context, tx *sql.Tx, id, householdID string, u InboxUpdate) (InboxItem, error) {
	sets := []string{}
	args := []any{}
	for _, f := range []struct {
		col string
		v   *string
	}{{"title", u.Title}, {"summary", u.Summary}, {"body", u.Body}, {"category", u.Category}} {
		if f.v != nil {
			sets = append(sets, f.col+" = ?")
			args = append(args, *f.v)
		}
	}
	if u.Metadata != nil {
		s, err := marshalJSON(u.Metadata)
		if err != nil {
			return InboxItem{}, err
		}
		sets = append(sets, "metadata_json = ?")
		args = append(args, s)
	}
	var it InboxItem
	err := m.inTx(ctx, tx, func(tx *sql.Tx) error {
		var found string
		err := tx.QueryRowContext(ctx, `SELECT id FROM notifications_inbox_items WHERE id = ? AND household_id = ?`, id, householdID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if len(sets) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE notifications_inbox_items SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
				append(args, id)...); err != nil {
				return err
			}
		}
		it, err = getInbox(ctx, tx, id)
		return err
	})
	if err != nil {
		return InboxItem{}, err
	}
	m.deps.Log.Info("notifications: updated inbox item", "id", id, "household_id", householdID)
	return it, nil
}

// visible restricts a query to what a user may see: their household's household-wide items
// and their own personal ones. Another member's personal item is invisible (intra-household
// IDOR guard).
const visible = `household_id = ? AND (user_id = ? OR user_id IS NULL)`

// --- HTTP: services ---

func (m *Module) handleCreateInbox(w http.ResponseWriter, r *http.Request) {
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	var in NewInboxItem
	in.HouseholdID, _ = o.str("household_id", true)
	in.Title, _ = o.str("title", true)
	in.Summary, _ = o.str("summary", true)
	in.Body, _ = o.str("body", true)
	in.Category, _ = o.str("category", true)
	in.SourceService, _ = o.str("source_service", true)
	if uid, ok := o.optInt("user_id"); ok {
		in.UserID = &uid
	}
	in.Metadata, _ = o.optDict("metadata")
	if !o.done(w) {
		return
	}
	it, err := m.CreateInboxItem(r.Context(), nil, in)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, it)
}

func (m *Module) handleUpdateInbox(w http.ResponseWriter, r *http.Request) {
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	hh, _ := o.str("household_id", true)
	var u InboxUpdate
	opt := func(name string) *string {
		if s, ok := o.str(name, false); ok {
			return &s
		}
		return nil
	}
	u.Title, u.Summary, u.Body, u.Category = opt("title"), opt("summary"), opt("body"), opt("category")
	u.Metadata, _ = o.optDict("metadata")
	if !o.done(w) {
		return
	}
	it, err := m.UpdateInboxItem(r.Context(), nil, r.PathValue("item_id"), hh, u)
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "Item not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, it)
}

// --- HTTP: mobile ---

func needHousehold(w http.ResponseWriter, u authn.User) bool {
	if u.HouseholdID == "" {
		httpx.Error(w, http.StatusBadRequest, "No household_id in token")
		return false
	}
	return true
}

// lastQuery is Starlette's QueryParams.get: the last value of a repeated parameter.
func lastQuery(r *http.Request, name string) (string, bool) {
	v := r.URL.Query()[name]
	if len(v) == 0 {
		return "", false
	}
	return v[len(v)-1], true
}

// pyBool parses a query boolean the way pydantic does.
func pyBool(s string) (bool, bool) {
	switch strings.ToLower(s) {
	case "1", "on", "t", "true", "y", "yes":
		return true, true
	case "0", "off", "f", "false", "n", "no":
		return false, true
	}
	return false, false
}

func (m *Module) handleListInbox(w http.ResponseWriter, r *http.Request, u authn.User) {
	var errs []httpx.FieldError
	limit, offset := 50, 0
	intParam := func(name string, dst *int, lo, hi int) {
		s, ok := lastQuery(r, name)
		if !ok {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		switch {
		case err != nil:
			errs = append(errs, httpx.FieldError{Type: "int_parsing", Loc: []any{"query", name}, Msg: "Input should be a valid integer, unable to parse string as an integer", Input: s})
		case n < lo:
			errs = append(errs, httpx.FieldError{Type: "greater_than_equal", Loc: []any{"query", name}, Msg: "Input should be greater than or equal to " + strconv.Itoa(lo), Input: s})
		case hi > 0 && n > hi:
			errs = append(errs, httpx.FieldError{Type: "less_than_equal", Loc: []any{"query", name}, Msg: "Input should be less than or equal to " + strconv.Itoa(hi), Input: s})
		default:
			*dst = n
		}
	}
	intParam("limit", &limit, 1, 200)
	intParam("offset", &offset, 0, 0)
	where := []string{visible}
	args := []any{u.HouseholdID, u.ID}
	if s, ok := lastQuery(r, "is_read"); ok {
		b, valid := pyBool(s)
		if !valid {
			errs = append(errs, httpx.FieldError{Type: "bool_parsing", Loc: []any{"query", "is_read"}, Msg: "Input should be a valid boolean, unable to interpret input", Input: s})
		}
		where = append(where, "is_read = ?")
		args = append(args, b)
	}
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	if !needHousehold(w, u) {
		return
	}
	if c, ok := lastQuery(r, "category"); ok {
		where = append(where, "category = ?")
		args = append(args, c)
	}
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), `SELECT `+inboxCols+` FROM notifications_inbox_items WHERE `+
		strings.Join(where, " AND ")+` ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []InboxItem{}
	for rows.Next() {
		it, err := scanInbox(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleUnreadCount(w http.ResponseWriter, r *http.Request, u authn.User) {
	if !needHousehold(w, u) {
		return
	}
	var n int
	if err := m.deps.DB.Read.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM notifications_inbox_items WHERE `+visible+` AND is_read = 0`, u.HouseholdID, u.ID).Scan(&n); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"count": n})
}

// markRead marks a visible item read and returns it, or ErrNotFound.
func (m *Module) markRead(ctx context.Context, id string, u authn.User) (InboxItem, error) {
	var it InboxItem
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notifications_inbox_items SET is_read = 1 WHERE id = ? AND `+visible,
			id, u.HouseholdID, u.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		it, err = getInbox(ctx, tx, id)
		return err
	})
	return it, err
}

// handleGetInbox returns one item and marks it read (legacy auto-mark on open).
func (m *Module) handleGetInbox(w http.ResponseWriter, r *http.Request, u authn.User) {
	if !needHousehold(w, u) {
		return
	}
	m.writeMarked(w, r, u)
}

func (m *Module) handleMarkRead(w http.ResponseWriter, r *http.Request, u authn.User) {
	if !needHousehold(w, u) {
		return
	}
	m.writeMarked(w, r, u)
}

func (m *Module) writeMarked(w http.ResponseWriter, r *http.Request, u authn.User) {
	it, err := m.markRead(r.Context(), r.PathValue("item_id"), u)
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "Item not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, it)
}

func (m *Module) handleDeleteInbox(w http.ResponseWriter, r *http.Request, u authn.User) {
	if !needHousehold(w, u) {
		return
	}
	res, err := m.deps.DB.Write.ExecContext(r.Context(), `DELETE FROM notifications_inbox_items WHERE id = ? AND `+visible,
		r.PathValue("item_id"), u.HouseholdID, u.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpx.Error(w, http.StatusNotFound, "Item not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// bulk validates {"ids": [...]} and runs stmt over the listed items the caller can see (plus
// the extra condition), answering {key: rows affected}.
func (m *Module) bulk(w http.ResponseWriter, r *http.Request, u authn.User, stmt, extra, key string) {
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	ids := o.strList("ids")
	if !o.done(w) {
		return
	}
	if !needHousehold(w, u) {
		return
	}
	var n int64
	if len(ids) > 0 {
		args := []any{u.HouseholdID, u.ID}
		for _, id := range ids {
			args = append(args, id)
		}
		res, err := m.deps.DB.Write.ExecContext(r.Context(),
			stmt+` WHERE `+visible+extra+` AND id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
		if err != nil {
			m.internalError(w, err)
			return
		}
		n, _ = res.RowsAffected()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{key: n})
}

func (m *Module) handleBulkRead(w http.ResponseWriter, r *http.Request, u authn.User) {
	// Only unread rows count, as before ("count actually updated").
	m.bulk(w, r, u, `UPDATE notifications_inbox_items SET is_read = 1`, ` AND is_read = 0`, "updated")
}

func (m *Module) handleBulkDelete(w http.ResponseWriter, r *http.Request, u authn.User) {
	m.bulk(w, r, u, `DELETE FROM notifications_inbox_items`, "", "deleted")
}
