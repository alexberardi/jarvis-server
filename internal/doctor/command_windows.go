//go:build windows

package doctor

import (
	"context"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// command passes the arguments to the program verbatim: netsh parses its own command line
// and wants `program="C:\Program Files\..."` as written, which Go's argv quoting would turn
// into `"program=\"C:\Program Files\...\""`.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: strings.Join(append([]string{name}, args...), " ")}
	return cmd
}

// Elevated reports whether this process runs with Administrator rights (an elevated token).
func Elevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
