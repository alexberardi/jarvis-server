package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The privileged helper (ID11): the step of an upgrade that needs to write the binary's
// directory, run by root or LocalSystem on behalf of a service account that can't.
//
//	Linux    the system unit's ExecStartPre=-+ (root, before every start)
//	macOS    the root LaunchDaemon net.jarvisautomation.jarvisd-updater (QueueDirectories)
//	Windows  the LocalSystem service jarvisd-updater (started by jarvisd through the SCM)
//
// Everything it reads from the data directory was written by the service account, so it is
// untrusted input to a privileged process:
//
//   - the home is opened once as an os.Root and every access goes through it: no symlink or
//     ".." can lead outside it, and the home itself must be a plain directory (not a symlink,
//     junction or reparse point) owned by the service account (unix);
//   - files it reads must be regular, have a single link (a hard link to a system file is
//     refused) and fit a size limit;
//   - the marker only selects the release: the executable must be the helper's own, the
//     archive name is derived from the version and platform, and the signed checksum list,
//     its signature and the archive are re-verified with the keys built into the running
//     binary (never the staged one); the archive is copied into the binary's own (root-owned)
//     directory and hashed there, so it can't change between the check and the extraction;
//   - only a strictly newer release than the running binary is installed;
//   - a rollback restores the binary's own jarvisd.prev, never a path from the marker, and
//     touches no database: the restarted (unprivileged) jarvisd restores the snapshot;
//   - files it writes in the home are created exclusively (O_EXCL after removing any
//     existing name, so a planted link is never written through), owned by the home's owner
//     through the open descriptor, then renamed into place;
//   - it executes nothing from the home, and reads no environment or env file from it.

// Helper actions.
const (
	// ActionSwapped: the staged release was verified and installed.
	ActionSwapped = "swapped"
	// ActionBinaryRestored: a requested rollback put jarvisd.prev back; the restarted
	// jarvisd finishes it (database snapshot, result).
	ActionBinaryRestored = "binary_restored"
	// ActionRefused: the staged release failed verification; the upgrade was closed as
	// failed.
	ActionRefused = "refused"
)

// StateBinaryRestored: a privileged helper restored the previous binary; the next start of
// jarvisd (unprivileged) restores the database snapshot if migrations ran and records the
// result.
const StateBinaryRestored = "binary_restored"

// HelperOptions configure PrivilegedStep.
type HelperOptions struct {
	// Version is the running binary's: a staged release must be strictly newer.
	Version string
	// Keys verify the staged checksum list; nil is TrustedKeys().
	Keys []PublicKey
	// OwnerUID, with CheckOwner, is the account the home must belong to (unix). Without
	// CheckOwner a root helper still refuses a home owned by root.
	OwnerUID   int
	CheckOwner bool
}

// Size limits for what the helper reads from the home.
const (
	maxMarker  = 1 << 20
	maxSums    = 1 << 20
	maxSig     = 64 << 10
	maxArchive = 4 << 30
)

// PrivilegedStep carries out the pending privileged step, if any: install a staged release
// (re-verified) or restore the previous binary for a requested rollback. It never restarts
// anything; the caller does.
func PrivilegedStep(ctx context.Context, p Paths, o HelperOptions) (string, *Marker, error) {
	CleanupOld(p)
	h, err := openHome(p.Home, o)
	if err != nil {
		return "", nil, err
	}
	defer h.root.Close()
	m, err := h.readMarker()
	if err != nil || m == nil {
		return "", m, err
	}
	switch m.State {
	case StateStaged:
		nm, err := h.swap(ctx, p, m, o)
		if err != nil {
			if ferr := h.finish(Result{Outcome: ResultFailed, From: m.From, To: m.To, Reason: err.Error()}); ferr != nil {
				err = errors.Join(err, ferr)
			}
			return ActionRefused, m, err
		}
		return ActionSwapped, nm, nil
	case StateRollbackRequested:
		if err := RestorePrevious(p); err != nil {
			return "", m, err
		}
		m.State = StateBinaryRestored
		if err := h.writeJSON(markerName, m); err != nil {
			return "", m, err
		}
		return ActionBinaryRestored, m, nil
	}
	return "", m, nil
}

