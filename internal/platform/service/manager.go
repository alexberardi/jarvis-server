package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

// InstallOptions configures `jarvisd service install`.
type InstallOptions struct {
	// Binary is the jarvisd the service runs (default: this executable). The service account
	// must be able to read it, so it belongs in /usr/local/bin or %ProgramFiles%\jarvisd,
	// not in someone's home or Downloads.
	Binary string
	// Home is the data directory (default: the per-OS service default, DefaultHome).
	Home string
	// User installs a systemd --user unit for the invoking account (Linux only).
	User bool
	// RunAs is the macOS account the LaunchDaemon runs as (default $SUDO_USER).
	RunAs string
	// NoStart registers the service without starting it.
	NoStart bool
}

// Status is what the service manager says about jarvisd.
type Status struct {
	Kind      Kind   `json:"supervisor"`
	Installed bool   `json:"installed"`
	State     string `json:"state"` // the manager's own word: active/running/stopped/...
	Running   bool   `json:"running"`
	PID       int    `json:"pid,omitempty"`
	Home      string `json:"home,omitempty"`
	Detail    string `json:"detail,omitempty"` // e.g. the definition's path, restart count
}

// Manager registers and controls the jarvisd service with one service manager.
type Manager interface {
	Kind() Kind
	Install(ctx context.Context, o InstallOptions) error
	// Uninstall stops and removes the service definition. The data directory and env file
	// are kept.
	Uninstall(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	Status(ctx context.Context) (Status, error)
	// InstalledHome is the --home of the installed service, "" when none is installed.
	InstalledHome() string
	// PurgePlan lists what Purge deletes for a service that used home.
	PurgePlan(home string) PurgePlan
	// Purge deletes the plan's data directory, paths and account (`service uninstall
	// --purge`, after Uninstall).
	Purge(ctx context.Context, p PurgePlan) error
}

// ErrNotInstalled is returned when the service is not registered.
var ErrNotInstalled = errors.New("jarvisd service is not installed")

// LogFile is the file name of the supervisor-level log (launchd's StandardErrorPath, the
// Windows service's stderr) under <home>/logs.
const LogFile = "jarvisd.log"

// LogPath is <home>/logs/jarvisd.log.
func LogPath(home string) string { return filepath.Join(home, "logs", LogFile) }

// runFunc runs a command and returns its combined output; injectable for tests.
type runFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// homeFromArgs finds the value of --home (or --home=) in a command line.
func homeFromArgs(args []string) string {
	for i, a := range args {
		if a == "--home" && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--home="); ok {
			return v
		}
	}
	return ""
}

// writeFile writes data with exactly perm (the process umask may be 077).
func writeFile(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

// writeEnvTemplate creates the commented env file unless it exists; it reports whether it
// wrote one.
func writeEnvTemplate(path string, perm fs.FileMode) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	return true, writeFile(path, []byte(config.EnvFileTemplate), perm)
}

// makeHome creates the data directory owner-only.
func makeHome(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.Chmod(home, 0o700)
}

// poll calls f every interval until it returns true or timeout passes.
func poll(ctx context.Context, timeout, interval time.Duration, f func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if f() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
}

// printf writes progress for the operator.
func printf(w io.Writer, format string, a ...any) {
	if w != nil {
		fmt.Fprintf(w, format, a...)
	}
}
