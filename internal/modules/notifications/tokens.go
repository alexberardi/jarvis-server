package notifications

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// DeviceToken is a registered push token, in the legacy TokenResponse shape.
type DeviceToken struct {
	ID         string  `json:"id"`
	PushToken  string  `json:"push_token"`
	DeviceType string  `json:"device_type"`
	DeviceName *string `json:"device_name"`
	IsActive   bool    `json:"is_active"`
}

const tokenCols = `id, push_token, device_type, device_name, is_active`

func scanToken(sc interface{ Scan(...any) error }) (DeviceToken, error) {
	var t DeviceToken
	var name sql.NullString
	if err := sc.Scan(&t.ID, &t.PushToken, &t.DeviceType, &name, &t.IsActive); err != nil {
		return DeviceToken{}, err
	}
	if name.Valid {
		t.DeviceName = &name.String
	}
	return t, nil
}

// handleRegisterToken upserts by push_token: a token moves to whoever registers it last and
// is reactivated.
func (m *Module) handleRegisterToken(w http.ResponseWriter, r *http.Request, u authn.User) {
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	pushToken, _ := o.str("push_token", true)
	deviceType, _ := o.str("device_type", true)
	var name any
	if s, ok := o.str("device_name", false); ok {
		name = s
	}
	if !o.done(w) {
		return
	}
	if deviceType != "ios" && deviceType != "android" {
		httpx.Error(w, http.StatusBadRequest, "device_type must be 'ios' or 'android'")
		return
	}
	if u.HouseholdID == "" {
		httpx.Error(w, http.StatusBadRequest, "User JWT missing household_id claim")
		return
	}
	var t DeviceToken
	err := m.deps.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		now := ts(m.now())
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO notifications_device_tokens
			(id, user_id, household_id, push_token, device_type, device_name, is_active, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT (push_token) DO UPDATE SET user_id = excluded.user_id, household_id = excluded.household_id,
				device_type = excluded.device_type, device_name = excluded.device_name, is_active = 1,
				updated_at = excluded.updated_at`,
			newUUID(), u.ID, u.HouseholdID, pushToken, deviceType, name, now, now); err != nil {
			return err
		}
		var err error
		t, err = scanToken(tx.QueryRowContext(r.Context(), `SELECT `+tokenCols+` FROM notifications_device_tokens WHERE push_token = ?`, pushToken))
		return err
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

// handleUnregisterToken deactivates a token (mobile logout). LEGACY-BUG kept: any
// authenticated user may deactivate any token they know; mobile relies on nothing stricter,
// and a push token is not guessable.
func (m *Module) handleUnregisterToken(w http.ResponseWriter, r *http.Request, _ authn.User) {
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	pushToken, _ := o.str("push_token", true)
	if !o.done(w) {
		return
	}
	res, err := m.deps.DB.Write.ExecContext(r.Context(),
		`UPDATE notifications_device_tokens SET is_active = 0, updated_at = ? WHERE push_token = ?`, ts(m.now()), pushToken)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpx.Error(w, http.StatusNotFound, "Token not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (m *Module) handleMyTokens(w http.ResponseWriter, r *http.Request, u authn.User) {
	rows, err := m.deps.DB.Read.QueryContext(r.Context(),
		`SELECT `+tokenCols+` FROM notifications_device_tokens WHERE user_id = ? AND is_active = 1 ORDER BY created_at, rowid`, u.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []DeviceToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// --- account deletion (docs/cc D20) ---

// PurgeUser deletes every device token and personal inbox item of userID, inside tx. It has
// the auth module's UserDeletedHook signature: register it with auth.OnUserDeleted. The
// notification_log rows stay: they are the activity record of what the server sent. Queued
// pushes to the user's devices go nowhere, because the job drops tokens that no longer exist.
func (m *Module) PurgeUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM notifications_device_tokens WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notifications_inbox_items WHERE user_id = ?`, userID); err != nil {
		return err
	}
	// D20: the delivery log is activity history, kept but de-identified (target_id is NOT NULL).
	_, err := tx.ExecContext(ctx, `UPDATE notifications_notification_log SET target_id = 'deleted-user'
		WHERE target_type = 'user' AND target_id = ?`, strconv.FormatInt(userID, 10))
	return err
}

