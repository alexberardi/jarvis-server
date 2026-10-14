package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/alexberardi/jarvis-server/internal/update"
)

// checkMarker refuses a marker that doesn't fit this updater: another executable or bundle, an
// archive name that isn't this release's for this platform, or a version the policy refuses.
// Same user, so this is consistency, not a privilege boundary: whoever can write the marker can
// write the agent.
func (u *Updater) checkMarker(m *Marker, state string) error {
	switch {
	case m.State != state:
		return fmt.Errorf("the update is %s, not %s", m.State, state)
	case m.Exe != u.Target.Exe || m.Bundle != u.Target.Bundle:
		return fmt.Errorf("the update is for %s, not this agent (%s)", m.Exe, u.Target.Exe)
	case m.Asset != u.AssetName(m.To):
		return fmt.Errorf("the update names %q, not %s", m.Asset, u.AssetName(m.To))
	}
	return nil
}

// Swap puts the staged version in place: the archive is verified again, the new version is
// unpacked next to the running one (and, for a bundle, its signature checked again), the
// running one is kept as the previous version, and the new one takes its path in one rename
// (Windows: the running exe is renamed aside first; macOS bundle: the two directories are
// exchanged). The marker becomes StateSwapped.
func (u *Updater) Swap(m *Marker) error {
	if err := u.checkMarker(m, StateStaged); err != nil {
		return err
	}
	if err := u.Allowed(m.To, m.Force); err != nil {
		return err
	}
	keys, err := u.keys()
	if err != nil {
		return err
	}
	f := &update.Fetched{Dir: u.Paths.Staged(), Tag: m.To, Name: m.Asset, Archive: filepath.Join(u.Paths.Staged(), m.Asset)}
	if err := f.Verify(keys); err != nil {
		return err
	}
	t := u.Target
	newp, prev := t.newPath(), t.prevPath()
	_ = os.RemoveAll(newp)
	if err := u.unpackAndCheck(f, newp); err != nil {
		_ = os.RemoveAll(newp)
		return err
	}
	if err := discard(prev); err != nil {
		_ = os.RemoveAll(newp)
		return fmt.Errorf("remove the old previous version: %w", err)
	}
	switch {
	case t.Bundle != "":
		err = swapDirs(newp, t.Bundle, prev)
	case t.GOOS == "windows":
		// A running exe can be renamed, not replaced or deleted.
		if err = update.RenameRetry(t.Exe, prev); err == nil {
			if err = update.RenameRetry(newp, t.Exe); err != nil {
				_ = update.RenameRetry(prev, t.Exe)
			}
		}
	default:
		// Keep a copy, then one rename: the path always holds a whole binary.
		if err = copyFile(t.Exe, prev); err == nil {
			err = os.Rename(newp, t.Exe)
		}
	}
	if err != nil {
		_ = os.RemoveAll(newp)
		return fmt.Errorf("swap in %s: %w", m.To, err)
	}
	m.State = StateSwapped
	return writeMarker(u.Paths, m)
}

// swapDirs makes newp the app at bundle and keeps the app it replaces at prev: an atomic
// exchange where the OS has one (macOS renamex_np RENAME_SWAP), else two renames.
func swapDirs(newp, bundle, prev string) error {
	if err := exchange(newp, bundle); err == nil {
		return os.Rename(newp, prev) // newp now holds the old app
	}
	if err := os.Rename(bundle, prev); err != nil {
		return err
	}
	if err := os.Rename(newp, bundle); err != nil {
		_ = os.Rename(prev, bundle)
		return err
	}
	return nil
}

// Rollback restores the previous version Swap kept. The version it replaces is removed (on
// Windows, renamed to jarvis-agent.rolledback.exe: it may still be running).
func (u *Updater) Rollback(m *Marker) error {
	if err := u.checkMarker(m, StateSwapped); err != nil {
		return err
	}
	t := u.Target
	prev := t.prevPath()
	if _, err := os.Lstat(prev); err != nil {
		return fmt.Errorf("no previous version to restore (%s): %w", prev, err)
	}
	switch {
	case t.Bundle != "":
		failed := t.newPath()
		_ = os.RemoveAll(failed)
		if err := swapDirs(prev, t.Bundle, failed); err != nil {
			return err
		}
		return os.RemoveAll(failed)
	case t.GOOS == "windows":
		aside := t.rolledBackPath()
		_ = discard(aside)
		if err := update.RenameRetry(t.Exe, aside); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return update.RenameRetry(prev, t.Exe)
	default:
		return os.Rename(prev, t.Exe)
	}
}

// cleanup removes what a finished update leaves: the staged files, and after a success the
// previous version (best effort: on Windows it may still be running for a moment).
func (u *Updater) cleanup(succeeded bool) {
	_ = os.RemoveAll(u.Paths.Staged())
	_ = os.RemoveAll(u.Target.newPath())
	if succeeded {
		_ = discard(u.Target.prevPath())
		if u.Target.GOOS == "windows" {
			_ = discard(u.Target.rolledBackPath())
		}
	}
	// What discard had to set aside earlier, now that nothing runs it.
	stale, _ := filepath.Glob(filepath.Join(filepath.Dir(u.Target.prevPath()), "*"+staleMark+"*"))
	for _, p := range stale {
		_ = os.RemoveAll(p)
	}
}

// staleMark names what discard set aside.
const staleMark = ".stale-"

// discard removes p (missing is fine). Windows can't delete an executable that is still running
// (the installer runs from the previous version until it exits), but it can rename one: such a
// file is set aside under a unique name and removed by a later cleanup.
func discard(p string) error {
	err := os.RemoveAll(p)
	if err == nil {
		return nil
	}
	if _, serr := os.Lstat(p); serr != nil {
		return nil
	}
	aside := p + staleMark + strconv.FormatInt(time.Now().UnixNano(), 36)
	if rerr := update.RenameRetry(p, aside); rerr != nil {
		return err
	}
	return nil
}

// copyFile copies src to dst (created fresh, src's permission bits).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