// Names inside the home, slash-separated for os.Root.
const (
	markerName = "updates/upgrade.json"
	resultName = "updates/last-upgrade.json"
	stagedName = "updates/staged"
)

// homeDir is the data directory as the helper sees it: an os.Root plus the owner to give
// what it writes.
type homeDir struct {
	root     *os.Root
	uid, gid int
	chown    bool
}

// openHome opens the data directory for the helper, refusing anything but a plain directory
// (owned by the expected account on unix).
func openHome(home string, o HelperOptions) (*homeDir, error) {
	if !filepath.IsAbs(home) {
		return nil, fmt.Errorf("update: the home %q is not an absolute path", home)
	}
	lst, err := os.Lstat(home)
	if err != nil {
		return nil, err
	}
	if !lst.IsDir() || lst.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || isReparsePoint(lst) {
		return nil, fmt.Errorf("update: refusing %s: not a plain directory", home)
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	st, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	if !os.SameFile(lst, st) {
		root.Close()
		return nil, fmt.Errorf("update: refusing %s: it changed while being opened", home)
	}
	h := &homeDir{root: root}
	uid, gid, ok := ownerOfInfo(st)
	if ok {
		switch {
		case o.CheckOwner && uid != o.OwnerUID:
			root.Close()
			return nil, fmt.Errorf("update: refusing %s: owned by uid %d, not the service account (uid %d)", home, uid, o.OwnerUID)
		case !o.CheckOwner && uid == 0 && os.Geteuid() == 0:
			root.Close()
			return nil, fmt.Errorf("update: refusing %s: owned by root, not a service account", home)
		}
		h.uid, h.gid, h.chown = uid, gid, os.Geteuid() == 0
	}
	return h, nil
}

// openRegular opens name read-only and requires a regular file with one link, no larger
// than limit.
func (h *homeDir) openRegular(name string, limit int64) (*os.File, int64, error) {
	f, err := h.root.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("update: refusing %s: not a regular file", name)
	}
	if n, ok := linkCount(f, st); ok && n != 1 {
		f.Close()
		return nil, 0, fmt.Errorf("update: refusing %s: it has %d links", name, n)
	}
	if st.Size() > limit {
		f.Close()
		return nil, 0, fmt.Errorf("update: refusing %s: larger than %d bytes", name, limit)
	}
	return f, st.Size(), nil
}

func (h *homeDir) readFile(name string, limit int64) ([]byte, error) {
	f, _, err := h.openRegular(name, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("update: refusing %s: larger than %d bytes", name, limit)
	}
	return b, err
}

func (h *homeDir) readMarker() (*Marker, error) {
	b, err := h.readFile(markerName, maxMarker)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New("update: the upgrade marker is unreadable")
	}
	return &m, nil
}

// writeJSON writes v to name (in the home) without ever writing through an existing file or
// link: any old temp name is removed, the new one created exclusively, owned like the home
// through its descriptor, then renamed into place (a rename replaces a link, not its target).
func (h *homeDir) writeJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := name + ".tmp"
	if err := h.root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := h.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err == nil && h.chown {
		err = f.Chown(h.uid, h.gid)
	}
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		_ = h.root.Remove(tmp)
		return err
	}
	return h.root.Rename(tmp, name)
}

