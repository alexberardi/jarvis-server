package legacyimport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PGConfig says how to reach the legacy Postgres. Password is never printed.
type PGConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	// DBNames maps LegacyDB keys to database names; a missing key uses the default name.
	DBNames map[string]string
	// SSLMode is passed through (default "prefer", as libpq).
	SSLMode string
}

// Addr is host:port.
func (c PGConfig) Addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// String describes the connection without the password.
func (c PGConfig) String() string { return fmt.Sprintf("postgres://%s@%s", c.User, c.Addr()) }

func (c PGConfig) dbName(key string) string {
	if n := c.DBNames[key]; n != "" {
		return n
	}
	if d, ok := legacyDB(key); ok {
		return d.DefaultName
	}
	return key
}

// ParsePostgresURL reads postgres://USER:PASSWORD@HOST[:PORT][/ignored][?sslmode=…]. The path's
// database is ignored: the import connects to each legacy service database on that server.
func ParsePostgresURL(raw string) (PGConfig, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse echoes the input (with the password) in its error.
		return PGConfig{}, errors.New("--from is not a postgres:// URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return PGConfig{}, errors.New("--from must be a postgres:// URL")
	}
	c := PGConfig{Host: u.Hostname(), Port: 5432, SSLMode: u.Query().Get("sslmode")}
	if c.Host == "" {
		return PGConfig{}, errors.New("--from names no host")
	}
	if p := u.Port(); p != "" {
		if c.Port, err = strconv.Atoi(p); err != nil || c.Port <= 0 || c.Port > 65535 {
			return PGConfig{}, fmt.Errorf("--from has a bad port %q", p)
		}
	}
	if u.User != nil {
		c.User = u.User.Username()
		c.Password, _ = u.User.Password()
	}
	if c.User == "" {
		return PGConfig{}, errors.New("--from names no user")
	}
	return c, nil
}

// PGSource reads the legacy Postgres read-only: every session runs with
// default_transaction_read_only=on, every query inside a READ ONLY transaction, SELECT only.
type PGSource struct {
	cfg    PGConfig
	conns  map[string]*pgx.Conn
	absent map[string]bool
}

// OpenPostgres checks that the legacy Postgres is reachable (it connects to the auth database)
// and returns the source. Connections to the other databases open on first use.
func OpenPostgres(ctx context.Context, cfg PGConfig) (*PGSource, error) {
	if cfg.Port == 0 {
		cfg.Port = 5432
	}
	s := &PGSource{cfg: cfg, conns: map[string]*pgx.Conn{}, absent: map[string]bool{}}
	if _, err := s.conn(ctx, "auth"); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *PGSource) Describe() string { return s.cfg.String() }

// redact removes the password from an error's text (pgx doesn't print it, but be sure).
func (s *PGSource) redact(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if s.cfg.Password != "" && strings.Contains(msg, s.cfg.Password) {
		return errors.New(strings.ReplaceAll(msg, s.cfg.Password, "********"))
	}
	return err
}

// errAbsent marks a database that does not exist.
var errAbsent = errors.New("database does not exist")

func (s *PGSource) conn(ctx context.Context, key string) (*pgx.Conn, error) {
	if c := s.conns[key]; c != nil {
		return c, nil
	}
	if s.absent[key] {
		return nil, errAbsent
	}
	name := s.cfg.dbName(key)
	u := url.URL{Scheme: "postgres", User: url.UserPassword(s.cfg.User, s.cfg.Password), Host: s.cfg.Addr(), Path: "/" + name}
	if s.cfg.Password == "" {
		u.User = url.User(s.cfg.User) // libpq's PGPASSWORD / ~/.pgpass apply
	}
	q := url.Values{}
	q.Set("connect_timeout", "10")
	if s.cfg.SSLMode != "" {
		q.Set("sslmode", s.cfg.SSLMode)
	}
	u.RawQuery = q.Encode()
	pc, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, errors.New("bad Postgres connection settings")
	}
	pc.RuntimeParams["default_transaction_read_only"] = "on"
	pc.RuntimeParams["application_name"] = "jarvisd-import-legacy"
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := pgx.ConnectConfig(cctx, pc)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "3D000" { // invalid_catalog_name
			s.absent[key] = true
			return nil, errAbsent
		}
		if errors.As(err, &pe) && (pe.Code == "28P01" || pe.Code == "28000") {
			return nil, fmt.Errorf("legacy Postgres at %s refused user %q (wrong password?)", s.cfg.Addr(), s.cfg.User)
		}
		return nil, fmt.Errorf("legacy Postgres is not reachable at %s (database %s): %w; is the legacy stack's postgres container running?",
			s.cfg.Addr(), name, s.redact(err))
	}
	var ro string
	if err := c.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&ro); err != nil || ro != "on" {
		c.Close(ctx)
		return nil, fmt.Errorf("legacy Postgres %s: could not make the session read-only", name)
	}
	s.conns[key] = c
	return c, nil
}

// readOnly runs fn inside a READ ONLY transaction that is always rolled back.
func (s *PGSource) readOnly(ctx context.Context, key string, fn func(pgx.Tx) error) (bool, error) {
	c, err := s.conn(ctx, key)
	if errors.Is(err, errAbsent) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tx, err := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return false, s.redact(err)
	}
	defer tx.Rollback(ctx)
	err = fn(tx)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42P01" { // undefined_table
		return false, nil
	}
	return err == nil, s.redact(err)
}

func (s *PGSource) Head(ctx context.Context, key string) (string, bool, error) {
	var head string
	ok, err := s.readOnly(ctx, key, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT version_num FROM alembic_version").Scan(&head)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("%s: alembic head: %w", s.cfg.dbName(key), err)
	}
	return head, ok, nil
}

func (s *PGSource) Rows(ctx context.Context, q Query) ([]Row, bool, error) {
	var raw string
	ok, err := s.readOnly(ctx, q.DB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, selectSQL(q)).Scan(&raw)
	})
	if err != nil {
		return nil, false, fmt.Errorf("%s.%s: %w", s.cfg.dbName(q.DB), q.Table, err)
	}
	if !ok {
		return nil, false, nil
	}
	rows, err := decodeRows([]byte(raw))
	if err != nil {
		return nil, false, fmt.Errorf("%s.%s: %w", s.cfg.dbName(q.DB), q.Table, err)
	}
	return rows, true, nil
}

func (s *PGSource) Close() error {
	for k, c := range s.conns {
		c.Close(context.Background())
		delete(s.conns, k)
	}
	return nil
}
