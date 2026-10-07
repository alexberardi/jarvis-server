package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mklinkJ makes a directory junction, which any account that can write the parent may create
// (unlike a symlink): the Windows way to redirect a path.
func mklinkJ(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v: %s", err, out)
	}
}

func TestPrivilegedRefusesJunctionHome(t *testing.T) {
	in, o, _, _ := staged(t)
	link := filepath.Join(filepath.Dir(in.paths.Home), "home-junction")
	mklinkJ(t, link, in.paths.Home)
	p := Paths{Home: link, Exe: in.paths.Exe}
	if _, _, err := PrivilegedStep(context.Background(), p, helperOpts(o)); err == nil || !strings.Contains(err.Error(), "not a plain directory") {
		t.Fatalf("junction home: %v", err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("executable replaced")
	}
}

// A junction inside the home can't lead the helper outside it.
func TestPrivilegedJunctionStagedDir(t *testing.T) {
	in, o, _, m := staged(t)
	outside := t.TempDir()
	// The genuine staged files, outside the home: still refused, the path escapes.
	for _, f := range []string{m.Sums, m.Sig, m.Archive} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, filepath.Base(f)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(in.paths.StagedDir()); err != nil {
		t.Fatal(err)
	}
	mklinkJ(t, in.paths.StagedDir(), outside)
	action, _, err := PrivilegedStep(context.Background(), in.paths, helperOpts(o))
	if action != ActionRefused || err == nil {
		t.Fatalf("%q %v", action, err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("executable replaced")
	}
	if _, err := os.Stat(filepath.Join(outside, m.Asset)); err != nil {
		t.Fatal("the helper deleted files outside the home")
	}
}
