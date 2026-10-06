package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// --- admin_app_clients.py ---

func (m *Module) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	id, _ := b.str("app_id", true, 0, 0)
	name, _ := b.str("name", true, 0, 0)
	if !b.done(w) {
		return
	}
	key := tokenURLSafe(48)
	hash, err := hashSecret(key)
	if err != nil {
		m.internalError(w, err)
		return
	}
	t := dbTime(now())
	_, err = m.deps.DB.Write.ExecContext(r.Context(), `INSERT INTO auth_app_clients
		(app_id, name, key_hash, is_active, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, id, name, hash, t, t)
	if isUnique(err) {
		detail(w, http.StatusBadRequest, "app_id already exists")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"app_id": id, "name": name, "key": key, "created_at": pyTime(t), "last_rotated_at": nil,
	})
}

// handleRotateApp issues a new key and reactivates the client.
func (m *Module) handleRotateApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("app_id")
	key := tokenURLSafe(48)
	hash, err := hashSecret(key)
	if err != nil {
		m.internalError(w, err)
		return
	}
	t := now()
	res, err := m.deps.DB.Write.ExecContext(r.Context(), `UPDATE auth_app_clients
		SET key_hash = ?, last_rotated_at = ?, is_active = 1, updated_at = ? WHERE app_id = ?`, hash, dbTime(t), dbTime(t), id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		detail(w, http.StatusNotFound, "App client not found")
		return
	}
	m.verified.invalidate("app", id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"app_id": id, "key": key, "last_rotated_at": pyTimeOf(t)})
}

func (m *Module) handleRevokeApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("app_id")
	res, err := m.deps.DB.Write.ExecContext(r.Context(),
		`UPDATE auth_app_clients SET is_active = 0, updated_at = ? WHERE app_id = ?`, dbTime(now()), id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		detail(w, http.StatusNotFound, "App client not found")
		return
	}
	m.verified.invalidate("app", id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"app_id": id, "is_active": false})
}

func (m *Module) handleListApps(w http.ResponseWriter, r *http.Request) {
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), `SELECT `+appCols+` FROM auth_app_clients ORDER BY id`)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, map[string]any{
			"app_id": a.appID, "name": a.name, "is_active": a.isActive,
			"created_at": pyTime(a.createdAt), "last_rotated_at": pyTimeNull(a.lastRotatedAt),
		})
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleAppPing(w http.ResponseWriter, _ *http.Request, a *appClient) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"app_id": a.appID, "name": a.name})
}

// --- admin_users.py ---

func adminUser(u *user) map[string]any {
	return map[string]any{"id": u.id, "email": u.email, "username": u.username, "is_active": u.isActive, "is_superuser": u.isSuperuser}
}

func (m *Module) handleSetSuperuser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "user_id")
	if !ok {
		return
	}
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	on := b.boolean("is_superuser", false, true)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	u, err := m.userByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, fmt.Sprintf("User with ID %d not found", id))
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE auth_users SET is_superuser = ?, updated_at = ? WHERE id = ?`,
		on, dbTime(now()), id); err != nil {
		m.internalError(w, err)
		return
	}
	action := "revoked"
	if on {
		action = "granted"
	}
	msg := fmt.Sprintf("Superuser access %s for user %s", action, u.email)
	if u.isSuperuser == on {
		msg = fmt.Sprintf("User %s already has is_superuser=%s", u.email, pyBool(on))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true, "user_id": u.id, "email": u.email, "is_superuser": on, "message": msg,
	})
}

func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func (m *Module) handleAdminGetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "user_id")
	if !ok {
		return
	}
	u, err := m.userByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, fmt.Sprintf("User with ID %d not found", id))
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, adminUser(u))
}

func (m *Module) handleAdminGetUserByEmail(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	u, err := m.userByEmail(r.Context(), email)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, fmt.Sprintf("User with email %s not found", email))
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, adminUser(u))
}

// --- internal.py household validation and user lookup ---

func (m *Module) handleValidateHouseholdAccess(w http.ResponseWriter, r *http.Request, _ *appClient) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	uid, _ := b.integer("user_id", true)
	hh, _ := b.str("household_id", true, 0, 0)
	req := b.role("required_role", "", true)
	if !b.done(w) {
		return
	}
	rl, member, err := membershipRole(r.Context(), m.deps.DB.Read, hh, uid)
	if err != nil {
		m.internalError(w, err)
		return
	}
	invalid := func(reason string) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"valid": false, "user_id": nil, "household_id": nil, "role": nil, "reason": reason,
		})
	}
	if !member {
		invalid("User is not a member of this household")
		return
	}
	if !rl.atLeast(req) {
		invalid(fmt.Sprintf("User has %s role, requires %s or higher", rl, req))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"valid": true, "user_id": uid, "household_id": hh, "role": string(rl), "reason": nil,
	})
}

func (m *Module) handleValidateNodeHousehold(w http.ResponseWriter, r *http.Request, _ *appClient) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	id, _ := b.str("node_id", true, 0, 0)
	hh, _ := b.str("household_id", true, 0, 0)
	if !b.done(w) {
		return
	}
	invalid := func(reason string) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"valid": false, "node_id": nil, "household_id": nil, "reason": reason})
	}
	n, err := nodeByID(r.Context(), m.deps.DB.Read, id)
	if errors.Is(err, sql.ErrNoRows) {
		invalid("Node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n.householdID != hh {
		invalid("Node does not belong to this household")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"valid": true, "node_id": n.nodeID, "household_id": n.householdID, "reason": nil})
}

