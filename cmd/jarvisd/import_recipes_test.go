package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// emptyBundle writes a valid bundle with no rows.
func emptyBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{}
	for _, f := range []string{"recipes", "ingredients", "steps", "tags", "recipe_tags", "meal_plans", "meal_plan_items",
		"staples", "grocery_sku_map", "auth_users", "auth_households", "auth_household_memberships"} {
		if err := os.WriteFile(filepath.Join(dir, f+".json"), []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256([]byte("[]"))
		files[f+".json"] = hex.EncodeToString(s[:])
	}
	man, _ := json.Marshal(recipes.BundleManifest{Format: recipes.BundleFormat, RecipesAlembicHead: recipes.LegacyAlembicHead, Files: files})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), man, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestImportRecipesCommand(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("JARVIS_HOME", home)
	bundle := emptyBundle(t)
	var out bytes.Buffer

	if err := run(ctx, []string{"import-recipes"}, &out); err == nil || !strings.Contains(err.Error(), "needs the bundle") {
		t.Fatalf("no bundle: %v", err)
	}
	if err := run(ctx, []string{"import-recipes", "--household", "nope", bundle}, &out); err == nil || !strings.Contains(err.Error(), "LEGACY_ID=JARVISD_ID") {
		t.Fatalf("bad --household: %v", err)
	}
	// A database jarvisd never migrated: refuse with what to do.
	if err := run(ctx, []string{"import-recipes", bundle}, &out); err == nil || !strings.Contains(err.Error(), "start jarvisd") {
		t.Fatalf("unmigrated: %v", err)
	}

	d, err := db.Open(ctx, filepath.Join(home, "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range modules() {
		if fsys := m.Migrations(); fsys != nil {
			if err := db.Migrate(ctx, d, m.Name(), fsys); err != nil {
				t.Fatal(err)
			}
		}
	}
	d.Close()
	out.Reset()
	// Flags after the bundle work too.
	if err := run(ctx, []string{"import-recipes", bundle, "--legacy-host", "old:7030"}, &out); err != nil {
		t.Fatalf("dry run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "DRY RUN") {
		t.Fatalf("report:\n%s", out.String())
	}
	out.Reset()
	if err := run(ctx, []string{"import-recipes", "--apply", bundle}, &out); err != nil || !strings.Contains(out.String(), "APPLIED") {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
}
