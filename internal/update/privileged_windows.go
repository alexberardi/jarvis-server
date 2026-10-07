package update

import (
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// ownerOfInfo: Windows protects the home with its ACL (00-installers §2.3), not an owner.
func ownerOfInfo(fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

// linkCount is the file's number of hard links, from its handle.
func linkCount(f *os.File, _ fs.FileInfo) (uint64, bool) {
	var d windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &d); err != nil {
		return 0, false
	}
	return uint64(d.NumberOfLinks), true
}

// isReparsePoint reports a junction, mount point, symlink or any other reparse point: the
// helper refuses a home that is one.
func isReparsePoint(fi fs.FileInfo) bool {
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return ok && d.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
