//go:build !unix && !windows

package update

import (
	"io/fs"
	"os"
)

func ownerOfInfo(fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
func linkCount(*os.File, fs.FileInfo) (uint64, bool)  { return 0, false }
func isReparsePoint(fs.FileInfo) bool                 { return false }
