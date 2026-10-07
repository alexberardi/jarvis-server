package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Paths locate an install's update state: the data directory and the executable the service
// runs (symlinks resolved; the unit, plist or SCM entry points at it).
type Paths struct {
	Home string
	Exe  string
}

// UpdatesDir holds the download, the staged files and the marker.
func (p Paths) UpdatesDir() string { return filepath.Join(p.Home, "updates") }

// StagedDir holds the verified release files waiting to be swapped in.
func (p Paths) StagedDir() string { return filepath.Join(p.UpdatesDir(), "staged") }

// MarkerPath is the in-progress upgrade's state.
func (p Paths) MarkerPath() string { return filepath.Join(p.UpdatesDir(), "upgrade.json") }

// ResultPath is the last finished upgrade's outcome (succeeded / rolled back / failed).
func (p Paths) ResultPath() string { return filepath.Join(p.UpdatesDir(), "last-upgrade.json") }

// BackupsDir holds the database snapshots (the last 3 per database).
func (p Paths) BackupsDir() string { return filepath.Join(p.Home, "backups") }

// Prev is the rollback copy of the previous binary, next to the executable: jarvisd.prev
// (jarvisd.prev.exe on Windows).
func (p Paths) Prev() string {
	if ext := filepath.Ext(p.Exe); strings.EqualFold(ext, ".exe") {
		return strings.TrimSuffix(p.Exe, ext) + ".prev" + ext
	}
	return p.Exe + ".prev"
}

// RolledBack is the binary a rollback replaced (the newer version), kept next to the
// executable for inspection or to put back by hand: jarvisd.rolledback (jarvisd.rolledback.exe
// on Windows). One copy; the next swap removes it.
func (p Paths) RolledBack() string {
	if ext := filepath.Ext(p.Exe); strings.EqualFold(ext, ".exe") {
		return strings.TrimSuffix(p.Exe, ext) + ".rolledback" + ext
	}
	return p.Exe + ".rolledback"
}

// old is where Windows parks the running executable (it can be renamed, not replaced, while it
// runs); CleanupOld removes it on the next start.
func (p Paths) old() string { return p.Exe + ".old" }

// States of an upgrade in the marker.
const (
	// StateStaged: verified and unpacked under updates/staged, waiting for the swap (in
	// process, or by a privileged helper: PrivilegedStep).
	StateStaged = "staged"
	// StateSwapped: the new binary is in place; the next start of that version runs the
	// health gate.
	StateSwapped = "swapped"
	// StateRollbackRequested: the new version failed its gate or crash-looped; the next
	// pre-start restores the previous binary (and the DB snapshot if migrations ran).
	StateRollbackRequested = "rollback_requested"
)

// Outcomes in the result file.
const (
	ResultSucceeded  = "succeeded"
	ResultRolledBack = "rolled_back"
	ResultFailed     = "failed"
)

// Marker is updates/upgrade.json: everything a later process (the restarted jarvisd, the
// privileged pre-start, `jarvisd upgrade` waiting for the result) needs.
type Marker struct {
	State string `json:"state"`
	From  string `json:"from_version"`
	To    string `json:"to_version"`
	// Exe is the executable being replaced; Prev its rollback copy.
	Exe  string `json:"exe"`
	Prev string `json:"prev"`
	// Archive is the verified release archive under the staged dir and Asset its release name;
	// Sums/Sig the signed checksum list, kept so a privileged pre-start re-verifies before
	// trusting anything an unprivileged process wrote.
	Archive string `json:"archive,omitempty"`
	Asset   string `json:"asset,omitempty"`
	Sums    string `json:"sums,omitempty"`
	Sig     string `json:"sig,omitempty"`
	// Snapshots maps each database file to its VACUUM INTO copy.
	Snapshots map[string]string `json:"snapshots,omitempty"`
	// GooseBefore is every goose_* table's applied versions before the upgrade, per database
	// file: a rollback restores a snapshot only when these changed (migrations ran).
	GooseBefore map[string]map[string][]int64 `json:"goose_before,omitempty"`
	// Attempts counts starts of the new version that haven't passed the gate.
	Attempts  int       `json:"attempts"`
	Reason    string    `json:"reason,omitempty"`
	StagedAt  time.Time `json:"staged_at"`
	SwappedAt time.Time `json:"swapped_at,omitzero"`
	// By is who started it: "cli" or "admin".
	By string `json:"by,omitempty"`
	// FromFinishesRestore: the From binary (the one a rollback restores) finishes a rollback a
	// privileged helper began (StateBinaryRestored). Stage sets it when the staging jarvisd is
	// that binary; markers staged by v0.1.0-rc5 and older lack it.
	FromFinishesRestore bool `json:"from_finishes_restore,omitempty"`
}

// Result is updates/last-upgrade.json.
type Result struct {
	Outcome    string    `json:"outcome"`
	From       string    `json:"from_version"`
	To         string    `json:"to_version"`
	Reason     string    `json:"reason,omitempty"`
	DBRestored bool      `json:"db_restored,omitempty"`
	At         time.Time `json:"at"`
}

// ReadMarker returns the in-progress upgrade, nil when there is none.
func ReadMarker(p Paths) (*Marker, error) {
	b, err := os.ReadFile(p.MarkerPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("update: %s is unreadable: %w", p.MarkerPath(), err)
	}
	return &m, nil
}

// WriteMarker saves the marker atomically, owned like the data directory (a root pre-start
// must not leave root-owned state the service can't update).
func WriteMarker(p Paths, m *Marker) error {
	return writeJSON(p, p.MarkerPath(), m)
}

// ReadResult returns the last upgrade's outcome, nil when none.
func ReadResult(p Paths) (*Result, error) {
	b, err := os.ReadFile(p.ResultPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// finish records the outcome and removes the marker and the staged files.
func finish(p Paths, r Result) error {
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	if err := writeJSON(p, p.ResultPath(), r); err != nil {
		return err
	}
	_ = os.RemoveAll(p.StagedDir())
	if err := os.Remove(p.MarkerPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func writeJSON(p Paths, path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := mkdirOwned(p.Home, filepath.Dir(path)); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := createFresh(tmp)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err := errors.Join(err, f.Close()); err != nil {
		os.Remove(tmp)
		return err
	}
	chownLike(p.Home, tmp)
	return os.Rename(tmp, path)
}

// createFresh creates path (0600) for writing, replacing whatever was there without following
// it: a root pre-start writes these temporaries in the data directory, where the service
// account could have left a symlink to a file it can't write itself.
func createFresh(path string) (*os.File, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

// mkdirOwned creates dir (under home) owner-only, owned like home.
func mkdirOwned(home, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	chownLike(home, dir)
	return nil
}

// chownLike gives path the owner of ref when this process runs as root (the systemd pre-start)
// and they differ. Best effort; nothing to do on Windows.
func chownLike(ref, path string) {
	if runtime.GOOS == "windows" || os.Geteuid() != 0 {
		return
	}
	uid, gid, ok := ownerOf(ref)
	if !ok {
		return
	}
	_ = os.Lchown(path, uid, gid)
}
