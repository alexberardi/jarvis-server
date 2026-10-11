package browser

import (
	"io/fs"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func peerUID(c *net.UnixConn) (int, bool, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, true, err
	}
	var cred *unix.Xucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, true, err
	}
	if cerr != nil {
		return 0, true, cerr
	}
	return int(cred.Uid), true, nil
}

func ownerUID(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
