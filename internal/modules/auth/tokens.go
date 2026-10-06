package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// newUUID returns a random (v4) UUID string.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// httpErr is an error carrying a FastAPI-style response, returned out of transactions.
type httpErr struct {
	status int
	detail string
}

func (e *httpErr) Error() string { return e.detail }

func fail(status int, msg string) error { return &httpErr{status, msg} }

// writeErr writes an httpErr, or a 500 for anything else.
func (m *Module) writeErr(w http.ResponseWriter, err error) {
	var he *httpErr
	if errors.As(err, &he) {
		detail(w, he.status, he.detail)
		return
	}
	m.internalError(w, err)
}

// --- token pairs ---

// insertRefresh stores a new refresh token (sha256 of the plain token) and returns the plain
// token and its row id. parent=0 starts a new family.
func (m *Module) insertRefresh(ctx context.Context, tx *sql.Tx, userID int64, family string, parent int64) (string, int64, error) {
	plain := tokenURLSafe(48)
	days := m.settings.Int(ctx, settingRefreshDays, noScope)
	exp := now().Add(time.Duration(days) * 24 * time.Hour)
	var parentArg any
	if parent != 0 {
		parentArg = parent
	}
	if family == "" {
		family = newUUID()
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO auth_refresh_tokens (user_id, token_hash, expires_at, revoked, created_at, family_id, parent_id)
		VALUES (?, ?, ?, 0, ?, ?, ?)`, userID, sha256Hex(plain), dbTime(exp), dbTime(now()), family, parentArg)
	if err != nil {
		return "", 0, err
	}
	id, err := res.LastInsertId()
	return plain, id, err
}

// accessFor mints an access token for u with their first household (_build_jwt_claims).
func (m *Module) accessFor(ctx context.Context, u *user) (string, error) {
	hh, err := firstHouseholdID(ctx, m.deps.DB.Read, u.id)
	if err != nil {
		return "", err
	}
	return m.mintAccess(ctx, accessClaims{userID: u.id, email: u.email, isSuperuser: u.isSuperuser, householdID: hh})
}

func tokenResponse(access, refresh string, u *user) map[string]any {
	return map[string]any{
		"access_token": access, "refresh_token": refresh, "token_type": "bearer",
		"user": u.out(), "must_change_password": u.mustChange,
	}
}

// revokeRows marks rows revoked and returns every row id touched (for the grace-cache purge)
// and how many were newly revoked (services/token_revocation.py).
func revokeRows(ctx context.Context, tx *sql.Tx, where string, arg any) ([]int64, int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, revoked FROM auth_refresh_tokens WHERE `+where, arg)
	if err != nil {
		return nil, 0, err
	}
	var ids []int64
	n := 0
	for rows.Next() {
		var id int64
		var revoked bool
		if err := rows.Scan(&id, &revoked); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
		if !revoked {
			n++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE auth_refresh_tokens SET revoked = 1 WHERE `+where, arg)
	return ids, n, err
}

// --- register / setup ---

type registerInput struct {
	email, password string
	username        *string
	inviteCode      *string
}

func readRegister(w http.ResponseWriter, r *http.Request) (registerInput, bool) {
	b, ok := readBody(w, r, false)
	if !ok {
		return registerInput{}, false
	}
	var in registerInput
	in.email, _ = b.email("email")
	in.username = b.optStr("username", 0, 0)
	in.password, _ = b.str("password", true, 8, 255)
	in.inviteCode = b.optStr("invite_code", 0, 0)
	return in, b.done(w)
}

// usernameFor is _ensure_username: the given username, else the email's local part.
func usernameFor(email string, username *string) string {
	if username != nil && *username != "" {
		return *username
	}
	return strings.SplitN(email, "@", 2)[0]
}

// insertUser creates a user, translating unique violations into the legacy 400s.
func insertUser(ctx context.Context, tx *sql.Tx, email, username, hash string, superuser bool) (int64, error) {
	t := dbTime(now())
	res, err := tx.ExecContext(ctx, `
		INSERT INTO auth_users (email, username, password_hash, is_active, is_superuser, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?)`, email, username, hash, superuser, t, t)
	if isUnique(err) {
		if strings.Contains(err.Error(), "email") {
			return 0, fail(http.StatusBadRequest, "Email already registered")
		}
		// Deviation: legacy let the IntegrityError escape as a 500 when the defaulted username
		// (email local part) was taken; this is auth_service's detail for the same case.
		return 0, fail(http.StatusBadRequest, "Username already taken")
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// createHousehold inserts a household with the user as admin.
func createHousehold(ctx context.Context, tx *sql.Tx, name string, userID int64) (string, string, error) {
	id, t := newUUID(), dbTime(now())
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO auth_households (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, id, name, t, t); err != nil {
		return "", "", err
	}
	if err := addMembership(ctx, tx, id, userID, roleAdmin); err != nil {
		return "", "", err
	}
	return id, t, nil
}

func addMembership(ctx context.Context, tx *sql.Tx, householdID string, userID int64, r role) error {
	t := dbTime(now())
	_, err := tx.ExecContext(ctx, `
		INSERT INTO auth_household_memberships (household_id, user_id, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, householdID, userID, string(r), t, t)
	return err
}

// handleRegister is POST /auth/register: create the user, join the invite's household (or
// create "My Home" as admin), and log in.
//
// Deviation (D5, security): the legacy X-Household-Id header let anyone who knew a
// household's id register straight into it, unauthenticated. No client sends it. An unknown
// id still gets the legacy 400 "Household not found"; an existing one is refused like a bad
// invite, since an invite code is now the only way into an existing household.
func (m *Module) handleRegister(w http.ResponseWriter, r *http.Request) {
	in, ok := readRegister(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	hasInvite := in.inviteCode != nil && *in.inviteCode != ""
	if _, err := m.userByEmail(ctx, in.email); err == nil {
		detail(w, http.StatusBadRequest, "Email already registered")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		m.internalError(w, err)
		return
	}
	if hh := r.Header.Get("X-Household-Id"); hh != "" && !hasInvite {
		_, found, err := householdExists(ctx, m.deps.DB.Read, hh)
		if err != nil {
			m.internalError(w, err)
			return
		}
		if !found {
			detail(w, http.StatusBadRequest, "Household not found")
		} else {
			detail(w, http.StatusBadRequest, "Invalid or expired invite code")
		}
		return
	}
	var inv *invite
	if hasInvite {
		var err error
		if inv, err = validInvite(ctx, m.deps.DB.Read, strings.ToUpper(*in.inviteCode)); err != nil {
			m.internalError(w, err)
			return
		}
		if inv == nil {
			detail(w, http.StatusBadRequest, "Invalid or expired invite code")
			return
		}
	}
	hash, err := hashSecret(in.password)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var (
		u       *user
		hh      string
		refresh string
	)
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		id, err := insertUser(ctx, tx, in.email, usernameFor(in.email, in.username), hash, false)
		if err != nil {
			return err
		}
		if inv != nil {
			// Re-check under the write lock so max_uses can't be overrun by a race.
			res, err := tx.ExecContext(ctx, `UPDATE auth_household_invites SET use_count = use_count + 1
				WHERE id = ? AND revoked = 0 AND (max_uses IS NULL OR use_count < max_uses)`, inv.id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fail(http.StatusBadRequest, "Invalid or expired invite code")
			}
			if _, found, err := householdExists(ctx, tx, inv.householdID); err != nil {
				return err
			} else if !found {
				return fail(http.StatusBadRequest, "Household not found")
			}
			hh = inv.householdID
			if err := addMembership(ctx, tx, hh, id, inv.defaultRole); err != nil {
				return err
			}
		} else if hh, _, err = createHousehold(ctx, tx, "My Home", id); err != nil {
			return err
		}
		if refresh, _, err = m.insertRefresh(ctx, tx, id, "", 0); err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM auth_users WHERE id = ?`, id))
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.respondRegistered(w, r, u, hh, refresh)
}

func (m *Module) respondRegistered(w http.ResponseWriter, r *http.Request, u *user, hh, refresh string) {
	access, err := m.accessFor(r.Context(), u)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"access_token": access, "refresh_token": refresh, "token_type": "bearer",
		"user": u.out(), "household_id": hh,
	})
}

// handleSetupStatus is GET /auth/setup-status.
func (m *Module) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := count(r.Context(), m.deps.DB.Read, `SELECT COUNT(*) FROM auth_users WHERE is_superuser = 1`)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"needs_setup": n == 0})
}

// handleSetup is POST /auth/setup: create the first superuser, only while none exists.
func (m *Module) handleSetup(w http.ResponseWriter, r *http.Request) {
	in, ok := readRegister(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	hash, err := hashSecret(in.password)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var (
		u       *user
		hh      string
		refresh string
	)
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		// Checked inside the write transaction so two racing setups can't both win.
		if n, err := count(ctx, tx, `SELECT COUNT(*) FROM auth_users WHERE is_superuser = 1`); err != nil {
			return err
		} else if n > 0 {
			return fail(http.StatusConflict, "Setup already completed")
		}
		if n, err := count(ctx, tx, `SELECT COUNT(*) FROM auth_users WHERE email = ?`, in.email); err != nil {
			return err
		} else if n > 0 {
			return fail(http.StatusBadRequest, "Email already registered")
		}
		id, err := insertUser(ctx, tx, in.email, usernameFor(in.email, in.username), hash, true)
		if err != nil {
			return err
		}
		if hh, _, err = createHousehold(ctx, tx, "My Home", id); err != nil {
			return err
		}
		if refresh, _, err = m.insertRefresh(ctx, tx, id, "", 0); err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM auth_users WHERE id = ?`, id))
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.respondRegistered(w, r, u, hh, refresh)
}

// --- login ---

// handleLogin is POST /auth/login. Unknown emails still pay a bcrypt check (timing), and
// failures count towards a per-(email, IP) lockout.
func (m *Module) handleLogin(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	email, _ := b.email("email")
	password, _ := b.str("password", true, 0, 0)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	ip := m.clientIP(r)
	rl := m.limiter
	if !rl.cfg.Disabled && rl.locked(email, ip) {
		w.Header().Set("Retry-After", fmt.Sprint(int(rl.cfg.LoginLockout.Seconds())))
		detail(w, http.StatusTooManyRequests, "Too many failed login attempts. Please try again later.")
		return
	}
	u, err := m.userByEmail(ctx, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.internalError(w, err)
		return
	}
	hash := dummyHash()
	if u != nil {
		hash = u.passwordHash
	}
	valid := verifySecret(password, hash)
	if u == nil || !valid {
		if !rl.cfg.Disabled {
			rl.recordFailure(email, ip)
		}
		detail(w, http.StatusUnauthorized, "Invalid email or password")
		return
	}
	if !u.isActive {
		detail(w, http.StatusUnauthorized, "Invalid email or password")
		return
	}
	if u.mustChange && u.tempExpired() {
		detail(w, http.StatusUnauthorized, "Temporary password expired. Ask your administrator for a new one.")
		return
	}
	if !rl.cfg.Disabled {
		rl.clearFailures(email, ip)
	}
	var refresh string
	if err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		refresh, _, err = m.insertRefresh(ctx, tx, u.id, "", 0)
		return err
	}); err != nil {
		m.internalError(w, err)
		return
	}
	access, err := m.accessFor(ctx, u)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tokenResponse(access, refresh, u))
}

