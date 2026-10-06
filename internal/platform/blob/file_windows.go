//go:build windows

package blob

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Windows refuses to replace or delete a file that another handle has open unless every
// handle was opened with FILE_SHARE_DELETE, which os.Open doesn't set. Readers open with it,
// and replace/remove retry briefly to ride out a concurrent rename holding the destination.

func openShared(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) || errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
			return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

const errSharingViolation syscall.Errno = 32

func retry(op func() error) error {
	var err error
	for i := range 50 {
		if err = op(); err == nil {
			return nil
		}
		if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, errSharingViolation) {
			return err
		}
		time.Sleep(time.Duration(i+1) * time.Millisecond)
	}
	return err
}

func replaceFile(from, to string) error { return retry(func() error { return os.Rename(from, to) }) }
func removeFile(path string) error      { return retry(func() error { return os.Remove(path) }) }
