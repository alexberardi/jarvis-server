package db

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func migrationsFS(module string, names ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for _, n := range names {
		fsys[n] = &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE " + module + "_" + strings.TrimSuffix(n, ".sql") + " (id INTEGER);\n-- +goose Down\n")}
	}
	return fsys
}

func TestDowngradeGuard(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	older := migrationsFS("mod", "00001_a.sql", "00002_b.sql")
	newer := migrationsFS("mod", "00001_a.sql", "00002_b.sql", "00003_c.sql")

	// Nothing migrated yet: passes.
	if err := CheckDowngrade(ctx, d, "mod", older); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, d, "mod", older); err != nil {
		t.Fatal(err)
	}
	if err := CheckDowngrade(ctx, d, "mod", older); err != nil {
		t.Fatal(err)
	}
	// The newer binary migrates; the older one is refused.
	if err := Migrate(ctx, d, "mod", newer); err != nil {
		t.Fatal(err)
	}
	err = CheckDowngrade(ctx, d, "mod", older)
	var de *DowngradeError
	if !errors.As(err, &de) || de.Module != "mod" || len(de.Unknown) != 1 || de.Unknown[0] != 3 || de.Latest != 2 {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "newer jarvisd") {
		t.Fatal(err)
	}
	// An out-of-order migration the old binary lacks (numbered below its latest) also counts.
	gap := migrationsFS("gap", "00001_a.sql", "00003_c.sql", "00005_e.sql")
	if err := Migrate(ctx, d, "gap", gap); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, d, "gap", migrationsFS("gap", "00001_a.sql", "00002_b.sql", "00003_c.sql", "00005_e.sql")); err != nil {
		t.Fatal(err)
	}
	if err := CheckDowngrade(ctx, d, "gap", gap); !errors.As(err, &de) || de.Unknown[0] != 2 {
		t.Fatalf("out of order: %v", err)
	}

	got, err := AppliedVersions(ctx, d.Read)
	if err != nil {
		t.Fatal(err)
	}
	if v := got["goose_mod"]; len(v) != 3 || v[2] != 3 {
		t.Fatalf("applied %v", got)
	}
}
