package cc

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

var expectedTables = []string{
	// 05 nodes
	"cc_nodes", "cc_node_tasks", "cc_provisioning_tokens", "cc_settings_requests",
	"cc_settings_snapshots", "cc_request_traces",
	// 07 smart home
	"cc_rooms", "cc_devices", "cc_device_scan_requests", "cc_device_list_requests",
	"cc_config_pushes", "cc_auth_sessions", "cc_bluetooth_scan_requests", "cc_bluetooth_pair_requests",
	// 04 memory
	"cc_user_memories", "cc_conversation_transcripts", "cc_person_characterizations",
	// 08 routines and schedules
	"cc_routines", "cc_schedules",
	// 09 errands
	"cc_errand_plans", "cc_workflows",
	// 10 signals, attention, proposals
	"cc_signals", "cc_proposal_suppressions", "cc_attention_events", "cc_attention_deliveries",
	"cc_automation_actions", "cc_reaction_claims",
	// 11 phone
	"cc_phone_contacts", "cc_phone_call_sessions",
	// 12 packages
	"cc_package_install_requests",
	// 13 mobile
	"cc_callback_jobs",
}

// Cut by decisions in docs/cc/QUESTIONS.md; they must never come back by accident.
var cutTables = []string{
	"cc_active_adapter", "cc_adapter_history", "cc_adapter_proposals", "cc_adapter_training_state",
	"cc_attention_source_tiers", "cc_attention_consents", "cc_attention_feedback",
	"cc_routine_executions", "cc_prompt_provider_install_requests", "cc_test_install_requests",
	"cc_service_configs", "cc_settings",
}

func migrated(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "cc", Migrations()); err != nil {
		t.Fatal(err)
	}
	return d
}

func tables(t *testing.T, q *sql.DB) []string {
	t.Helper()
	rows, err := q.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'cc\_%' ESCAPE '\'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBaselineTables(t *testing.T) {
	d := migrated(t)
	got := tables(t, d.Read)
	for _, want := range expectedTables {
		if !slices.Contains(got, want) {
			t.Errorf("missing table %s", want)
		}
	}
	for _, cut := range cutTables {
		if slices.Contains(got, cut) {
			t.Errorf("cut table %s exists", cut)
		}
	}
	if len(got) != len(expectedTables) {
		t.Errorf("got %d cc tables %v, want %d", len(got), got, len(expectedTables))
	}
}

func TestBaselineForeignKeys(t *testing.T) {
	d := migrated(t)
	rows, err := d.Read.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check reported violations")
	}
	// Every FK must name an existing parent table and column (foreign_key_check only sees rows).
	for _, tbl := range expectedTables {
		fks, err := d.Read.Query(`SELECT "table", "to" FROM pragma_foreign_key_list(?)`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for fks.Next() {
			var parent, col string
			if err := fks.Scan(&parent, &col); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := d.Read.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, parent, col).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("%s: FK to missing %s.%s", tbl, parent, col)
			}
		}
		fks.Close()
	}
}

