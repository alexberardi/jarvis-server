package cc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Memory routes (docs/cc/04 §2.1): mobile memories CRUD (mobile_memories.py), the Recent
// Commands transcripts (transcripts.py), the account purge (me.py) and agent inject
// (memories.py). Status codes and detail strings are the legacy ones the clients see.

// registerMemory wires memory into the module: routes, server tools, the default
// MemoryProfile and the queue jobs (memory_jobs.go).
func (m *Module) registerMemory(mux *http.ServeMux) {
	const v0 = "/api/v0"
	mux.HandleFunc("GET "+v0+"/mobile/memories", m.user(m.handleListMemories))
	mux.HandleFunc("POST "+v0+"/mobile/memories", m.user(m.handleCreateMemory))
	mux.HandleFunc("GET "+v0+"/mobile/memories/{memory_id}", m.user(m.handleGetMemory))
	mux.HandleFunc("PUT "+v0+"/mobile/memories/{memory_id}", m.user(m.handleUpdateMemory))
	mux.HandleFunc("DELETE "+v0+"/mobile/memories/{memory_id}", m.user(m.handleDeleteMemory))
	mux.HandleFunc("GET "+v0+"/transcripts/recent", m.user(m.handleRecentTranscripts))
	mux.HandleFunc("POST "+v0+"/transcripts/{transcript_id}/rate", m.user(m.handleRateTranscript))
	mux.HandleFunc("DELETE "+v0+"/me/data", m.user(m.handleDeleteMyData))
	mux.HandleFunc("POST "+v0+"/memories/inject", m.handleInjectMemories)

	if m.Memory == nil {
		m.Memory = memoryProfile{m}
	}
	if m.tools != nil {
		m.tools.Register(&rememberTool{m})
		m.tools.Register(&recallTool{m})
		m.tools.Register(&forgetTool{m})
	}
	m.registerMemoryJobs()
}

// --- mobile memories (permission matrix, 04 §3.9) ---

// memCaller is the caller's standing in the target household.
type memCaller struct {
	userID    int64
	role      authn.Role
	superuser bool
}

// elevated: POWER_USER or higher sees and manages household-wide memories.
func (c memCaller) elevated() bool {
	return c.superuser || roleRank(c.role) >= roleRank(authn.RolePowerUser)
}

func (c memCaller) admin() bool { return c.superuser || roleRank(c.role) >= roleRank(authn.RoleAdmin) }

func agentInjected(r memoryRow) bool { return r.Source == "agent" || r.Category == "agent_context" }

func (c memCaller) canRead(r memoryRow) bool {
	if r.UserID != nil {
		return *r.UserID == c.userID
	}
	return c.elevated()
}

func (c memCaller) canWrite(r memoryRow) bool {
	switch {
	case c.admin():
		return true
	case r.UserID != nil:
		return *r.UserID == c.userID
	case c.elevated():
		return !agentInjected(r)
	}
	return false
}

// memoryCaller is resolve_household_role: the caller's role in the target household. A
// non-member (an outsider, or a member of another household) is refused; a superuser is no
// exception, as legacy.
func (m *Module) memoryCaller(ctx context.Context, u authn.User, householdID string) (memCaller, error) {
	role, member, err := m.Auth.HouseholdRole(ctx, u.ID, householdID)
	if err != nil {
		return memCaller{}, err
	}
	if !member {
		return memCaller{}, fail(http.StatusForbidden, "User is not a member of this household")
	}
	return memCaller{userID: u.ID, role: role, superuser: u.IsSuperuser}, nil
}