// PurgeHousehold deletes a deleted household's inbox items (household-wide and personal) and
// device tokens, inside tx (docs/cc D49: nothing is orphaned when auth deletes a household).
func (m *Module) PurgeHousehold(ctx context.Context, tx *sql.Tx, householdID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM notifications_device_tokens WHERE household_id = ?`, householdID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM notifications_inbox_items WHERE household_id = ?`, householdID)
	return err
}

// handlePurgeMe is the self-scoped purge legacy jarvis-auth called on account deletion. It
// stays for any caller that still uses it; jarvisd's auth calls PurgeUser in-process instead.
func (m *Module) handlePurgeMe(w http.ResponseWriter, r *http.Request, u authn.User) {
	if err := m.deps.DB.Tx(r.Context(), func(tx *sql.Tx) error { return m.PurgeUser(r.Context(), tx, u.ID) }); err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("notifications: purged user data", "user_id", u.ID)
	w.WriteHeader(http.StatusNoContent)
}

// --- admin and cleanup ---

// StaleTokenAge is how long an unused token stays active (legacy STALE_TOKEN_DAYS = 90).
const StaleTokenAge = 90 * 24 * time.Hour

// Cleanup prunes notification_log rows older than the retention and deactivates tokens not
// used for StaleTokenAge (tokens never used are left alone, as before).
func (m *Module) Cleanup(ctx context.Context) (logsPruned, tokensDeactivated int64, err error) {
	now := m.now()
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		cutoff := ts(now.Add(-time.Duration(m.LogRetentionDays) * 24 * time.Hour))
		res, err := tx.ExecContext(ctx, `DELETE FROM notifications_notification_log WHERE julianday(created_at) < julianday(?)`, cutoff)
		if err != nil {
			return err
		}
		logsPruned, _ = res.RowsAffected()
		res, err = tx.ExecContext(ctx, `UPDATE notifications_device_tokens SET is_active = 0, updated_at = ?
			WHERE is_active = 1 AND last_used_at IS NOT NULL AND julianday(last_used_at) < julianday(?)`,
			ts(now), ts(now.Add(-StaleTokenAge)))
		if err != nil {
			return err
		}
		tokensDeactivated, _ = res.RowsAffected()
		return nil
	})
	return logsPruned, tokensDeactivated, err
}

func (m *Module) runCleanup(ctx context.Context, _ queue.Job) ([]byte, error) {
	logs, tokens, err := m.Cleanup(ctx)
	if err != nil {
		return nil, err
	}
	if logs > 0 || tokens > 0 {
		m.deps.Log.Info("notifications: cleanup", "logs_pruned", logs, "tokens_deactivated", tokens)
	}
	return nil, nil
}

func (m *Module) handleCleanup(w http.ResponseWriter, r *http.Request) {
	logs, tokens, err := m.Cleanup(r.Context())
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "logs_pruned": logs, "tokens_deactivated": tokens})
}

func (m *Module) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	db := m.deps.DB.Read
	var total, active, recent int
	err := errors.Join(
		db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications_device_tokens`).Scan(&total),
		db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications_device_tokens WHERE is_active = 1`).Scan(&active),
	)
	if err != nil {
		m.internalError(w, err)
		return
	}
	type hhCount struct {
		HouseholdID string `json:"household_id"`
		Count       int    `json:"count"`
	}
	byHH := []hhCount{}
	byStatus := map[string]int{}
	cutoff := ts(m.now().Add(-24 * time.Hour))
	err = func() error {
		rows, err := db.QueryContext(ctx, `SELECT household_id, COUNT(*) FROM notifications_device_tokens
			WHERE is_active = 1 GROUP BY household_id ORDER BY household_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c hhCount
			if err := rows.Scan(&c.HouseholdID, &c.Count); err != nil {
				return err
			}
			byHH = append(byHH, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows2, err := db.QueryContext(ctx, `SELECT delivery_status, COUNT(*) FROM notifications_notification_log
			WHERE julianday(created_at) >= julianday(?) GROUP BY delivery_status`, cutoff)
		if err != nil {
			return err
		}
		defer rows2.Close()
		for rows2.Next() {
			var s string
			var n int
			if err := rows2.Scan(&s, &n); err != nil {
				return err
			}
			byStatus[s] = n
			recent += n
		}
		return rows2.Err()
	}()
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"tokens":            map[string]any{"total": total, "active": active, "by_household": byHH},
		"notifications_24h": map[string]any{"total": recent, "by_status": byStatus},
	})
}
