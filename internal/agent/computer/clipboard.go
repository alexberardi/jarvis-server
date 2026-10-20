package computer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// The clipboard for computer tasks only (COMPUTER-CONTROL.md C7): computer_act's clipboard_read
// and clipboard_write. Dictation and voice turns never use it.

// cmdClipboard is the clipboard through the session's command-line tools.
type cmdClipboard struct {
	read, write []string
	run         func(ctx context.Context, stdin *string, argv []string) ([]byte, error)
}

const clipboardTimeout = 5 * time.Second

// NativeClipboard is this session's clipboard: pbpaste/pbcopy on macOS, wl-paste/wl-copy on
// Wayland, xclip on X11; nil where there's none.
func NativeClipboard() Clipboard {
	if c := clipboardFor(runtime.GOOS, os.Getenv); c != nil {
		return c
	}
	return nil
}

func clipboardFor(goos string, getenv func(string) string) *cmdClipboard {
	c := &cmdClipboard{run: runCmd}
	switch {
	case goos == "darwin":
		c.read, c.write = []string{"pbpaste"}, []string{"pbcopy"}
	case goos == "linux" && getenv("WAYLAND_DISPLAY") != "":
		c.read, c.write = []string{"wl-paste", "--no-newline"}, []string{"wl-copy"}
	case goos == "linux" && getenv("DISPLAY") != "":
		c.read = []string{"xclip", "-selection", "clipboard", "-out"}
		c.write = []string{"xclip", "-selection", "clipboard", "-in"}
	default:
		return nil
	}
	return c
}

func (c *cmdClipboard) Read(ctx context.Context) (string, error) {
	out, err := c.run(ctx, nil, c.read)
	return string(out), err
}

func (c *cmdClipboard) Write(ctx context.Context, text string) error {
	_, err := c.run(ctx, &text, c.write)
	return err
}

func runCmd(ctx context.Context, stdin *string, argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, clipboardTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(*stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(argv[0] + ": " + msg)
		}
		return nil, err
	}
	return out, nil
}
