package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// The swap, the rollback and the start-time state machine.
//
//	stage ──(writable)──▶ Swap ─────────────────────▶ swapped ──gate ok──▶ (result: succeeded)
//	  │                                                  │
//	  └─(binary dir not writable: systemd system unit)   ├─gate fails / 2 failed starts
//	     staged ──PreStart (ExecStartPre=+, root)──▶ ────┘        ▼
//	                                                     rollback_requested ──PreStart / in
//	                                                     process──▶ Rollback (result: rolled_back)

// ErrNeedPrivilege means this process can't write the executable's directory; a privileged
// pre-start (or `sudo jarvisd upgrade`) has to finish the step.
var ErrNeedPrivilege = errors.New("update: can't write the jarvisd binary's directory")

// renameAside is true where a running executable can be renamed but not replaced (Windows).
var renameAside = runtime.GOOS == "windows"

// CanWrite reports whether this process can create files next to exe (to swap it).
func CanWrite(exe string) bool {
	f, err := os.CreateTemp(filepath.Dir(exe), ".jarvisd-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// SwapOptions configure Swap.
type SwapOptions struct {
	// Keys re-verify the staged files; nil is TrustedKeys().
	Keys []PublicKey
	// NewerThan, when set, refuses a staged version that isn't newer (the privileged
	// pre-start: it must not let an unprivileged writer of the staged files downgrade).
	NewerThan string
}

// Swap installs the staged release: it re-verifies the signed checksum list and the archive
// (the staged files may have been written by a less privileged process), extracts the binary
// next to the executable, keeps the current executable as Prev and renames the new one into
// place. The marker moves to StateSwapped with no attempts.
func Swap(ctx context.Context, p Paths, o SwapOptions) (*Marker, error) {
	m, err := ReadMarker(p)
	if err != nil {
		return nil, err
	}
	if m == nil || m.State != StateStaged {
		return nil, errors.New("update: nothing is staged")
	}
	if filepath.Clean(m.Exe) != filepath.Clean(p.Exe) {
		return nil, fmt.Errorf("update: the staged upgrade is for %s, not %s", m.Exe, p.Exe)
	}
	if o.NewerThan != "" {
		tv, okT := ParseVersion(m.To)
		cv, okC := ParseVersion(o.NewerThan)
		if !okT || (okC && tv.Compare(cv) <= 0) {
			return nil, fmt.Errorf("update: staged %s is not newer than %s; refusing", m.To, o.NewerThan)
		}
	}
	if !CanWrite(p.Exe) {
		return nil, fmt.Errorf("%w (%s)", ErrNeedPrivilege, filepath.Dir(p.Exe))
	}
	keys := o.Keys
	if keys == nil {
		if keys, err = TrustedKeys(); err != nil {
			return nil, err
		}
	}
	staged := p.StagedDir()
	inStaged := func(f string) bool { return filepath.Dir(filepath.Clean(f)) == staged }
	if !inStaged(m.Archive) || !inStaged(m.Sums) || !inStaged(m.Sig) {
		return nil, errors.New("update: the marker points outside the staged directory")
	}
	sums, err := os.ReadFile(m.Sums)
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(m.Sig)
	if err != nil {
		return nil, err
	}
	if filepath.Base(m.Archive) != m.Asset {
		return nil, errors.New("update: the marker's archive and asset disagree")
	}
	want, err := verifySums(sums, sig, keys, m.To, m.Asset)
	if err != nil {
		return nil, err
	}
	if err := checkSHA256(m.Archive, want); err != nil {
		return nil, err
	}
	dir := filepath.Dir(p.Exe)
	newBin := filepath.Join(dir, ".jarvisd.new")
	if err := extractBinary(m.Archive, newBin); err != nil {
		os.Remove(newBin)
		return nil, err
	}
	if err := installBinary(p, newBin, true); err != nil {
		os.Remove(newBin)
		return nil, err
	}
	m.State, m.Attempts, m.SwappedAt, m.Reason = StateSwapped, 0, time.Now().UTC(), ""
	if err := WriteMarker(p, m); err != nil {
		return nil, err
	}
	_ = os.Remove(m.Archive) // the extracted binary is in place; the snapshot stays
	return m, nil
}

// installBinary moves src (in the executable's directory) into place, first copying the current
// executable to Prev when keepPrev. Unix: rename over the executable (atomic; the running
// process keeps its inode). Windows: the running executable is renamed aside, then src renamed
// in; CleanupOld deletes the parked copy on the next start.
func installBinary(p Paths, src string, keepPrev bool) error {
	if keepPrev {
		tmp := p.Prev() + ".tmp"
		if err := copyFile(p.Exe, tmp); err != nil {
			return fmt.Errorf("update: keep the previous binary: %w", err)
		}
		if err := os.Rename(tmp, p.Prev()); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if renameAside {
		if _, err := os.Stat(p.Exe); err == nil {
			aside := p.old()
			for i := 1; ; i++ {
				if _, err := os.Stat(aside); os.IsNotExist(err) {
					break
				}
				if os.Remove(aside) == nil {
					break
				}
				aside = fmt.Sprintf("%s.%d", p.old(), i)
			}
			if err := os.Rename(p.Exe, aside); err != nil {
				return fmt.Errorf("update: move the running binary aside: %w", err)
			}
			if err := os.Rename(src, p.Exe); err != nil {
				_ = os.Rename(aside, p.Exe)
				return err
			}
			return nil
		}
	}
	return os.Rename(src, p.Exe)
}

// copyFile copies src to dst with src's permissions (dst is replaced).
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
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := errors.Join(out.Sync(), out.Close()); err != nil {
		return err
	}
	return os.Chmod(dst, st.Mode().Perm())
}

// CleanupOld deletes executables parked by a Windows swap (the process that ran them has
// exited by the time the next one starts).
func CleanupOld(p Paths) {
	matches, _ := filepath.Glob(p.old() + "*")
	for _, f := range matches {
		_ = os.Remove(f)
	}
}

// Rollback restores the previous binary and, when the new version changed a database's
// migrations, that database's snapshot; it records the result and clears the marker. jarvisd
// must not have the database open.
func Rollback(ctx context.Context, p Paths, reason string) (*Result, error) {
	m, err := ReadMarker(p)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("update: no upgrade to roll back")
	}
	if reason == "" {
		reason = m.Reason
	}
	res := Result{Outcome: ResultRolledBack, From: m.From, To: m.To, Reason: reason}
	switch m.State {
	case StateStaged:
		// Nothing was swapped: drop the staged files.
		res.Outcome = ResultFailed
		return &res, finish(p, res)
	case StateSwapped, StateRollbackRequested:
	default:
		return nil, fmt.Errorf("update: unknown upgrade state %q", m.State)
	}
	if !CanWrite(p.Exe) {
		return nil, fmt.Errorf("%w (%s)", ErrNeedPrivilege, filepath.Dir(p.Exe))
	}
	prev := m.Prev
	if prev == "" {
		prev = p.Prev()
	}
	tmp := filepath.Join(filepath.Dir(p.Exe), ".jarvisd.rollback")
	if err := copyFile(prev, tmp); err != nil {
		return nil, fmt.Errorf("update: restore %s: %w", prev, err)
	}
	if err := installBinary(p, tmp, false); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	for file, snap := range m.Snapshots {
		changed, err := migrationsChanged(ctx, p.Home, file, m.GooseBefore[file])
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		if err := restoreSnapshot(p.Home, snap, file); err != nil {
			return nil, fmt.Errorf("update: restore the database snapshot %s: %w", snap, err)
		}
		res.DBRestored = true
	}
	if err := finish(p, res); err != nil {
		return nil, err
	}
	return &res, nil
}

// RestorePrevious puts Prev back in place with no upgrade in progress (a manual rollback after
// an upgrade passed its gate) and records it in last-upgrade.json as rolled back from `to`
// (the version that was running) to `from` (Prev's; "" when unknown). Databases are left
// alone: if the newer version migrated them, the downgrade guard refuses to start and the
// snapshots under BackupsDir are the way back.
func RestorePrevious(p Paths, from, to, reason string) (*Result, error) {
	if _, err := os.Stat(p.Prev()); err != nil {
		return nil, fmt.Errorf("update: no previous binary to restore: %w", err)
	}
	if !CanWrite(p.Exe) {
		return nil, fmt.Errorf("%w (%s)", ErrNeedPrivilege, filepath.Dir(p.Exe))
	}
	tmp := filepath.Join(filepath.Dir(p.Exe), ".jarvisd.rollback")
	if err := copyFile(p.Prev(), tmp); err != nil {
		return nil, err
	}
	if err := installBinary(p, tmp, false); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	res := Result{Outcome: ResultRolledBack, From: from, To: to, Reason: reason, At: time.Now().UTC()}
	if err := finish(p, res); err != nil {
		return nil, err
	}
	return &res, nil
}

// PreStart does the pending file work before jarvisd opens its database: swap a staged
// release in, or carry out a requested rollback. It runs as the systemd ExecStartPre=+ helper
// (root) and at the start of serve (a no-op when the helper already did it). version is the
// running binary's. It returns the action taken ("", "swapped", "rolled_back").
func PreStart(ctx context.Context, p Paths, version string) (string, *Marker, error) {
	CleanupOld(p)
	m, err := ReadMarker(p)
	if err != nil || m == nil {
		return "", m, err
	}
	switch m.State {
	case StateStaged:
		nm, err := Swap(ctx, p, SwapOptions{NewerThan: version})
		if err != nil {
			if !errors.Is(err, ErrNeedPrivilege) {
				_ = finish(p, Result{Outcome: ResultFailed, From: m.From, To: m.To, Reason: err.Error()})
			}
			return "", m, err
		}
		return "swapped", nm, nil
	case StateRollbackRequested:
		if _, err := Rollback(ctx, p, ""); err != nil {
			return "", m, err
		}
		return "rolled_back", m, nil
	}
	return "", m, nil
}

// MaxFailedStarts is how many starts of a new version may fail the gate (or crash) before the
// next one rolls back.
const MaxFailedStarts = 2

// ErrRollback means the new version must be rolled back now (it crash-looped).
var ErrRollback = errors.New("update: the new version keeps failing; rolling back")

// BeginStart is serve's start-time check, after PreStart. When this version is the one an
// upgrade swapped in, it counts the start and returns the marker: the caller runs the health
// gate. After MaxFailedStarts failed starts it requests a rollback and returns ErrRollback. A
// swapped marker for another version is stale (someone replaced the binary by hand) and is
// closed as failed.
func BeginStart(p Paths, version string) (*Marker, error) {
	m, err := ReadMarker(p)
	if err != nil || m == nil {
		return nil, err
	}
	if m.State != StateSwapped {
		return nil, nil
	}
	if m.To != version {
		return nil, finish(p, Result{Outcome: ResultFailed, From: m.From, To: m.To,
			Reason: fmt.Sprintf("jarvisd %s started instead of %s", version, m.To)})
	}
	if m.Attempts >= MaxFailedStarts {
		m.State = StateRollbackRequested
		m.Reason = fmt.Sprintf("%s failed to start %d times", m.To, m.Attempts)
		if err := WriteMarker(p, m); err != nil {
			return nil, err
		}
		return m, ErrRollback
	}
	m.Attempts++
	return m, WriteMarker(p, m)
}

// Confirm records that the new version passed its health gate.
func Confirm(p Paths) error {
	m, err := ReadMarker(p)
	if err != nil || m == nil {
		return err
	}
	return finish(p, Result{Outcome: ResultSucceeded, From: m.From, To: m.To})
}

// RequestRollback marks the swapped version for rollback (it failed its health gate).
func RequestRollback(p Paths, reason string) error {
	m, err := ReadMarker(p)
	if err != nil || m == nil {
		return err
	}
	m.State, m.Reason = StateRollbackRequested, reason
	return WriteMarker(p, m)
}

// Abort drops a staged (not yet swapped) upgrade.
func Abort(p Paths, reason string) error {
	m, err := ReadMarker(p)
	if err != nil || m == nil {
		return err
	}
	if m.State != StateStaged {
		return fmt.Errorf("update: the upgrade is already %s", m.State)
	}
	return finish(p, Result{Outcome: ResultFailed, From: m.From, To: m.To, Reason: reason})
}
