package browser

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSocketPathFor(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	dir := func(d string) func() (string, error) { return func() (string, error) { return d, nil } }
	for _, tc := range []struct {
		goos string
		env  map[string]string
		cfg  string
		want string
	}{
		{"linux", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, "/home/u/.config", "/run/user/1000/jarvis-agent/browser.sock"},
		{"linux", nil, "/home/u/.config", "/home/u/.config/jarvis-agent/browser.sock"},
		{"darwin", nil, "/Users/u/Library/Application Support", "/Users/u/Library/Application Support/jarvis-agent/browser.sock"},
		{"darwin", map[string]string{"TMPDIR": "/var/folders/xy/T"}, "/Users/" + strings.Repeat("u", 80) + "/Library/Application Support",
			"/var/folders/xy/T/jarvis-agent/browser.sock"},
	} {
		got, err := socketPathFor(tc.goos, env(tc.env), dir(tc.cfg), dir(`C:\Users\u\AppData\Local`))
		if err != nil || filepath.ToSlash(got) != tc.want {
			t.Errorf("%s %v: %q, %v; want %q", tc.goos, tc.env, got, err, tc.want)
		}
	}
	got, err := socketPathFor("windows", env(nil), dir("x"), dir(`C:\Users\u\AppData\Local`))
	if err != nil || !strings.HasSuffix(got, "browser.sock") || !strings.Contains(got, "jarvis-agent") {
		t.Errorf("windows: %q, %v", got, err)
	}
	if _, err := socketPathFor("linux", env(map[string]string{"XDG_RUNTIME_DIR": "/" + strings.Repeat("x", 120)}), dir("x"), dir("x")); err == nil {
		t.Error("a socket path over sun_path was accepted")
	}
}

func TestListenPermissionsAndReuse(t *testing.T) {
	path := shortSocket(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("socket mode %v, want 0600", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(path))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v, want 0700", di.Mode().Perm())
		}
	}
	// A live socket: a second agent must not steal it.
	if _, err := Listen(path); !errors.Is(err, ErrInUse) {
		t.Fatalf("second Listen: %v, want ErrInUse", err)
	}
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	// The same user is accepted.
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	_ = ln.Close()

	// A stale socket file (agent crashed) is replaced.
	if runtime.GOOS != "windows" {
		raw, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		raw.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = raw.Close()
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stale socket not left behind: %v", err)
		}
		ln2, err := Listen(path)
		if err != nil {
			t.Fatalf("stale socket not replaced: %v", err)
		}
		_ = ln2.Close()
	}
}

func TestListenRefusesNonSocketAndLongPaths(t *testing.T) {
	path := shortSocket(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if _, err := Listen(path); err == nil {
			t.Fatal("Listen replaced a regular file")
		}
		if b, _ := os.ReadFile(path); string(b) != "not a socket" {
			t.Fatal("the file was touched")
		}
	}
	if _, err := Listen(filepath.Join(os.TempDir(), strings.Repeat("x", 120), "b.sock")); err == nil {
		t.Fatal("over-long path accepted")
	}
}

func TestListenTightensOwnDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory ACLs, not modes")
	}
	path := shortSocket(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Dir(path), 0o777)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir left %v", di.Mode().Perm())
	}
}

func TestCheckPeerSameUser(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer credentials here")
	}
	path := shortSocket(t)
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, _ := net.Dial("unix", path)
		if c != nil {
			defer c.Close()
			buf := make([]byte, 1)
			_, _ = c.Read(buf)
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uid, supported, err := peerUID(c.(*net.UnixConn))
	if err != nil || !supported || uid != os.Getuid() {
		t.Fatalf("peerUID = %d, %v, %v; want %d", uid, supported, err, os.Getuid())
	}
	if err := CheckPeer(c); err != nil {
		t.Fatal(err)
	}
}

func TestListenerDropsOtherUsers(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer credentials here")
	}
	path := shortSocket(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// The same listener, expecting another user: every peer now looks like someone else.
	ln := &peerListener{Listener: l.(*peerListener).Listener, uid: os.Getuid() + 1}
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The listener closes it without handing it on: the client sees EOF.
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err == nil {
		t.Fatal("a foreign peer's connection stayed open")
	}
	select {
	case <-accepted:
		t.Fatal("a foreign peer was accepted")
	default:
	}
}
