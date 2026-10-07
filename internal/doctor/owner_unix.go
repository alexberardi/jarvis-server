//go:build unix

package doctor

import (
	"io/fs"
	"syscall"
)

// unixModes: the permission checks apply (Windows relies on the directory's ACL).
const unixModes = true

func fileOwner(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
