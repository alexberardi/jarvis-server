package dictate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Keyboard sends key events to whatever window has focus. Implementations compute
// availability per call (a session or a grant can come and go) and never focus anything.
type Keyboard interface {
	// Name is the backend, for status and logs ("wtype", "xdotool", "CGEvent", "SendInput").
	Name() string
	// Available says whether typing works now, and why not when it doesn't.
	Available(ctx context.Context) (bool, string)
	// Type types text (already sanitized: no control characters) into the focused window.
	Type(ctx context.Context, text string) error
	// Press presses one key of the closed set.
	Press(ctx context.Context, k Key) error
}

// Window is one window a Windows backend can focus.
type Window struct {
	ID      string // opaque, backend-defined (Hyprland address, sway con id, X11 id, HWND, pid/index)
	Apps    []string
	Title   string
	Recency int  // lower = focused more recently
	Visible bool // on screen now
}

// Windows lists and focuses windows, and says which one has focus. Typing into a named app
// needs all three: focus is requested, then confirmed, then the text goes.
type Windows interface {
	Available(ctx context.Context) (bool, string)
	List(ctx context.Context) ([]Window, error)
	Focus(ctx context.Context, id string) error
	// Active is the focused window's id ("" when nothing has focus), comparable with Same.
	Active(ctx context.Context) (string, error)
	// Same reports whether a window id from List is the active id.
	Same(id, active string) bool
}

// ErrUnavailable: this desktop can't do it (no backend, no session, no grant).
var ErrUnavailable = errors.New("not supported on this desktop")

// Cmd is one subprocess.
type Cmd struct {
	Name    string
	Args    []string
	Stdin   string        // fed to the command (text to type never goes on a command line)
	Env     []string      // added to the environment
	Timeout time.Duration // 0: 15 s
}

// Exec is the slice of the OS the shell-out backends use, injected for tests.
type Exec interface {
	LookPath(name string) bool
	Getenv(key string) string
	Exists(path string) bool
	// Run runs a command and returns its trimmed stdout. A non-zero exit is an error carrying
	// stderr.
	Run(ctx context.Context, c Cmd) (string, error)
}

// OSExec is the real Exec.
type OSExec struct{}

func (OSExec) LookPath(name string) bool { _, err := exec.LookPath(name); return err == nil }
func (OSExec) Getenv(key string) string  { return os.Getenv(key) }
func (OSExec) Exists(path string) bool   { _, err := os.Stat(path); return err == nil }

func (OSExec) Run(ctx context.Context, c Cmd) (string, error) {
	d := c.Timeout
	if d == 0 {
		d = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", c.Name, msg)
	}
	return strings.TrimSpace(out.String()), nil
}
