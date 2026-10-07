package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDetect(t *testing.T) {
	const unitCgroup = "0::/system.slice/jarvisd.service\n"
	const userCgroup = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/jarvisd.service\n"
	const termCgroup = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-kitty-123.scope\n"
	for _, tc := range []struct {
		name   string
		env    map[string]string
		ppid   int
		cgroup string
		win    bool
		want   Kind
	}{
		{"windows service", nil, 4, "", true, SCM},
		{"systemd system unit", map[string]string{"INVOCATION_ID": "abc"}, 1, unitCgroup, false, Systemd},
		{"systemd user unit", map[string]string{"INVOCATION_ID": "abc"}, 900, userCgroup, false, Systemd},
		{"terminal inheriting INVOCATION_ID", map[string]string{"INVOCATION_ID": "abc"}, 900, termCgroup, false, None},
		{"launchd daemon", map[string]string{"XPC_SERVICE_NAME": LaunchdLabel}, 1, "", false, Launchd},
		{"macOS terminal", map[string]string{"XPC_SERVICE_NAME": "0"}, 1, "", false, None},
		{"macOS app terminal", map[string]string{"XPC_SERVICE_NAME": "application.com.apple.Terminal.1"}, 512, "", false, None},
		{"plain shell", nil, 700, termCgroup, false, None},
	} {
		got := detect(probe{
			getenv:  func(k string) string { return tc.env[k] },
			getppid: func() int { return tc.ppid },
			readFile: func(string) ([]byte, error) {
				if tc.cgroup == "" {
					return nil, errors.New("no cgroup")
				}
				return []byte(tc.cgroup), nil
			},
			winService: func() bool { return tc.win },
		})
		if got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestRestarter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := NewRestarter(Systemd, cancel)
	if err := r.Request(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil || !r.Requested() {
		t.Fatal("restart did not cancel serve's context")
	}
	if err := r.Err(nil); !errors.Is(err, ErrRestart) || ExitCode(err) != RestartExitCode {
		t.Fatalf("err %v code %d", err, ExitCode(err))
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	u := NewRestarter(None, cancel2)
	if err := u.Request(); !errors.Is(err, ErrUnsupervised) {
		t.Fatalf("unsupervised: %v", err)
	}
	if ctx2.Err() != nil || u.Requested() {
		t.Fatal("unsupervised restart must not stop jarvisd")
	}
	boom := errors.New("boom")
	if u.Err(boom) != boom || ExitCode(boom) != 1 || ExitCode(nil) != 0 {
		t.Fatal("exit codes")
	}
	if ExitCode(fmt.Errorf("wrapped: %w", ErrRestart)) != RestartExitCode {
		t.Fatal("wrapped restart")
	}
}

func TestNotify(t *testing.T) {
	if runtime.GOOS != "linux" {
		if err := notify("/nonexistent", "READY=1"); err != nil {
			t.Fatalf("notify must be a no-op off linux: %v", err)
		}
		return
	}
	if err := notify("", "READY=1"); err != nil {
		t.Fatal(err)
	}
	for _, abstract := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "notify.sock")
		name := path
		if abstract {
			name = "@jarvisd-test-" + time.Now().Format("150405.000000000")
		}
		addr := &net.UnixAddr{Name: path, Net: "unixgram"}
		if abstract {
			addr.Name = "\x00" + name[1:]
		}
		l, err := net.ListenUnixgram("unixgram", addr)
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithReady(context.Background(), func() {})
		t.Setenv("NOTIFY_SOCKET", name)
		if err := Ready(ctx); err != nil {
			t.Fatal(err)
		}
		if err := Stopping(); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		l.SetReadDeadline(time.Now().Add(2 * time.Second))
		for _, want := range []string{"READY=1", "STOPPING=1"} {
			n, err := l.Read(buf)
			if err != nil || string(buf[:n]) != want {
				t.Fatalf("abstract=%v: got %q %v, want %q", abstract, buf[:n], err, want)
			}
		}
		l.Close()
	}
}

func TestReadyHook(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	called := false
	if err := Ready(WithReady(context.Background(), func() { called = true })); err != nil || !called {
		t.Fatalf("hook not called: %v", err)
	}
	if err := Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
}
