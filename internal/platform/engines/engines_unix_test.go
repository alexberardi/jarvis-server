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
