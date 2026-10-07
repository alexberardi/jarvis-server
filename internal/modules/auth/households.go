package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// requireMembership is _require_membership: the caller must belong to the TARGET household
// (the path's, not the token's), with at least req when req is set.
func (m *Module) requireMembership(ctx context.Context, q queryer, householdID string, userID int64, req role) (role, error) {
	r, ok, err := membershipRole(ctx, q, householdID, userID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fail(http.StatusForbidden, "Not a member of this household")
	}
	if req != "" && !r.atLeast(req) {
		return "", fail(http.StatusForbidden, "Requires "+string(req)+" role or higher")
	}
	return r, nil
}

type household struct {
	id, name, createdAt, updatedAt string
}

func householdByID(ctx context.Context, q queryer, id string) (*household, error) {
	var h household
	err := q.QueryRowContext(ctx, `SELECT id, name, created_at, updated_at FROM auth_households WHERE id = ?`, id).
		Scan(&h.id, &h.name, &h.createdAt, &h.updatedAt)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (h *household) response() map[string]any {
	return map[string]any{"id": h.id, "name": h.name, "created_at": pyTime(h.createdAt), "updated_at": pyTime(h.updatedAt)}
}

func (m *Module) handleCreateHousehold(w http.ResponseWriter, r *http.Request, u *user) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	name, _ := b.str("name", true, 0, 0)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	var id, created string
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		id, created, err = createHousehold(ctx, tx, name, u.id)
		return err
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name, "created_at": pyTime(created)})
}

func (m *Module) handleListHouseholds(w http.ResponseWriter, r *http.Request, u *user) {
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), `
		SELECT h.id, h.name, m.role, h.created_at
		FROM auth_household_memberships m JOIN auth_households h ON h.id = m.household_id
		WHERE m.user_id = ? ORDER BY m.id`, u.id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, rl, created string
		if err := rows.Scan(&id, &name, &rl, &created); err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "role": strings.ToLower(rl), "created_at": pyTime(created)})
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleGetHousehold(w http.ResponseWriter, r *http.Request, u *user) {
	ctx := r.Context()
	id := r.PathValue("household_id")
	if _, err := m.requireMembership(ctx, m.deps.DB.Read, id, u.id, ""); err != nil {
		m.writeErr(w, err)
		return
	}
	h, err := householdByID(ctx, m.deps.DB.Read, id)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Household not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.response())
}

func (m *Module) handleUpdateHousehold(w http.ResponseWriter, r *http.Request, u *user) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	name, _ := b.str("name", true, 0, 0)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("household_id")
	var h *household
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := m.requireMembership(ctx, tx, id, u.id, roleAdmin); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE auth_households SET name = ?, updated_at = ? WHERE id = ?`, name, dbTime(now()), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fail(http.StatusNotFound, "Household not found")
		}
		h, err = householdByID(ctx, tx, id)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.response())
}

// deleteHousehold removes a household; members, nodes, grants and invites cascade, and the
// household-deleted hooks erase every other module's data for it (D49).
func (m *Module) deleteHousehold(ctx context.Context, tx *sql.Tx, id string) error {
	if err := m.householdDeleted(ctx, tx, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM auth_households WHERE id = ?`, id)
	return err
}

func (m *Module) handleDeleteHousehold(w http.ResponseWriter, r *http.Request, u *user) {
	ctx := r.Context()
	id := r.PathValue("household_id")
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := m.requireMembership(ctx, tx, id, u.id, roleAdmin); err != nil {
			return err
		}
		return m.deleteHousehold(ctx, tx, id)
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.verified.invalidate("node") // its nodes are gone; cheap to forget every cached proof
	w.WriteHeader(http.StatusNoContent)
}

// --- members ---

func memberResponse(userID int64, username, email string, r role, created string) map[string]any {
	return map[string]any{"user_id": userID, "username": username, "email": email, "role": string(r), "created_at": pyTime(created)}
}

