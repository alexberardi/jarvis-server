package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// Log rotation for the supervisor-level log: checked when jarvisd starts, which is when
// the Windows service opens it. The logs module's own store is the everyday log; this file
// is for what happens before it opens and for crashes.
const (
	logMaxBytes = 10 << 20
	logKeep     = 3
)

// OpenLog opens path for appending (creating its directory), first rotating it to path.1
// … path.3 when it is over 10 MB.
func OpenLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err == nil && st.Size() > logMaxBytes {
		rotate(path, logKeep)
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// rotate shifts path.N-1 → path.N … path → path.1, dropping the oldest.
func rotate(path string, keep int) {
	os.Remove(fmt.Sprintf("%s.%d", path, keep))
	for i := keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	os.Rename(path, path+".1")
}
