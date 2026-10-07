//go:build unix

package engines

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestStopKillsProcessGroup(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	pidFile := filepath.Join(f.dir, "child")
	s := start(t, f.spec(map[string]string{"HELPER_CHILD_PID_FILE": pidFile}))
	waitHealthy(t, s)
	enginePID := s.Status().PID
	var child int
	eventually(t, "grandchild pid", func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(string(b))
		return err == nil
	})
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{enginePID, child} {
		eventually(t, "process gone", func() bool {
			return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
		})
	}
}

// Unix only: Windows has no graceful stop to ignore (interrupt is unsupported there and Stop
// kills at once), so whether ctx expires first is a race with taskkill.
func TestStopContextForcesKill(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	spec := f.spec(map[string]string{"HELPER_IGNORE_TERM": "1"})
	spec.StopTimeout = time.Hour
	s := start(t, spec)
	waitHealthy(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v", err)
	}
	if s.State() != Stopped {
		t.Fatalf("state = %s", s.State())
	}
}
