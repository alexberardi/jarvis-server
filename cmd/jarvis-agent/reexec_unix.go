//go:build !windows

package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"syscall"
)

// reexecRun starts the agent in place of the waiting login item: on Linux it replaces this
// process with a fresh `run` of the same binary and arguments, under the same PID, so systemd
// keeps supervising it; on macOS it starts a fresh process and lets this one exit (handOff).
func reexecRun(log *slog.Logger) error {
	exe, err := agentExe()
	if err != nil {
		return err
	}
	log.Info("starting the agent in place of the waiting login item")
	if cmd := handOff(runtime.GOOS, os.Getenv("XPC_SERVICE_NAME"), os.Getuid(), exe, os.Args[1:]); cmd != nil {
		s, err := installSys(io.Discard)
		if err != nil {
			return err
		}
		// Detached: launchd's kickstart -k stops this process on the way.
		if err := s.Spawn(cmd...); err != nil {
			return fmt.Errorf("starting the agent: %w", err)
		}
		return nil
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("starting the agent: %w", err)
	}
	return nil // not reached
}