func TestBaselineDecisionColumns(t *testing.T) {
	d := migrated(t)
	has := func(tbl, col string) bool {
		var n int
		if err := d.Read.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, tbl, col).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	for _, c := range [][2]string{
		{"cc_node_tasks", "reset_token"},               // D10
		{"cc_settings_requests", "include_values"},     // D40 05.Q8
		{"cc_settings_requests", "user_id"},            // D40 05.Q8
		{"cc_phone_call_sessions", "in_call_at"},       // D40 11.Q6
		{"cc_package_install_requests", "verified_at"}, // D39
		{"cc_request_traces", "user_id"},               // D20
		{"cc_errand_plans", "expires_at"},              // D40 09.Q6 draft TTL
	} {
		if !has(c[0], c[1]) {
			t.Errorf("missing %s.%s", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"cc_nodes", "api_key"}, {"cc_nodes", "adapter_hash"},
		{"cc_schedules", "title"},
		{"cc_errand_plans", "routine_slug"}, {"cc_errand_plans", "cursor"}, {"cc_errand_plans", "results_json"},
		{"cc_phone_contacts", "overlay_json"}, {"cc_phone_call_sessions", "constraints"},
	} {
		if has(c[0], c[1]) {
			t.Errorf("cut column %s.%s exists", c[0], c[1])
		}
	}
}

// The attention TTL cleanup deletes events and relies on the FK cascade (doc 10 §7).
func TestAttentionCascadeAndNodeDelete(t *testing.T) {
	d := migrated(t)
	ctx := context.Background()
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		for _, s := range []string{
			`INSERT INTO cc_attention_events (id, household_id, source, category, title) VALUES ('e1', 'h', 's', 'c', 't')`,
			`INSERT INTO cc_attention_deliveries (id, event_id, household_id, rung) VALUES ('d1', 'e1', 'h', 'inbox')`,
			`INSERT INTO cc_rooms (id, household_id, name, normalized_name) VALUES ('r1', 'h', 'Kitchen', 'kitchen')`,
			`INSERT INTO cc_nodes (node_id, room, room_id, household_id) VALUES ('n1', 'kitchen', 'r1', 'h')`,
			`INSERT INTO cc_node_tasks (id, node_id, kind) VALUES ('t1', 'n1', 'factory_reset')`,
			`INSERT INTO cc_request_traces (id, conversation_id, request_type, source, node_id, total_duration_ms, spans_json) VALUES ('tr1', 'c', 'stt', 'node', 'n1', 1.5, '[]')`,
			`DELETE FROM cc_attention_events`,
			`DELETE FROM cc_rooms`,
		} {
			if _, err := tx.Exec(s); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := d.Read.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM cc_attention_deliveries`); n != 0 {
		t.Errorf("deliveries not cascaded: %d", n)
	}
	if n := count(`SELECT count(*) FROM cc_nodes WHERE room_id IS NULL`); n != 1 {
		t.Errorf("node room_id not set null")
	}
	if _, err := d.Write.Exec(`DELETE FROM cc_nodes`); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM cc_node_tasks`); n != 0 {
		t.Errorf("node tasks not cascaded: %d", n)
	}
	if n := count(`SELECT count(*) FROM cc_request_traces WHERE node_id IS NULL`); n != 1 {
		t.Errorf("trace node_id not set null")
	}
}

func TestBaselineChecks(t *testing.T) {
	d := migrated(t)
	for _, bad := range []string{
		`INSERT INTO cc_schedules (id, household_id, intent, next_fire_at, state, created_at, updated_at) VALUES ('s', 'h', 'i', 'x', 'paused', 'x', 'x')`,
		`INSERT INTO cc_phone_contacts (id, household_id, name, normalized_name, number, source, created_at, updated_at) VALUES ('c', 'h', 'n', 'n', '+1', 'web', 'x', 'x')`,
		`INSERT INTO cc_user_memories (household_id, content, embedding) VALUES ('h', 'c', x'00')`,
	} {
		if _, err := d.Write.Exec(bad); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
	var ts string
	if err := d.Write.QueryRow(`INSERT INTO cc_routines (id, household_id, slug, name) VALUES ('r', 'h', 's', 'n') RETURNING created_at`).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if len(ts) != len("2026-10-06T12:00:00.000Z") || ts[10] != 'T' || ts[len(ts)-1] != 'Z' {
		t.Errorf("default timestamp %q is not ISO-8601 UTC", ts)
	}
}

func TestBaselineRerun(t *testing.T) {
	d := migrated(t)
	// Re-running is a no-op.
	if err := db.Migrate(context.Background(), d, "cc", Migrations()); err != nil {
		t.Fatal(err)
	}
	st, err := db.Status(context.Background(), d, "cc", Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if st.Current < 1 || st.Pending != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestBaselineDownRemovesEverything(t *testing.T) {
	d := migrated(t)
	ctx := context.Background()
	store, err := database.NewStore(database.DialectSQLite3, "goose_cc")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider("", d.Write, Migrations(), goose.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := tables(t, d.Read); len(got) != 0 {
		t.Fatalf("tables left after down: %v", got)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
}
