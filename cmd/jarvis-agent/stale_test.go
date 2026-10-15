package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/buildinfo"
)

const (
	headSHA  = "0123456789abcdef0123456789abcdef01234567"
	otherSHA = "fedcba9876543210fedcba9876543210fedcba98"
)

func TestStaleBuildLine(t *testing.T) {
	head := func(string) (string, error) { return headSHA, nil }
	older := func(string, string, string) (bool, error) { return true, nil }
	newer := func(string, string, string) (bool, error) { return false, nil }
	noGit := func(string, string, string) (bool, error) { return false, errors.New("no git") }
	calledGit := false
	spy := func(string, string, string) (bool, error) { calledGit = true; return true, nil }

	// The same commit: nothing, and git isn't run.
	if got := staleBuildLine("dev-0123456", "/src", head, spy); got != "" || calledGit {
		t.Fatalf("same commit: %q (git run: %v)", got, calledGit)
	}
	if got := staleBuildLine("dev-fedcba9", "/src", head, older); !strings.Contains(got, "older than your checkout") ||
		!strings.Contains(got, "0123456") || !strings.Contains(got, "rebuild") {
		t.Fatalf("behind: %q", got)
	}
	for _, f := range []func(string, string, string) (bool, error){newer, noGit} {
		if got := staleBuildLine("dev-fedcba9", "/src", head, f); !strings.Contains(got, "isn't your checkout's commit") {
			t.Fatalf("differs: %q", got)
		}
	}
	// Release builds, unknown commits, no checkout recorded, or the checkout gone: nothing.
	gone := func(string) (string, error) { return "", os.ErrNotExist }
	for _, c := range []struct{ version, dir string }{{"v0.1.0", "/src"}, {"dev", "/src"}, {"dev-unknown", "/src"}, {"dev-fedcba9", ""}} {
		if got := staleBuildLine(c.version, c.dir, head, older); got != "" {
			t.Errorf("%+v: %q", c, got)
		}
	}
	if got := staleBuildLine("dev-fedcba9", "/src", gone, older); got != "" {
		t.Errorf("checkout gone: %q", got)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitHead(t *testing.T) {
	// A plain checkout on a branch with a loose ref.
	a := t.TempDir()
	write(t, filepath.Join(a, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(a, ".git", "refs", "heads", "main"), headSHA+"\n")
	if got, err := gitHead(a); err != nil || got != headSHA {
		t.Fatalf("loose ref: %q %v", got, err)
	}
	// Packed refs only.
	b := t.TempDir()
	write(t, filepath.Join(b, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(b, ".git", "packed-refs"), "# pack-refs with: peeled fully-peeled sorted\n"+otherSHA+" refs/heads/dev\n"+headSHA+" refs/heads/main\n")
	if got, err := gitHead(b); err != nil || got != headSHA {
		t.Fatalf("packed ref: %q %v", got, err)
	}
	// A worktree: .git is a file; its branch ref lives in the common dir.
	wt := t.TempDir()
	gd := filepath.Join(b, ".git", "worktrees", "wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
	write(t, filepath.Join(gd, "HEAD"), "ref: refs/heads/dev\n")
	write(t, filepath.Join(gd, "commondir"), "../..\n")
	if got, err := gitHead(wt); err != nil || got != otherSHA {
		t.Fatalf("worktree: %q %v", got, err)
	}
	// Detached.
	write(t, filepath.Join(gd, "HEAD"), headSHA+"\n")
	if got, err := gitHead(wt); err != nil || got != headSHA {
		t.Fatalf("detached: %q %v", got, err)
	}
	if _, err := gitHead(t.TempDir()); err == nil {
		t.Fatal("no checkout: no error")
	}
}

// Against a real repository, when git is installed: two commits, and gitHead agrees with git.
func TestStaleBuildWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	if runtime.GOOS == "windows" {
		t.Skip("git's read-only object files outlive t.TempDir's cleanup on Windows")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com",
			"-c", "commit.gpgsign=false"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "one")
	first := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "two")
	second := git("rev-parse", "HEAD")
	if got, err := gitHead(dir); err != nil || got != second {
		t.Fatalf("gitHead = %q %v, want %s", got, err, second)
	}
	if older, err := gitIsAncestor(dir, first[:7], second); err != nil || !older {
		t.Fatalf("first before second: %v %v", older, err)
	}
	if older, err := gitIsAncestor(dir, second[:7], first); err != nil || older {
		t.Fatalf("second before first: %v %v", older, err)
	}
	if got := staleBuildLine("dev-"+first[:7], dir, gitHead, gitIsAncestor); !strings.Contains(got, "older than your checkout") {
		t.Fatalf("got %q", got)
	}
	git("pack-refs", "--all")
	if got := staleBuildLine("dev-"+second[:7], dir, gitHead, gitIsAncestor); got != "" {
		t.Fatalf("current build: %q", got)
	}

	// A plain go build: the embedded commit against HEAD.
	if got := vcsStaleLine("dev", buildinfo.VCS{Revision: first}, true, dir, gitHead, gitIsAncestor, gitCommitTime); !strings.Contains(got, "older than your checkout") {
		t.Fatalf("vcs, behind: %q", got)
	}
	if got := vcsStaleLine("dev", buildinfo.VCS{Revision: second}, true, dir, gitHead, gitIsAncestor, gitCommitTime); got != "" {
		t.Fatalf("vcs, current: %q", got)
	}
	// A commit this checkout doesn't have, dated long before HEAD: older, by time.
	if ct, err := gitCommitTime(dir, second); err != nil || ct.IsZero() {
		t.Fatalf("gitCommitTime: %v %v", ct, err)
	}
	gone := buildinfo.VCS{Revision: otherSHA, Time: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)}
	if got := vcsStaleLine("dev", gone, true, dir, gitHead, gitIsAncestor, gitCommitTime); !strings.Contains(got, "older than your checkout") {
		t.Fatalf("vcs, unknown commit: %q", got)
	}
}

func TestVCSStaleLine(t *testing.T) {
	head := func(string) (string, error) { return headSHA, nil }
	older := func(string, string, string) (bool, error) { return true, nil }
	newer := func(string, string, string) (bool, error) { return false, nil }
	unknown := func(string, string, string) (bool, error) { return false, errors.New("not a valid commit") }
	built := time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)
	at := func(t time.Time) func(string, string) (time.Time, error) {
		return func(string, string) (time.Time, error) { return t, nil }
	}
	noTime := func(string, string) (time.Time, error) { return time.Time{}, errors.New("no git") }
	build := buildinfo.VCS{Revision: otherSHA, Time: built}

	got := vcsStaleLine("dev", build, true, "/src", head, older, noTime)
	if !strings.Contains(got, "dev (fedcba9) is older than your checkout (/src at 0123456)") {
		t.Fatalf("behind: %q", got)
	}
	if got := vcsStaleLine("dev", build, true, "/src", head, newer, noTime); !strings.Contains(got, "isn't your checkout's commit") {
		t.Fatalf("diverged: %q", got)
	}
	// git can't place the commit: the checkout's newer commit time says the build is older.
	if got := vcsStaleLine("dev", build, true, "/src", head, unknown, at(built.Add(time.Hour))); !strings.Contains(got, "older than your checkout") {
		t.Fatalf("by time, older: %q", got)
	}
	for _, ct := range []func(string, string) (time.Time, error){at(built.Add(-time.Hour)), noTime} {
		if got := vcsStaleLine("dev", build, true, "/src", head, unknown, ct); !strings.Contains(got, "isn't your checkout's commit") {
			t.Fatalf("by time, not older: %q", got)
		}
	}
	dirty := buildinfo.VCS{Revision: otherSHA, Modified: true}
	if got := vcsStaleLine("dev", dirty, true, "/src", head, older, noTime); !strings.Contains(got, "dev (fedcba9+dirty) is older") {
		t.Fatalf("dirty: %q", got)
	}
	// Current, a release, no VCS details, or no checkout found: nothing.
	gone := func(string) (string, error) { return "", os.ErrNotExist }
	for _, c := range []struct {
		version string
		vcs     buildinfo.VCS
		ok      bool
		dir     string
		head    func(string) (string, error)
	}{
		{"dev", buildinfo.VCS{Revision: headSHA, Modified: true}, true, "/src", head},
		{"v0.4.0", build, true, "/src", head},
		{"dev", buildinfo.VCS{}, false, "/src", head},
		{"dev", build, true, "", head},
		{"dev", build, true, "/src", gone},
	} {
		if got := vcsStaleLine(c.version, c.vcs, c.ok, c.dir, c.head, older, noTime); got != "" {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

func TestFindCheckout(t *testing.T) {
	const mod = "github.com/alexberardi/jarvis-server"
	repo := t.TempDir()
	write(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(repo, "go.mod"), "// x\nmodule "+mod+"\n\ngo 1.27.1\n")
	src := filepath.Join(repo, "cmd", "jarvis-agent")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir() // some other repository the binary was copied into
	write(t, filepath.Join(other, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(other, "go.mod"), "module example.com/other\n")
	wt := t.TempDir() // a worktree: .git is a file
	write(t, filepath.Join(wt, ".git"), "gitdir: /elsewhere\n")
	write(t, filepath.Join(wt, "go.mod"), "module "+mod+"\n")

	for _, c := range []struct {
		dirs []string
		want string
	}{
		{[]string{repo}, repo},             // the binary built at the repo root
		{[]string{t.TempDir(), src}, repo}, // installed elsewhere: found from its source path
		{[]string{other, src}, repo},       // the wrong repository is skipped
		{[]string{wt}, wt},
		{[]string{other}, ""},
		{nil, ""},
	} {
		if got := findCheckout(c.dirs, mod); got != c.want {
			t.Errorf("findCheckout(%v) = %q, want %q", c.dirs, got, c.want)
		}
	}
}
