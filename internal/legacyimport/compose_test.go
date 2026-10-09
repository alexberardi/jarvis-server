package legacyimport

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverInstallerLayout(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "docker-compose.yml"), "name: jarvis\n")
	write(t, filepath.Join(dir, ".env"), `# generated
POSTGRES_PASSWORD="s3cret #not a comment"
DB_USER=jarvis
POSTGRES_PORT=5433 # published port
JARVIS_INFRA_BIND_HOST=0.0.0.0
export DB_NAME_AUTH='auth_custom'
`)
	d, err := DiscoverCompose(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Layout != "installer" || d.PG.Host != "127.0.0.1" || d.PG.Port != 5433 || d.PG.User != "jarvis" ||
		d.PG.Password != "s3cret #not a comment" || d.PG.dbName("auth") != "auth_custom" || d.PG.dbName("cc") != "jarvis_command_center" {
		t.Fatalf("got %+v", d)
	}
	if strings.Contains(d.PG.String(), "s3cret") {
		t.Fatalf("String leaks the password: %s", d.PG)
	}
	// Defaults: user jarvis, port 5432, loopback.
	write(t, filepath.Join(dir, ".env"), "POSTGRES_PASSWORD=x\n")
	if d, err = DiscoverCompose(dir, ""); err != nil || d.PG.User != "jarvis" || d.PG.Port != 5432 || d.PG.Host != "127.0.0.1" {
		t.Fatalf("defaults: %+v %v", d, err)
	}
	write(t, filepath.Join(dir, ".env"), "DB_USER=jarvis\n")
	if _, err := DiscoverCompose(dir, ""); err == nil || !strings.Contains(err.Error(), "POSTGRES_PASSWORD") {
		t.Fatalf("no password: %v", err)
	}
	write(t, filepath.Join(dir, ".env"), "POSTGRES_PASSWORD=x\nPOSTGRES_PORT=http\n")
	if _, err := DiscoverCompose(dir, ""); err == nil || !strings.Contains(err.Error(), "not a port") {
		t.Fatalf("bad port: %v", err)
	}
}

func TestDiscoverSourceLayout(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "jarvis-data-services", ".env"), "POSTGRES_USER=postgres\nPOSTGRES_PASSWORD=pw\nPOSTGRES_DB=app\nPOSTGRES_PORT=5432\n")
	names := filepath.Join(t.TempDir(), "databases.env")
	write(t, names, "DB_NAME_COMMAND_CENTER=cc_dev\nDB_NAME_WHISPER=whisper_dev\n")
	for _, dir := range []string{root, filepath.Join(root, "jarvis-data-services")} {
		d, err := DiscoverCompose(dir, names)
		if err != nil {
			t.Fatal(err)
		}
		if d.Layout != "source" || d.PG.User != "postgres" || d.PG.Password != "pw" || d.PG.Port != 5432 ||
			d.PG.dbName("cc") != "cc_dev" || d.PG.dbName("stt") != "whisper_dev" || len(d.Files) != 2 {
			t.Fatalf("%s: %+v", dir, d)
		}
	}
	d, err := DiscoverCompose(root, filepath.Join(t.TempDir(), "missing.env"))
	if err != nil || len(d.Notes) != 1 || d.PG.dbName("cc") != "jarvis_command_center" {
		t.Fatalf("missing names file: %+v %v", d, err)
	}
}

func TestDiscoverRefusals(t *testing.T) {
	dir := t.TempDir()
	if _, err := DiscoverCompose(dir, ""); err == nil || !strings.Contains(err.Error(), "no legacy compose files") {
		t.Fatalf("empty: %v", err)
	}
	write(t, filepath.Join(dir, "docker-compose.yml"), "services: {}\n")
	if _, err := DiscoverCompose(dir, ""); err == nil || !strings.Contains(err.Error(), "--from") {
		t.Fatalf("compose export: %v", err)
	}
	if _, err := DiscoverCompose(filepath.Join(dir, "nope"), ""); err == nil {
		t.Fatal("missing dir")
	}
}

func TestParsePostgresURL(t *testing.T) {
	c, err := ParsePostgresURL("postgres://jarvis:p%40ss@10.0.0.5:6543/whatever?sslmode=disable")
	if err != nil || c.Host != "10.0.0.5" || c.Port != 6543 || c.User != "jarvis" || c.Password != "p@ss" || c.SSLMode != "disable" {
		t.Fatalf("got %+v %v", c, err)
	}
	if c, err := ParsePostgresURL("postgresql://u:x@localhost"); err != nil || c.Port != 5432 {
		t.Fatalf("default port: %+v %v", c, err)
	}
	for _, bad := range []string{"mysql://u:secretpw@h", "postgres://h", "postgres://u:secretpw@", "postgres://u:secretpw@h:0", "postgres://u:secretpw@h:x\x7f"} {
		_, err := ParsePostgresURL(bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
		} else if strings.Contains(err.Error(), "secretpw") {
			t.Errorf("%q: error leaks the password: %v", bad, err)
		}
	}
}

// OpenPostgres against a closed port fails with a clear, password-free error.
func TestOpenPostgresUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	_, err = OpenPostgres(context.Background(), PGConfig{Host: "127.0.0.1", Port: port, User: "jarvis", Password: "hunter2-secret"})
	if err == nil || !strings.Contains(err.Error(), "not reachable") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}
}

// TestPostgresSource runs against a real legacy Postgres when JARVIS_TEST_LEGACY_PG is set
// (postgres://USER:PASS@HOST:PORT); it only reads.
func TestPostgresSource(t *testing.T) {
	u := os.Getenv("JARVIS_TEST_LEGACY_PG")
	if u == "" {
		t.Skip("JARVIS_TEST_LEGACY_PG not set")
	}
	cfg, err := ParsePostgresURL(u)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := OpenPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok, err := s.Head(ctx, "auth"); err != nil || !ok {
		t.Fatalf("auth head: %v %v", ok, err)
	}
	if _, ok, err := s.Rows(ctx, Query{DB: "auth", Table: "no_such_table", Columns: []string{"id"}}); err != nil || ok {
		t.Fatalf("absent table: %v %v", ok, err)
	}
	c, _ := s.conn(ctx, "auth")
	var ro string
	if err := c.QueryRow(ctx, "SHOW transaction_read_only").Scan(&ro); err != nil || ro != "on" {
		t.Fatalf("session not read-only: %q %v", ro, err)
	}
}
