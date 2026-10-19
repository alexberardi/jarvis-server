package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// leaderIs tells whether process pid, if it is still there, still runs argv0's program (by name,
// in its executable path and arguments, as /proc has them on Linux; when the leader is gone and
// only its group is left, yes). macOS only shows a process's arguments to its own user, so a
// leader whose arguments are hidden is someone else's: not ours.
func leaderIs(pid int, argv0 string) bool {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) <= 4 {
		_, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		return err != nil
	}
	return strings.Contains(string(b[4:]), filepath.Base(argv0)) // past argc
}
