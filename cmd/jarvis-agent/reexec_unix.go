//go:build !windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

// reexecRun replaces this process with a fresh `run` of the same binary and arguments: the
// waiting login item becomes the agent under the same PID, so systemd or launchd keeps
// supervising it.
func reexecRun(log *slog.Logger) error {
	exe, err := agentExe()
	if err != nil {
		return err
	}
	log.Info("starting the agent in place of the waiting login item")
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("starting the agent: %w", err)
	}
	return nil // not reached
}
