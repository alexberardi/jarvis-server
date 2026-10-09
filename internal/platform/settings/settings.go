// Package settings is the scoped runtime-settings framework every module shares, ported from
// jarvis-settings-client (docs/cc/00-platform.md §3.2).
//
// Each module declares its keys as Definitions and gets its own table, <module>_settings
// (every legacy service had its own `settings` table). A value is looked up through a cascade,
// most specific first:
//
//  1. user + node + household (all three given)
//  2. user only (household and node NULL)
//  3. node (household + node, user NULL)
//  4. household (household only)
//  5. system (all NULL)
//
// then the definition's env fallback, then its default. Unlike the Python client there is no
// cache: jarvisd is one process, SQLite reads are cheap, and every read sees the latest write.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing/fstest"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// Type is a setting's value type.
type Type string

const (
	String Type = "string"
	Int    Type = "int"
	Float  Type = "float"
	Bool   Type = "bool"
	JSON   Type = "json"
)

// Definition declares one setting.
type Definition struct {
	Key            string
	Category       string
	Type           Type
	Default        any
	Description    string
	EnvFallback    string // env var consulted when no DB row exists; "" for none (M3: secrets/URLs only)
	RequiresReload bool
	IsSecret       bool
	Options        []any
	// Validate, when set, checks a value before Set stores it. nil (which clears the scope's
	// value) is never validated. The error's text is shown to whoever wrote the value.
	Validate func(v any) error
}

// Scope selects where a value is read or written. Zero fields are unset; user ids start at 1.
type Scope struct {
	HouseholdID string
	NodeID      string
	UserID      int64
}

// ErrUnknownKey is returned for a key with no Definition.
var ErrUnknownKey = errors.New("settings: unknown key")

// ErrInvalidValue wraps a Definition.Validate rejection (errors.Is; the message follows it).
var ErrInvalidValue = errors.New("settings: invalid value")

// Service reads and writes one module's settings.
type Service struct {
	db     *db.DB
	table  string
	defs   map[string]Definition
	log    *slog.Logger
	getenv func(string) string
}

var moduleName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// New creates the service for a module. Call Migrate before use.
func New(d *db.DB, module string, defs []Definition, log *slog.Logger) (*Service, error) {
	if !moduleName.MatchString(module) {
		return nil, fmt.Errorf("settings: invalid module name %q", module)
	}
	m := make(map[string]Definition, len(defs))
	for _, def := range defs {
		if _, dup := m[def.Key]; dup {
			return nil, fmt.Errorf("settings: duplicate key %q", def.Key)
		}
		switch def.Type {
		case String, Int, Float, Bool, JSON:
		default:
			return nil, fmt.Errorf("settings: %s has unknown type %q", def.Key, def.Type)
		}
		m[def.Key] = def
	}
	return &Service{db: d, table: module + "_settings", defs: m, log: log, getenv: os.Getenv}, nil
}

// Migrate creates the module's settings table (version table goose_<module>_settings).
func (s *Service) Migrate(ctx context.Context) error {
	sqlText := fmt.Sprintf(`-- +goose Up
CREATE TABLE %[1]s (
    id              INTEGER PRIMARY KEY,
    key             TEXT    NOT NULL,
    value           TEXT,
    value_type      TEXT    NOT NULL DEFAULT 'string',
    category        TEXT    NOT NULL DEFAULT 'general',
    description     TEXT,
    requires_reload INTEGER NOT NULL DEFAULT 0,
    is_secret       INTEGER NOT NULL DEFAULT 0,
    env_fallback    TEXT,
    household_id    TEXT,
    node_id         TEXT,
    user_id         INTEGER,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now'))
);
-- One row per (key, scope). COALESCE because NULLs never collide in a UNIQUE index; Postgres
-- allowed duplicate system rows, SQLite won't.
CREATE UNIQUE INDEX %[1]s_scope ON %[1]s (key, COALESCE(household_id, ''), COALESCE(node_id, ''), COALESCE(user_id, 0));
CREATE INDEX %[1]s_category ON %[1]s (category);
-- +goose Down
DROP TABLE %[1]s;
`, s.table)
	fsys := fstest.MapFS{"00001_settings.sql": {Data: []byte(sqlText)}}
	return db.Migrate(ctx, s.db, s.table, fsys)
}

// Definition returns a key's definition.
func (s *Service) Definition(key string) (Definition, bool) {
	d, ok := s.defs[key]
	return d, ok
}

