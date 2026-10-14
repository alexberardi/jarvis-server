package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// redirectLaunchdOutput points stdout and stderr at the agent's log file when the app's
// SMAppService LaunchAgent started it with both on /dev/null (launchdLogTarget).
func redirectLaunchdOutput() {
	home, _ := os.UserHomeDir()
	path := launchdLogTarget(os.Getenv("XPC_SERVICE_NAME"), isDevNull(os.Stderr), home)
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755) // ~/Library/Logs: there on any Mac, but cheap
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return // nowhere to say so: it stays /dev/null
	}
	defer f.Close()
	_ = unix.Dup2(int(f.Fd()), 1)
	_ = unix.Dup2(int(f.Fd()), 2)
}

func isDevNull(f *os.File) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Stat(os.DevNull)
	return err == nil && os.SameFile(a, b)
}
