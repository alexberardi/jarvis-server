package sysinfo

import "golang.org/x/sys/unix"

func totalMemory() uint64 {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return n
}

func release() string {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}

func diskFree(path string) uint64 {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}
