//go:build unix

package update

import (
	"io/fs"
	"os"
	"syscall"
)

func ownerOfInfo(fi fs.FileInfo) (uid, gid int, ok bool) {
	s, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(s.Uid), int(s.Gid), true
}

// linkCount is the file's number of hard links.
func linkCount(_ *os.File, fi fs.FileInfo) (uint64, bool) {
	s, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(s.Nlink), true
}

// isReparsePoint is Windows-only; Lstat's mode covers symlinks here.
func isReparsePoint(fs.FileInfo) bool { return false }
