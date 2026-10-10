package host

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Sys is the slice of the operating system the shell-out backends (Linux, macOS) touch. It is
// injected so every backend can be exercised on every OS with a fake, and so construction never
// probes anything.
type Sys interface {
	LookPath(name string) bool
	Getenv(key string) string
	// Run runs a command to completion and returns its trimmed stdout. A non-zero exit is an
	// error carrying stderr (or the exit status).
	Run(ctx context.Context, name string, args ...string) (string, error)
	// Start launches a command and returns without waiting (fire-and-forget); the child is
	// reaped in the background so it never lingers as a zombie.
	Start(name string, args ...string) error
}

// OSSys is the real Sys.
type OSSys struct{}

// DefaultRunTimeout bounds a Run whose context has no deadline.
const DefaultRunTimeout = 15 * time.Second

func (OSSys) LookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (OSSys) Getenv(key string) string { return os.Getenv(key) }

func (OSSys) Run(ctx context.Context, name string, args ...string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultRunTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", &RunError{Name: name, Msg: msg}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (OSSys) Start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// RunError is a failed Run: the command name and its stderr.
type RunError struct {
	Name string
	Msg  string
}

func (e *RunError) Error() string { return e.Name + ": " + e.Msg }