// --- refresh ---

type refreshRow struct {
	id        int64
	userID    int64
	expiresAt string
	revoked   bool
	familyID  string
	rotatedAt sql.NullString
}

const refreshCols = `id, user_id, expires_at, revoked, family_id, rotated_at`

func scanRefresh(sc interface{ Scan(...any) error }) (*refreshRow, error) {
	var t refreshRow
	if err := sc.Scan(&t.id, &t.userID, &t.expiresAt, &t.revoked, &t.familyID, &t.rotatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func (t *refreshRow) expired() bool {
	exp, ok := parseTime(t.expiresAt)
	return !ok || !now().Before(exp)
}

// handleRefresh is POST /auth/refresh: rotate the presented token within its family.
//
//   - A token rotated less than auth.token.refresh_grace_seconds ago gets the successor it
//     already produced (a benign double submit), from the in-process cache.
//   - Any other replay of a rotated or revoked token is a 401. The family is revoked too only
//     when RevokeFamilyOnReuse is set (legacy default off: replays are far likelier benign).
//
// The whole check-and-rotate runs in one write transaction; jarvisd has a single writer, so
// it serializes with revocation (the legacy user-row lock).
func (m *Module) handleRefresh(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	presented, _ := b.str("refresh_token", true, 0, 0)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hash := sha256Hex(presented)
	rec, err := scanRefresh(m.deps.DB.Read.QueryRowContext(ctx,
		`SELECT `+refreshCols+` FROM auth_refresh_tokens WHERE token_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusUnauthorized, "Invalid refresh token")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if rec.expired() {
		detail(w, http.StatusUnauthorized, "Refresh token expired")
		return
	}
	grace := time.Duration(m.settings.Int(ctx, settingGraceSeconds, noScope)) * time.Second

	var (
		u        *user
		newPlain string
		cached   string
	)
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM auth_users WHERE id = ?`, rec.userID))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !u.isActive) {
			return fail(http.StatusUnauthorized, "Invalid refresh token")
		}
		if err != nil {
			return err
		}
		if u.mustChange && u.tempExpired() {
			return fail(http.StatusUnauthorized, "Temporary password expired. Ask your administrator for a new one.")
		}
		// Re-read under the write lock: a revocation may have landed since the first read.
		rec, err = scanRefresh(tx.QueryRowContext(ctx, `SELECT `+refreshCols+` FROM auth_refresh_tokens WHERE id = ?`, rec.id))
		if errors.Is(err, sql.ErrNoRows) {
			return fail(http.StatusUnauthorized, "Invalid refresh token")
		}
		if err != nil {
			return err
		}
		if rec.revoked || rec.rotatedAt.Valid {
			if !rec.revoked {
				if rot, ok := parseTime(rec.rotatedAt.String); ok && now().Sub(rot) <= grace {
					if cached = m.grace.get(rec.id); cached != "" {
						return nil
					}
					// A miss inside the window means the process restarted after rotating.
					// Like legacy, it falls through to the reuse handling below.
				}
			}
			m.deps.Log.Warn("refresh_token_reuse_detected", "family_id", rec.familyID, "user_id", rec.userID,
				"presented_token_id", rec.id, "revoked_family", m.RevokeFamilyOnReuse)
			if m.RevokeFamilyOnReuse {
				return errReuse
			}
			return fail(http.StatusUnauthorized, "Invalid refresh token")
		}
		if newPlain, _, err = m.insertRefresh(ctx, tx, rec.userID, rec.familyID, rec.id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE auth_refresh_tokens SET rotated_at = ? WHERE id = ?`, dbTime(now()), rec.id)
		return err
	})
	if errors.Is(err, errReuse) {
		// The family revocation is committed separately: the request still fails.
		if err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE auth_refresh_tokens SET revoked = 1 WHERE family_id = ?`, rec.familyID)
			return err
		}); err != nil {
			m.deps.Log.Error("auth: revoke family on reuse", "err", err)
		}
		detail(w, http.StatusUnauthorized, "Invalid refresh token")
		return
	}
	if err != nil {
		m.writeErr(w, err)
		return
	}
	refresh := cached
	if cached == "" {
		refresh = newPlain
		m.grace.set(rec.id, newPlain, grace)
	}
	access, err := m.accessFor(ctx, u)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tokenResponse(access, refresh, u))
}

