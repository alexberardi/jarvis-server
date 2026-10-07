//go:build !windows

package doctor

import (
	"context"
	"os"
	"os/exec"
)

func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// Elevated reports whether this process runs as root.
func Elevated() bool { return os.Geteuid() == 0 }
