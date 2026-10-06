package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Legacy services whose user data was purged over HTTP on account deletion, in order
// (services/account_deletion.py). Each is skipped once jarvisd serves it (InProcess); its
// module registers an OnUserDeleted hook instead.
var purgeServices = []string{"jarvis-command-center", "jarvis-notifications"}

const purgePath = "/api/v0/me/data"

const deletionFailed = "Could not complete account deletion. Please try again."

// handleDeleteMe is DELETE /auth/me (decision D20). In order:
//
//  1. verify the password;
//  2. guards: no active node registered by the user; not the only admin of a household that
//     has other members;
//  3. the legacy HTTP purge for services not yet served by jarvisd (strangler phase), with
//     the legacy semantics: 2xx/404 ok, unreachable logged and skipped, 5xx or another 4xx
//     aborts with 502 before anything local is touched;
//  4. one write transaction: every OnUserDeleted hook, the user's auth settings, their
//     memberships (deleting households left empty, with their nodes and invites), then the
//     user (refresh tokens cascade). Any error rolls the whole thing back.
func (m *Module) handleDeleteMe(w http.ResponseWriter, r *http.Request, u *user) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	password, _ := b.str("password", true, 0, 0)
	if !b.done(w) {
		return
	}
	if !verifySecret(password, u.passwordHash) {
		detail(w, http.StatusUnauthorized, "Incorrect password")
		return
	}
	ctx := r.Context()
	if err := m.deletionGuards(ctx, u.id); err != nil {
		m.writeErr(w, err)
		return
	}
	token, _ := bearer(r)
	if err := m.purgeDownstream(ctx, token); err != nil {
		m.writeErr(w, err)
		return
	}
	m.hookMu.Lock()
	hooks := slices.Clone(m.hooks)
	m.hookMu.Unlock()
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, h := range hooks {
			if err := h(ctx, tx, u.id); err != nil {
				return fmt.Errorf("user-deleted hook: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_settings WHERE user_id = ?`, u.id); err != nil {
			return err
		}
		hhs, err := userHouseholds(ctx, tx, u.id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_household_memberships WHERE user_id = ?`, u.id); err != nil {
			return err
		}
		for _, hh := range hhs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM auth_households WHERE id = ?
				AND NOT EXISTS (SELECT 1 FROM auth_household_memberships WHERE household_id = ?)`, hh, hh); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM auth_users WHERE id = ?`, u.id)
		return err
	})
	if err != nil {
		m.deps.Log.Error("auth: local account deletion failed", "user_id", u.id, "err", err)
		m.internalError(w, err)
		return
	}
	m.verified.invalidate("node")
	m.deps.Log.Info("account_deleted", "user_id", u.id)
	w.WriteHeader(http.StatusNoContent)
}

func userHouseholds(ctx context.Context, q queryer, userID int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT household_id FROM auth_household_memberships WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var hh string
		if err := rows.Scan(&hh); err != nil {
			return nil, err
		}
		out = append(out, hh)
	}
	return out, rows.Err()
}

// deletionGuards are the 409s that keep an account from being deleted.
func (m *Module) deletionGuards(ctx context.Context, userID int64) error {
	q := m.deps.DB.Read
	n, err := count(ctx, q, `SELECT COUNT(*) FROM auth_node_registrations WHERE registered_by_user_id = ? AND is_active = 1`, userID)
	if err != nil {
		return err
	}
	if n > 0 {
		return fail(http.StatusConflict, "Cannot delete account with nodes registered to it")
	}
	// Only the sole admin of a household with other members blocks: a solo household is
	// deleted with the account, and a shared one with another admin is fine.
	n, err = count(ctx, q, `
		SELECT COUNT(*) FROM auth_household_memberships m
		WHERE m.user_id = ? AND m.role = 'admin'
		  AND (SELECT COUNT(*) FROM auth_household_memberships o WHERE o.household_id = m.household_id) > 1
		  AND NOT EXISTS (SELECT 1 FROM auth_household_memberships a
		                  WHERE a.household_id = m.household_id AND a.user_id != m.user_id AND a.role = 'admin')`, userID)
	if err != nil {
		return err
	}
	if n > 0 {
		return fail(http.StatusConflict, "Cannot delete your account while you are the only admin of a household with other members. Make another member an admin first.")
	}
	return nil
}

// purgeDownstream calls DELETE <service>/api/v0/me/data with the user's bearer token for each
// legacy service jarvisd doesn't serve, resolving URLs from the config module's registry.
func (m *Module) purgeDownstream(ctx context.Context, token string) error {
	for _, name := range purgeServices {
		if slices.Contains(m.InProcess, name) {
			continue
		}
		base, err := m.serviceURL(ctx, name)
		if err != nil {
			m.deps.Log.Warn("auth: registry lookup failed; skipping purge", "service", name, "err", err)
			continue
		}
		if base == "" {
			m.deps.Log.Info("auth: service not registered; skipping purge", "service", name)
			continue
		}
		if err := m.purgeOne(ctx, name, base, token); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) purgeOne(ctx context.Context, name, base, token string) error {
	url := strings.TrimRight(base, "/") + purgePath
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := m.client.Do(req)
	if err != nil {
		// Unreachable: most likely not deployed in this install. Best effort.
		m.deps.Log.Warn("auth: downstream purge unreachable", "service", name, "url", url, "err", err)
		return nil
	}
	res.Body.Close()
	if res.StatusCode < 300 || res.StatusCode == http.StatusNotFound {
		return nil
	}
	m.deps.Log.Error("auth: downstream purge failed; aborting account deletion", "service", name, "status", res.StatusCode)
	return fail(http.StatusBadGateway, deletionFailed)
}

// serviceURL reads a service's base URL from the config module's registry table (same DB).
// "" when the service isn't registered or the table doesn't exist.
func (m *Module) serviceURL(ctx context.Context, name string) (string, error) {
	var scheme, host string
	var port int
	err := m.deps.DB.Read.QueryRowContext(ctx,
		`SELECT scheme, host, port FROM config_services WHERE name = ?`, name).Scan(&scheme, &host, &port)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return "", nil
		}
		return "", err
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port), nil
}
