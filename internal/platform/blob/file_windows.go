//go:build windows

package blob

import (
	"errors"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows refuses to replace a file that another handle has open, unless the rename uses
// POSIX semantics (Windows 10 1607+, NTFS): then the open readers keep the old contents and
// new opens see the new file, just as on unix. Readers must also open with FILE_SHARE_DELETE,
// which os.Open doesn't set. If POSIX rename isn't supported, fall back to MoveFileEx with
// short retries.

func openShared(path string) (*os.File, error) {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

const (
	fileRenameInfoEx         = 22 // FILE_INFO_BY_HANDLE_CLASS FileRenameInfoEx
	renameFlagReplace        = 0x1
	renameFlagPosixSemantics = 0x2
)

// posixRename renames from over to with FILE_RENAME_FLAG_POSIX_SEMANTICS.
func posixRename(from, to string) error {
	src, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	dst, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(src, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	// FILE_RENAME_INFO: Flags (u32, padded), RootDirectory (HANDLE), FileNameLength (u32),
	// FileName (WCHAR[], no terminator counted).
	name := dst[:len(dst)-1]
	const nameOff = unsafe.Offsetof(renameInfo{}.FileName)
	buf := make([]byte, int(nameOff)+len(name)*2+2)
	info := (*renameInfo)(unsafe.Pointer(&buf[0]))
	info.Flags = renameFlagReplace | renameFlagPosixSemantics
	info.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[nameOff])), len(name)), name)
	return windows.SetFileInformationByHandle(h, fileRenameInfoEx, &buf[0], uint32(len(buf)))
}

type renameInfo struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

func retry(op func() error) error {
	var err error
	for i := range 50 {
		if err = op(); err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return err
		}
		time.Sleep(time.Duration(i+1) * time.Millisecond)
	}
	return err
}

func replaceFile(from, to string) error {
	return retry(func() error {
		err := posixRename(from, to)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
			errors.Is(err, syscall.EWINDOWS) {
			return os.Rename(from, to) // no POSIX rename here (old Windows, FAT)
		}
		return err
	})
}

func removeFile(path string) error { return retry(func() error { return os.Remove(path) }) }