// Definitions returns every definition, sorted by (category, key).
func (s *Service) Definitions() []Definition {
	out := make([]Definition, 0, len(s.defs))
	for _, d := range s.defs {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Definition) int {
		if c := strings.Compare(a.Category, b.Category); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

// Categories returns the sorted unique categories.
func (s *Service) Categories() []string {
	var out []string
	for _, d := range s.defs {
		if !slices.Contains(out, d.Category) {
			out = append(out, d.Category)
		}
	}
	slices.Sort(out)
	return out
}

// Value is a resolved setting.
type Value struct {
	Value  any
	FromDB bool
}

type level struct {
	need  func(Scope) bool
	where string
	args  func(Scope) []any
}

var cascade = []level{
	{func(s Scope) bool { return s.HouseholdID != "" && s.NodeID != "" && s.UserID != 0 },
		"household_id = ? AND node_id = ? AND user_id = ?",
		func(s Scope) []any { return []any{s.HouseholdID, s.NodeID, s.UserID} }},
	{func(s Scope) bool { return s.UserID != 0 },
		"household_id IS NULL AND node_id IS NULL AND user_id = ?",
		func(s Scope) []any { return []any{s.UserID} }},
	{func(s Scope) bool { return s.HouseholdID != "" && s.NodeID != "" },
		"household_id = ? AND node_id = ? AND user_id IS NULL",
		func(s Scope) []any { return []any{s.HouseholdID, s.NodeID} }},
	{func(s Scope) bool { return s.HouseholdID != "" },
		"household_id = ? AND node_id IS NULL AND user_id IS NULL",
		func(s Scope) []any { return []any{s.HouseholdID} }},
	{func(Scope) bool { return true },
		"household_id IS NULL AND node_id IS NULL AND user_id IS NULL",
		func(Scope) []any { return nil }},
}

// Get resolves key at scope. A DB error is returned (callers decide whether to fall back);
// an unknown key returns ErrUnknownKey.
func (s *Service) Get(ctx context.Context, key string, sc Scope) (Value, error) {
	def, ok := s.defs[key]
	if !ok {
		return Value{}, ErrUnknownKey
	}
	for _, l := range cascade {
		if !l.need(sc) {
			continue
		}
		var raw sql.NullString
		var vt string
		err := s.db.Read.QueryRowContext(ctx,
			"SELECT value, value_type FROM "+s.table+" WHERE key = ? AND "+l.where+" ORDER BY id LIMIT 1",
			append([]any{key}, l.args(sc)...)...).Scan(&raw, &vt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return Value{}, fmt.Errorf("settings: get %s: %w", key, err)
		}
		if !raw.Valid {
			// A NULL value counts as "found in DB" but falls through to env/default.
			return Value{Value: s.fallback(def), FromDB: true}, nil
		}
		// The row's own value_type wins over the definition's (parity with the Python client).
		return Value{Value: s.coerce(raw.String, Type(vt), def), FromDB: true}, nil
	}
	return Value{Value: s.fallback(def)}, nil
}

// GetExact reads key at exactly sc, with no cascade and no env or default fallback: found is
// false when that scope has no row or the row's value is NULL or empty. For values that must
// come as a set from one level (per-household credentials), where the cascade would mix them.
func (s *Service) GetExact(ctx context.Context, key string, sc Scope) (value any, found bool, err error) {
	def, ok := s.defs[key]
	if !ok {
		return nil, false, ErrUnknownKey
	}
	where := "household_id IS NULL AND node_id IS NULL AND user_id IS NULL"
	var args []any
	for _, l := range cascade {
		if l.need(sc) {
			where, args = l.where, l.args(sc)
			break
		}
	}
	var raw sql.NullString
	var vt string
	err = s.db.Read.QueryRowContext(ctx,
		"SELECT value, value_type FROM "+s.table+" WHERE key = ? AND "+where+" ORDER BY id LIMIT 1",
		append([]any{key}, args...)...).Scan(&raw, &vt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("settings: get %s: %w", key, err)
	}
	if !raw.Valid || raw.String == "" {
		return nil, false, nil
	}
	return s.coerce(raw.String, Type(vt), def), true, nil
}

func (s *Service) fallback(def Definition) any {
	if def.EnvFallback != "" {
		if v, ok := lookupEnv(s.getenv, def.EnvFallback); ok {
			return s.coerce(v, def.Type, def)
		}
	}
	return def.Default
}

func lookupEnv(getenv func(string) string, k string) (string, bool) {
	if getenv == nil {
		return "", false
	}
	v := getenv(k)
	return v, v != ""
}

// coerce parses a stored string. Empty means default; a parse failure logs and uses default.
func (s *Service) coerce(raw string, t Type, def Definition) any {
	if raw == "" {
		return def.Default
	}
	var (
		v   any
		err error
	)
	switch t {
	case Int:
		v, err = strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	case Float:
		v, err = strconv.ParseFloat(strings.TrimSpace(raw), 64)
	case Bool:
		switch strings.ToLower(raw) {
		case "true", "1", "yes", "on":
			v = true
		default:
			v = false
		}
	case JSON:
		var j any
		err = json.Unmarshal([]byte(raw), &j)
		v = j
	default:
		v = raw
	}
	if err != nil {
		s.log.Warn("settings: cannot coerce value; using default", "key", def.Key, "type", t, "err", err)
		return def.Default
	}
	return v
}

// Serialize renders a value for storage, as the Python client did.
func Serialize(v any, t Type) (*string, error) {
	if v == nil {
		return nil, nil
	}
	var out string
	switch t {
	case JSON:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out = string(b)
	case Bool:
		if truthy(v) {
			out = "true"
		} else {
			out = "false"
		}
	default:
		out = pyStr(v, t)
	}
	return &out, nil
}

// pyStr matches Python's str() for values decoded from a JSON request body, where every
// number arrives as float64: 5 -> "5" for an int setting, 5 -> "5.0" for a float one.
func pyStr(v any, t Type) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == float64(int64(x)) && t != Float {
			return strconv.FormatInt(int64(x), 10)
		}
		s := strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s
	default:
		return fmt.Sprint(x)
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int, int64:
		return fmt.Sprint(x) != "0"
	default:
		return v != nil
	}
}

