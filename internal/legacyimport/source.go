// Package legacyimport carries a legacy (Python/Postgres) Jarvis install's data into a fresh
// jarvisd database, keeping ids (docs/install/legacy-import.md, ID6r): users, households,
// memberships, node registrations and grants, cc nodes, rooms, devices, settings, memories,
// routines and active schedules, phone contacts and finished calls, unexpired signals,
// proposal suppressions and inbox items.
//
// The legacy data comes from a Source: the legacy Postgres itself, read-only (postgres.go;
// compose.go finds its credentials in a compose directory), or JSON fixtures in tests. Run
// validates every row against jarvisd's own constraints inside one transaction and commits only
// when asked to and when nothing failed.
package legacyimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Row is one legacy row, as Postgres' row_to_json renders it: strings, json.Number, bools,
// nil.
type Row map[string]any

// Query names one legacy table and the columns the import reads from it.
type Query struct {
	DB      string // a LegacyDB key
	Table   string
	Columns []string
	Order   string // ORDER BY clause (column names), "" for none
}

// Source yields the legacy rows.
type Source interface {
	// Describe names the source for the report, without secrets.
	Describe() string
	// Head returns a legacy database's alembic head; present is false when the database (or
	// its alembic_version table) does not exist.
	Head(ctx context.Context, db string) (head string, present bool, err error)
	// Rows returns a table's rows in Query.Order; present is false when the database or the
	// table does not exist.
	Rows(ctx context.Context, q Query) (rows []Row, present bool, err error)
	Close() error
}

// LegacyDB is one legacy service database the import reads.
type LegacyDB struct {
	Key         string // short name used in queries, flags and the report
	DefaultName string // its Postgres database name on a default install
	EnvName     string // the DB_NAME_<X> variable naming it in ./jarvis's databases.env
	Module      string // the jarvisd module whose <module>_settings its settings go to; "" for none
	// Pin is the alembic head this importer was written against; "" accepts any head (the
	// database contributes only its settings table, whose shape never changed).
	Pin string
	// Required databases must exist.
	Required bool
}

// LegacyDBs lists every legacy database the import reads, in import order.
var LegacyDBs = []LegacyDB{
	{Key: "auth", DefaultName: "jarvis_auth", EnvName: "DB_NAME_AUTH", Module: "auth", Pin: "c5d6e7f8a9b0", Required: true},
	{Key: "cc", DefaultName: "jarvis_command_center", EnvName: "DB_NAME_COMMAND_CENTER", Module: "cc", Pin: "sb01signals", Required: true},
	{Key: "notifications", DefaultName: "jarvis_notifications", EnvName: "DB_NAME_NOTIFICATIONS", Pin: "002"},
	{Key: "config", DefaultName: "jarvis_config", EnvName: "DB_NAME_CONFIG", Module: "config"},
	{Key: "llm", DefaultName: "jarvis_llm_proxy", EnvName: "DB_NAME_LLM_PROXY", Module: "llm"},
	{Key: "tts", DefaultName: "jarvis_tts", EnvName: "DB_NAME_TTS", Module: "tts"},
	{Key: "stt", DefaultName: "jarvis_whisper", EnvName: "DB_NAME_WHISPER", Module: "stt"},
	{Key: "ocr", DefaultName: "jarvis_ocr", EnvName: "DB_NAME_OCR", Module: "ocr"},
	{Key: "recipes", DefaultName: "jarvis_recipes", EnvName: "DB_NAME_RECIPES", Module: "recipes"},
	{Key: "logs", DefaultName: "jarvis_logs", EnvName: "DB_NAME_LOGS", Module: "logs"},
}

func legacyDB(key string) (LegacyDB, bool) {
	for _, d := range LegacyDBs {
		if d.Key == key {
			return d, true
		}
	}
	return LegacyDB{}, false
}

// DefaultDBNames maps every LegacyDB key to its default database name.
func DefaultDBNames() map[string]string {
	m := map[string]string{}
	for _, d := range LegacyDBs {
		m[d.Key] = d.DefaultName
	}
	return m
}

// DirSource reads fixtures: <dir>/heads.json ({"auth": "c5d6e7f8a9b0", …}; a database missing
// there is absent) and one JSON array per table, <dir>/<db>.<table>.json (a missing file is an
// absent table). Rows keep only the queried columns, as Postgres would return them. For tests.
type DirSource struct{ Dir string }

func (s DirSource) Describe() string { return "fixtures in " + s.Dir }
func (s DirSource) Close() error     { return nil }

func (s DirSource) heads() (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "heads.json"))
	if err != nil {
		return nil, err
	}
	var h map[string]string
	return h, json.Unmarshal(b, &h)
}

func (s DirSource) Head(_ context.Context, db string) (string, bool, error) {
	h, err := s.heads()
	if err != nil {
		return "", false, err
	}
	v, ok := h[db]
	return v, ok, nil
}

func (s DirSource) Rows(_ context.Context, q Query) ([]Row, bool, error) {
	h, err := s.heads()
	if err != nil {
		return nil, false, err
	}
	if _, ok := h[q.DB]; !ok {
		return nil, false, nil
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, q.DB+"."+q.Table+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	rows, err := decodeRows(b)
	if err != nil {
		return nil, false, fmt.Errorf("%s.%s: %w", q.DB, q.Table, err)
	}
	for _, r := range rows {
		for k := range r {
			if !contains(q.Columns, k) {
				delete(r, k)
			}
		}
		for _, c := range q.Columns {
			if _, ok := r[c]; !ok {
				return nil, false, fmt.Errorf("%s.%s: fixture row lacks column %q", q.DB, q.Table, c)
			}
		}
	}
	return rows, true, nil
}

// decodeRows decodes a JSON array of objects, keeping numbers exact.
func decodeRows(b []byte) ([]Row, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var rows []Row
	if err := dec.Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// selectSQL renders q as one SELECT returning a JSON array (Postgres).
func selectSQL(q Query) string {
	cols := make([]string, len(q.Columns))
	for i, c := range q.Columns {
		cols[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	sel := "SELECT " + strings.Join(cols, ", ") + ` FROM "` + q.Table + `"`
	if q.Order != "" {
		sel += " ORDER BY " + q.Order
	}
	return "SELECT coalesce(json_agg(t), '[]'::json)::text FROM (" + sel + ") t"
}
