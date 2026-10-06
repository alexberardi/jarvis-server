package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// role is a household role (HouseholdRole), stored lower-case.
type role string

const (
	roleMember    role = "member"
	rolePowerUser role = "power_user"
	roleAdmin     role = "admin"
)

func (r role) valid() bool { return r == roleMember || r == rolePowerUser || r == roleAdmin }

func (r role) rank() int {
	switch r {
	case roleAdmin:
		return 2
	case rolePowerUser:
		return 1
	}
	return 0
}

// atLeast is HouseholdRole.has_permission.
func (r role) atLeast(req role) bool { return r.rank() >= req.rank() }

// isUnique reports a UNIQUE constraint violation.
func isUnique(err error) bool { return err != nil && strings.Contains(err.Error(), "UNIQUE") }

// --- users ---

type user struct {
	id           int64
	email        string
	username     string
	passwordHash string
	isActive     bool
	isSuperuser  bool
	mustChange   bool
	tempExpires  sql.NullString
	createdAt    string
	updatedAt    string
}

const userCols = `id, email, username, password_hash, is_active, is_superuser, must_change_password,
	temp_password_expires_at, created_at, updated_at`

func scanUser(sc interface{ Scan(...any) error }) (*user, error) {
	var u user
	if err := sc.Scan(&u.id, &u.email, &u.username, &u.passwordHash, &u.isActive, &u.isSuperuser,
		&u.mustChange, &u.tempExpires, &u.createdAt, &u.updatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// tempExpired is User.temp_password_expired.
func (u *user) tempExpired() bool {
	if !u.tempExpires.Valid {
		return false
	}
	t, ok := parseTime(u.tempExpires.String)
	return ok && !now().Before(t)
}

// out is UserOut.
func (u *user) out() map[string]any {
	return map[string]any{
		"id": u.id, "email": u.email, "username": u.username,
		"is_superuser": u.isSuperuser, "must_change_password": u.mustChange,
	}
}

// userByID returns (nil, sql.ErrNoRows) when absent.
func (m *Module) userByID(ctx context.Context, id int64) (*user, error) {
	return scanUser(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+userCols+` FROM auth_users WHERE id = ?`, id))
}

func (m *Module) userByEmail(ctx context.Context, email string) (*user, error) {
	return scanUser(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+userCols+` FROM auth_users WHERE email = ?`, email))
}

// firstHouseholdID is _get_user_household_id: the user's first membership, or "".
func firstHouseholdID(ctx context.Context, q queryer, userID int64) (string, error) {
	var hh string
	err := q.QueryRowContext(ctx,
		`SELECT household_id FROM auth_household_memberships WHERE user_id = ? ORDER BY id LIMIT 1`, userID).Scan(&hh)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return hh, err
}

// --- app clients ---

type appClient struct {
	appID         string
	name          string
	keyHash       string
	isActive      bool
	createdAt     string
	lastRotatedAt sql.NullString
}

func scanApp(sc interface{ Scan(...any) error }) (*appClient, error) {
	var a appClient
	if err := sc.Scan(&a.appID, &a.name, &a.keyHash, &a.isActive, &a.createdAt, &a.lastRotatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

const appCols = `app_id, name, key_hash, is_active, created_at, last_rotated_at`

func (m *Module) appByID(ctx context.Context, id string) (*appClient, error) {
	return scanApp(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+appCols+` FROM auth_app_clients WHERE app_id = ?`, id))
}

// --- memberships ---

// membership returns the user's role in a household; ok=false when not a member.
func membershipRole(ctx context.Context, q queryer, householdID string, userID int64) (role, bool, error) {
	var r string
	err := q.QueryRowContext(ctx,
		`SELECT role FROM auth_household_memberships WHERE household_id = ? AND user_id = ?`, householdID, userID).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return role(strings.ToLower(r)), true, nil
}

// householdExists reports whether a household row exists.
func householdExists(ctx context.Context, q queryer, id string) (string, bool, error) {
	var name string
	err := q.QueryRowContext(ctx, `SELECT name FROM auth_households WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

func count(ctx context.Context, q queryer, query string, args ...any) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}