// Validate runs key's Definition.Validate on value: nil when it passes (or the key has no
// validator, or value is nil), else an error wrapping ErrInvalidValue.
func (s *Service) Validate(key string, value any) error {
	def, ok := s.defs[key]
	if !ok {
		return ErrUnknownKey
	}
	if def.Validate == nil || value == nil {
		return nil
	}
	if err := def.Validate(value); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidValue, err.Error())
	}
	return nil
}

// InvalidValueMessage is a validation error's text without the ErrInvalidValue prefix.
func InvalidValueMessage(err error) string {
	return strings.TrimPrefix(err.Error(), ErrInvalidValue.Error()+": ")
}

// Set upserts the value at exactly scope. A value its Definition.Validate rejects is not
// stored (ErrInvalidValue).
func (s *Service) Set(ctx context.Context, key string, value any, sc Scope) error {
	def, ok := s.defs[key]
	if !ok {
		return ErrUnknownKey
	}
	if err := s.Validate(key, value); err != nil {
		return err
	}
	ser, err := Serialize(value, def.Type)
	if err != nil {
		return fmt.Errorf("settings: serialize %s: %w", key, err)
	}
	_, err = s.db.Write.ExecContext(ctx, `
		INSERT INTO `+s.table+` (key, value, value_type, category, description, requires_reload, is_secret,
		                         env_fallback, household_id, node_id, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key, COALESCE(household_id, ''), COALESCE(node_id, ''), COALESCE(user_id, 0))
		DO UPDATE SET value = excluded.value, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`,
		key, ser, string(def.Type), def.Category, def.Description, def.RequiresReload, def.IsSecret,
		nullString(def.EnvFallback), nullString(sc.HouseholdID), nullString(sc.NodeID), nullInt(sc.UserID))
	if err != nil {
		return fmt.Errorf("settings: set %s: %w", key, err)
	}
	return nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}

// Typed getters for module code. A DB error logs and yields the definition default, which is
// what the Python client did (it swallowed DB errors).

func (s *Service) value(ctx context.Context, key string, sc Scope) any {
	v, err := s.Get(ctx, key, sc)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownKey):
		case ctx.Err() != nil || errors.Is(err, context.Canceled):
			// The caller went away (its request ended, jarvisd is stopping): nothing failed.
			s.log.Debug("settings: read cancelled; using default", "key", key, "err", err)
		default:
			s.log.Error("settings: read failed; using default", "key", key, "err", err)
		}
		return s.defs[key].Default
	}
	return v.Value
}

func (s *Service) String(ctx context.Context, key string, sc Scope) string {
	switch v := s.value(ctx, key, sc).(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func (s *Service) Bool(ctx context.Context, key string, sc Scope) bool {
	b, _ := s.value(ctx, key, sc).(bool)
	return b
}

func (s *Service) Int(ctx context.Context, key string, sc Scope) int64 {
	switch v := s.value(ctx, key, sc).(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

func (s *Service) Float(ctx context.Context, key string, sc Scope) float64 {
	switch v := s.value(ctx, key, sc).(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	}
	return 0
}
