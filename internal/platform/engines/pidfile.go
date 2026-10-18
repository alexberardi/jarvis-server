package engines

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// An engine started with Spec.PIDDir leaves <PIDDir>/<name>.pid while it runs, so a jarvisd
// that was SIGKILLed or crashed can find it on the next start (Sweep). On Linux Pdeathsig and
// on Windows the job object already take the engine down with jarvisd; on macOS nothing does,
// and an orphaned llama-server keeps holding unified memory.
//
// The file records the process's identity as the OS reported it right after launch: its
// executable and start time. Sweep kills only a live process that still matches both, so a
// reused PID is never touched.
type pidFile struct {
	PID   int    `json:"pid"`
	Exe   string `json:"exe"`
	Start string `json:"start"`
}

// sweepGrace is how long Sweep waits after SIGTERM before it kills a leftover's group.
const sweepGrace = 5 * time.Second

func pidPath(dir, name string) string { return filepath.Join(dir, name+".pid") }

// writePID records a just-launched engine. Failing to is logged, not fatal: the engine runs,
// it just won't be swept after a crash.
func (s *Supervisor) writePID(pid int) {
	dir := s.spec.PIDDir
	if dir == "" {
		return
	}
	exe, start, err := procIdentity(pid)
	if errors.Is(err, errors.ErrUnsupported) {
		return
	}
	if err == nil {
		err = writePIDFile(dir, s.spec.Name, pidFile{PID: pid, Exe: exe, Start: start})
	}
	if err != nil {
		s.log.Warn("engine pid file not written; a crash of jarvisd may leave it running", "err", err)
	}
}

func writePIDFile(dir, name string, pf pidFile) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(pf)
	if err != nil {
		return err
	}
	tmp := pidPath(dir, name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, pidPath(dir, name))
}

// removePID drops the engine's pid file once its process has been reaped.
func (s *Supervisor) removePID() {
	if s.spec.PIDDir == "" {
		return
	}
	if err := os.Remove(pidPath(s.spec.PIDDir, s.spec.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("engine pid file not removed", "err", err)
	}
}

// Sweep kills the engines a previous jarvisd left running and removes their pid files. Call it
// once at startup, before any engine is started with the same dir. A pid file whose process is
// gone, or is now some other program, is just removed. It returns how many engines it killed.
func Sweep(dir string, log *slog.Logger) int {
	if dir == "" {
		return 0
	}
	if log == nil {
		log = slog.Default()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warn("engine pid dir unreadable; leftover engines not swept", "dir", dir, "err", err)
		}
		return 0
	}
	killed := 0
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".pid.tmp") {
			_ = os.Remove(path)
			continue
		}
		if !strings.HasSuffix(e.Name(), ".pid") {
			continue
		}
		if sweepOne(path, log) {
			killed++
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("engine pid file not removed", "path", path, "err", err)
		}
	}
	return killed
}

// sweepOne kills the engine recorded in path if it is still the same process.
func sweepOne(path string, log *slog.Logger) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Warn("engine pid file unreadable", "path", path, "err", err)
		return false
	}
	var pf pidFile
	if err := json.Unmarshal(b, &pf); err != nil || pf.PID <= 1 || pf.Exe == "" || pf.Start == "" {
		log.Warn("engine pid file malformed; ignored", "path", path)
		return false
	}
	exe, start, err := procIdentity(pf.PID)
	if err != nil || exe != pf.Exe || start != pf.Start {
		return false // gone, or the PID now belongs to something else
	}
	log.Warn("killing engine left running by a previous jarvisd", "pid", pf.PID, "exe", pf.Exe)
	if err := interruptGroup(pf.PID); err != nil {
		killLeftovers(pf.PID)
		return true
	}
	deadline := time.Now().Add(sweepGrace)
	for time.Now().Before(deadline) {
		if e, s, err := procIdentity(pf.PID); err != nil || e != pf.Exe || s != pf.Start {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Whatever is left in the group goes too; the group outlives a leader that exited, and its
	// id can't be reused while any member is alive.
	killLeftovers(pf.PID)
	return true
}