// errReuse aborts the refresh transaction; the family revocation is then committed alone.
var errReuse = errors.New("auth: refresh token reuse")

// --- change password / logout ---

// handleChangePassword is POST /auth/change-password: revoke every session and return a fresh
// pair for this one (which the client must adopt).
func (m *Module) handleChangePassword(w http.ResponseWriter, r *http.Request, u *user) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	current, _ := b.str("current_password", true, 0, 0)
	next, _ := b.str("new_password", true, 8, 255)
	if !b.done(w) {
		return
	}
	if !verifySecret(current, u.passwordHash) {
		detail(w, http.StatusUnauthorized, "Incorrect password")
		return
	}
	if next == current {
		detail(w, http.StatusBadRequest, "New password must be different from the current password")
		return
	}
	hash, err := hashSecret(next)
	if err != nil {
		m.internalError(w, err)
		return
	}
	ctx := r.Context()
	var (
		refresh string
		ids     []int64
		revoked int
	)
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE auth_users SET password_hash = ?, must_change_password = 0,
			temp_password_expires_at = NULL, updated_at = ? WHERE id = ?`, hash, dbTime(now()), u.id); err != nil {
			return err
		}
		var err error
		if ids, revoked, err = revokeRows(ctx, tx, "user_id = ?", u.id); err != nil {
			return err
		}
		refresh, _, err = m.insertRefresh(ctx, tx, u.id, "", 0)
		return err
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.grace.delete(ids...)
	m.deps.Log.Info("password_changed", "user_id", u.id, "revoked_tokens", revoked)
	u.passwordHash, u.mustChange, u.tempExpires = hash, false, sql.NullString{}
	access, err := m.accessFor(ctx, u)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tokenResponse(access, refresh, u))
}

// handleLogout is POST /auth/logout: revoke the presented token's family (or every session).
// It authenticates with the refresh token itself and is always 204.
func (m *Module) handleLogout(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	presented, _ := b.str("refresh_token", true, 0, 0)
	all := b.boolean("all_devices", false, false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	rec, err := scanRefresh(m.deps.DB.Read.QueryRowContext(ctx,
		`SELECT `+refreshCols+` FROM auth_refresh_tokens WHERE token_hash = ?`, sha256Hex(presented)))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.internalError(w, err)
		return
	}
	if rec != nil {
		var (
			ids []int64
			n   int
		)
		err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
			var err error
			if all {
				ids, n, err = revokeRows(ctx, tx, "user_id = ?", rec.userID)
			} else {
				ids, n, err = revokeRows(ctx, tx, "family_id = ?", rec.familyID)
			}
			return err
		})
		if err != nil {
			m.internalError(w, err)
			return
		}
		m.grace.delete(ids...)
		m.deps.Log.Info("user_logged_out", "user_id", rec.userID, "all_devices", all, "revoked_tokens", n)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMe is GET /auth/me.
func (m *Module) handleMe(w http.ResponseWriter, _ *http.Request, u *user) {
	httpx.WriteJSON(w, http.StatusOK, u.out())
}

// handleSwitchHousehold is POST /auth/switch-household: a new access token for another of the
// caller's households.
func (m *Module) handleSwitchHousehold(w http.ResponseWriter, r *http.Request, u *user) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	hh, _ := b.str("household_id", true, 0, 0)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	_, member, err := membershipRole(ctx, m.deps.DB.Read, hh, u.id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !member {
		detail(w, http.StatusForbidden, "Not a member of this household")
		return
	}
	access, err := m.mintAccess(ctx, accessClaims{userID: u.id, email: u.email, isSuperuser: u.isSuperuser, householdID: hh})
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"access_token": access, "household_id": hh})
}
