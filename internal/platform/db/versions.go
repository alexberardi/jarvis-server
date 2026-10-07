package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Applied versions and the downgrade guard (ID10, 00-installers §4.3).

// AppliedVersions reads every goose_* table's applied migration versions, sorted (goose's
// version-0 bootstrap row excluded). Self-update compares them before and after an upgrade to
// know whether the new binary migrated the database.
func AppliedVersions(ctx context.Context, q *sql.DB) (map[string][]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'goose\_%' ESCAPE '\'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string][]int64{}
	for _, t := range tables {
		if !tableName.MatchString(t) {
			continue
		}
		v, err := appliedIn(ctx, q, t)
		if err != nil {
			return nil, err
		}
		out[t] = v
	}
	return out, nil
}

var tableName = regexp.MustCompile(`^goose_[a-z0-9_]+$`)

func appliedIn(ctx context.Context, q *sql.DB, table string) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT version_id FROM "`+table+`" WHERE is_applied AND version_id > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	slices.Sort(out)
	return out, rows.Err()
}

// knownVersions lists the migration versions in a goose directory (NNNNN_name.sql/.go).
func knownVersions(fsys fs.FS) (map[int64]bool, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	known := map[int64]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".go")) {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		if !ok {
			continue
		}
		if v, err := strconv.ParseInt(num, 10, 64); err == nil {
			known[v] = true
		}
	}
	return known, nil
}

// DowngradeError means the database holds migrations this binary doesn't have: a newer
// jarvisd migrated it.
type DowngradeError struct {
	Module  string
	Unknown []int64 // applied versions this binary has no migration for
	Latest  int64   // the newest migration this binary knows
}

func (e *DowngradeError) Error() string {
	return fmt.Sprintf("the database was migrated by a newer jarvisd: module %s has migration(s) %v applied, "+
		"but this jarvisd only knows up to %d", e.Module, e.Unknown, e.Latest)
}

// CheckDowngrade reports a DowngradeError when the module's version table holds a version
// that isn't one of its migrations (out-of-order migrations are allowed, so "unknown" rather
// than only "higher than the latest" is what proves a newer binary ran). A module whose table
// doesn't exist yet passes.
func CheckDowngrade(ctx context.Context, d *DB, module string, migrations fs.FS) error {
	if !moduleName.MatchString(module) {
		return fmt.Errorf("db: invalid module name %q", module)
	}
	table := versionTable(module)
	var n int
	if err := d.Read.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	known, err := knownVersions(migrations)
	if err != nil {
		return err
	}
	applied, err := appliedIn(ctx, d.Read, table)
	if err != nil {
		return err
	}
	e := &DowngradeError{Module: module}
	for v := range known {
		e.Latest = max(e.Latest, v)
	}
	for _, v := range applied {
		if !known[v] {
			e.Unknown = append(e.Unknown, v)
		}
	}
	if len(e.Unknown) > 0 {
		return e
	}
	return nil
}
