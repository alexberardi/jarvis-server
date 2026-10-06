package modules_test

import (
	"context"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/auth"
	"github.com/alexberardi/jarvis-server/internal/modules/config"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/logs"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/modules/ocr"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// nonCCBaselines lists every non-CC module's baseline and the tables it must create.
var nonCCBaselines = []struct {
	module     string
	migrations fs.FS
	tables     []string
}{
	{"auth", auth.Migrations(), []string{
		"auth_app_clients", "auth_household_invites", "auth_household_memberships", "auth_households",
		"auth_node_registrations", "auth_node_service_access", "auth_refresh_tokens", "auth_signing_keys",
		"auth_users",
	}},
	{"config", config.Migrations(), []string{"config_services"}},
	{"logs", logs.Migrations(), []string{"logs_entries"}},
	{"notifications", notifications.Migrations(), []string{
		"notifications_device_tokens", "notifications_inbox_items", "notifications_notification_log",
	}},
	{"recipes", recipes.Migrations(), []string{
		"recipes_grocery_sku_map", "recipes_ingredients", "recipes_mailbox_messages", "recipes_meal_plan_items",
		"recipes_meal_plans", "recipes_recipe_ingestions", "recipes_recipe_parse_jobs", "recipes_recipe_tags",
		"recipes_recipes", "recipes_stage_recipes", "recipes_staples", "recipes_steps", "recipes_stock_ingredients",
		"recipes_stock_units_of_measure", "recipes_tags", "recipes_users",
	}},
	{"ocr", ocr.Migrations(), nil},
	{"llm", llm.Migrations(), nil},
	{"stt", stt.Migrations(), nil},
	{"tts", tts.Migrations(), nil},
}

func TestNonCCBaselinesApply(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	for _, b := range nonCCBaselines {
		if err := db.Migrate(ctx, d, b.module, b.migrations); err != nil {
			t.Fatalf("%s: %v", b.module, err)
		}
		st, err := db.Status(ctx, d, b.module, b.migrations)
		if err != nil || st.Current != st.Latest || st.Pending != 0 {
			t.Fatalf("%s: status %+v, %v", b.module, st, err)
		}
		got := tablesWithPrefix(t, d, b.module+"_")
		if !slices.Equal(got, b.tables) {
			t.Errorf("%s: tables\n got %v\nwant %v", b.module, got, b.tables)
		}
	}

	// Every FK must point at an existing table (an empty foreign_key_check can't catch that),
	// and the schema must hold no FK violations.
	rows, err := d.Read.QueryContext(ctx, `
		SELECT m.name, f."table" FROM sqlite_schema m, pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND f."table" NOT IN (SELECT name FROM sqlite_schema WHERE type = 'table')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			t.Fatal(err)
		}
		t.Errorf("%s references missing table %s", child, parent)
	}
	rows.Close()
	assertNoFKViolations(t, d)

	// Smoke the auth graph: FKs, the role CHECK and ON DELETE behaviour.
	mustExec(t, d, `INSERT INTO auth_users (email, username, password_hash) VALUES ('a@x', 'a', 'h')`)
	mustExec(t, d, `INSERT INTO auth_households (id, name) VALUES ('hh1', 'Home')`)
	mustExec(t, d, `INSERT INTO auth_household_memberships (household_id, user_id, role) VALUES ('hh1', 1, 'admin')`)
	mustExec(t, d, `INSERT INTO auth_node_registrations (node_id, node_key_hash, name, household_id, registered_by_user_id)
		VALUES ('n1', 'h', 'Kitchen', 'hh1', 1)`)
	mustExec(t, d, `INSERT INTO auth_node_service_access (node_id, service_id, granted_by) VALUES ('n1', 'jarvis-logs', 1)`)
	if _, err := d.Write.ExecContext(ctx, `INSERT INTO auth_household_memberships (household_id, user_id, role) VALUES ('hh1', 1, 'MEMBER')`); err == nil {
		t.Error("role CHECK accepted a legacy enum name")
	}
	if _, err := d.Write.ExecContext(ctx, `INSERT INTO auth_refresh_tokens (user_id, token_hash, expires_at, family_id) VALUES (99, 't', 'x', 'f')`); err == nil {
		t.Error("FK to auth_users not enforced")
	}
	mustExec(t, d, `DELETE FROM auth_users WHERE id = 1`)
	var by any
	if err := d.Read.QueryRowContext(ctx, `SELECT registered_by_user_id FROM auth_node_registrations WHERE node_id = 'n1'`).Scan(&by); err != nil || by != nil {
		t.Errorf("ON DELETE SET NULL: got %v, %v", by, err)
	}
	mustExec(t, d, `DELETE FROM auth_households WHERE id = 'hh1'`)
	var n int
	if err := d.Read.QueryRowContext(ctx, `SELECT count(*) FROM auth_node_service_access`).Scan(&n); err != nil || n != 0 {
		t.Errorf("cascade household -> node -> service access: %d rows left, %v", n, err)
	}
	assertNoFKViolations(t, d)
}

func tablesWithPrefix(t *testing.T, d *db.DB, prefix string) []string {
	t.Helper()
	rows, err := d.Read.Query(`SELECT name FROM sqlite_schema WHERE type = 'table' AND substr(name, 1, ?) = ? ORDER BY name`,
		len(prefix), prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func assertNoFKViolations(t *testing.T, d *db.DB) {
	t.Helper()
	rows, err := d.Read.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("PRAGMA foreign_key_check reported violations")
	}
}

func mustExec(t *testing.T, d *db.DB, q string) {
	t.Helper()
	if _, err := d.Write.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
