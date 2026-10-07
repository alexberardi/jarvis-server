package update

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	_ "modernc.org/sqlite" // the "sqlite" driver
)

// KeepSnapshots is how many snapshots of each database are kept.
const KeepSnapshots = 3

// DatabaseFiles lists the SQLite databases directly under home (jarvis.db today).
func DatabaseFiles(home string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(home, "*.db"))
	if err != nil {
		return nil, err
	}
	slices.Sort(matches)
	return matches, nil
}

func openSQLite(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	d, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	return d, nil
}

// Snapshot copies the database at path into dir with VACUUM INTO (consistent while jarvisd
// keeps writing, through its own connection) and prunes older snapshots of it to KeepSnapshots.
// The copy is named <base>-<version>-<UTC time>.db.
func Snapshot(ctx context.Context, home, path, dir, version string) (string, error) {
	if err := mkdirOwned(home, dir); err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(path), ".db")
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	dst := filepath.Join(dir, fmt.Sprintf("%s-%s-%s.db", base, safeName(version), stamp))
	d, err := openSQLite(path)
	if err != nil {
		return "", err
	}
	defer fixOwners(home, path)
	defer d.Close()
	if _, err := d.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("update: snapshot %s: %w", filepath.Base(path), err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return "", err
	}
	chownLike(home, dst)
	pruneSnapshots(dir, base, KeepSnapshots)
	return dst, nil
}

// pruneSnapshots keeps the newest keep snapshots of one database (names sort by time).
func pruneSnapshots(dir, base string, keep int) {
	matches, _ := filepath.Glob(filepath.Join(dir, base+"-*.db"))
	slices.Sort(matches)
	byTime := func(a, b string) int {
		return strings.Compare(snapshotStamp(a), snapshotStamp(b))
	}
	slices.SortFunc(matches, byTime)
	for len(matches) > keep {
		_ = os.Remove(matches[0])
		matches = matches[1:]
	}
}

// snapshotStamp is the time part of a snapshot name (after the last '-').
func snapshotStamp(p string) string {
	n := strings.TrimSuffix(filepath.Base(p), ".db")
	if i := strings.LastIndexByte(n, '-'); i >= 0 {
		return n[i+1:]
	}
	return n
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			return r
		}
		return '_'
	}, s)
}

// fixOwners gives a database's WAL and shm files the home's owner: a root `jarvisd upgrade`
// opening a stopped install's database may have created them.
func fixOwners(home, file string) {
	for _, sfx := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(file + sfx); err == nil {
			chownLike(home, file+sfx)
		}
	}
}

// gooseVersions reads every goose table of every database file.
func gooseVersions(ctx context.Context, home string, files []string) (map[string]map[string][]int64, error) {
	out := map[string]map[string][]int64{}
	for _, f := range files {
		d, err := openSQLite(f)
		if err != nil {
			return nil, err
		}
		v, err := db.AppliedVersions(ctx, d)
		d.Close()
		fixOwners(home, f)
		if err != nil {
			return nil, fmt.Errorf("update: read migrations of %s: %w", filepath.Base(f), err)
		}
		out[f] = v
	}
	return out, nil
}

// migrationsChanged reports whether the database's goose tables differ from before.
func migrationsChanged(ctx context.Context, home, file string, before map[string][]int64) (bool, error) {
	now, err := gooseVersions(ctx, home, []string{file})
	if err != nil {
		return false, err
	}
	after := now[file]
	if len(after) != len(before) {
		return true, nil
	}
	for t, v := range after {
		if !slices.Equal(v, before[t]) {
			return true, nil
		}
	}
	return false, nil
}

// restoreSnapshot puts a snapshot back in place of the database (jarvisd stopped): written
// next to it, renamed over it, the old WAL and shm removed, owned like home.
func restoreSnapshot(home, snapshot, file string) error {
	in, err := os.Open(snapshot)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := file + ".restore"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := errors.Join(out.Sync(), out.Close()); err != nil {
		os.Remove(tmp)
		return err
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		if err := os.Remove(file + sfx); err != nil && !errors.Is(err, fs.ErrNotExist) {
			os.Remove(tmp)
			return err
		}
	}
	chownLike(home, tmp)
	return os.Rename(tmp, file)
}
