package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"testing/fstest"
)

func open(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "sub", "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func pragma(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenPragmas(t *testing.T) {
	d := open(t)
	for _, db := range []*sql.DB{d.Write, d.Read} {
		if v := pragma(t, db, "journal_mode"); v != "wal" {
			t.Errorf("journal_mode=%s", v)
		}
		if v := pragma(t, db, "foreign_keys"); v != "1" {
			t.Errorf("foreign_keys=%s", v)
		}
	}
}

func TestReaderIsReadOnly(t *testing.T) {
	d := open(t)
	if _, err := d.Write.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Read.Exec(`INSERT INTO t VALUES (1)`); err == nil {
		t.Fatal("reader accepted a write")
	}
}

func TestConcurrentWritesSerialize(t *testing.T) {
	d := open(t)
	if _, err := d.Write.Exec(`CREATE TABLE c (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for range 50 {
		wg.Go(func() {
			errs <- d.Tx(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO c VALUES (1)`)
				return err
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	d.Read.QueryRow(`SELECT count(*) FROM c`).Scan(&n)
	if n != 50 {
		t.Fatalf("rows=%d", n)
	}
}

func TestTxRollsBack(t *testing.T) {
	d := open(t)
	d.Write.Exec(`CREATE TABLE r (n INTEGER)`)
	boom := errors.New("boom")
	err := d.Tx(context.Background(), func(tx *sql.Tx) error {
		tx.Exec(`INSERT INTO r VALUES (1)`)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	var n int
	d.Read.QueryRow(`SELECT count(*) FROM r`).Scan(&n)
	if n != 0 {
		t.Fatal("not rolled back")
	}
}

func TestForeignKeyCascade(t *testing.T) {
	d := open(t)
	if _, err := d.Write.Exec(`
		CREATE TABLE p (id INTEGER PRIMARY KEY);
		CREATE TABLE ch (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id) ON DELETE CASCADE);
		INSERT INTO p VALUES (1); INSERT INTO ch VALUES (1, 1);
		DELETE FROM p;`); err != nil {
		t.Fatal(err)
	}
	var n int
	d.Read.QueryRow(`SELECT count(*) FROM ch`).Scan(&n)
	if n != 0 {
		t.Fatal("cascade did not run")
	}
}

func migrations(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return m
}

func TestMigratePerModule(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	authV1 := migrations(map[string]string{
		"00001_users.sql": "-- +goose Up\nCREATE TABLE auth_users (id INTEGER PRIMARY KEY);\n-- +goose Down\nDROP TABLE auth_users;\n",
	})
	ccV1 := migrations(map[string]string{
		"00001_nodes.sql": "-- +goose Up\nCREATE TABLE cc_nodes (id TEXT PRIMARY KEY);\n-- +goose Down\nDROP TABLE cc_nodes;\n",
	})
	for _, step := range []struct {
		mod string
		fs  fstest.MapFS
	}{{"auth", authV1}, {"cc", ccV1}, {"auth", authV1}} { // re-running is a no-op
		if err := Migrate(ctx, d, step.mod, step.fs); err != nil {
			t.Fatal(err)
		}
	}

	authV2 := migrations(map[string]string{
		"00001_users.sql": "-- +goose Up\nCREATE TABLE auth_users (id INTEGER PRIMARY KEY);\n-- +goose Down\nDROP TABLE auth_users;\n",
		"00002_email.sql": "-- +goose Up\nALTER TABLE auth_users ADD COLUMN email TEXT;\n-- +goose Down\nSELECT 1;\n",
	})
	st, err := Status(ctx, d, "auth", authV2)
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != 1 || st.Latest != 2 || st.Pending != 1 {
		t.Fatalf("status %+v", st)
	}
	if err := Migrate(ctx, d, "auth", authV2); err != nil {
		t.Fatal(err)
	}
	// cc's version table is untouched by auth's migration.
	cs, _ := Status(ctx, d, "cc", ccV1)
	if cs.Current != 1 || cs.Pending != 0 {
		t.Fatalf("cc status %+v", cs)
	}
	if _, err := d.Write.Exec(`INSERT INTO auth_users (id, email) VALUES (1, 'a@b')`); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateRejectsBadModuleName(t *testing.T) {
	d := open(t)
	if err := Migrate(context.Background(), d, "Bad-Name; DROP", migrations(nil)); err == nil {
		t.Fatal("want error")
	}
}

// Sub-systems land in parallel: a DB that already applied 00090 must still apply a 00080
// that arrives later (jarvis-dev hit "missing (out-of-order) migration" and wouldn't start).
func TestMigrateAppliesOutOfOrder(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	m90 := "-- +goose Up\nCREATE TABLE cc_memory (id INTEGER);\n-- +goose Down\nDROP TABLE cc_memory;\n"
	m80 := "-- +goose Up\nCREATE TABLE cc_routine_seeds (id INTEGER);\n-- +goose Down\nDROP TABLE cc_routine_seeds;\n"
	if err := Migrate(ctx, d, "cc", migrations(map[string]string{"00090_memory.sql": m90})); err != nil {
		t.Fatal(err)
	}
	both := migrations(map[string]string{"00080_routines.sql": m80, "00090_memory.sql": m90})
	if st, err := Status(ctx, d, "cc", both); err != nil || st.Pending != 1 {
		t.Fatalf("status %+v %v", st, err)
	}
	if err := Migrate(ctx, d, "cc", both); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec(`INSERT INTO cc_routine_seeds (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if st, _ := Status(ctx, d, "cc", both); st.Pending != 0 {
		t.Fatalf("after %+v", st)
	}
}

// The DB holds signing keys: its file and WAL/shm are owner-only, including a file an older
// build created world-readable.
func TestFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ignores Unix modes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "jarvis.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil { // an old install's file
		t.Fatal(err)
	}
	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Write.Exec(`CREATE TABLE t (x)`); err != nil { // touches the WAL
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", filepath.Base(p), fi.Mode().Perm())
		}
	}
}
