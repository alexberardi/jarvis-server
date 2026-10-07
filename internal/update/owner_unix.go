//go:build unix

package update

import (
	"os"
	"syscall"
)

func ownerOf(path string) (uid, gid int, ok bool) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(s.Uid), int(s.Gid), true
}
