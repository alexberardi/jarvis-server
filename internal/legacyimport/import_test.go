package legacyimport

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/auth"
	"github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

const (
	h1 = "11111111-1111-4111-8111-111111111111"
	h2 = "22222222-2222-4222-8222-222222222222"
)

var fixture = DirSource{Dir: "testdata/legacy"}

func testDefs() map[string][]settings.Definition {
	return map[string][]settings.Definition{
		"auth": auth.Definitions(), "cc": cc.Definitions(), "stt": stt.Definitions, "config": configmod.Definitions,
	}
}

// migrated opens a fresh database with the tables the import writes.
func migrated(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "auth", auth.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, "cc", cc.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, "notifications", notifications.Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, defs := range testDefs() {
		s, err := settings.New(d, name, defs, log)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func opts(apply bool) Options {
	return Options{
		Apply: apply, Settings: testDefs(),
		PromptProvider: func(n string) error { _, err := prompts.Lookup(n); return err },
		Now:            func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) },
	}
}

func count(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func str(t *testing.T, d *db.DB, q string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := d.Read.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

func tableReport(rep *Report, name string) *TableReport {
	for _, t := range rep.Tables {
		if t.Table == name {
			return t
		}
	}
	return nil
}

func TestImportFixture(t *testing.T) {
	d := migrated(t)
	rep, err := Run(context.Background(), d, fixture, opts(true))
	if err != nil {
		var sb strings.Builder
		rep.Write(&sb)
		t.Fatalf("Run: %v\n%s", err, sb.String())
	}
	if !rep.Committed || len(rep.Errors) > 0 {
		t.Fatalf("not committed: %+v", rep.Errors)
	}

	// auth: ids kept, timestamps UTC with Z, roles lower-cased.
	if n := count(t, d, `SELECT COUNT(*) FROM auth_users`); n != 3 {
		t.Fatalf("users: %d", n)
	}
	if got := str(t, d, `SELECT created_at FROM auth_users WHERE id = 1`); got != "2026-01-02T03:04:05.123456Z" {
		t.Fatalf("auth timestamp: %q", got)
	}
	if got := str(t, d, `SELECT role FROM auth_household_memberships WHERE id = 3`); got != "power_user" {
		t.Fatalf("role: %q", got)
	}
	if tr := tableReport(rep, "auth_household_memberships"); tr.Fixed["role lower-cased (ADMIN → admin)"] != 1 {
		t.Fatalf("role fix-up not reported: %+v", tr.Fixed)
	}
	// Explicit ids advance the AUTOINCREMENT sequence: the next user gets a new id.
	if _, err := d.Write.Exec(`INSERT INTO auth_users (email, username, password_hash) VALUES ('new@example.com', 'new', 'x')`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT MAX(id) FROM auth_users`); n != 4 {
		t.Fatalf("next user id: %d", n)
	}
	if !slices.Equal(rep.InactiveNodes, []string{"old-node"}) {
		t.Fatalf("inactive nodes: %v", rep.InactiveNodes)
	}
	if len(rep.Superusers) != 1 || !strings.Contains(rep.Superusers[0], "o***@e***.com") {
		t.Fatalf("superusers: %v", rep.Superusers)
	}

	// Grants: legacy ones kept, the command-center and logs grants backfilled.
	grants := func(node string) []string {
		rows, err := d.Read.Query(`SELECT service_id FROM auth_node_service_access WHERE node_id = ? ORDER BY service_id`, node)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	if g := grants("kitchen-node"); !slices.Equal(g, []string{"jarvis-command-center", "jarvis-logs"}) {
		t.Fatalf("kitchen grants: %v", g)
	}
	if g := grants("old-node"); !slices.Equal(g, []string{"jarvis-command-center", "jarvis-logs"}) {
		t.Fatalf("old-node grants: %v", g)
	}
	if g := grants("orphan-reg"); len(g) != 3 {
		t.Fatalf("orphan grants: %v", g)
	}
	if tr := tableReport(rep, "auth_node_service_access"); tr.Fixed["grant jarvis-logs backfilled"] != 2 || tr.Fixed["grant jarvis-command-center backfilled"] != 1 || tr.Imported != 4 {
		t.Fatalf("grant report: %+v", tr)
	}

	// cc nodes: household from the auth registration, busy cleared, unknown room nulled,
	// a node without a registration skipped, a registration without a node noted.
	if got := str(t, d, `SELECT household_id FROM cc_nodes WHERE node_id = 'kitchen-node'`); got != h1 {
		t.Fatalf("kitchen household: %q", got)
	}
	if got := str(t, d, `SELECT household_id FROM cc_nodes WHERE node_id = 'old-node'`); got != h2 {
		t.Fatalf("old-node household: %q", got)
	}
	if n := count(t, d, `SELECT is_busy + needs_k2 FROM cc_nodes WHERE node_id = 'kitchen-node'`); n != 0 {
		t.Fatalf("kitchen busy/needs_k2: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM cc_nodes WHERE node_id = 'old-node' AND room_id IS NULL AND is_active = 0`); n != 1 {
		t.Fatal("old-node room/active")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM cc_nodes WHERE node_id = 'stray-node'`); n != 0 {
		t.Fatal("stray node imported")
	}
	if !slices.ContainsFunc(rep.Notes, func(s string) bool { return strings.Contains(s, "orphan-reg") }) {
		t.Fatalf("orphan registration not noted: %v", rep.Notes)
	}

	// Rooms: parents first, other households' rooms skipped; devices lose a skipped room.
	if got := str(t, d, `SELECT parent_room_id FROM cc_rooms WHERE id = 'r-kitchen'`); got != "r-floor" {
		t.Fatalf("parent: %q", got)
	}
	if got := str(t, d, `SELECT created_at FROM cc_rooms WHERE id = 'r-kitchen'`); got != "2026-01-02T03:04:05.123Z" {
		t.Fatalf("cc timestamp: %q", got)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM cc_rooms`); n != 2 {
		t.Fatalf("rooms: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM cc_devices WHERE id = 'd2' AND room_id IS NULL`); n != 1 {
		t.Fatal("device room")
	}

	// Memories: active and unexpired only, embeddings left for the sweep.
	if got := str(t, d, `SELECT group_concat(id) FROM (SELECT id FROM cc_user_memories ORDER BY id)`); got != "1,4" {
		t.Fatalf("memories: %q", got)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM cc_user_memories WHERE embedding IS NOT NULL`); n != 0 {
		t.Fatal("embedding copied")
	}
	if tr := tableReport(rep, "cc_user_memories"); tr.Skipped["forgotten (is_active = false)"] != 1 || tr.Skipped["expired"] != 1 || tr.Skipped["user not imported"] != 1 {
		t.Fatalf("memory skips: %+v", tr.Skipped)
	}

	// Routines, schedules (active only), contacts (web → manual), calls (finished only).
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM cc_routines`:                                                      1,
		`SELECT COUNT(*) FROM cc_schedules WHERE id = 'sch_1'`:                                  1,
		`SELECT COUNT(*) FROM cc_schedules`:                                                     1,
		`SELECT COUNT(*) FROM cc_phone_contacts WHERE id = 'c1' AND source = 'manual'`:          1,
		`SELECT COUNT(*) FROM cc_phone_call_sessions`:                                           1,
		`SELECT COUNT(*) FROM cc_phone_call_sessions WHERE id = 'call1' AND in_call_at IS NULL`: 1,
		`SELECT COUNT(*) FROM cc_signals`:                                                       2,
		`SELECT COUNT(*) FROM cc_signals WHERE id IN (1, 5)`:                                    2,
		`SELECT COUNT(*) FROM cc_proposal_suppressions WHERE source_key = 'k:` + "éè" + `'`:     1,
		`SELECT COUNT(*) FROM notifications_inbox_items`:                                        2,
		`SELECT COUNT(*) FROM notifications_inbox_items WHERE category = 'adapter_proposal'`:    0,
	} {
		if n := count(t, d, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}

	// Settings: changed values and scoped choices only; rename, dedupe, cut keys, blocked keys.
	setting := func(table, key, scope string) string {
		return str(t, d, `SELECT COALESCE((SELECT value FROM `+table+` WHERE key = ? AND COALESCE(household_id, '') = ?), '<none>')`, key, scope)
	}
	for _, c := range []struct{ table, key, scope, want string }{
		{"auth_settings", "auth.token.access_expire_minutes", "", "45"},
		{"auth_settings", "auth.algorithm", "", "<none>"},
		{"auth_settings", "auth.token.refresh_expire_days", "", "<none>"},
		{"cc_settings", "llm.prompt_provider", "", "Qwen3_8B_Compressed"},
		{"cc_settings", "conversation.max_turns", "", "14"},
		{"cc_settings", "conversation.max_turns", h1, "<none>"},
		{"cc_settings", "memory.enabled", h1, "false"},
		{"stt_settings", "voice.recognition_enabled", h1, "true"},
		{"stt_settings", "voice.recognition_enabled", "", "<none>"},
		{"config_settings", "health_check.enabled", "", "<none>"},
	} {
		if got := setting(c.table, c.key, c.scope); got != c.want {
			t.Errorf("%s %s@%q = %q, want %q", c.table, c.key, c.scope, got, c.want)
		}
	}
	if got := str(t, d, `SELECT value_type || '/' || category FROM cc_settings WHERE key = 'llm.prompt_provider'`); got != "string/llm" {
		t.Errorf("prompt provider row: %q", got)
	}
	cs := tableReport(rep, "cc_settings")
	if cs.Skipped["duplicate of a newer row for the same scope"] != 1 || cs.Skipped["not a jarvisd setting (cut or renamed)"] != 1 ||
		cs.Skipped["value not valid for jarvisd (not a int)"] != 1 || cs.Fixed["llm.interface renamed llm.prompt_provider"] != 1 {
		t.Errorf("cc settings report: %+v %+v", cs.Skipped, cs.Fixed)
	}
	if ls := tableReport(rep, "llm_settings"); ls.Imported != 0 || ls.Read != 1 {
		t.Errorf("llm settings: %+v", ls)
	}

	// The log names every row, and stdout masks emails.
	var out strings.Builder
	rep.Write(&out)
	if strings.Contains(out.String(), "owner@example.com") || strings.Contains(out.String(), "$2b$") {
		t.Fatalf("report leaks: %s", out.String())
	}
	if !slices.ContainsFunc(rep.Log, func(s string) bool { return strings.Contains(s, "owner@example.com") }) {
		t.Fatal("log lacks the user mapping")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	d := migrated(t)
	rep, err := Run(context.Background(), d, fixture, opts(false))
	if err != nil || rep.Committed || !rep.OK() {
		t.Fatalf("dry run: %v %+v", err, rep.Refusals)
	}
	if tr := tableReport(rep, "auth_users"); tr.Imported != 3 {
		t.Fatalf("dry run should count what it would import: %+v", tr)
	}
	for _, tbl := range append(slices.Clone(freshTables), "cc_settings", "auth_settings") {
		if n := count(t, d, "SELECT COUNT(*) FROM "+tbl); n != 0 {
			t.Fatalf("dry run wrote %d rows to %s", n, tbl)
		}
	}
}

func TestRefusesNonFreshDatabase(t *testing.T) {
	d := migrated(t)
	if _, err := d.Write.Exec(`INSERT INTO auth_households (id, name) VALUES ('h', 'My Home')`); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), d, fixture, opts(true))
	if !errors.Is(err, ErrRefused) || len(rep.Refusals) != 1 || !strings.Contains(rep.Refusals[0], "auth_households (1)") {
		t.Fatalf("got %v %v", err, rep.Refusals)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM auth_users`); n != 0 {
		t.Fatal("wrote users")
	}
}

// copyFixture copies the fixture to a temp dir and applies edit to one file's text.
func copyFixture(t *testing.T, file string, edit func(string) string) DirSource {
	t.Helper()
	dir := t.TempDir()
	ents, err := os.ReadDir(fixture.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(fixture.Dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if e.Name() == file {
			s = edit(s)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return DirSource{Dir: dir}
}

func TestRefusesUnexpectedHead(t *testing.T) {
	src := copyFixture(t, "heads.json", func(s string) string { return strings.Replace(s, "c5d6e7f8a9b0", "zzz", 1) })
	d := migrated(t)
	rep, err := Run(context.Background(), d, src, opts(true))
	if !errors.Is(err, ErrRefused) || !strings.Contains(strings.Join(rep.Refusals, " "), "--accept-head auth=zzz") {
		t.Fatalf("got %v %v", err, rep.Refusals)
	}
	o := opts(true)
	o.AcceptHeads = map[string]string{"auth": "zzz"}
	if _, err := Run(context.Background(), d, src, o); err != nil {
		t.Fatalf("accepted head: %v", err)
	}
	// A required database missing refuses too.
	src = copyFixture(t, "heads.json", func(s string) string { return strings.Replace(s, `"cc"`, `"cc-gone"`, 1) })
	if rep, err := Run(context.Background(), migrated(t), src, opts(true)); !errors.Is(err, ErrRefused) || !strings.Contains(rep.Refusals[0], "cc") {
		t.Fatalf("missing cc: %v %v", err, rep.Refusals)
	}
}

func TestRollsBackEverythingOnABadRow(t *testing.T) {
	src := copyFixture(t, "auth.household_memberships.json", func(s string) string { return strings.Replace(s, `"POWER_USER"`, `"OWNER"`, 1) })
	for _, apply := range []bool{false, true} {
		d := migrated(t)
		rep, err := Run(context.Background(), d, src, opts(apply))
		if !errors.Is(err, ErrRefused) || len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "CHECK constraint failed") ||
			!strings.Contains(rep.Errors[0], "id=3") {
			t.Fatalf("apply=%v: %v %v", apply, err, rep.Errors)
		}
		// The other rows were still checked (savepoints), and nothing stayed.
		if tr := tableReport(rep, "notifications_inbox_items"); tr == nil || tr.Imported != 2 {
			t.Fatalf("later tables not validated: %+v", tr)
		}
		for _, tbl := range freshTables {
			if n := count(t, d, "SELECT COUNT(*) FROM "+tbl); n != 0 {
				t.Fatalf("apply=%v left %d rows in %s", apply, n, tbl)
			}
		}
	}
	// An unreadable timestamp is a rejected row too.
	src = copyFixture(t, "cc.routines.json", func(s string) string { return strings.Replace(s, `"2026-01-02T03:04:05.123456"`, `"yesterday"`, 1) })
	rep, err := Run(context.Background(), migrated(t), src, opts(true))
	if !errors.Is(err, ErrRefused) || len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "unreadable timestamp") {
		t.Fatalf("bad timestamp: %v %v", err, rep.Errors)
	}
}

func TestNoSuperuserNoted(t *testing.T) {
	src := copyFixture(t, "auth.users.json", func(s string) string { return strings.ReplaceAll(s, `"is_superuser": true`, `"is_superuser": false`) })
	rep, err := Run(context.Background(), migrated(t), src, opts(false))
	if err != nil || len(rep.Superusers) != 0 || !slices.ContainsFunc(rep.Notes, func(s string) bool { return strings.Contains(s, "no active superuser") }) {
		t.Fatalf("got %v %v %v", err, rep.Superusers, rep.Notes)
	}
}

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"alice@example.com": "a***@e***.com", "b@x": "b***@x***", "nope": "***", "c@.io": "c***@.***",
	} {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTime(t *testing.T) {
	for in, want := range map[string]string{
		"2026-01-02T03:04:05.123456+00:00": "2026-01-02T03:04:05.123456Z",
		"2026-01-02T05:04:05+02:00":        "2026-01-02T03:04:05.000000Z",
		"2026-01-02T03:04:05.5":            "2026-01-02T03:04:05.500000Z",
		"2026-01-02 03:04:05":              "2026-01-02T03:04:05.000000Z",
	} {
		got, err := parseTime(in)
		if err != nil || got.Format(usTime) != want {
			t.Errorf("parseTime(%q) = %v %v, want %s", in, got.Format(usTime), err, want)
		}
	}
}

func TestSelectSQL(t *testing.T) {
	got := selectSQL(Query{Table: "nodes", Columns: []string{"node_id", "user"}, Order: "node_id"})
	want := `SELECT coalesce(json_agg(t), '[]'::json)::text FROM (SELECT "node_id", "user" FROM "nodes" ORDER BY node_id) t`
	if got != want {
		t.Fatalf("got %s", got)
	}
}
