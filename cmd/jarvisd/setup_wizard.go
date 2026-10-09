package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// wizardUnfinished reports whether a superuser exists but the setup wizard was never
// finished (admin setting setup.completed not true): after import-legacy, or a wizard left
// after its Account step. Any error (no database, no read access) reports false.
func wizardUnfinished(dbPath string) bool {
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	c, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return false
	}
	defer c.Close()
	var supers int
	if err := c.QueryRow(`SELECT COUNT(*) FROM auth_users WHERE is_superuser = 1`).Scan(&supers); err != nil || supers == 0 {
		return false
	}
	var v sql.NullString
	err = c.QueryRow(`SELECT value FROM admin_settings WHERE key = 'setup.completed'
		AND household_id IS NULL AND node_id IS NULL AND user_id IS NULL ORDER BY id LIMIT 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		// No admin_settings table yet: the admin module never registered, so never finished.
		return strings.Contains(err.Error(), "no such table")
	}
	switch strings.ToLower(strings.TrimSpace(v.String)) {
	case "true", "1", "yes", "on":
		return false
	}
	return true
}
