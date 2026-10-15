package main

import (
	"fmt"
	"io"
	"log/slog"
)

// reexecRun starts a fresh `run` detached and lets this one exit: Windows has no exec, and the
// Run key that started the waiting login item supervises nothing.
func reexecRun(log *slog.Logger) error {
	exe, err := agentExe()
	if err != nil {
		return err
	}
	s, err := installSys(io.Discard)
	if err != nil {
		return err
	}
	log.Info("starting the agent in place of the waiting login item")
	if err := s.Spawn(exe, "run"); err != nil {
		return fmt.Errorf("starting the agent: %w", err)
	}
	return nil
}
