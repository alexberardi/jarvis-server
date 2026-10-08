//go:build unix

package main

import (
	"io/fs"
	"syscall"
)

func homeOwner(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
