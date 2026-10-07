package cc

import (
	"context"
	"database/sql"
	"fmt"
)

// Household lifecycle (D20, D49): auth's OnMemberRemoved and OnHouseholdDeleted hooks, run
// inside auth's transaction.

// PurgeUserHousehold erases a user's data in one household they left or were removed from:
// what the account-deletion purge removes, scoped to that household. Household-owned data
// (rooms, devices, routines, the phonebook) and activity history (call logs, de-identified)
// stay; their other households are untouched.
func (m *Module) PurgeUserHousehold(ctx context.Context, tx *sql.Tx, userID int64, hh string) error {
	if err := purgeMemoryUserHousehold(ctx, tx, userID, hh); err != nil {
		return err
	}
	for _, q := range []string{
		// Their schedules' triggers go with them (a fired trigger with no row only lapses).
		`DELETE FROM platform_triggers WHERE name IN
			(SELECT 'cc.schedule:' || id FROM cc_schedules WHERE user_id = ? AND household_id = ?)`,
		`DELETE FROM cc_schedules WHERE user_id = ? AND household_id = ?`,
		`DELETE FROM cc_errand_plans WHERE user_id = ? AND household_id = ?`,
		`UPDATE cc_workflows SET user_id = NULL WHERE user_id = ? AND household_id = ?`,
		`DELETE FROM cc_signals WHERE user_id = ? AND household_id = ?`,
		`DELETE FROM cc_proposal_suppressions WHERE user_id = ? AND household_id = ?`,
		`UPDATE cc_attention_events SET target_user_id = NULL, payload_json = NULL WHERE target_user_id = ? AND household_id = ?`,
		`DELETE FROM cc_callback_jobs WHERE user_id = ? AND household_id = ?`,
		`DELETE FROM cc_request_traces WHERE user_id = ? AND household_id = ?`,
		`UPDATE cc_bluetooth_scan_requests SET user_id = NULL WHERE user_id = ? AND household_id = ?`,
		`UPDATE cc_provisioning_tokens SET created_by_user_id = NULL WHERE created_by_user_id = ? AND household_id = ?`,
		`DELETE FROM cc_phone_call_sessions WHERE user_id = ? AND household_id = ? AND state = 'draft'`,
		`UPDATE cc_phone_call_sessions SET user_id = NULL WHERE user_id = ? AND household_id = ?`,
		`UPDATE cc_phone_call_sessions SET confirmed_by = NULL WHERE confirmed_by = ? AND household_id = ?`,
		// Their personal settings within this household (user-level rows elsewhere stay).
		`DELETE FROM cc_settings WHERE user_id = ? AND household_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID, hh); err != nil {
			return fmt.Errorf("cc: purge user %d in household: %w", userID, err)
		}
	}
	// OAuth sessions carry no household: they go when started on one of its nodes.
	if _, err := tx.ExecContext(ctx, `DELETE FROM cc_auth_sessions WHERE user_id = ?
		AND node_id IN (SELECT node_id FROM cc_nodes WHERE household_id = ?)`, userID, hh); err != nil {
		return err
	}
	if m.convs != nil {
		m.convs.purgeUser(userID) // conversation identity is per user, not per household
	}
	return nil
}

// PurgeHousehold erases everything CC holds for a deleted household: every cc_ table's rows
// with its household_id (node children cascade with its nodes), its nodes' settings, and its
// scheduler triggers. Tables are found from the schema, so later tables are covered.
func (m *Module) PurgeHousehold(ctx context.Context, tx *sql.Tx, hh string) error {
	pre := []string{
		`DELETE FROM platform_triggers WHERE name = 'cc.attention_journal:' || ?`,
		`DELETE FROM platform_triggers WHERE name IN (SELECT 'cc.routine:' || id FROM cc_routines WHERE household_id = ?)`,
		`DELETE FROM platform_triggers WHERE name IN (SELECT 'cc.schedule:' || id FROM cc_schedules WHERE household_id = ?)`,
		`DELETE FROM cc_settings WHERE node_id IN (SELECT node_id FROM cc_nodes WHERE household_id = ?)`,
		`DELETE FROM cc_auth_sessions WHERE node_id IN (SELECT node_id FROM cc_nodes WHERE household_id = ?)`,
	}
	for _, q := range pre {
		if _, err := tx.ExecContext(ctx, q, hh); err != nil {
			return fmt.Errorf("cc: purge household: %w", err)
		}
	}
	tables, err := householdTables(ctx, tx)
	if err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM "`+t+`" WHERE household_id = ?`, hh); err != nil {
			return fmt.Errorf("cc: purge household from %s: %w", t, err)
		}
	}
	// A live call ends at its next heartbeat: the household's phone_calls.enabled row is gone,
	// so the gate reads its default (off). (phone.CancelHousehold writes through DB.Write and
	// would deadlock inside this transaction.)
	return nil
}

// householdTables lists the cc_ tables with a household_id column.
func householdTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.name FROM sqlite_master m
		WHERE m.type = 'table' AND m.name LIKE 'cc\_%' ESCAPE '\'
		AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name = 'household_id')
		ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
