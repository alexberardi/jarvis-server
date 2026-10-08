package doctor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// jarvisdServer is a listener answering /health as jarvisd after delay; hang never answers
// (until the request is cancelled), like a jarvisd still loading its models.
func jarvisdServer(t *testing.T, delay time.Duration, hang bool) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hang {
			<-r.Context().Done()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
		w.Header().Set("Server", httpx.ServerName)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

func shortTimeouts(t *testing.T, first, retry time.Duration) {
	t.Helper()
	h, s := headerTimeout, slowHeaderTimeout
	headerTimeout, slowHeaderTimeout = first, retry
	t.Cleanup(func() { headerTimeout, slowHeaderTimeout = h, s })
}

func listeningOpts(owner string, ports ...Port) Options {
	return Options{Ports: ports, GOOS: "linux",
		Dial:          (&net.Dialer{Timeout: time.Second}).DialContext,
		ServerHeader:  serverHeader,
		ListenerOwner: func(context.Context, int) string { return owner }}
}

func checkNamed(checks []Check, name string) *Check {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

// A10e: right after `jarvisd upgrade` passed its gate on the Mac, the doctor said "another
// program holds llm (7704)" although jarvisd held it and answered a few seconds later: its
// /health was slow while the llm module loaded models.
func TestSlowJarvisdListenerIsNotAnotherProgram(t *testing.T) {
	shortTimeouts(t, 100*time.Millisecond, 2*time.Second)
	ctx := context.Background()
	config := Port{"config", jarvisdServer(t, 0, false), "tcp"}

	// Slow, answers on the second try: all good.
	llm := Port{"llm", jarvisdServer(t, 400*time.Millisecond, false), "tcp"}
	checks := listening(ctx, listeningOpts("", config, llm))
	if c := checkNamed(checks, "listening"); c == nil || c.Status != OK || checkNamed(checks, "ports") != nil {
		t.Fatalf("slow but answering: %+v", checks)
	}

	// Never answers, owner unknown (no root for ss/lsof), next to a port answering as jarvisd:
	// jarvisd binds all its listeners before serving, so it is jarvisd's, only slow.
	shortTimeouts(t, 100*time.Millisecond, 200*time.Millisecond)
	hung := Port{"llm", jarvisdServer(t, 0, true), "tcp"}
	broker := Port{"mqtt", jarvisdServer(t, 0, true), "tcp"} // accepts; not asked
	checks = listening(ctx, listeningOpts("", config, hung, broker))
	c := checkNamed(checks, "listening")
	if c == nil || c.Status != Warn || !strings.Contains(c.Detail, "llm (") || checkNamed(checks, "ports") != nil {
		t.Fatalf("hung next to jarvisd: %+v", checks)
	}

	// The only HTTP listener hangs, but the process behind the socket is jarvisd.
	checks = listening(ctx, listeningOpts("jarvisd", hung, broker))
	if c := checkNamed(checks, "listening"); c == nil || c.Status != Warn || checkNamed(checks, "ports") != nil {
		t.Fatalf("hung, owner jarvisd: %+v", checks)
	}

	// Another program behind the socket stays another program, next to jarvisd or not.
	checks = listening(ctx, listeningOpts("python3", config, hung))
	if c := checkNamed(checks, "ports"); c == nil || c.Status != Fail || !strings.Contains(c.Detail, "llm (") || strings.Contains(c.Detail, "config") {
		t.Fatalf("owner python3: %+v", checks)
	}
	// And so does a hung listener nothing vouches for (a wedged legacy service).
	checks = listening(ctx, listeningOpts("", hung, broker))
	if c := checkNamed(checks, "ports"); c == nil || c.Status != Fail || !strings.Contains(c.Detail, "mqtt (") {
		t.Fatalf("hung, unknown owner, no jarvisd: %+v", checks)
	}
}

func TestListenerOwner(t *testing.T) {
	ctx := context.Background()
	run := fakeRun(map[string]string{
		"lsof -nP -iTCP:7704 -sTCP:LISTEN -Fc": "p4242\ncjarvisd\nf9\n",
		"ss -ltnpH sport = :7704":              `LISTEN 0 4096 0.0.0.0:7704 0.0.0.0:* users:(("jarvisd",pid=4242,fd=9))` + "\n",
		"powershell -NoProfile -NonInteractive -Command (Get-Process -Id (Get-NetTCPConnection -LocalPort 7704 -State Listen -ErrorAction Stop | Select-Object -First 1).OwningProcess).ProcessName": "jarvisd\r\n",
	})
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if got := listenerOwner(ctx, goos, run, 7704); !isJarvisd(got) {
			t.Errorf("%s: owner %q", goos, got)
		}
		if got := listenerOwner(ctx, goos, run, 7031); got != "" {
			t.Errorf("%s: unknown port owner %q", goos, got)
		}
	}
	// ss without root shows no process for another account's socket.
	if got := ssProcess("LISTEN 0 4096 0.0.0.0:7704 0.0.0.0:*\n"); got != "" {
		t.Errorf("ss without users: %q", got)
	}
	for name, want := range map[string]bool{"jarvisd": true, `C:\Program Files\jarvisd\jarvisd.exe`: true, "JARVISD.EXE": true,
		"python3": false, "jarvisd-updater": false, "": false} {
		if isJarvisd(name) != want {
			t.Errorf("isJarvisd(%q) != %v", name, want)
		}
	}
}