func (m *Module) handleListMembers(w http.ResponseWriter, r *http.Request, u *user) {
	ctx := r.Context()
	id := r.PathValue("household_id")
	if _, err := m.requireMembership(ctx, m.deps.DB.Read, id, u.id, ""); err != nil {
		m.writeErr(w, err)
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `
		SELECT u.id, u.username, u.email, m.role, m.created_at
		FROM auth_household_memberships m JOIN auth_users u ON u.id = m.user_id
		WHERE m.household_id = ? ORDER BY m.id`, id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var uid int64
		var username, email, rl, created string
		if err := rows.Scan(&uid, &username, &email, &rl, &created); err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, memberResponse(uid, username, email, role(strings.ToLower(rl)), created))
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleAddMember(w http.ResponseWriter, r *http.Request, u *user) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	uid, _ := b.integer("user_id", true)
	rl := b.role("role", roleMember, false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	var resp map[string]any
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := m.requireMembership(ctx, tx, hh, u.id, roleAdmin); err != nil {
			return err
		}
		if _, found, err := householdExists(ctx, tx, hh); err != nil {
			return err
		} else if !found {
			return fail(http.StatusNotFound, "Household not found")
		}
		target, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM auth_users WHERE id = ?`, uid))
		if errors.Is(err, sql.ErrNoRows) {
			return fail(http.StatusNotFound, "User not found")
		}
		if err != nil {
			return err
		}
		if _, member, err := membershipRole(ctx, tx, hh, uid); err != nil {
			return err
		} else if member {
			return fail(http.StatusBadRequest, "User is already a member of this household")
		}
		if err := addMembership(ctx, tx, hh, uid, rl); err != nil {
			return err
		}
		var created string
		if err := tx.QueryRowContext(ctx, `SELECT created_at FROM auth_household_memberships WHERE household_id = ? AND user_id = ?`,
			hh, uid).Scan(&created); err != nil {
			return err
		}
		resp = memberResponse(target.id, target.username, target.email, rl, created)
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, resp)
}

func (m *Module) handleUpdateMember(w http.ResponseWriter, r *http.Request, u *user) {
	uid, ok := pathInt(w, r, "user_id")
	if !ok {
		return
	}
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	rl := b.role("role", "", true)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	var resp map[string]any
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := m.requireMembership(ctx, tx, hh, u.id, roleAdmin); err != nil {
			return err
		}
		if uid == u.id && rl != roleAdmin {
			return fail(http.StatusBadRequest, "Cannot demote yourself")
		}
		if _, member, err := membershipRole(ctx, tx, hh, uid); err != nil {
			return err
		} else if !member {
			return fail(http.StatusNotFound, "Member not found")
		}
		target, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM auth_users WHERE id = ?`, uid))
		if errors.Is(err, sql.ErrNoRows) {
			return fail(http.StatusNotFound, "User not found")
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE auth_household_memberships SET role = ?, updated_at = ?
			WHERE household_id = ? AND user_id = ?`, string(rl), dbTime(now()), hh, uid); err != nil {
			return err
		}
		var created string
		if err := tx.QueryRowContext(ctx, `SELECT created_at FROM auth_household_memberships WHERE household_id = ? AND user_id = ?`,
			hh, uid).Scan(&created); err != nil {
			return err
		}
		resp = memberResponse(target.id, target.username, target.email, rl, created)
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) handleRemoveMember(w http.ResponseWriter, r *http.Request, u *user) {
	uid, ok := pathInt(w, r, "user_id")
	if !ok {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := m.requireMembership(ctx, tx, hh, u.id, roleAdmin); err != nil {
			return err
		}
		if uid == u.id {
			return fail(http.StatusBadRequest, "Cannot kick yourself. Use POST /households/{id}/leave instead.")
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM auth_household_memberships WHERE household_id = ? AND user_id = ?`, hh, uid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fail(http.StatusNotFound, "Member not found")
		}
		return m.memberRemoved(ctx, tx, uid, hh)
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLeave is POST /households/{id}/leave. Not your only household; not the last admin of a
// household others remain in; the last member out deletes the household.
func (m *Module) handleLeave(w http.ResponseWriter, r *http.Request, u *user) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	deleted := false
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		rl, member, err := membershipRole(ctx, tx, hh, u.id)
		if err != nil {
			return err
		}
		if !member {
			return fail(http.StatusNotFound, "Not a member of this household")
		}
		mine, err := count(ctx, tx, `SELECT COUNT(*) FROM auth_household_memberships WHERE user_id = ?`, u.id)
		if err != nil {
			return err
		}
		if mine <= 1 {
			return fail(http.StatusBadRequest, "Cannot leave your only household. Join or create another household first.")
		}
		total, err := count(ctx, tx, `SELECT COUNT(*) FROM auth_household_memberships WHERE household_id = ?`, hh)
		if err != nil {
			return err
		}
		if total > 1 && rl == roleAdmin {
			others, err := count(ctx, tx, `SELECT COUNT(*) FROM auth_household_memberships
				WHERE household_id = ? AND user_id != ? AND role = 'admin'`, hh, u.id)
			if err != nil {
				return err
			}
			if others == 0 {
				return fail(http.StatusBadRequest, "You're the only admin. Promote another member to admin before leaving.")
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_household_memberships WHERE household_id = ? AND user_id = ?`, hh, u.id); err != nil {
			return err
		}
		if err := m.memberRemoved(ctx, tx, u.id, hh); err != nil {
			return err
		}
		if total == 1 {
			deleted = true
			return m.deleteHousehold(ctx, tx, hh)
		}
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if deleted {
		m.verified.invalidate("node")
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"left": true, "household_id": hh, "household_deleted": deleted})
}

// --- household nodes ---

func (m *Module) handleListHouseholdNodes(w http.ResponseWriter, r *http.Request, u *user) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if _, err := m.requireMembership(ctx, m.deps.DB.Read, hh, u.id, ""); err != nil {
		m.writeErr(w, err)
		return
	}
	out, err := m.listNodes(ctx, "household_id = ?", "id", hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleRegisterHouseholdNode is POST /households/{id}/nodes (power_user or admin). The caller
// is recorded as the registering user, whatever the payload says.
func (m *Module) handleRegisterHouseholdNode(w http.ResponseWriter, r *http.Request, u *user) {
	in, ok := readNodeInput(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	key := tokenURLSafe(48)
	hash, err := hashSecret(key)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var created string
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := m.requireMembership(ctx, tx, hh, u.id, rolePowerUser); err != nil {
			return err
		}
		if in.householdID != hh {
			return fail(http.StatusBadRequest, "Household ID in payload must match URL")
		}
		if _, err := nodeByID(ctx, tx, in.nodeID); err == nil {
			return fail(http.StatusBadRequest, "node_id already exists")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		uid := u.id
		var err error
		created, err = insertNode(ctx, tx, in, hash, &uid, in.services, &uid)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	uid := u.id
	httpx.WriteJSON(w, http.StatusCreated, createdNodeResponse(in, &uid, key, created, in.services))
}

// --- invites ---

// codeAlphabet has no ambiguous characters (0/O/1/I/L).
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

type invite struct {
	id          int64
	householdID string
	code        string
	defaultRole role
	maxUses     sql.NullInt64
	useCount    int64
	expiresAt   string
	revoked     bool
	createdAt   string
}

const inviteCols = `id, household_id, code, default_role, max_uses, use_count, expires_at, revoked, created_at`

func scanInvite(sc interface{ Scan(...any) error }) (*invite, error) {
	var i invite
	var rl string
	if err := sc.Scan(&i.id, &i.householdID, &i.code, &rl, &i.maxUses, &i.useCount, &i.expiresAt, &i.revoked, &i.createdAt); err != nil {
		return nil, err
	}
	i.defaultRole = role(strings.ToLower(rl))
	return &i, nil
}

func (i *invite) response() map[string]any {
	return map[string]any{
		"id": i.id, "household_id": i.householdID, "code": i.code, "default_role": string(i.defaultRole),
		"max_uses": nullInt(i.maxUses), "use_count": i.useCount, "expires_at": pyTime(i.expiresAt),
		"revoked": i.revoked, "created_at": pyTime(i.createdAt),
	}
}

// validInvite is _get_valid_invite: nil unless the code exists, isn't revoked or expired, and
// has uses left.
func validInvite(ctx context.Context, q queryer, code string) (*invite, error) {
	i, err := scanInvite(q.QueryRowContext(ctx, `SELECT `+inviteCols+` FROM auth_household_invites WHERE code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if i.revoked {
		return nil, nil
	}
	if exp, ok := parseTime(i.expiresAt); !ok || !now().Before(exp) {
		return nil, nil
	}
	if i.maxUses.Valid && i.useCount >= i.maxUses.Int64 {
		return nil, nil
	}
	return i, nil
}

func (m *Module) handleCreateInvite(w http.ResponseWriter, r *http.Request, u *user) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	rl := b.role("default_role", roleMember, false)
	maxUses, _ := b.optInt("max_uses")
	days := int64(7)
	if v, present := b.integer("expires_in_days", false); present {
		switch {
		case v < 1:
			b.fail("expires_in_days", "greater_than_equal", "Input should be greater than or equal to 1", v)
		case v > 90:
			b.fail("expires_in_days", "less_than_equal", "Input should be less than or equal to 90", v)
		default:
			days = v
		}
	}
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	var inv *invite
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := m.requireMembership(ctx, tx, hh, u.id, rolePowerUser); err != nil {
			return err
		}
		if rl == roleAdmin {
			return fail(http.StatusBadRequest, "Invite codes cannot assign admin role")
		}
		t := now()
		for range 20 {
			code := randomFrom(codeAlphabet, 8)
			res, err := tx.ExecContext(ctx, `INSERT INTO auth_household_invites
				(household_id, code, created_by_user_id, default_role, max_uses, use_count, expires_at, revoked, created_at)
				VALUES (?, ?, ?, ?, ?, 0, ?, 0, ?)`, hh, code, u.id, string(rl), maxUses,
				dbTime(t.Add(time.Duration(days)*24*time.Hour)), dbTime(t))
			if isUnique(err) {
				continue // collision: draw another code
			}
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			inv, err = scanInvite(tx.QueryRowContext(ctx, `SELECT `+inviteCols+` FROM auth_household_invites WHERE id = ?`, id))
			return err
		}
		return errors.New("auth: could not draw a unique invite code")
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, inv.response())
}

