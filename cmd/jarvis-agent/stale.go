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
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/buildinfo"
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
	older, err := isAncestor(dir, built, head)
	return staleMessage(version, dir, head[:min(len(head), len(built))], err == nil && older)
}

func staleMessage(build, dir, head string, older bool) string {
	if older {
		return fmt.Sprintf("build:        %s is older than your checkout (%s at %s): rebuild (scripts/agent-dev-run.sh, or your go build)", build, dir, head)
	}
	return fmt.Sprintf("build:        %s isn't your checkout's commit (%s at %s): rebuild if you meant to run that one", build, dir, head)
}

// vcsStaleLine is staleBuildLine for a plain `go build`, which stamps neither the version nor
// the checkout but embeds the commit (vcs): dir is the checkout found for it (findCheckout),
// "" when none. When git can't place the build's commit relative to HEAD (no git, or a commit
// the checkout doesn't have), the commit times decide whether it is older. A release build,
// a build with no VCS details (go run) or no checkout found: "".
func vcsStaleLine(version string, vcs buildinfo.VCS, ok bool, dir string, headOf func(dir string) (string, error),
	isAncestor func(dir, a, b string) (bool, error), commitTime func(dir, commit string) (time.Time, error)) string {
	if !buildinfo.IsDev(version) || !ok || dir == "" {
		return ""
	}
	head, err := headOf(dir)
	if err != nil || head == vcs.Revision {
		return ""
	}
	label := buildinfo.Describe(version, vcs, ok)
	short := head[:min(len(head), 7)]
	older, err := isAncestor(dir, vcs.Revision, head)
	if err != nil {
		t, terr := commitTime(dir, head)
		older = terr == nil && !vcs.Time.IsZero() && t.After(vcs.Time)
	}
	return staleMessage(label, dir, short, older)
}

// sourceFile is this file's path as compiled: absolute for a plain go build, so the checkout
// it sits in can be found; module-relative under -trimpath (then unused).
var _, sourceFile, _, _ = runtime.Caller(0)

// checkoutCandidates are where to look for the checkout of a plain go build: the binary's own
// directory (`go build -o jarvis-agent` in the repo), then the source it was compiled from.
func checkoutCandidates() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if exe, err := filepath.EvalSymlinks(exe); err == nil {
			dirs = append(dirs, filepath.Dir(exe))
		}
	}
	if p := filepath.FromSlash(sourceFile); filepath.IsAbs(p) {
		dirs = append(dirs, filepath.Dir(p))
	}
	return dirs
}

// findCheckout returns the first git checkout at or above one of dirs whose go.mod declares
// module (so a binary copied into some other repository isn't compared with it); "" if none.
func findCheckout(dirs []string, module string) string {
	for _, d := range dirs {
		for d := filepath.Clean(d); ; {
			if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
				if goModule(filepath.Join(d, "go.mod")) == module {
					return d
				}
				break
			}
			up := filepath.Dir(d)
			if up == d {
				break
			}
			d = up
		}
	}
	return ""
}

func goModule(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(b)) {
		if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(m), `"`)
		}
	}
	return ""
}

// mainModule is the module this binary was built from (github.com/…/jarvis-server).
func mainModule() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.Main.Path
	}
	return ""
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

// gitIsAncestor asks git whether commit a is an ancestor of b (only asked when they differ).
func gitIsAncestor(dir, a, b string) (bool, error) {
	_, err := runGit(dir, "merge-base", "--is-ancestor", a, b)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

// gitCommitTime asks git for a commit's committer time.
func gitCommitTime(dir, commit string) (time.Time, error) {
	out, err := runGit(dir, "log", "-1", "--format=%ct", commit)
	if err != nil {
		return time.Time{}, err
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, 0), nil
}

// runGit runs git in dir, bounded so status stays quick. On macOS, /usr/bin/git without the
// developer tools would show their install dialog, so it is skipped then.
func runGit(dir string, args ...string) ([]byte, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "darwin" && git == "/usr/bin/git" && exec.Command("/usr/bin/xcode-select", "-p").Run() != nil {
		return nil, errors.New("no developer tools")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, git, append([]string{"-C", dir}, args...)...).Output()
}
