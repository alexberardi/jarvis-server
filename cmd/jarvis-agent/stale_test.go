package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
}
