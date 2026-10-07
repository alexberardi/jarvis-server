package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The privileged helper (ID11) reads a data directory the unprivileged service account can
// write: every input there is hostile until verified.

func helperOpts(o StageOptions) HelperOptions {
	return HelperOptions{Version: "v1.0.0", Keys: o.Keys}
}

// staged returns an install with v1.1.0 staged by the unprivileged side.
func staged(t *testing.T) (install, StageOptions, []byte, *Marker) {
	t.Helper()
	_, in, o, bin := setup(t)
	m, err := Stage(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return in, o, bin, m
}

// refused runs the helper and expects it to refuse with want in the error, leaving the binary
// alone and the upgrade closed as failed.
func refused(t *testing.T, in install, ho HelperOptions, want string) {
	t.Helper()
	action, _, err := PrivilegedStep(context.Background(), in.paths, ho)
	if action != ActionRefused || err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want a refusal with %q, got %q %v", want, action, err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("executable replaced")
	}
	if _, err := os.Stat(in.paths.Prev()); err == nil {
		t.Fatal("jarvisd.prev written")
	}
	res, _ := ReadResult(in.paths)
	if res == nil || res.Outcome != ResultFailed || !strings.Contains(res.Reason, want) {
		t.Fatalf("result %+v", res)
	}
	if m, _ := ReadMarker(in.paths); m != nil {
		t.Fatal("marker kept")
	}
}

func TestPrivilegedSwap(t *testing.T) {
	in, o, bin, m := staged(t)
	action, nm, err := PrivilegedStep(context.Background(), in.paths, helperOpts(o))
	if err != nil || action != ActionSwapped || nm.State != StateSwapped {
		t.Fatalf("%q %+v %v", action, nm, err)
	}
	if readString(t, in.paths.Exe) != string(bin) || readString(t, in.paths.Prev()) != "old binary" {
		t.Fatal("swap")
	}
	if _, err := os.Stat(m.Archive); !os.IsNotExist(err) {
		t.Error("staged archive kept")
	}
	// Nothing private is left in the binary's directory.
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(in.paths.Exe), ".jarvisd-*"))
	if len(left) != 0 {
		t.Errorf("left %v", left)
	}
	// Nothing pending: a second run is a no-op.
	if action, _, err := PrivilegedStep(context.Background(), in.paths, helperOpts(o)); action != "" || err != nil {
		t.Fatalf("second run %q %v", action, err)
	}
}

func TestPrivilegedNothingStaged(t *testing.T) {
	_, in, o, _ := setup(t)
	if action, m, err := PrivilegedStep(context.Background(), in.paths, helperOpts(o)); action != "" || m != nil || err != nil {
		t.Fatalf("%q %+v %v", action, m, err)
	}
}

