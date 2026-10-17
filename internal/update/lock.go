package update

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrLocked means another process is staging an upgrade (it holds updates/stage.lock).
var ErrLocked = errors.New("another upgrade is running")

// staleLockAge is how old an empty or unreadable lock file must be before it counts as left
// behind: a fresh one may belong to a holder that hasn't written its PID yet.
const staleLockAge = time.Minute

// LockPath is the lock Stage holds while it rewrites updates/staged.
func (p Paths) LockPath() string { return filepath.Join(p.UpdatesDir(), "stage.lock") }

// Lock takes updates/stage.lock (O_EXCL), so a CLI `jarvisd upgrade` and the admin's apply (or
// two CLIs) can't stage at the same time. A lock whose PID is no longer running, or an empty
// one older than staleLockAge, is taken over once. release removes the file only while it is
// still this holder's.
func Lock(p Paths) (release func(), err error) {
	if err := mkdirOwned(p.Home, p.UpdatesDir()); err != nil {
		return nil, err
	}
	var tok [8]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, err
	}
	body := []byte(strconv.Itoa(os.Getpid()) + "\n" + hex.EncodeToString(tok[:]) + "\n")
	path := p.LockPath()
	for attempt := 0; ; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := f.Write(body)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				_ = os.Remove(path)
				return nil, werr
			}
			chownLike(p.Home, path)
			return func() {
				if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, body) {
					_ = os.Remove(path)
				}
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		pid, stale := lockHolder(path)
		if !stale || attempt > 0 {
			holder := "unknown pid"
			if pid > 0 {
				holder = "pid " + strconv.Itoa(pid)
			}
			return nil, fmt.Errorf("%w (%s); wait for it, or remove %s if it isn't running", ErrLocked, holder, path)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
}

// lockHolder reads the lock's PID and reports whether the lock was left behind: its process is
// gone, or it has no PID and is older than staleLockAge.
func lockHolder(path string) (pid int, stale bool) {
	b, err := os.ReadFile(path)
	if err == nil {
		first, _, _ := strings.Cut(string(b), "\n")
		if pid, err = strconv.Atoi(strings.TrimSpace(first)); err == nil && pid > 0 {
			return pid, !processAlive(pid)
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		return 0, true // released meanwhile
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, errors.Is(err, fs.ErrNotExist)
	}
	return 0, time.Since(fi.ModTime()) > staleLockAge
}
