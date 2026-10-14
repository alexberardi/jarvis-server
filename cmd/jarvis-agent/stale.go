package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// sourceDir is the checkout a dev build was made from: scripts/agent-dev-run.sh sets it with
// -ldflags -X, next to version (dev-<commit>). Release builds have neither.
var sourceDir string

// staleBuildLine is status's warning when this dev build's commit isn't the checkout's it was
// built from, typically because a `git pull` came after the last build ("the fix isn't in the
// agent I'm running"). "" when it matches, for a release build, or when the checkout is gone.
// headOf reads the checkout's HEAD commit; isAncestor reports whether a is an ancestor of b
// there (an error when it can't tell).
func staleBuildLine(version, dir string, headOf func(dir string) (string, error),
	isAncestor func(dir, a, b string) (bool, error)) string {
	built, ok := strings.CutPrefix(version, "dev-")
	if !ok || dir == "" || len(built) < 7 || strings.Trim(built, "0123456789abcdef") != "" {
		return ""
	}
	head, err := headOf(dir)
	if err != nil || strings.HasPrefix(head, built) {
		return ""
	}
	short := head[:min(len(head), len(built))]
	if older, err := isAncestor(dir, built, head); err == nil && older {
		return fmt.Sprintf("build:        %s is older than your checkout (%s at %s): rebuild (scripts/agent-dev-run.sh)", version, dir, short)
	}
	return fmt.Sprintf("build:        %s isn't your checkout's commit (%s at %s): rebuild if you meant to run that one", version, dir, short)
}

// gitHead reads a checkout's HEAD commit from its files, without running git: HEAD, the ref it
// names (loose, or in packed-refs), in the checkout's own git dir or, for a worktree, the
// common one.
func gitHead(dir string) (string, error) {
	gitDir := filepath.Join(dir, ".git")
	if b, err := os.ReadFile(gitDir); err == nil { // a worktree: "gitdir: PATH"
		p, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if !ok {
			return "", errors.New("unknown .git file")
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		gitDir = p
	}
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", err
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: ")
	if !ok {
		return strings.TrimSpace(string(b)), nil // detached
	}
	dirs := []string{gitDir}
	if c, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		p := strings.TrimSpace(string(c))
		if !filepath.IsAbs(p) {
			p = filepath.Join(gitDir, p)
		}
		dirs = append(dirs, p)
	}
	for _, d := range dirs {
		if b, err := os.ReadFile(filepath.Join(d, filepath.FromSlash(ref))); err == nil {
			return strings.TrimSpace(string(b)), nil
		}
		f, err := os.Open(filepath.Join(d, "packed-refs"))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if sha, name, ok := strings.Cut(sc.Text(), " "); ok && name == ref {
				f.Close()
				return sha, nil
			}
		}
		f.Close()
	}
	return "", fmt.Errorf("%s not found", ref)
}

// gitIsAncestor asks git whether commit a is an ancestor of b. Only when they differ, and
// bounded: status stays quick. On macOS, /usr/bin/git without the developer tools would show
// their install dialog, so it is skipped then.
func gitIsAncestor(dir, a, b string) (bool, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return false, err
	}
	if runtime.GOOS == "darwin" && git == "/usr/bin/git" && exec.Command("/usr/bin/xcode-select", "-p").Run() != nil {
		return false, errors.New("no developer tools")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = exec.CommandContext(ctx, git, "-C", dir, "merge-base", "--is-ancestor", a, b).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, err
}
