// Package db opens jarvisd's single SQLite database and runs per-module migrations.
//
// SQLite allows one writer at a time, so DB keeps two pools (PLAN §3.2):
//   - Write: exactly one connection; every write and write transaction goes here, so writers
//     queue in Go instead of failing with SQLITE_BUSY.
//   - Read: a pool of read-only connections; WAL lets them run alongside the writer.
//
// The driver is modernc.org/sqlite (pure Go), which keeps the build cgo-free (PLAN §3.4).
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

type DB struct {
	Write *sql.DB
	Read  *sql.DB
}

// ownerOnly makes the database file (created empty if missing) and any WAL/shm files
// readable by the owner only: the DB holds signing keys and credentials. SQLite creates the
// -wal and -shm files with the database file's permissions. (Windows ignores the mode; the
// data dir's ACL protects it there.)
func ownerOnly(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("db: create file: %w", err)
	}
	f.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("db: restrict %s: %w", filepath.Base(p), err)
		}
	}
	return nil
}

// Open opens (creating if needed) the database file at path, owner-only.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("db: create dir: %w", err)
	}
	if err := ownerOnly(path); err != nil {
		return nil, err
	}
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, fmt.Errorf("db: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	// The writer must exist (and WAL be set) before read-only connections open the file.
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("db: ping writer: %w", err)
	}

	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("db: open reader: %w", err)
	}
	n := max(4, runtime.NumCPU())
	r.SetMaxOpenConns(n)
	r.SetMaxIdleConns(n)
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, fmt.Errorf("db: ping reader: %w", err)
	}
	return &DB{Write: w, Read: r}, nil
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"foreign_keys(1)", // per connection; the attention cascade depends on it (doc 10)
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
	} {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		// BEGIN IMMEDIATE: take the write lock up front rather than failing on upgrade.
		q.Set("_txlock", "immediate")
	}
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// Close closes both pools.
func (d *DB) Close() error {
	return errors.Join(d.Read.Close(), d.Write.Close())
}

// Tx runs fn inside a write transaction, committing if fn returns nil.
func (d *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