// finish records the outcome and drops the marker and the staged files, all inside the home.
func (h *homeDir) finish(r Result) error {
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	if err := h.writeJSON(resultName, r); err != nil {
		return err
	}
	_ = h.root.RemoveAll(stagedName)
	if err := h.root.Remove(markerName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// swap is Swap for the helper: everything re-derived or re-verified, the archive copied out
// of the home before it is hashed and unpacked.
func (h *homeDir) swap(ctx context.Context, p Paths, m *Marker, o HelperOptions) (*Marker, error) {
	if filepath.Clean(m.Exe) != filepath.Clean(p.Exe) {
		return nil, fmt.Errorf("update: the staged upgrade is for %s, not %s", m.Exe, p.Exe)
	}
	tv, okT := ParseVersion(m.To)
	cv, okC := ParseVersion(o.Version)
	switch {
	case !okT:
		return nil, fmt.Errorf("update: staged version %q is not a release version; refusing", m.To)
	case !okC:
		return nil, fmt.Errorf("update: this jarvisd (%s) is not a release build, so it can't tell whether %s is newer; refusing", o.Version, m.To)
	case tv.Compare(cv) <= 0:
		return nil, fmt.Errorf("update: staged %s is not newer than %s; refusing", m.To, o.Version)
	}
	asset := ArchiveName(m.To, Platform())
	if m.Asset != asset {
		return nil, fmt.Errorf("update: the staged archive %q is not %s; refusing", m.Asset, asset)
	}
	keys := o.Keys
	if keys == nil {
		var err error
		if keys, err = TrustedKeys(); err != nil {
			return nil, err
		}
	}
	sums, err := h.readFile(path.Join(stagedName, SumsName), maxSums)
	if err != nil {
		return nil, err
	}
	sig, err := h.readFile(path.Join(stagedName, SigName), maxSig)
	if err != nil {
		return nil, err
	}
	want, err := verifySums(sums, sig, keys, m.To, asset)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(p.Exe)
	private, err := h.copyOut(path.Join(stagedName, asset), dir, asset, want)
	if err != nil {
		return nil, err
	}
	defer os.Remove(private)
	newBin := filepath.Join(dir, ".jarvisd.new")
	if err := extractBinary(private, newBin); err != nil {
		os.Remove(newBin)
		return nil, err
	}
	if err := installBinary(p, newBin, true); err != nil {
		os.Remove(newBin)
		return nil, err
	}
	m.State, m.Attempts, m.SwappedAt, m.Reason = StateSwapped, 0, time.Now().UTC(), ""
	m.Exe, m.Prev = p.Exe, p.Prev()
	if err := h.writeJSON(markerName, m); err != nil {
		return nil, err
	}
	_ = h.root.Remove(path.Join(stagedName, asset))
	return m, nil
}

// copyOut copies the staged archive into dir (the binary's directory, which only the
// administrator can write) and checks its SHA-256 there; the copy is what gets unpacked.
func (h *homeDir) copyOut(name, dir, asset, want string) (string, error) {
	in, _, err := h.openRegular(name, maxArchive)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".jarvisd-archive-*-"+asset)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, sum), io.LimitReader(in, maxArchive))
	if err = errors.Join(err, out.Close()); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		os.Remove(out.Name())
		return "", fmt.Errorf("%s doesn't match %s (got %s, expected %s)", asset, SumsName, got, want)
	}
	return out.Name(), nil
}

// RequestsDir is where jarvisd drops a file to wake the macOS helper (its QueueDirectories):
// launchd runs the helper while the directory is non-empty.
func (p Paths) RequestsDir() string { return filepath.Join(p.UpdatesDir(), "requests") }

// DrainRequests empties the helper's request directory (the requests carry no data: the
// marker is the only input). It returns how many entries it removed.
func DrainRequests(p Paths, o HelperOptions) (int, error) {
	h, err := openHome(p.Home, o)
	if err != nil {
		return 0, err
	}
	defer h.root.Close()
	const dir = "updates/requests"
	d, err := h.root.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, name := range names {
		if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			continue
		}
		if err := h.root.RemoveAll(path.Join(dir, name)); err == nil {
			n++
		}
	}
	return n, nil
}

// Request wakes the macOS helper: a new file in RequestsDir (created by the service account).
func Request(p Paths) error {
	dir := p.RequestsDir()
	if err := mkdirOwned(p.Home, dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "request-*")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s\n", time.Now().UTC().Format(time.RFC3339Nano))
	return errors.Join(err, f.Close())
}
