package db

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

var moduleName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// versionTable is the goose bookkeeping table for one module. Each module migrates
// independently, so modules can be added, ported and imported one at a time.
func versionTable(module string) string { return "goose_" + module }

func provider(d *DB, module string, migrations fs.FS) (*goose.Provider, error) {
	if !moduleName.MatchString(module) {
		return nil, fmt.Errorf("db: invalid module name %q", module)
	}
	store, err := database.NewStore(database.DialectSQLite3, versionTable(module))
	if err != nil {
		return nil, err
	}
	return goose.NewProvider("", d.Write, migrations, goose.WithStore(store))
}

// Migrate applies a module's pending migrations (goose SQL files at the root of migrations).
func Migrate(ctx context.Context, d *DB, module string, migrations fs.FS) error {
	p, err := provider(d, module, migrations)
	if err != nil {
		return fmt.Errorf("db: migrations for %s: %w", module, err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("db: migrate %s: %w", module, err)
	}
	return nil
}

// MigrationStatus reports a module's applied and pending migrations.
type MigrationStatus struct {
	Module  string
	Current int64
	Latest  int64
	Pending int
}

func Status(ctx context.Context, d *DB, module string, migrations fs.FS) (MigrationStatus, error) {
	p, err := provider(d, module, migrations)
	if err != nil {
		return MigrationStatus{}, err
	}
	st, err := p.Status(ctx)
	if err != nil {
		return MigrationStatus{}, err
	}
	out := MigrationStatus{Module: module}
	for _, s := range st {
		v := s.Source.Version
		out.Latest = max(out.Latest, v)
		if s.State == goose.StateApplied {
			out.Current = max(out.Current, v)
		} else {
			out.Pending++
		}
	}
	return out, nil
}
