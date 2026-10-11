//go:build !linux && !darwin

package browser

import (
	"io/fs"
	"net"
)

// No peer credentials here: the socket's directory (the user's own profile on Windows) is the
// only barrier.
func peerUID(*net.UnixConn) (int, bool, error) { return 0, false, nil }

func ownerUID(fs.FileInfo) (int, bool) { return 0, false }
