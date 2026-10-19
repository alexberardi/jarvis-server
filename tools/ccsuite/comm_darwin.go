package main

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// procComm is process pid's command name (the kernel's p_comm, at most 16 bytes), or "" when
// there is no such process.
func procComm(pid int) string {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || k.Proc.P_pid != int32(pid) {
		return ""
	}
	c := k.Proc.P_comm[:]
	if i := bytes.IndexByte(c, 0); i >= 0 {
		c = c[:i]
	}
	return string(c)
}