// mobileMemory is MobileMemoryResponse.
type mobileMemory struct {
	ID          int64   `json:"id"`
	UserID      *int64  `json:"user_id"`
	HouseholdID string  `json:"household_id"`
	Category    string  `json:"category"`
	Key         *string `json:"key"`
	Content     string  `json:"content"`
	Source      string  `json:"source"`
	IsActive    bool    `json:"is_active"`
	IsPinned    bool    `json:"is_pinned"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	ExpiresAt   any     `json:"expires_at"`
	Editable    bool    `json:"editable"`
}

func (c memCaller) serialize(r memoryRow) mobileMemory {
	var exp any
	if r.ExpiresAt != nil {
		exp = naiveTS(*r.ExpiresAt)
	}
	return mobileMemory{ID: r.ID, UserID: r.UserID, HouseholdID: r.HouseholdID, Category: r.Category, Key: r.Key,
		Content: r.Content, Source: r.Source, IsActive: r.IsActive, IsPinned: r.IsPinned,
		CreatedAt: pyNaive(parseTS(r.CreatedAt)), UpdatedAt: pyNaive(parseTS(r.UpdatedAt)), ExpiresAt: exp,
		Editable: c.canWrite(r)}
}

// householdQuery reads the required household_id query parameter (ok=false: 400 written).
func householdQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	q := r.URL.Query()
	if _, present := q["household_id"]; !present {
		validationError(w, "query -> household_id: Field required")
		return "", false
	}
	return q.Get("household_id"), true
}

func pathInt(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		validationError(w, "path -> "+name+": Input should be a valid integer, unable to parse string as an integer")
		return 0, false
	}
	return n, true
}

// strLen checks a pydantic min/max string length (code points).
func (b *body) strLen(name, s string, min, max int) {
	n := utf8.RuneCountInString(s)
	switch {
	case min > 0 && n < min:
		b.fail(name, fmt.Sprintf("String should have at least %d character%s", min, plural(min)))
	case max > 0 && n > max:
		b.fail(name, fmt.Sprintf("String should have at most %d character%s", max, plural(max)))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (m *Module) handleListMemories(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, ok := householdQuery(w, r)
	if !ok {
		return
	}
	includeHH, ok := queryBool(w, r, "include_household", true)
	if !ok {
		return
	}
	ctx := r.Context()
	c, err := m.memoryCaller(ctx, u, hh)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	q := `SELECT ` + memoryCols + ` FROM cc_user_memories
		WHERE household_id = ? AND is_active = 1 AND (expires_at IS NULL OR expires_at > ?)`
	args := []any{hh, dbTime(m.now())}
	if c.elevated() && includeHH {
		q += ` AND (user_id = ? OR user_id IS NULL)`
	} else {
		q += ` AND user_id = ?`
	}
	args = append(args, u.ID)
	if cat := r.URL.Query().Get("category"); cat != "" {
		q += ` AND category = ?`
		args = append(args, cat)
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, q+` ORDER BY updated_at DESC, id DESC`, args...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []mobileMemory{}
	for rows.Next() {
		row, err := scanMemory(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, c.serialize(row))
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleCreateMemory(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, ok := householdQuery(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	content, hasContent := b.str("content", true)
	if hasContent {
		b.strLen("content", content, 1, 2000)
	}
	category := "general"
	if b.has("category") {
		category, _ = b.str("category", true)
	}
	key, _ := b.optStrPtr("key")
	if key != nil {
		b.strLen("key", *key, 0, 500)
	}
	pinned, _ := b.boolean("is_pinned")
	scope := "user"
	if b.has("scope") {
		s, isStr := b.m["scope"].(string)
		if !isStr || (s != "user" && s != "household") {
			b.fail("scope", "Input should be 'user' or 'household'")
		} else {
			scope = s
		}
	}
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	c, err := m.memoryCaller(ctx, u, hh)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	var target *int64
	if scope == "household" {
		if !c.elevated() {
			detail(w, http.StatusForbidden, "Only POWER_USER or higher can create household-wide memories")
			return
		}
	} else {
		id := u.ID
		target = &id
	}
	if category == "agent_context" && !c.admin() {
		detail(w, http.StatusForbidden, "Only ADMIN can create agent_context memories")
		return
	}
	wr := memWrite{UserID: target, HouseholdID: hh, Content: content, Category: category, Source: "ui", Pinned: pinned}
	if key != nil {
		wr.Key = *key
	}
	res, err := m.saveMemory(ctx, m.deps.DB.Write, wr)
	if err != nil {
		m.internalError(w, err)
		return
	}
	row, err := memoryByID(ctx, m.deps.DB.Write, res.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, c.serialize(row))
}

// loadMemoryForCaller enforces read or write scope: 404 for a row that doesn't exist, lives
// in another household, or is invisible to the caller (existence never leaks); 403 for a
// visible row the caller can't change.
func (m *Module) loadMemoryForCaller(ctx context.Context, u authn.User, id int64, hh string, write bool) (memoryRow, memCaller, error) {
	c, err := m.memoryCaller(ctx, u, hh)
	if err != nil {
		return memoryRow{}, c, err
	}
	row, err := memoryByID(ctx, m.deps.DB.Write, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (row.HouseholdID != hh || !c.canRead(row))) {
		return memoryRow{}, c, fail(http.StatusNotFound, "Memory not found")
	}
	if err != nil {
		return memoryRow{}, c, err
	}
	if write && !c.canWrite(row) {
		if agentInjected(row) {
			return memoryRow{}, c, fail(http.StatusForbidden, "Agent-injected memories are read-only for non-admin users")
		}
		return memoryRow{}, c, fail(http.StatusForbidden, "Insufficient permissions")
	}
	return row, c, nil
}

func (m *Module) handleGetMemory(w http.ResponseWriter, r *http.Request, u authn.User) {
	id, ok := pathInt(w, r, "memory_id")
	if !ok {
		return
	}
	hh, ok := householdQuery(w, r)
	if !ok {
		return
	}
	row, c, err := m.loadMemoryForCaller(r.Context(), u, id, hh, false)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c.serialize(row))
}

func (m *Module) handleUpdateMemory(w http.ResponseWriter, r *http.Request, u authn.User) {
	id, ok := pathInt(w, r, "memory_id")
	if !ok {
		return
	}
	hh, ok := householdQuery(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	// MobileMemoryUpdate: only the fields sent change (exclude_unset); a null leaves a field
	// alone (legacy wrote NULL into NOT NULL columns and 500ed).
	content, hasContent := b.optStrPtr("content")
	if content != nil {
		b.strLen("content", *content, 1, 2000)
	}
	category, _ := b.optStrPtr("category")
	active, hasActive := b.boolean("is_active")
	pinned, hasPinned := b.boolean("is_pinned")
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	row, c, err := m.loadMemoryForCaller(ctx, u, id, hh, true)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if category != nil && *category == "agent_context" && !c.admin() {
		detail(w, http.StatusForbidden, "Only ADMIN can set category=agent_context")
		return
	}
	set := []string{"updated_at = ?"}
	args := []any{dbTime(m.now())}
	if hasContent && content != nil {
		set = append(set, "content = ?")
		args = append(args, *content)
		if *content != row.Content { // D8 (04 §8.9): never let recall match the old text
			set = append(set, "embedding = NULL", "embedding_model = NULL")
		}
	}
	if category != nil {
		set = append(set, "category = ?")
		args = append(args, *category)
	}
	if hasActive {
		set = append(set, "is_active = ?")
		args = append(args, active)
	}
	if hasPinned {
		set = append(set, "is_pinned = ?")
		args = append(args, pinned)
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_user_memories SET `+strings.Join(set, ", ")+` WHERE id = ?`,
		append(args, id)...); err != nil {
		m.internalError(w, err)
		return
	}
	row, err = memoryByID(ctx, m.deps.DB.Write, id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c.serialize(row))
}

