package phone

import (
	"context"
	"database/sql"
)

// PurgeUser is the D20 account-deletion hook, inside the caller's transaction: the user's
// pending call drafts and their call context (PII) are hard-deleted; call sessions are
// activity history and stay, de-identified (user_id and confirmed_by → NULL). The phonebook
// is household-owned and stays. settingsTable is cc's settings table name.
func PurgeUser(ctx context.Context, tx *sql.Tx, settingsTable string, userID int64) error {
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM cc_phone_call_sessions WHERE user_id = ? AND state = 'draft'`, []any{userID}},
		{`UPDATE cc_phone_call_sessions SET user_id = NULL WHERE user_id = ?`, []any{userID}},
		{`UPDATE cc_phone_call_sessions SET confirmed_by = NULL WHERE confirmed_by = ?`, []any{userID}},
		{`DELETE FROM ` + settingsTable + ` WHERE key = ? AND user_id = ?`, []any{SettingCallContext, userID}},
	} {
		if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
			return err
		}
	}
	return nil
}
