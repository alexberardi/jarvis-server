package recipes

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/blob"
)

// Fixture: a legacy kitchen (household lh1) with an admin (legacy 1, ann) and a member
// (legacy 4, bob); legacy 7 (cat) has her own household lh2. On jarvisd, ann is user 10 and bob
// user 11, both in household nh1; cat has no account.

const (
	lh1 = "11111111-1111-1111-1111-111111111111"
	lh2 = "22222222-2222-2222-2222-222222222222"
	nh1 = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	nh2 = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

var jpegBytes = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00fake jpeg body")

func str(s string) *string { return &s }
func i64(n int64) *int64   { return &n }

type fixture struct {
	tables map[string]any
	media  map[string][]byte
	head   string
}

func baseFixture() *fixture {
	photo := "0123456789abcdef0123456789abcdef.jpg"
	abs := "fedcba9876543210fedcba9876543210.png"
	return &fixture{
		head:  LegacyAlembicHead,
		media: map[string][]byte{photo: jpegBytes, abs: []byte("\x89PNG\r\n\x1a\nfake png")},
		tables: map[string]any{
			"recipes.json": []map[string]any{
				{"id": 1, "user_id": "1", "household_id": lh1, "title": "Soup", "description": "hot", "image_url": "/media/" + photo,
					"source_type": "MANUAL", "source_url": nil, "servings": 4, "total_time_minutes": 30,
					"created_at": "2026-01-02T03:04:05.123456", "updated_at": "2026-01-03T00:00:00"},
				{"id": 2, "user_id": "1", "household_id": lh1, "title": "Salad", "image_url": "http://10.0.0.5:7030/media/" + abs,
					"source_type": "URL", "source_url": "https://example.com/salad", "created_at": "2026-01-02T03:04:05"},
				{"id": 3, "user_id": "1", "household_id": lh1, "title": "Stew", "image_url": "https://cdn.example.com/media/stew.jpg",
					"source_type": "IMAGE", "created_at": "2026-01-02T03:04:05"},
				{"id": 4, "user_id": "4", "household_id": nil, "title": "Bob's secret", "source_type": "MANUAL", "created_at": nil},
				{"id": 5, "user_id": "4", "household_id": lh1, "title": "Bob's shared", "source_type": "MANUAL",
					"image_url": "/media/missing0123456789abcdef0123456.jpg", "created_at": "2026-01-02T03:04:05"},
				{"id": 6, "user_id": "7", "household_id": lh2, "title": "Cat's", "source_type": "MANUAL", "created_at": "2026-01-02T03:04:05"},
			},
			"ingredients.json": []map[string]any{
				{"id": 1, "recipe_id": 1, "text": "1 1/2 cups water", "quantity_display": "1 1/2", "quantity_value": json.Number("1.5000"), "unit": "cups"},
				{"id": 2, "recipe_id": 1, "text": "salt", "quantity_display": nil, "quantity_value": nil, "unit": nil},
				{"id": 3, "recipe_id": 2, "text": "lettuce", "quantity_display": "1/3", "quantity_value": json.Number("0.3333"), "unit": "head"},
			},
			"steps.json": []map[string]any{
				{"id": 2, "recipe_id": 1, "step_number": 2, "text": "Simmer."},
				{"id": 1, "recipe_id": 1, "step_number": 1, "text": "Boil."},
			},
			"tags.json":        []map[string]any{{"id": 1, "name": "Dinner"}, {"id": 2, "name": "easy"}},
			"recipe_tags.json": []map[string]any{{"recipe_id": 1, "tag_id": 2}, {"recipe_id": 1, "tag_id": 1}},
			"meal_plans.json": []map[string]any{
				{"id": 1, "user_id": "4", "household_id": lh1, "name": "Week", "start_date": "2026-01-05", "created_at": "2026-01-04T10:00:00"},
			},
			"meal_plan_items.json": []map[string]any{
				{"id": 1, "meal_plan_id": 1, "recipe_id": 1, "date": "2026-01-05", "meal_type": "dinner"},
				{"id": 2, "meal_plan_id": 1, "recipe_id": 6, "date": "2026-01-06", "meal_type": "dinner"},
			},
			"staples.json": []map[string]any{
				{"id": 1, "user_id": "1", "household_id": lh1, "name": "olive oil", "created_at": "2026-01-01T00:00:00"},
				{"id": 2, "user_id": "4", "household_id": lh1, "name": "salt", "created_at": "2026-01-01T00:00:00"},
			},
			"grocery_sku_map.json": []map[string]any{
				{"id": 1, "user_id": "1", "household_id": lh1, "retailer": "walmart", "ingredient_name": "olive oil", "sku": "123",
					"product_name": "Oil", "unit_size": "1 l", "source": "manual", "created_at": "2026-01-01T00:00:00", "updated_at": nil},
			},
			"auth_users.json": []map[string]any{
				{"id": 1, "email": "Ann@Example.com"}, {"id": 4, "email": "bob@example.com"}, {"id": 7, "email": "cat@example.com"},
			},
			"auth_households.json": []map[string]any{{"id": lh1, "name": "Kitchen"}, {"id": lh2, "name": "Cat's place"}},
			"auth_household_memberships.json": []map[string]any{
				{"household_id": lh1, "user_id": 4, "role": "member"},
				{"household_id": lh1, "user_id": 1, "role": "ADMIN"},
				{"household_id": lh2, "user_id": 7, "role": "admin"},
			},
		},
	}
}

func baseAccounts() Accounts {
	return Accounts{
		Emails:         map[int64]string{10: "ann@example.com", 11: "bob@example.com"},
		HouseholdNames: map[string]string{nh1: "Kitchen", nh2: "Other"},
		Memberships:    []Membership{{nh1, 10, "admin"}, {nh1, 11, "member"}},
	}
}

func (f *fixture) files(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	sums := map[string]string{}
	for name, v := range f.tables {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = raw
	}
	for name, data := range f.media {
		files["media/"+name] = data
	}
	for name, data := range files {
		s := sha256.Sum256(data)
		sums[name] = hex.EncodeToString(s[:])
	}
	man, _ := json.Marshal(BundleManifest{Format: BundleFormat, ExportedAt: "2026-10-08T12:00:00Z", Source: "test",
		RecipesAlembicHead: f.head, Files: sums})
	files["manifest.json"] = man
	return files
}

func (f *fixture) dir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range f.files(t) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (f *fixture) bundle(t *testing.T) *Bundle {
	t.Helper()
	b, err := LoadBundle(f.dir(t))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runImport(t *testing.T, e *env, b *Bundle, acc Accounts, opt ImportOptions) *ImportReport {
	t.Helper()
	opt.Now = func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }
	rep, err := Import(e.ctx, e.d, e.blobs, b, acc, opt)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func reportText(r *ImportReport) string {
	var buf bytes.Buffer
	r.Write(&buf)
	return buf.String()
}

func count(t *testing.T, e *env, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Read.QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportDryRunWritesNothing(t *testing.T) {
	e := setup(t)
	rep := runImport(t, e, baseFixture().bundle(t), baseAccounts(), ImportOptions{LegacyHosts: []string{"10.0.0.5:7030"}})
	if rep.Applied || len(rep.Refusals) > 0 {
		t.Fatalf("dry run: applied=%v refusals=%v", rep.Applied, rep.Refusals)
	}
	if c := rep.Counts["recipe"]; c.Imported != 5 || c.Skipped != 1 {
		t.Fatalf("recipe counts %+v\n%s", c, reportText(rep))
	}
	for _, tbl := range []string{"recipes_recipes", "recipes_meal_plans", "recipes_staples", "recipes_grocery_sku_map", "recipes_import_log", "recipes_tags"} {
		if n := count(t, e, `SELECT COUNT(*) FROM `+tbl); n != 0 {
			t.Fatalf("dry run wrote %d rows into %s", n, tbl)
		}
	}
	if infos, _ := e.blobs.List(e.ctx, mediaBlobPrefix); len(infos) != 0 {
		t.Fatalf("dry run copied photos: %v", infos)
	}
	if rep.Photos.Copied != 2 {
		t.Fatalf("photos to copy %+v", rep.Photos)
	}
	if !strings.Contains(reportText(rep), "DRY RUN") {
		t.Fatal("report does not say dry run")
	}
}

func TestImportApplyThroughTheAPI(t *testing.T) {
	e := setup(t)
	e.hh.set(10, nh1)
	e.hh.set(11, nh1)
	e.hh.set(12, nh2)
	rep := runImport(t, e, baseFixture().bundle(t), baseAccounts(), ImportOptions{Apply: true, LegacyHosts: []string{"10.0.0.5:7030"}})
	if !rep.Applied {
		t.Fatalf("not applied:\n%s", reportText(rep))
	}
	txt := reportText(rep)
	// Users: ann and bob matched (case-insensitive), cat reported with a masked email.
	if len(rep.MatchedUsers) != 2 || len(rep.UnmatchedUsers) != 1 || !strings.Contains(txt, "c***@example.com") || strings.Contains(txt, "cat@example.com") {
		t.Fatalf("users:\n%s", txt)
	}
	if len(rep.Households) != 1 || rep.Households[0].New != nh1 || rep.Households[0].How != "owner" {
		t.Fatalf("households %+v", rep.Households)
	}

	ann, bob := tok(10, nh1), tok(11, nh1)
	recs := e.list(t, "/recipes", ann...)
	// 1, 2, 3 (ann, household), 4 (bob, private: visible to bob only), 5 (bob, shared); not 6 (cat).
	if len(recs) != 4 {
		t.Fatalf("ann sees %d recipes: %v", len(recs), recs)
	}
	if got := len(e.list(t, "/recipes", bob...)); got != 5 {
		t.Fatalf("bob sees %d recipes", got)
	}
	if got := len(e.list(t, "/recipes", tok(12, nh2)...)); got != 0 {
		t.Fatalf("an outsider sees %d recipes", got)
	}
	soup := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/1", nil, ann...)
	if soup["title"] != "Soup" || soup["user_id"] != "10" || soup["source_type"] != "manual" ||
		soup["image_url"] != "/media/0123456789abcdef0123456789abcdef.jpg" {
		t.Fatalf("soup %v", soup)
	}
	ings := soup["ingredients"].([]any)
	if len(ings) != 2 || ings[0].(map[string]any)["quantity_value"] != "1.5000" {
		t.Fatalf("ingredients %v", ings)
	}
	steps := soup["steps"].([]any)
	if steps[0].(map[string]any)["text"] != "Boil." || steps[1].(map[string]any)["text"] != "Simmer." {
		t.Fatalf("steps %v", steps)
	}
	tags := soup["tags"].([]any)
	if len(tags) != 2 || tags[0].(map[string]any)["name"] != "easy" || tags[1].(map[string]any)["name"] != "Dinner" {
		t.Fatalf("tags (attach order) %v", tags)
	}
	if tags[0].(map[string]any)["id"] != 2.0 || tags[1].(map[string]any)["id"] != 1.0 {
		t.Fatalf("tag ids not kept: %v", tags)
	}
	if soup["created_at"] == nil || !strings.HasPrefix(soup["created_at"].(string), "2026-01-02T03:04:05") {
		t.Fatalf("created_at %v", soup["created_at"])
	}
	salad := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/2", nil, ann...)
	if salad["image_url"] != "/media/fedcba9876543210fedcba9876543210.png" || salad["source_type"] != "url" {
		t.Fatalf("salad: absolute legacy URL not made relative: %v", salad["image_url"])
	}
	stew := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/3", nil, ann...)
	if stew["image_url"] != "https://cdn.example.com/media/stew.jpg" {
		t.Fatalf("stew: a site's image must stay verbatim: %v", stew["image_url"])
	}
	shared := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/5", nil, ann...)
	if shared["image_url"] != nil || rep.Photos.Dropped != 1 {
		t.Fatalf("a photo missing from the bundle is dropped: %v %+v", shared["image_url"], rep.Photos)
	}
	// The photo is served from the blob store, unauthenticated.
	if c, body := e.do(t, http.MethodGet, "/media/0123456789abcdef0123456789abcdef.jpg", nil); c != http.StatusOK || body != string(jpegBytes) {
		t.Fatalf("media %d %q", c, body)
	}
	// Bob's plan, in the household; its item for cat's recipe is left out.
	plans := e.list(t, "/planner/plans", ann...)
	if len(plans) != 1 {
		t.Fatalf("plans %v", plans)
	}
	plan := e.obj(t, http.StatusOK, http.MethodGet, "/planner/plans/1", nil, ann...)
	if items, _ := plan["items"].([]any); len(items) != 1 {
		t.Fatalf("plan items %v", plan)
	}
	staples := e.list(t, "/staples", ann...)
	if len(staples) != 2 {
		t.Fatalf("staples %v", staples)
	}
	if m := e.list(t, "/grocery/sku-map", bob...); len(m) != 1 {
		t.Fatalf("sku map %v", m)
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_users WHERE user_id IN ('10', '11')`); n != 2 {
		t.Fatalf("shadow users %d", n)
	}

	// A second run imports nothing.
	again := runImport(t, e, baseFixture().bundle(t), baseAccounts(), ImportOptions{Apply: true, LegacyHosts: []string{"10.0.0.5:7030"}})
	for _, k := range ImportKinds {
		if c := again.Counts[k]; c.Imported != 0 || c.Matched != 0 {
			t.Fatalf("second run imported %s: %+v\n%s", k, c, reportText(again))
		}
	}
	if again.Counts["recipe"].Already != 5 || again.Counts["meal_plan_item"].Already != 1 || again.Photos.Copied != 0 {
		t.Fatalf("second run:\n%s", reportText(again))
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_recipes`); n != 5 {
		t.Fatalf("%d recipes after two runs", n)
	}
}

func TestImportIncrementalAfterSignUp(t *testing.T) {
	e := setup(t)
	acc := baseAccounts()
	delete(acc.Emails, 11) // bob has not signed up yet
	acc.Memberships = acc.Memberships[:1]
	b := baseFixture().bundle(t)
	rep := runImport(t, e, b, acc, ImportOptions{Apply: true})
	if c := rep.Counts["recipe"]; c.Imported != 3 || c.Skipped != 3 {
		t.Fatalf("first run %+v\n%s", c, reportText(rep))
	}
	if rep.Counts["meal_plan"].Skipped != 1 || rep.Counts["staple"].Imported != 1 {
		t.Fatalf("first run:\n%s", reportText(rep))
	}
	if !strings.Contains(reportText(rep), "b***@example.com") {
		t.Fatalf("bob not reported:\n%s", reportText(rep))
	}
	// Bob signs up; the re-run imports exactly his rows.
	acc = baseAccounts()
	rep = runImport(t, e, b, acc, ImportOptions{Apply: true})
	if c := rep.Counts["recipe"]; c.Imported != 2 || c.Already != 3 || c.Skipped != 1 {
		t.Fatalf("second run %+v\n%s", c, reportText(rep))
	}
	if rep.Counts["meal_plan"].Imported != 1 || rep.Counts["meal_plan_item"].Imported != 1 || rep.Counts["staple"].Imported != 1 {
		t.Fatalf("second run:\n%s", reportText(rep))
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_recipes WHERE user_id = '11'`); n != 2 {
		t.Fatalf("bob's recipes %d", n)
	}
}

func TestImportParkUnmatched(t *testing.T) {
	e := setup(t)
	e.hh.set(10, nh1)
	acc := baseAccounts()
	delete(acc.Emails, 11)
	acc.Memberships = acc.Memberships[:1]
	b := baseFixture().bundle(t)
	rep := runImport(t, e, b, acc, ImportOptions{Apply: true, ParkUnmatched: true})
	// Bob's shared recipe and plan (and its item) come in now; his private recipe waits.
	if c := rep.Counts["recipe"]; c.Imported != 4 || c.Skipped != 2 {
		t.Fatalf("parked run %+v\n%s", c, reportText(rep))
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_recipes WHERE user_id = 'legacy-4' AND household_id = ?`, nh1); n != 1 {
		t.Fatalf("parked recipes %d", n)
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_recipes WHERE user_id = 'legacy-4' AND household_id IS NULL`); n != 0 {
		t.Fatal("a private row was parked")
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_users WHERE user_id LIKE 'legacy-%'`); n != 0 {
		t.Fatal("a parked author got a shadow row")
	}
	// The household sees the parked rows.
	if got := len(e.list(t, "/recipes", tok(10, nh1)...)); got != 4 {
		t.Fatalf("ann sees %d", got)
	}
	// Bob signs up with a staple of his own named like his parked one: the re-run re-owns his
	// rows, keeps his staple, and imports his private recipe.
	if _, err := e.d.Write.ExecContext(e.ctx, `INSERT INTO recipes_staples (user_id, household_id, name) VALUES ('11', ?, 'salt')`, nh2); err != nil {
		t.Fatal(err)
	}
	rep = runImport(t, e, b, baseAccounts(), ImportOptions{Apply: true})
	if rep.Reowned < 2 || rep.Counts["recipe"].Imported != 1 {
		t.Fatalf("re-own run:\n%s", reportText(rep))
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_recipes WHERE user_id LIKE 'legacy-%'`); n != 0 {
		t.Fatal("parked rows left")
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_staples WHERE user_id = '11'`); n != 1 {
		t.Fatalf("bob's staples %d", n)
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_import_log WHERE legacy_kind = 'parked'`); n != 0 {
		t.Fatal("parked note left in the log")
	}
}

func TestImportRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(f *fixture, acc *Accounts, opt *ImportOptions)
		want string
	}{
		{"two legacy users, one account", func(f *fixture, acc *Accounts, _ *ImportOptions) {
			f.tables["auth_users.json"] = []map[string]any{{"id": 1, "email": "ann@example.com"}, {"id": 4, "email": "ANN@example.com"}, {"id": 7, "email": "cat@example.com"}}
		}, "both match jarvisd user 10"},
		{"two legacy households, one household", func(f *fixture, acc *Accounts, _ *ImportOptions) {
			acc.Emails[12] = "cat@example.com"
			acc.Memberships = append(acc.Memberships, Membership{nh1, 12, "admin"})
		}, "both map to jarvisd household " + nh1},
		{"override to a missing household", func(_ *fixture, _ *Accounts, opt *ImportOptions) {
			opt.Households = map[string]string{lh1: "nope"}
		}, "jarvisd has no such household"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			f, acc, opt := baseFixture(), baseAccounts(), ImportOptions{Apply: true}
			tc.edit(f, &acc, &opt)
			rep := runImport(t, e, f.bundle(t), acc, opt)
			if rep.Applied || len(rep.Refusals) == 0 || !strings.Contains(strings.Join(rep.Refusals, "\n"), tc.want) {
				t.Fatalf("want refusal %q:\n%s", tc.want, reportText(rep))
			}
			if n := count(t, e, `SELECT COUNT(*) FROM recipes_import_log`) + count(t, e, `SELECT COUNT(*) FROM recipes_recipes`); n != 0 {
				t.Fatal("a refused run wrote rows")
			}
		})
	}
}

func TestImportRefusesAnAccountAnEarlierRunGaveAway(t *testing.T) {
	e := setup(t)
	rep := runImport(t, e, baseFixture().bundle(t), baseAccounts(), ImportOptions{Apply: true})
	if !rep.Applied {
		t.Fatal(reportText(rep))
	}
	// A later bundle where cat (legacy 7) has bob's email: jarvisd 11 already holds legacy 4's rows.
	f := baseFixture()
	f.tables["auth_users.json"] = []map[string]any{{"id": 1, "email": "ann@example.com"}, {"id": 7, "email": "bob@example.com"}}
	f.tables["recipes.json"] = []map[string]any{{"id": 9, "user_id": "7", "household_id": nil, "title": "x", "source_type": "MANUAL"}}
	rep = runImport(t, e, f.bundle(t), baseAccounts(), ImportOptions{Apply: true})
	if rep.Applied || !strings.Contains(strings.Join(rep.Refusals, "\n"), "earlier run") {
		t.Fatalf("want refusal:\n%s", reportText(rep))
	}
}

func TestImportAmbiguousHousehold(t *testing.T) {
	e := setup(t)
	acc := baseAccounts()
	acc.HouseholdNames[nh2] = "Weekend"
	acc.Memberships = append(acc.Memberships, Membership{nh2, 10, "admin"}, Membership{nh2, 11, "member"})
	acc.HouseholdNames[nh1] = "Home" // no name match either
	b := baseFixture().bundle(t)
	rep := runImport(t, e, b, acc, ImportOptions{Apply: true})
	if len(rep.UnmappedHouseholds) != 2 || !strings.Contains(reportText(rep), "ambiguous") || rep.Counts["recipe"].Imported != 1 {
		t.Fatalf("ambiguous:\n%s", reportText(rep))
	}
	// --household settles it; the re-run brings in the household rows.
	rep = runImport(t, e, b, acc, ImportOptions{Apply: true, Households: map[string]string{lh1: nh2}})
	if rep.Counts["recipe"].Imported != 4 || rep.Households[0].How != "override" {
		t.Fatalf("override:\n%s", reportText(rep))
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_recipes WHERE household_id = ?`, nh2); n != 4 {
		t.Fatalf("%d recipes in the chosen household", n)
	}
}

func TestImportNarrowsBySameName(t *testing.T) {
	e := setup(t)
	acc := baseAccounts()
	acc.Memberships = append(acc.Memberships, Membership{nh2, 10, "admin"}) // "Other" vs "Kitchen"
	rep := runImport(t, e, baseFixture().bundle(t), acc, ImportOptions{})
	if len(rep.Households) != 1 || rep.Households[0].New != nh1 {
		t.Fatalf("narrowing:\n%s", reportText(rep))
	}
}

func TestImportRemapsTakenIDs(t *testing.T) {
	e := setup(t)
	// jarvisd already has recipe 1 and plan 1 (made after cutover).
	for _, q := range []string{
		`INSERT INTO recipes_recipes (id, user_id, title) VALUES (1, '99', 'mine')`,
		`INSERT INTO recipes_meal_plans (id, user_id, start_date) VALUES (1, '99', '2026-10-01')`,
	} {
		if _, err := e.d.Write.ExecContext(e.ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rep := runImport(t, e, baseFixture().bundle(t), baseAccounts(), ImportOptions{Apply: true})
	// Only the displaced rows move; the other legacy ids stay put.
	if rep.Counts["recipe"].IDChanged != 1 || rep.Counts["meal_plan"].IDChanged != 1 {
		t.Fatalf("id changes:\n%s", reportText(rep))
	}
	var soupID, planID int64
	if err := e.d.Read.QueryRowContext(e.ctx, `SELECT id FROM recipes_recipes WHERE title = 'Soup'`).Scan(&soupID); err != nil {
		t.Fatal(err)
	}
	if soupID != 7 { // above both jarvisd's max (1) and the bundle's max legacy id (6)
		t.Fatalf("soup got id %d, want 7", soupID)
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_recipes WHERE id IN (2, 3, 4, 5)`); n != 4 {
		t.Fatal("legacy ids 2-5 not kept")
	}
	if err := e.d.Read.QueryRowContext(e.ctx, `SELECT meal_plan_id FROM recipes_meal_plan_items WHERE recipe_id = ?`, soupID).Scan(&planID); err != nil {
		t.Fatalf("the plan item does not point at the moved recipe: %v", err)
	}
	if planID == 1 {
		t.Fatal("plan item still on the old plan id")
	}
	var ingRecipe int64
	if err := e.d.Read.QueryRowContext(e.ctx, `SELECT recipe_id FROM recipes_ingredients WHERE text = 'salt'`).Scan(&ingRecipe); err != nil || ingRecipe != soupID {
		t.Fatalf("ingredient on recipe %d, want %d (%v)", ingRecipe, soupID, err)
	}
}

func TestImportMatchesExistingStaplesAndMappings(t *testing.T) {
	e := setup(t)
	for _, q := range []string{
		`INSERT INTO recipes_tags (id, name) VALUES (7, 'DINNER')`,
		`INSERT INTO recipes_staples (user_id, household_id, name) VALUES ('11', '` + nh1 + `', 'olive oil')`,
		`INSERT INTO recipes_grocery_sku_map (user_id, household_id, retailer, ingredient_name, sku) VALUES ('10', '` + nh1 + `', 'walmart', 'olive oil', '999')`,
	} {
		if _, err := e.d.Write.ExecContext(e.ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rep := runImport(t, e, baseFixture().bundle(t), baseAccounts(), ImportOptions{Apply: true})
	if rep.Counts["staple"].Matched != 1 || rep.Counts["staple"].Imported != 1 || rep.Counts["sku_map"].Matched != 1 {
		t.Fatalf("matching:\n%s", reportText(rep))
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_tags`); n != 2 {
		t.Fatalf("%d tags: an existing tag of another case was duplicated", n)
	}
	if n := count(t, e, `SELECT COUNT(*) FROM recipes_recipe_tags WHERE tag_id = 7`); n != 1 {
		t.Fatal("the existing tag was not reused")
	}
	var sku string
	if err := e.d.Read.QueryRowContext(e.ctx, `SELECT sku FROM recipes_grocery_sku_map`).Scan(&sku); err != nil || sku != "999" {
		t.Fatalf("existing mapping overwritten: %q %v", sku, err)
	}
}

func TestImportLoggedRowDeletedIsNotResurrected(t *testing.T) {
	e := setup(t)
	e.hh.set(10, nh1)
	b := baseFixture().bundle(t)
	runImport(t, e, b, baseAccounts(), ImportOptions{Apply: true})
	if c, b := e.do(t, http.MethodDelete, "/recipes/2", nil, tok(10, nh1)...); c != http.StatusNoContent {
		t.Fatalf("delete %d %s", c, b)
	}
	rep := runImport(t, e, b, baseAccounts(), ImportOptions{Apply: true})
	if rep.Counts["recipe"].Imported != 0 {
		t.Fatalf("resurrected:\n%s", reportText(rep))
	}
}

func TestLoadBundleChecks(t *testing.T) {
	t.Run("tar.gz", func(t *testing.T) {
		f := baseFixture()
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for name, data := range f.files(t) {
			if err := tw.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			tw.Write(data)
		}
		tw.Close()
		gz.Close()
		p := filepath.Join(t.TempDir(), "b.tar.gz")
		os.WriteFile(p, buf.Bytes(), 0o600)
		b, err := LoadBundle(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Recipes) != 6 || len(b.Media) != 2 || len(b.AuthMembers) != 3 {
			t.Fatalf("bundle %d recipes %d media", len(b.Recipes), len(b.Media))
		}
	})
	t.Run("sha mismatch", func(t *testing.T) {
		dir := baseFixture().dir(t)
		os.WriteFile(filepath.Join(dir, "recipes.json"), []byte("[]"), 0o644)
		if _, err := LoadBundle(dir); err == nil || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("wrong head", func(t *testing.T) {
		f := baseFixture()
		f.head = "000000000000"
		if _, err := LoadBundle(f.dir(t)); err == nil || !strings.Contains(err.Error(), "schema") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("pre-staples head", func(t *testing.T) {
		// Prod stopped one migration short of staples: no staples table, so the export writes [].
		f := baseFixture()
		f.head = LegacyAlembicHeadPreStaples
		f.tables["staples.json"] = []map[string]any{}
		if _, err := LoadBundle(f.dir(t)); err != nil {
			t.Fatalf("pre-staples bundle refused: %v", err)
		}
		f.tables["staples.json"] = []map[string]any{{"id": 1, "user_id": "1", "household_id": lh1, "name": "salt", "created_at": "2026-01-02T03:04:05"}}
		if _, err := LoadBundle(f.dir(t)); err == nil || !strings.Contains(err.Error(), "staples") {
			t.Fatalf("staples rows under the pre-staples head: err %v", err)
		}
	})
	t.Run("empty tables are json null", func(t *testing.T) {
		f := baseFixture()
		for _, k := range []string{"meal_plans.json", "meal_plan_items.json", "grocery_sku_map.json"} {
			f.tables[k] = nil
		}
		b := f.bundle(t)
		if len(b.MealPlans) != 0 || len(b.SkuMap) != 0 {
			t.Fatal("null tables")
		}
	})
	t.Run("unsafe tar path", func(t *testing.T) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		tw.WriteHeader(&tar.Header{Name: "../evil", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		tw.Write([]byte("x"))
		tw.Close()
		p := filepath.Join(t.TempDir(), "b.tar")
		os.WriteFile(p, buf.Bytes(), 0o600)
		if _, err := LoadBundle(p); err == nil {
			t.Fatal("accepted ../evil")
		}
	})
}

func TestImportSkipsNonImagePhoto(t *testing.T) {
	e := setup(t)
	f := baseFixture()
	f.media["0123456789abcdef0123456789abcdef.jpg"] = []byte("<html><script>alert(1)</script></html>")
	rep := runImport(t, e, f.bundle(t), baseAccounts(), ImportOptions{Apply: true})
	var img sql.NullString
	if err := e.d.Read.QueryRowContext(e.ctx, `SELECT image_url FROM recipes_recipes WHERE id = 1`).Scan(&img); err != nil {
		t.Fatal(err)
	}
	if img.Valid || !strings.Contains(reportText(rep), "not an image") {
		t.Fatalf("html photo kept: %v\n%s", img, reportText(rep))
	}
	if _, err := e.blobs.Stat(e.ctx, mediaBlobPrefix+"0123456789abcdef0123456789abcdef.jpg"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("html stored: %v", err)
	}
}

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{"ann@example.com": "a***@example.com", "x": "***", "@d": "***", " Bob@x.io ": "B***@x.io"} {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
