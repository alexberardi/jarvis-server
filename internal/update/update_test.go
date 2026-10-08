package update

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for a released jarvisd: with JARVISD_FAKE_VERSION set,
// `<test binary> version` prints it (Stage runs the new binary's `version`).
func TestMain(m *testing.M) {
	if v := os.Getenv("JARVISD_FAKE_VERSION"); v != "" && len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(v)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeBinary is a runnable "jarvisd" whose `version` prints JARVISD_FAKE_VERSION: a shell
// script on unix (small, so the race build stays fast), the test binary itself on Windows.
func fakeBinary(t *testing.T) []byte {
	t.Helper()
	if runtime.GOOS != "windows" {
		return []byte("#!/bin/sh\necho \"$JARVISD_FAKE_VERSION\"\n")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// install is a fake install: an "executable" with known content in its own dir and a home
// with a database that has goose tables.
type install struct {
	paths Paths
	db    string
}

func newInstall(t *testing.T) install {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	home := filepath.Join(root, "home")
	for _, d := range []string{bin, home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(bin, "jarvisd")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := install{paths: Paths{Home: home, Exe: exe}, db: filepath.Join(home, "jarvis.db")}
	in.exec(t, `CREATE TABLE goose_auth (id INTEGER PRIMARY KEY, version_id INTEGER NOT NULL, is_applied INTEGER NOT NULL)`,
		`INSERT INTO goose_auth (version_id, is_applied) VALUES (0, 1), (1, 1), (2, 1)`,
		`CREATE TABLE users (name TEXT)`, `INSERT INTO users VALUES ('before')`)
	return in
}

func (in install) exec(t *testing.T, stmts ...string) {
	t.Helper()
	d, err := openSQLite(in.db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			t.Fatal(s, err)
		}
	}
}

func (in install) users(t *testing.T) []string {
	t.Helper()
	d, err := sql.Open("sqlite", in.db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rows, err := d.Query(`SELECT name FROM users ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// setup publishes v1.1.0 (and older releases) signed by a throwaway key.
func setup(t *testing.T) (*fakeGitHub, install, StageOptions, []byte) {
	t.Helper()
	t.Setenv("JARVISD_FAKE_VERSION", "v1.1.0")
	s := newTestSigner(t)
	gh := newFakeGitHub(t)
	bin := fakeBinary(t)
	gh.add(releaseSpec{tag: "v0.9.0", platform: Platform(), binary: []byte("older"), signer: &s})
	gh.add(releaseSpec{tag: "v1.1.0", platform: Platform(), binary: bin, signer: &s})
	gh.add(releaseSpec{tag: "v1.2.0-rc.1", platform: Platform(), binary: bin, signer: &s, pre: true})
	in := newInstall(t)
	o := StageOptions{Paths: in.paths, Current: "v1.0.0", Source: gh.source(), Keys: []PublicKey{s.pub}, By: "test"}
	return gh, in, o, bin
}

func TestStageSwapConfirm(t *testing.T) {
	_, in, o, bin := setup(t)
	ctx := context.Background()
	var steps []string
	o.Progress = func(p Progress) {
		if len(steps) == 0 || steps[len(steps)-1] != p.Step {
			steps = append(steps, p.Step)
		}
	}
	m, err := Stage(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != StateStaged || m.To != "v1.1.0" || m.From != "v1.0.0" || m.Exe != in.paths.Exe {
		t.Fatalf("marker %+v", m)
	}
	if got := strings.Join(steps, ","); got != "checking,verifying,downloading,verifying,unpacking,testing,snapshot" {
		t.Fatalf("steps %s", got)
	}
	snap := m.Snapshots[in.db]
	if snap == "" || filepath.Dir(snap) != in.paths.BackupsDir() {
		t.Fatalf("snapshot %q", snap)
	}
	if got := m.GooseBefore[in.db]["goose_auth"]; len(got) != 2 || got[1] != 2 {
		t.Fatalf("goose before %v", m.GooseBefore)
	}
	// A second upgrade can't start while this one is pending.
	if _, err := Stage(ctx, o); !errors.Is(err, ErrInProgress) {
		t.Fatalf("second stage: %v", err)
	}
	// The executable hasn't changed yet.
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("staging touched the executable")
	}

	if _, err := Swap(ctx, in.paths, SwapOptions{Keys: o.Keys}); err != nil {
		t.Fatal(err)
	}
	if readString(t, in.paths.Exe) != string(bin) || readString(t, in.paths.Prev()) != "old binary" {
		t.Fatal("swap: executable or prev wrong")
	}
	if st, _ := os.Stat(in.paths.Exe); runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("new binary not executable: %v", st.Mode())
	}

	// The new version starts: one attempt counted, then the gate passes.
	m, err = BeginStart(in.paths, "v1.1.0")
	if err != nil || m == nil || m.Attempts != 1 {
		t.Fatalf("begin: %+v %v", m, err)
	}
	if err := Confirm(in.paths); err != nil {
		t.Fatal(err)
	}
	if m, _ := ReadMarker(in.paths); m != nil {
		t.Fatalf("marker left: %+v", m)
	}
	res, _ := ReadResult(in.paths)
	if res == nil || res.Outcome != ResultSucceeded || res.To != "v1.1.0" {
		t.Fatalf("result %+v", res)
	}
	if _, err := os.Stat(in.paths.StagedDir()); !os.IsNotExist(err) {
		t.Fatal("staged files left")
	}
	// The next start is a plain start.
	if m, err := BeginStart(in.paths, "v1.1.0"); m != nil || err != nil {
		t.Fatalf("after confirm: %+v %v", m, err)
	}
}

func TestResolve(t *testing.T) {
	gh, _, o, _ := setup(t)
	ctx := context.Background()
	// Stable build: prereleases skipped.
	p, err := Resolve(ctx, o)
	if err != nil || p.Release.Tag != "v1.1.0" {
		t.Fatalf("%+v %v", p, err)
	}
	// Running the newest: up to date.
	o.Current = "v1.1.0"
	if _, err := Resolve(ctx, o); !errors.Is(err, ErrUpToDate) {
		t.Fatal(err)
	}
	// A prerelease build moves to newer prereleases.
	o.Current = "v1.1.1-rc.1"
	if p, err := Resolve(ctx, o); err != nil || p.Release.Tag != "v1.2.0-rc.1" {
		t.Fatalf("%+v %v", p, err)
	}
	// A pinned older version needs AllowOlder.
	o.Current, o.Target = "v1.0.0", "v0.9.0"
	if _, err := Resolve(ctx, o); err == nil || !strings.Contains(err.Error(), "allow-older") {
		t.Fatal(err)
	}
	o.AllowOlder = true
	if p, err := Resolve(ctx, o); err != nil || p.Release.Tag != "v0.9.0" {
		t.Fatalf("%+v %v", p, err)
	}
	// Unknown tag.
	o.Target = "v9.9.9"
	if _, err := Resolve(ctx, o); !errors.Is(err, ErrNoRelease) {
		t.Fatal(err)
	}
	// No download for this platform.
	o.Target, o.Platform = "v1.1.0", "plan9-mips"
	if _, err := Resolve(ctx, o); err == nil || !strings.Contains(err.Error(), "no download") {
		t.Fatal(err)
	}
	_ = gh
}

func TestStageRefusesBadReleases(t *testing.T) {
	ctx := context.Background()
	other := newTestSigner(t)
	for name, spec := range map[string]func(*releaseSpec){
		"unsigned":      func(s *releaseSpec) { s.noSig = true },
		"wrong key":     func(s *releaseSpec) { s.signer = &other },
		"other tag":     func(s *releaseSpec) { s.comment = "jarvisd v1.0.9 SHA256SUMS" },
		"default comm.": func(s *releaseSpec) { s.comment = "timestamp:1\tfile:SHA256SUMS\thashed" },
		"bad checksum":  func(s *releaseSpec) { s.badSum = true },
		"wrong version": func(s *releaseSpec) { s.tag = "v1.3.0" }, // the binary says v1.1.0
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("JARVISD_FAKE_VERSION", "v1.1.0")
			s := newTestSigner(t)
			gh := newFakeGitHub(t)
			spec2 := releaseSpec{tag: "v1.1.0", platform: Platform(), binary: fakeBinary(t), signer: &s}
			spec(&spec2)
			gh.add(spec2)
			in := newInstall(t)
			_, err := Stage(ctx, StageOptions{Paths: in.paths, Current: "v1.0.0", Source: gh.source(), Keys: []PublicKey{s.pub}})
			if err == nil {
				t.Fatal("staged a bad release")
			}
			if m, _ := ReadMarker(in.paths); m != nil {
				t.Fatal("marker written")
			}
			if _, err := os.Stat(in.paths.StagedDir()); !os.IsNotExist(err) {
				t.Fatal("staged files left")
			}
			if readString(t, in.paths.Exe) != "old binary" {
				t.Fatal("executable touched")
			}
		})
	}
}

// TestSwapReverifies: a staged archive changed after staging (by whoever can write the data
// directory) is refused by the swap, which a privileged pre-start runs.
func TestSwapReverifies(t *testing.T) {
	_, in, o, _ := setup(t)
	ctx := context.Background()
	m, err := Stage(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Archive, []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Swap(ctx, in.paths, SwapOptions{Keys: o.Keys}); err == nil || !strings.Contains(err.Error(), "doesn't match") {
		t.Fatalf("tampered archive: %v", err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("executable replaced")
	}
	// A staged marker pointing outside the staged dir is refused.
	m.Archive = in.paths.Exe
	_ = WriteMarker(in.paths, m)
	if _, err := Swap(ctx, in.paths, SwapOptions{Keys: o.Keys}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside: %v", err)
	}
}

// TestPreStartRefusesDowngrade: the privileged pre-start only installs a newer version.
func TestPreStartRefusesDowngrade(t *testing.T) {
	_, in, o, _ := setup(t)
	ctx := context.Background()
	if _, err := Stage(ctx, o); err != nil {
		t.Fatal(err)
	}
	// PreStart uses the embedded keys, which don't include the test key, so check the
	// version guard through Swap directly.
	if _, err := Swap(ctx, in.paths, SwapOptions{Keys: o.Keys, NewerThan: "v1.1.0"}); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatal(err)
	}
	if _, err := Swap(ctx, in.paths, SwapOptions{Keys: o.Keys, NewerThan: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
}

func TestPreStartUntrustedKeyFails(t *testing.T) {
	_, in, o, _ := setup(t)
	ctx := context.Background()
	if _, err := Stage(ctx, o); err != nil {
		t.Fatal(err)
	}
	// Staged with a key the binary doesn't trust: the pre-start refuses and closes it.
	action, _, err := PreStart(ctx, in.paths, "v1.0.0")
	if action != "" || !errors.Is(err, ErrSignature) {
		t.Fatalf("%q %v", action, err)
	}
	res, _ := ReadResult(in.paths)
	if res == nil || res.Outcome != ResultFailed {
		t.Fatalf("result %+v", res)
	}
	if m, _ := ReadMarker(in.paths); m != nil {
		t.Fatal("marker kept")
	}
}

func swapped(t *testing.T) (install, StageOptions, []byte) {
	t.Helper()
	_, in, o, bin := setup(t)
	if _, err := Stage(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := Swap(context.Background(), in.paths, SwapOptions{Keys: o.Keys}); err != nil {
		t.Fatal(err)
	}
	return in, o, bin
}

// TestCrashLoopRollsBack: two failed starts, the third requests a rollback; no migration ran,
// so the database is kept as it is (with the new version's writes).
func TestCrashLoopRollsBack(t *testing.T) {
	in, _, _ := swapped(t)
	ctx := context.Background()
	for i := 1; i <= MaxFailedStarts; i++ {
		m, err := BeginStart(in.paths, "v1.1.0")
		if err != nil || m.Attempts != i {
			t.Fatalf("start %d: %+v %v", i, m, err)
		}
	}
	in.exec(t, `INSERT INTO users VALUES ('written by new')`)
	if _, err := BeginStart(in.paths, "v1.1.0"); !errors.Is(err, ErrRollback) {
		t.Fatalf("third start: %v", err)
	}
	m, _ := ReadMarker(in.paths)
	if m.State != StateRollbackRequested {
		t.Fatalf("state %s", m.State)
	}
	action, _, err := PreStart(ctx, in.paths, "v1.1.0")
	if err != nil || action != "rolled_back" {
		t.Fatalf("%q %v", action, err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("binary not restored")
	}
	res, _ := ReadResult(in.paths)
	if res.Outcome != ResultRolledBack || res.DBRestored || !strings.Contains(res.Reason, "failed to start") {
		t.Fatalf("result %+v", res)
	}
	if got := in.users(t); len(got) != 2 {
		t.Fatalf("database changed: %v", got)
	}
}

// TestGateFailureRestoresMigratedDB: the new version migrated, then failed its gate: the
// rollback restores the snapshot.
func TestGateFailureRestoresMigratedDB(t *testing.T) {
	in, _, _ := swapped(t)
	ctx := context.Background()
	if _, err := BeginStart(in.paths, "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	in.exec(t, `INSERT INTO goose_auth (version_id, is_applied) VALUES (3, 1)`, `INSERT INTO users VALUES ('after')`)
	if err := RequestRollback(in.paths, "health gate: no /health within 120s"); err != nil {
		t.Fatal(err)
	}
	res, err := Rollback(ctx, in.paths, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.DBRestored || res.Reason != "health gate: no /health within 120s" {
		t.Fatalf("result %+v", res)
	}
	if got := in.users(t); len(got) != 1 || got[0] != "before" {
		t.Fatalf("users %v", got)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("binary not restored")
	}
	// The previous binary is kept (a second rollback stays possible by hand).
	if readString(t, in.paths.Prev()) != "old binary" {
		t.Fatal("prev removed")
	}
}

func TestStaleSwappedMarker(t *testing.T) {
	in, _, _ := swapped(t)
	if m, err := BeginStart(in.paths, "v1.0.0"); m != nil || err != nil {
		t.Fatalf("%+v %v", m, err)
	}
	res, _ := ReadResult(in.paths)
	if res == nil || res.Outcome != ResultFailed || !strings.Contains(res.Reason, "started instead") {
		t.Fatalf("result %+v", res)
	}
}

// TestRenameAside runs the Windows swap (rename the running executable aside) on any OS.
func TestRenameAside(t *testing.T) {
	old := renameAside
	renameAside = true
	t.Cleanup(func() { renameAside = old })
	in, _, bin := swapped(t)
	if readString(t, in.paths.Exe) != string(bin) || readString(t, in.paths.old()) != "old binary" {
		t.Fatal("aside swap")
	}
	if err := RequestRollback(in.paths, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(context.Background(), in.paths, ""); err != nil {
		t.Fatal(err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("rollback")
	}
	// The earlier parked copy wasn't running, so it was replaced by the failed binary.
	if readString(t, in.paths.old()) != string(bin) {
		t.Fatal("the failed binary was not parked")
	}
	CleanupOld(in.paths)
	if m, _ := filepath.Glob(in.paths.old() + "*"); len(m) != 0 {
		t.Fatalf("left %v", m)
	}
}

func TestSnapshotPrune(t *testing.T) {
	in := newInstall(t)
	ctx := context.Background()
	var snaps []string
	for i := 0; i < 5; i++ {
		s, err := Snapshot(ctx, in.paths.Home, in.db, in.paths.BackupsDir(), "v1.0.0-rc.1")
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, s)
	}
	left, _ := filepath.Glob(filepath.Join(in.paths.BackupsDir(), "jarvis-*.db"))
	if len(left) != KeepSnapshots {
		t.Fatalf("kept %v", left)
	}
	for _, s := range snaps[2:] {
		if _, err := os.Stat(s); err != nil {
			t.Fatalf("newest pruned: %v", err)
		}
	}
	if st, _ := os.Stat(snaps[4]); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestCanWrite(t *testing.T) {
	dir := t.TempDir()
	if !CanWrite(filepath.Join(dir, "jarvisd")) {
		t.Fatal("temp dir not writable")
	}
	if CanWrite(filepath.Join(dir, "missing", "jarvisd")) {
		t.Fatal("missing dir writable")
	}
}

func TestVerifyDir(t *testing.T) {
	s := newTestSigner(t)
	dir := t.TempDir()
	name := ArchiveName("v1.0.0", "linux-amd64")
	archive := makeArchive(t, name, []byte("bin"))
	if err := os.WriteFile(filepath.Join(dir, name), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(archive)
	sum := hex.EncodeToString(h[:])
	sums := []byte(sum + "  " + name + "\n" + sum + "  jarvisd-v1.0.0-darwin-arm64.tar.gz\n")
	_ = os.WriteFile(filepath.Join(dir, SumsName), sums, 0o600)
	_ = os.WriteFile(filepath.Join(dir, SigName), s.sign(sums, "jarvisd v1.0.0 SHA256SUMS", true), 0o600)
	got, err := VerifyDir(dir, "v1.0.0", []PublicKey{s.pub})
	if err != nil || len(got) != 1 || got[0] != name {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := VerifyDir(dir, "v1.0.1", []PublicKey{s.pub}); !errors.Is(err, ErrSignature) {
		t.Fatalf("other version: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, name), []byte("tampered"), 0o600)
	if _, err := VerifyDir(dir, "v1.0.0", []PublicKey{s.pub}); err == nil {
		t.Fatal("tampered archive accepted")
	}
}

// TestRestorePreviousRecordsResult (A10c): a manual rollback after an upgrade passed its gate
// left last-upgrade.json saying "succeeded v1.0.0 -> v1.1.0" while v1.0.0 ran again; it now
// records the rollback.
func TestRestorePreviousRecordsResult(t *testing.T) {
	in := newInstall(t)
	if err := os.WriteFile(in.paths.Prev(), []byte("previous binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := finish(in.paths, Result{Outcome: ResultSucceeded, From: "v1.0.0", To: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	res, err := RestorePrevious(in.paths, "v1.0.0", "v1.1.0", "rolled back by hand")
	if err != nil {
		t.Fatal(err)
	}
	if readString(t, in.paths.Exe) != "previous binary" {
		t.Fatal("binary not restored")
	}
	got, _ := ReadResult(in.paths)
	if got == nil || !got.At.Equal(res.At) || got.Outcome != ResultRolledBack || got.From != "v1.0.0" ||
		got.To != "v1.1.0" || got.Reason != "rolled back by hand" || got.DBRestored || got.At.IsZero() {
		t.Fatalf("result %+v (returned %+v)", got, res)
	}
	if got := in.users(t); len(got) != 1 {
		t.Fatalf("database changed: %v", got)
	}

	// No previous binary: an error, and the result is left alone.
	os.Remove(in.paths.Prev())
	if _, err := RestorePrevious(in.paths, "v1.0.0", "v1.1.0", "again"); err == nil {
		t.Fatal("restored without a previous binary")
	}
	if got, _ := ReadResult(in.paths); got.Reason != "rolled back by hand" {
		t.Fatalf("result rewritten: %+v", got)
	}
}
