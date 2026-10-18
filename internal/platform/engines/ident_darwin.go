package engines

import (
	"bytes"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// procIdentity returns pid's executable path and start time. kern.procargs2 begins with argc
// and then the path the process was exec'd from; it fails for a zombie, which holds no memory.
func procIdentity(pid int) (exe, start string, err error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", "", err
	}
	if kp.Proc.P_pid != int32(pid) {
		return "", "", fmt.Errorf("pid %d: no such process", pid)
	}
	args, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", "", err
	}
	if len(args) < 4 {
		return "", "", errors.New("kern.procargs2: short read")
	}
	path, _, _ := bytes.Cut(args[4:], []byte{0})
	if len(path) == 0 {
		return "", "", errors.New("kern.procargs2: no exec path")
	}
	t := kp.Proc.P_starttime
	return string(path), fmt.Sprintf("%d.%06d", t.Sec, t.Usec), nil
}