func (m *Module) handleListInvites(w http.ResponseWriter, r *http.Request, u *user) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if _, err := m.requireMembership(ctx, m.deps.DB.Read, hh, u.id, ""); err != nil {
		m.writeErr(w, err)
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+inviteCols+` FROM auth_household_invites
		WHERE household_id = ? AND revoked = 0 ORDER BY created_at DESC, id DESC`, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, i.response())
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleRevokeInvite(w http.ResponseWriter, r *http.Request, u *user) {
	id, ok := pathInt(w, r, "invite_id")
	if !ok {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := m.requireMembership(ctx, tx, hh, u.id, rolePowerUser); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE auth_household_invites SET revoked = 1 WHERE id = ? AND household_id = ?`, id, hh)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fail(http.StatusNotFound, "Invite not found")
		}
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleValidateInvite is GET /invites/{code}/validate (public; generic invalid on any failure).
func (m *Module) handleValidateInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	i, err := validInvite(ctx, m.deps.DB.Read, strings.ToUpper(r.PathValue("code")))
	if err != nil {
		m.internalError(w, err)
		return
	}
	if i == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"valid": false, "household_name": nil})
		return
	}
	name, found, err := householdExists(ctx, m.deps.DB.Read, i.householdID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var hn any
	if found {
		hn = name
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"valid": true, "household_name": hn})
}

// handleJoin is POST /households/join with an invite code.
func (m *Module) handleJoin(w http.ResponseWriter, r *http.Request, u *user) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	code, _ := b.str("invite_code", true, 0, 0)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	var resp map[string]any
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		i, err := validInvite(ctx, tx, strings.ToUpper(code))
		if err != nil {
			return err
		}
		if i == nil {
			return fail(http.StatusBadRequest, "Invalid or expired invite code")
		}
		if _, member, err := membershipRole(ctx, tx, i.householdID, u.id); err != nil {
			return err
		} else if member {
			return fail(http.StatusBadRequest, "Already a member of this household")
		}
		name, found, err := householdExists(ctx, tx, i.householdID)
		if err != nil {
			return err
		}
		if !found {
			return fail(http.StatusBadRequest, "Invalid or expired invite code")
		}
		if err := addMembership(ctx, tx, i.householdID, u.id, i.defaultRole); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE auth_household_invites SET use_count = use_count + 1 WHERE id = ?`, i.id); err != nil {
			return err
		}
		resp = map[string]any{"household_id": i.householdID, "household_name": name, "role": string(i.defaultRole)}
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
