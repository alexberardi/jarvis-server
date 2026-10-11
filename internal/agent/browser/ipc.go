package browser

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// SocketEnv overrides the agent's socket path (tests, a second agent in a scratch session).
// The browser passes its environment to the host it starts, so setting it on the browser
// process points that browser's host at the same socket.
const SocketEnv = "JARVIS_AGENT_BROWSER_SOCKET"

// maxSunPath is the smallest sun_path in use (macOS/BSD: 104 bytes incl. the NUL).
const maxSunPath = 103

// SocketPath is where the agent listens for native hosts: a per-user directory nobody else can
// enter. Linux: $XDG_RUNTIME_DIR/jarvis-agent (else the user config dir); macOS: Application
// Support/jarvis-agent (else $TMPDIR, which is per-user, when that path is too long for a
// socket); Windows: %LocalAppData%\jarvis-agent (AF_UNIX, Windows 10 1803+).
func SocketPath() (string, error) {
	if p := os.Getenv(SocketEnv); p != "" {
		return p, nil
	}
	return socketPathFor(runtime.GOOS, os.Getenv, os.UserConfigDir, os.UserCacheDir)
}

func socketPathFor(goos string, getenv func(string) string, configDir, cacheDir func() (string, error)) (string, error) {
	var base string
	switch goos {
	case "windows":
		d, err := cacheDir() // %LocalAppData%
		if err != nil {
			return "", err
		}
		base = d
	case "linux", "freebsd", "openbsd", "netbsd":
		if d := getenv("XDG_RUNTIME_DIR"); d != "" {
			base = d
			break
		}
		fallthrough
	default:
		d, err := configDir()
		if err != nil {
			return "", err
		}
		base = d
	}
	p := filepath.Join(base, "jarvis-agent", "browser.sock")
	if len(p) > maxSunPath && goos == "darwin" {
		if t := getenv("TMPDIR"); t != "" {
			p = filepath.Join(t, "jarvis-agent", "browser.sock")
		}
	}
	if len(p) > maxSunPath {
		return "", fmt.Errorf("socket path too long (%d bytes): %s; set %s", len(p), p, SocketEnv)
	}
	return p, nil
}

// ErrInUse means another agent is already serving browsers on the socket.
var ErrInUse = errors.New("another jarvis-agent is serving the browser socket")

// Listen opens the agent's socket at path: the directory is created 0700 (and must not be
// group/world-writable or someone else's), a live socket there is ErrInUse, a stale one is
// replaced, and the socket itself is 0600. Accepted connections must still pass CheckPeer.
func Listen(path string) (net.Listener, error) {
	if len(path) > maxSunPath {
		return nil, fmt.Errorf("socket path too long for a Unix socket (%d bytes, max %d): %s", len(path), maxSunPath, path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := checkDir(dir); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return nil, ErrInUse
		}
		if fi.Mode()&fs.ModeSocket == 0 && runtime.GOOS != "windows" {
			return nil, fmt.Errorf("%s exists and is not a socket; not touching it", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = ln.Close()
			return nil, err
		}
	}
	return &peerListener{Listener: ln, uid: os.Getuid()}, nil
}

// Dial connects a native host to the agent.
func Dial(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 2*time.Second)
}

func checkDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil // %LocalAppData% is the user's own (profile ACL)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if uid, ok := ownerUID(fi); ok && uid != os.Getuid() {
		return fmt.Errorf("%s belongs to uid %d, not this user", dir, uid)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		// Ours, but others may enter it: tighten (the socket lives here).
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("%s is open to other users: %w", dir, err)
		}
	}
	return nil
}

// peerListener rejects connections from other users before anyone reads from them.
type peerListener struct {
	net.Listener
	uid int // the user peers must be
}

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if err := checkPeer(c, l.uid); err != nil {
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

// CheckPeer verifies the process on the other end of a Unix socket runs as this user (Linux
// SO_PEERCRED, macOS LOCAL_PEERCRED). Elsewhere it relies on the directory's permissions.
func CheckPeer(c net.Conn) error { return checkPeer(c, os.Getuid()) }

func checkPeer(c net.Conn, want int) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil
	}
	uid, supported, err := peerUID(uc)
	if err != nil {
		return err
	}
	if supported && uid != want {
		return fmt.Errorf("peer uid %d is not this user", uid)
	}
	return nil
}
