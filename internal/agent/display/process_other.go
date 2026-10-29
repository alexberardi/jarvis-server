//go:build !linux

package display

import "os/exec"

func SetProcessGroup(cmd *exec.Cmd) {}
func StopProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