// handleDeleteMemory hard-deletes (D40 04.Q6), and drops the owner's characterization (D30).
func (m *Module) handleDeleteMemory(w http.ResponseWriter, r *http.Request, u authn.User) {
	id, ok := pathInt(w, r, "memory_id")
	if !ok {
		return
	}
	hh, ok := householdQuery(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	row, _, err := m.loadMemoryForCaller(ctx, u, id, hh, true)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM cc_user_memories WHERE id = ?`, id); err != nil {
			return err
		}
		if row.UserID != nil {
			return deleteCharacterization(ctx, tx, *row.UserID, row.HouseholdID)
		}
		return nil
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id})
}

// --- transcripts (the Recent Commands rating screen) ---

const (
	recentTranscriptsDefault = 50
	recentTranscriptsMax     = 200
)

const transcriptCols = `id, user_id, household_id, conversation_id, user_message, assistant_message, tool_calls_json,
	created_at, user_rating, rating_notes, rated_at`

// transcriptResponse is TranscriptResponse, keys in the legacy order.
func scanTranscript(sc rowScanner) (*pyjson.Object, error) {
	var id, uid int64
	var hh, cid, um, created string
	var am, tc, notes, rated sql.NullString
	var rating sql.NullInt64
	if err := sc.Scan(&id, &uid, &hh, &cid, &um, &am, &tc, &created, &rating, &notes, &rated); err != nil {
		return nil, err
	}
	var calls any
	if tc.Valid && tc.String != "" {
		if v, err := pyjson.Loads(tc.String); err == nil {
			if l, isList := v.([]any); isList {
				calls = l
			}
		}
	}
	o := pyjson.NewObject()
	o.Set("id", id)
	o.Set("user_id", uid)
	o.Set("household_id", hh)
	o.Set("conversation_id", cid)
	o.Set("user_message", um)
	o.Set("assistant_message", nullable(am))
	o.Set("tool_calls", calls)
	o.Set("created_at", pyNaive(parseTS(created)))
	if rating.Valid {
		o.Set("user_rating", rating.Int64)
	} else {
		o.Set("user_rating", nil)
	}
	o.Set("rating_notes", nullable(notes))
	if rated.Valid {
		o.Set("rated_at", pyNaive(parseTS(rated.String)))
	} else {
		o.Set("rated_at", nil)
	}
	return o, nil
}

// parseQueryTime parses a FastAPI datetime query value: ISO-8601 with or without an offset
// (naive means UTC), or a bare date.
func parseQueryTime(s string) (time.Time, bool) {
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil { // pydantic accepts unix seconds
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}

func (m *Module) handleRecentTranscripts(w http.ResponseWriter, r *http.Request, u authn.User) {
	limit, ok := queryInt(w, r, "limit", recentTranscriptsDefault)
	if !ok {
		return
	}
	switch {
	case limit < 1:
		validationError(w, "query -> limit: Input should be greater than or equal to 1")
		return
	case limit > recentTranscriptsMax:
		validationError(w, "query -> limit: Input should be less than or equal to 200")
		return
	}
	q := `SELECT ` + transcriptCols + ` FROM cc_conversation_transcripts WHERE user_id = ?`
	args := []any{u.ID}
	if s := r.URL.Query().Get("since"); s != "" {
		since, ok := parseQueryTime(s)
		if !ok {
			validationError(w, "query -> since: Input should be a valid datetime")
			return
		}
		q += ` AND created_at >= ?`
		args = append(args, dbTime(since))
	}
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), q+` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		o, err := scanTranscript(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleRateTranscript(w http.ResponseWriter, r *http.Request, u authn.User) {
	id, ok := pathInt(w, r, "transcript_id")
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	rating, _ := b.integer("rating", true)
	notes, _ := b.optStrPtr("notes")
	if notes != nil {
		b.strLen("notes", *notes, 0, 2000)
	}
	if !b.done(w) {
		return
	}
	if rating < -1 || rating > 1 {
		detail(w, http.StatusBadRequest, "rating must be -1, 0, or 1")
		return
	}
	ctx := r.Context()
	res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_conversation_transcripts
		SET user_rating = ?, rating_notes = ?, rated_at = ? WHERE id = ? AND user_id = ?`,
		rating, nullStr(notes), dbTime(m.now()), id, u.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 { // missing or someone else's: opaque 404
		detail(w, http.StatusNotFound, "Transcript not found")
		return
	}
	o, err := scanTranscript(m.deps.DB.Write.QueryRowContext(ctx, `SELECT `+transcriptCols+`
		FROM cc_conversation_transcripts WHERE id = ?`, id))
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, o)
}

// --- /me/data (account purge, legacy HTTP path) ---

// handleDeleteMyData is CC's legacy account-purge endpoint: the user comes from the JWT only,
// one transaction, 204 even with nothing to delete, and a failure is a 5xx so the caller
// aborts the deletion. jarvisd's own auth calls PurgeUser in process instead (D20).
func (m *Module) handleDeleteMyData(w http.ResponseWriter, r *http.Request, u authn.User) {
	if err := m.deps.DB.Tx(r.Context(), func(tx *sql.Tx) error { return m.PurgeUser(r.Context(), tx, u.ID) }); err != nil {
		m.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- agent inject (memories.py inject_memories) ---

const (
	injectMaxItems   = 100
	injectDefaultTTL = 24.0
	injectMaxTTL     = 720.0
)

type injectItem struct {
	content, category, key, source string
	userID                         *int64
	ttlHours                       float64
}

// injectAuth is _verify_inject_auth: a node key first (household and members from the node),
// then app-to-app credentials (household from the body). ok=false: the response is written.
func (m *Module) injectAuth(w http.ResponseWriter, r *http.Request) (node *nodeCtx, ok bool) {
	ctx := r.Context()
	if key := r.Header.Get("X-API-Key"); key != "" {
		if id, nk, valid := authn.NodeKey(key); valid {
			v, err := m.Auth.ValidateNode(ctx, id, nk, m.serviceID())
			if err != nil {
				m.internalError(w, err)
				return nil, false
			}
			if v.Valid {
				if row, err := m.nodeByID(ctx, id); err == nil {
					m.touchLastSeen(ctx, row)
					return &nodeCtx{ID: id, HouseholdID: v.Node.HouseholdID, HouseholdMemberIDs: v.HouseholdMemberIDs, Key: nk, row: row}, true
				} else if !errors.Is(err, sql.ErrNoRows) {
					m.internalError(w, err)
					return nil, false
				}
			}
		}
		// A failed node key falls through to app auth, as legacy.
	}
	appID, appKey := r.Header.Get("X-Jarvis-App-Id"), r.Header.Get("X-Jarvis-App-Key")
	if appID != "" && appKey != "" {
		_, valid, err := m.Auth.ValidateApp(ctx, appID, appKey)
		if err != nil {
			m.deps.Log.Error("cc: app validation failed", "err", err)
			detail(w, http.StatusBadGateway, "Auth service unavailable")
			return nil, false
		}
		if !valid {
			detail(w, http.StatusUnauthorized, "Invalid app credentials")
			return nil, false
		}
		return nil, true
	}
	detail(w, http.StatusUnauthorized, "Authentication required (X-Api-Key or X-Jarvis-App-Id/Key)")
	return nil, false
}

func parseInjectItems(b *body) []injectItem {
	v, present := b.m["memories"]
	if !present {
		b.fail("memories", "Field required")
		return nil
	}
	list, isList := v.([]any)
	if !isList {
		b.fail("memories", "Input should be a valid list")
		return nil
	}
	if len(list) > injectMaxItems {
		b.fail("memories", fmt.Sprintf("List should have at most %d items after validation, not %d", injectMaxItems, len(list)))
		return nil
	}
	items := make([]injectItem, 0, len(list))
	for i, e := range list {
		obj, isObj := e.(map[string]any)
		ib := &body{m: obj, loc: fmt.Sprintf("body -> memories -> %d", i), errs: b.errs}
		if !isObj {
			*b.errs = append(*b.errs, ib.loc+": Input should be a valid dictionary or object to extract fields from")
			continue
		}
		it := injectItem{category: "agent_context", source: "agent", ttlHours: injectDefaultTTL}
		if s, ok := ib.str("content", true); ok {
			ib.strLen("content", s, 0, 2000)
			it.content = s
		}
		if ib.has("category") {
			it.category, _ = ib.str("category", true)
		}
		if s, ok := ib.str("key", true); ok {
			ib.strLen("key", s, 0, 500)
			it.key = s
		}
		if uid, ok := ib.integer("user_id", false); ok {
			it.userID = &uid
		}
		if ib.has("ttl_hours") {
			if f, ok := ib.number("ttl_hours"); ok {
				switch {
				case f <= 0:
					ib.fail("ttl_hours", "Input should be greater than 0")
				case f > injectMaxTTL:
					ib.fail("ttl_hours", "Input should be less than or equal to 720")
				default:
					it.ttlHours = f
				}
			}
		}
		if ib.has("source") {
			if s, ok := ib.str("source", true); ok {
				ib.strLen("source", s, 0, 100)
				it.source = s
			}
		}
		items = append(items, it)
	}
	return items
}

// handleInjectMemories batch-stores background-agent context (weather, calendar, news,
// reminders) with a TTL. Changed by D40 04.Q10: it always stores (model.advanced_context
// gates only the per-turn injection), and a personal item's user_id must be a member of the
// household. Dedup: last write wins per (key, user_id) in the batch, key upsert across
// batches, and a near-duplicate (cosine ≥ 0.9) of another row in the same pool replaces it
// (legacy's layer 3 compared each row with itself and never fired, D8).
func (m *Module) handleInjectMemories(w http.ResponseWriter, r *http.Request) {
	node, ok := m.injectAuth(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	bodyHH, _ := b.optStrPtr("household_id")
	items := parseInjectItems(b)
	if !b.done(w) {
		return
	}
	hh := ""
	if bodyHH != nil {
		hh = *bodyHH
	}
	if hh == "" && node != nil {
		hh = node.HouseholdID
	}
	if hh == "" {
		detail(w, http.StatusBadRequest, "household_id required (provide in body or use node auth)")
		return
	}
	if node != nil && bodyHH != nil && *bodyHH != "" && node.HouseholdID != "" && *bodyHH != node.HouseholdID {
		detail(w, http.StatusForbidden, "household_id does not match node's household")
		return
	}
	ctx := r.Context()
	if !m.householdBool(ctx, settingMemoryEnabled, hh) {
		detail(w, http.StatusConflict, "Memory system is disabled for this household")
		return
	}

	// Layer 1: last write wins per (key, user_id), in first-seen order.
	type ident struct {
		key string
		uid int64
		hh  bool
	}
	idx := map[ident]int{}
	var deduped []injectItem
	for _, it := range items {
		k := ident{key: it.key, hh: it.userID == nil}
		if it.userID != nil {
			k.uid = *it.userID
		}
		if i, seen := idx[k]; seen {
			deduped[i] = it
			continue
		}
		idx[k] = len(deduped)
		deduped = append(deduped, it)
	}
	dedupCount := len(items) - len(deduped)

	errs := []string{}
	type saved struct {
		id      int64
		item    injectItem
		updated bool
	}
	var done []saved
	injected, updated := 0, 0
	now := m.now()
	for _, it := range deduped {
		if it.userID != nil && !m.isHouseholdMember(ctx, node, hh, *it.userID) {
			errs = append(errs, fmt.Sprintf("key=%s: user_id %d is not a member of this household", it.key, *it.userID))
			continue
		}
		exp := now.Add(time.Duration(it.ttlHours * float64(time.Hour)))
		res, err := m.saveMemory(ctx, m.deps.DB.Write, memWrite{UserID: it.userID, HouseholdID: hh, Content: it.content,
			Category: it.category, Key: it.key, Source: it.source, ExpiresAt: &exp})
		if err != nil {
			m.deps.Log.Warn("cc: inject failed", "key", it.key, "err", err)
			errs = append(errs, fmt.Sprintf("key=%s: %v", it.key, err))
			continue
		}
		done = append(done, saved{res.ID, it, res.Updated})
		if res.Updated {
			updated++
		} else {
			injected++
		}
	}

	// Embeddings and layer 3, best effort: the agent isn't latency-sensitive.
	if len(done) > 0 {
		texts := make([]string, len(done))
		for i, s := range done {
			texts[i] = s.item.content
		}
		ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
		vecs, model, err := m.embedTexts(ectx, texts)
		cancel()
		if err != nil {
			m.deps.Log.Warn("cc: inject embedding failed (non-fatal; the sweep embeds later)", "err", err)
		} else {
			err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
				for i, s := range done {
					if vecs[i] == nil {
						continue
					}
					if err := setVector(ctx, tx, s.id, s.item.content, vecs[i], model); err != nil {
						return err
					}
				}
				gone := map[int64]bool{}
				for i, s := range done {
					if vecs[i] == nil || s.updated || gone[s.id] {
						continue
					}
					dup, err := m.similarDuplicate(ctx, tx, memScope{HouseholdID: hh, UserID: s.item.userID},
						unpackVector(vecs[i]), model, s.id)
					if err != nil {
						return err
					}
					if dup != 0 { // keep the newer row
						gone[dup] = true
						if _, err := tx.ExecContext(ctx, `DELETE FROM cc_user_memories WHERE id = ?`, dup); err != nil {
							return err
						}
						dedupCount++
					}
				}
				return nil
			})
			if err != nil {
				m.deps.Log.Warn("cc: inject vector write failed (non-fatal)", "err", err)
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"injected": injected, "updated": updated, "deduplicated": dedupCount, "errors": errs,
	})
}

// isHouseholdMember checks a personal inject's user_id (D40 04.Q10): from the node's
// validated member list, or the auth module for an app caller.
func (m *Module) isHouseholdMember(ctx context.Context, node *nodeCtx, hh string, userID int64) bool {
	if node != nil && node.HouseholdID == hh {
		return containsID(node.HouseholdMemberIDs, userID)
	}
	_, member, err := m.Auth.HouseholdRole(ctx, userID, hh)
	return err == nil && member
}