func TestPrivilegedRefusesTamperedArchive(t *testing.T) {
	in, o, _, m := staged(t)
	if err := os.WriteFile(m.Archive, []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused(t, in, helperOpts(o), "doesn't match")
}

func TestPrivilegedRefusesTamperedSums(t *testing.T) {
	in, o, _, m := staged(t)
	if err := os.WriteFile(m.Sums, []byte(strings.Repeat("0", 64)+"  "+m.Asset+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused(t, in, helperOpts(o), "signature")
}

func TestPrivilegedRefusesWrongKey(t *testing.T) {
	in, _, _, _ := staged(t)
	other := newTestSigner(t)
	ho := HelperOptions{Version: "v1.0.0", Keys: []PublicKey{other.pub}}
	action, _, err := PrivilegedStep(context.Background(), in.paths, ho)
	if action != ActionRefused || !errors.Is(err, ErrSignature) {
		t.Fatalf("%q %v", action, err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("executable replaced")
	}
}

func TestPrivilegedRefusesNotNewer(t *testing.T) {
	for _, tc := range []struct{ running, want string }{
		{"v1.1.0", "not newer"},
		{"v1.2.0", "not newer"},
		{"dev", "not a release build"},
	} {
		t.Run(tc.running, func(t *testing.T) {
			in, o, _, _ := staged(t)
			ho := helperOpts(o)
			ho.Version = tc.running
			refused(t, in, ho, tc.want)
		})
	}
}

// The marker selects a release; it can't name the binary, the archive's path or the
// previous binary.
func TestPrivilegedRefusesForgedMarker(t *testing.T) {
	for name, forge := range map[string]func(m *Marker, in install){
		"other exe":         func(m *Marker, in install) { m.Exe = filepath.Join(in.paths.Home, "jarvisd") },
		"asset traversal":   func(m *Marker, in install) { m.Asset = "../../" + m.Asset },
		"asset of another":  func(m *Marker, in install) { m.Asset = ArchiveName("v1.1.0", "plan9-mips") },
		"version not a tag": func(m *Marker, in install) { m.To = "../v1.1.0" },
	} {
		t.Run(name, func(t *testing.T) {
			in, o, _, m := staged(t)
			forge(m, in)
			if err := WriteMarker(in.paths, m); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"other exe": "is for", "asset traversal": "is not", "asset of another": "is not", "version not a tag": "not a release version",
			}[name]
			refused(t, in, helperOpts(o), want)
		})
	}
}

// A staged file hard-linked elsewhere (on macOS anyone may hard-link a file they can't
// write) is refused rather than read.
func TestPrivilegedRefusesHardLink(t *testing.T) {
	in, o, _, m := staged(t)
	if err := os.Link(m.Sums, filepath.Join(in.paths.Home, "sums-link")); err != nil {
		t.Skip("no hard links here:", err)
	}
	refused(t, in, helperOpts(o), "links")
}

func TestPrivilegedRefusesOversizedMarker(t *testing.T) {
	_, in, o, _ := setup(t)
	if err := os.MkdirAll(in.paths.UpdatesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.paths.MarkerPath(), make([]byte, maxMarker+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrivilegedStep(context.Background(), in.paths, helperOpts(o)); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.paths.MarkerPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrivilegedStep(context.Background(), in.paths, helperOpts(o)); err == nil || strings.Contains(err.Error(), "not json") {
		t.Fatalf("a parse error must not echo the file: %v", err)
	}
}

// A requested rollback restores the binary's own jarvisd.prev (never a path from the marker)
// and leaves the databases to the restarted, unprivileged jarvisd.
func TestPrivilegedRollback(t *testing.T) {
	in, o, _, _ := staged(t)
	ctx := context.Background()
	if _, _, err := PrivilegedStep(ctx, in.paths, helperOpts(o)); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginStart(in.paths, "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	in.exec(t, `INSERT INTO goose_auth (version_id, is_applied) VALUES (3, 1)`, `INSERT INTO users VALUES ('after')`)
	if err := RequestRollback(in.paths, "health gate"); err != nil {
		t.Fatal(err)
	}
	// The service account points Prev and the snapshots somewhere else.
	evil := filepath.Join(in.paths.Home, "evil")
	if err := os.WriteFile(evil, []byte("evil binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ := ReadMarker(in.paths)
	m.Prev = evil
	if err := WriteMarker(in.paths, m); err != nil {
		t.Fatal(err)
	}
	action, _, err := PrivilegedStep(ctx, in.paths, helperOpts(o))
	if err != nil || action != ActionBinaryRestored {
		t.Fatalf("%q %v", action, err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatalf("restored %q", readString(t, in.paths.Exe))
	}
	if got := in.users(t); len(got) != 2 {
		t.Fatalf("the helper touched the database: %v", got)
	}
	if m, _ := ReadMarker(in.paths); m == nil || m.State != StateBinaryRestored {
		t.Fatalf("marker %+v", m)
	}
	// The restarted old version finishes: snapshot (migrations ran), result.
	action, _, err = PreStart(ctx, in.paths, "v1.0.0")
	if err != nil || action != "rollback_finished" {
		t.Fatalf("%q %v", action, err)
	}
	res, _ := ReadResult(in.paths)
	if res == nil || res.Outcome != ResultRolledBack || !res.DBRestored || res.Reason != "health gate" {
		t.Fatalf("result %+v", res)
	}
	if got := in.users(t); len(got) != 1 || got[0] != "before" {
		t.Fatalf("users %v", got)
	}
}

func TestPrivilegedHomeRefusals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlinks and owners; see the Windows junction test")
	}
	in, o, _, _ := staged(t)
	// The home as a symlink (to a directory that looks just like it).
	link := filepath.Join(filepath.Dir(in.paths.Home), "home-link")
	if err := os.Symlink(in.paths.Home, link); err != nil {
		t.Fatal(err)
	}
	p := Paths{Home: link, Exe: in.paths.Exe}
	if _, _, err := PrivilegedStep(context.Background(), p, helperOpts(o)); err == nil || !strings.Contains(err.Error(), "not a plain directory") {
		t.Fatalf("symlinked home: %v", err)
	}
	// Owned by someone else than the service account.
	ho := helperOpts(o)
	ho.OwnerUID, ho.CheckOwner = os.Getuid()+1, true
	if _, _, err := PrivilegedStep(context.Background(), in.paths, ho); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("owner: %v", err)
	}
	ho.OwnerUID = os.Getuid()
	if action, _, err := PrivilegedStep(context.Background(), in.paths, ho); err != nil || action != ActionSwapped {
		t.Fatalf("right owner: %q %v", action, err)
	}
	// Relative.
	if _, _, err := PrivilegedStep(context.Background(), Paths{Home: "home", Exe: in.paths.Exe}, helperOpts(o)); err == nil {
		t.Fatal("relative home accepted")
	}
}

// Symlinks inside the home can't lead the helper outside it, for reading or writing.
func TestPrivilegedSymlinksStayInside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	in, o, _, m := staged(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("system file"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The archive → a file outside the home.
	if err := os.Remove(m.Archive); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, m.Archive); err != nil {
		t.Fatal(err)
	}
	// A planted temp name for the result → the same outside file: written through, it would
	// be truncated.
	if err := os.Symlink(outside, in.paths.ResultPath()+".tmp"); err != nil {
		t.Fatal(err)
	}
	action, _, err := PrivilegedStep(context.Background(), in.paths, helperOpts(o))
	if action != ActionRefused || err == nil {
		t.Fatalf("%q %v", action, err)
	}
	if readString(t, outside) != "system file" {
		t.Fatal("wrote through a symlink")
	}
	if res, _ := ReadResult(in.paths); res == nil || res.Outcome != ResultFailed {
		t.Fatalf("result %+v", res)
	}

	// The whole staged directory → outside.
	in2, o2, _, _ := staged(t)
	dir := t.TempDir()
	if err := os.RemoveAll(in2.paths.StagedDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, in2.paths.StagedDir()); err != nil {
		t.Fatal(err)
	}
	if action, _, err := PrivilegedStep(context.Background(), in2.paths, helperOpts(o2)); action != ActionRefused || err == nil {
		t.Fatalf("staged dir symlink: %q %v", action, err)
	}

	// The marker itself → outside.
	in3, o3, _, _ := staged(t)
	if err := os.Rename(in3.paths.MarkerPath(), outside+".json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside+".json", in3.paths.MarkerPath()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrivilegedStep(context.Background(), in3.paths, helperOpts(o3)); err == nil {
		t.Fatal("followed the marker outside the home")
	}
	if readString(t, in3.paths.Exe) != "old binary" {
		t.Fatal("executable replaced")
	}
}

// A hard link planted at the temp name is replaced, not written through.
func TestPrivilegedWriteNeverThroughLinks(t *testing.T) {
	in, o, _, _ := staged(t)
	victim := filepath.Join(in.paths.Home, "victim")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, in.paths.MarkerPath()+".tmp"); err != nil {
		t.Skip("no hard links here:", err)
	}
	if action, _, err := PrivilegedStep(context.Background(), in.paths, helperOpts(o)); err != nil || action != ActionSwapped {
		t.Fatalf("%q %v", action, err)
	}
	if readString(t, victim) != "keep me" {
		t.Fatal("wrote through a hard link")
	}
}

func TestRequestAndDrain(t *testing.T) {
	_, in, o, _ := setup(t)
	if err := Request(in.paths); err != nil {
		t.Fatal(err)
	}
	if err := Request(in.paths); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	keep := filepath.Join(outsideDir, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outsideDir, filepath.Join(in.paths.RequestsDir(), "link")); err != nil {
			t.Fatal(err)
		}
	}
	n, err := DrainRequests(in.paths, helperOpts(o))
	if err != nil || n < 2 {
		t.Fatalf("%d %v", n, err)
	}
	left, _ := os.ReadDir(in.paths.RequestsDir())
	if len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("drained through a symlink")
	}
}