// handleUsersBatch is GET /internal/users/batch?user_ids=1&user_ids=2: {"users": {"1": username}}.
func (m *Module) handleUsersBatch(w http.ResponseWriter, r *http.Request, _ *appClient) {
	raw := r.URL.Query()["user_ids"]
	if len(raw) == 0 {
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"query", "user_ids"}, Msg: "Field required"})
		return
	}
	var (
		ids  []any
		errs []httpx.FieldError
	)
	for i, s := range raw {
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			errs = append(errs, httpx.FieldError{Type: "int_parsing", Loc: []any{"query", "user_ids", i},
				Msg: "Input should be a valid integer, unable to parse string as an integer", Input: s})
			continue
		}
		ids = append(ids, n)
	}
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	if len(ids) > 100 {
		detail(w, http.StatusBadRequest, "Maximum 100 user IDs per request")
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(r.Context(),
		`SELECT id, username FROM auth_users WHERE id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, ids...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	users := map[string]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			m.internalError(w, err)
			return
		}
		users[strconv.FormatInt(id, 10)] = name
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"users": users})
}

// --- superuser_views.py ---

func (m *Module) handleSuperHouseholds(w http.ResponseWriter, r *http.Request, _ *user) {
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), `SELECT id, name, created_at, updated_at FROM auth_households ORDER BY name ASC`)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var h household
		if err := rows.Scan(&h.id, &h.name, &h.createdAt, &h.updatedAt); err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, h.response())
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleSuperUsers(w http.ResponseWriter, r *http.Request, _ *user) {
	ctx := r.Context()
	byUser := map[int64][]map[string]any{}
	mrows, err := m.deps.DB.Read.QueryContext(ctx, `
		SELECT m.user_id, m.household_id, h.name, m.role
		FROM auth_household_memberships m JOIN auth_households h ON h.id = m.household_id ORDER BY m.id`)
	if err != nil {
		m.internalError(w, err)
		return
	}
	for mrows.Next() {
		var uid int64
		var hh, name, rl string
		if err := mrows.Scan(&uid, &hh, &name, &rl); err != nil {
			mrows.Close()
			m.internalError(w, err)
			return
		}
		byUser[uid] = append(byUser[uid], map[string]any{"household_id": hh, "household_name": name, "role": strings.ToLower(rl)})
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+userCols+` FROM auth_users ORDER BY email ASC`)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		hhs := byUser[u.id]
		if hhs == nil {
			hhs = []map[string]any{}
		}
		out = append(out, map[string]any{
			"id": u.id, "email": u.email, "username": u.username, "is_active": u.isActive,
			"is_superuser": u.isSuperuser, "must_change_password": u.mustChange,
			"created_at": pyTime(u.createdAt), "updated_at": pyTime(u.updatedAt), "households": hhs,
		})
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// tempAlphabet has no 0/O/1/l/I/5/S/8/B: temp passwords get read aloud or retyped.
const tempAlphabet = "abcdefghjkmnpqrtuvwxyz234679ACDEFGHJKMNPQRTUVWXYZ"

func generateTempPassword() string {
	return randomFrom(tempAlphabet, 4) + "-" + randomFrom(tempAlphabet, 4) + "-" + randomFrom(tempAlphabet, 4)
}

// handleTempPassword is POST /superuser/users/{id}/temp-password: set a show-once temporary
// password, force a change at next login, and revoke every session.
func (m *Module) handleTempPassword(w http.ResponseWriter, r *http.Request, acting *user) {
	id, ok := pathInt(w, r, "user_id")
	if !ok {
		return
	}
	b, ok := readBody(w, r, true)
	if !ok {
		return
	}
	temp := b.optStr("temp_password", 8, 255)
	hours, _ := b.optInt("expires_in_hours")
	if hours != nil {
		if *hours < 1 {
			b.fail("expires_in_hours", "greater_than_equal", "Input should be greater than or equal to 1", *hours)
		} else if *hours > 168 {
			b.fail("expires_in_hours", "less_than_equal", "Input should be less than or equal to 168", *hours)
		}
	}
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	u, err := m.userByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "User not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !u.isActive {
		detail(w, http.StatusConflict, "User is deactivated; reactivate the account before issuing a temporary password")
		return
	}
	password := generateTempPassword()
	if temp != nil && *temp != "" {
		password = *temp
	}
	ttl := m.TempPasswordTTL
	if hours != nil && *hours != 0 {
		ttl = time.Duration(*hours) * time.Hour
	}
	expires := now().Add(ttl)
	hash, err := hashSecret(password)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var (
		ids     []int64
		revoked int
	)
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE auth_users SET password_hash = ?, must_change_password = 1,
			temp_password_expires_at = ?, updated_at = ? WHERE id = ?`, hash, dbTime(expires), dbTime(now()), id); err != nil {
			return err
		}
		var err error
		ids, revoked, err = revokeRows(ctx, tx, "user_id = ?", id)
		return err
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.grace.delete(ids...)
	m.deps.Log.Info("temp_password_issued", "target_user_id", id, "acting_user_id", acting.id,
		"expires_at", expires.Format(time.RFC3339Nano), "revoked_tokens", revoked)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"temp_password": password, "expires_at": pyTimeOf(expires), "must_change_password": true,
	})
}

func (m *Module) handleSuperNodes(w http.ResponseWriter, r *http.Request, _ *user) {
	out, err := m.listNodes(r.Context(), "1=1", "name ASC, id")
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
