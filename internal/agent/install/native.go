package install

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// NativeSys is Sys on this machine (Web and Portal are left to the caller).
func NativeSys(out io.Writer, logFile string) Sys {
	return Sys{
		Run:   runCommand,
		Spawn: func(args ...string) error { return spawn(args, logFile) },
		Reg:   nativeRegistry(),
		Login: nativeLoginItems(),
		Out:   out,
	}
}

// runCommand runs args, folding its output into the error when it fails.
func runCommand(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	var buf bytes.Buffer
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Stdout, c.Stderr = &buf, &buf
	hideWindow(c)
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(buf.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// spawn starts args detached, its output appended to logFile (stdin and stdout otherwise go
// nowhere: nobody is watching a login item's terminal).
func spawn(args []string, logFile string) error {
	var out *os.File
	if logFile != "" {
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err == nil {
			out, _ = os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		}
	}
	c := exec.Command(args[0], args[1:]...)
	if out != nil {
		defer out.Close()
		c.Stdout, c.Stderr = out, out
	}
	detach(c)
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}
