package install

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	release "github.com/alexberardi/jarvis-server/internal/update"
)

// Place puts the agent binary src at dst for an installer (`jarvis-agent install --bin`; the
// Windows installer's program folder, 01-no-cli-plan item 2.3), unless dst already holds
// srcVersion or a newer release: an agent that updated itself past the installer is kept, so
// running an older installer again never downgrades it. A binary it replaces (older, a
// development build, or one that doesn't run) is kept as the self-update's previous version,
// PrevPath(dst), after stop has stopped the running agent: Windows can rename a running
// program but not overwrite it. versionOf runs a binary's `version`.
func Place(src, dst, srcVersion string, versionOf func(string) (string, error), stop func(), w io.Writer) error {
	if _, err := os.Stat(dst); err == nil {
		have, verr := versionOf(dst)
		switch {
		case verr == nil && have == srcVersion:
			fmt.Fprintf(w, "jarvis-agent %s is already at %s\n", have, dst)
			return nil
		case verr == nil && newerRelease(have, srcVersion):
			fmt.Fprintf(w, "keeping jarvis-agent %s at %s (newer than this installer's %s)\n", have, dst, srcVersion)
			return nil
		}
		if verr != nil {
			have = "that doesn't run (" + verr.Error() + ")"
		}
		stop()
		prev := PrevPath(dst)
		if err := os.Remove(prev); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the old previous version %s: %w", prev, err)
		}
		if err := release.RenameRetry(dst, prev); err != nil {
			return fmt.Errorf("keep the old jarvis-agent as %s: %w", prev, err)
		}
		if err := copyExe(src, dst, 0o755); err != nil {
			_ = release.RenameRetry(prev, dst)
			return err
		}
		fmt.Fprintf(w, "replacing jarvis-agent %s at %s with %s (kept as %s)\n", have, dst, srcVersion, prev)
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := copyExe(src, dst, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(w, "installed jarvis-agent %s at %s\n", srcVersion, dst)
	return nil
}

// PrevPath is where the replaced binary is kept: jarvis-agent.prev.exe beside
// jarvis-agent.exe, as the self-update names it.
func PrevPath(exe string) string {
	if strings.HasSuffix(strings.ToLower(exe), ".exe") {
		return exe[:len(exe)-len(".exe")] + ".prev.exe"
	}
	return exe + ".prev"
}

// newerRelease reports whether release a sorts after release b.
func newerRelease(a, b string) bool {
	av, okA := release.ParseVersion(a)
	bv, okB := release.ParseVersion(b)
	return okA && okB && av.Compare(bv) > 0
}
