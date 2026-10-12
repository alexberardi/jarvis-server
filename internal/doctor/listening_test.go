package doctor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// answers scripts /health per port: the nth request gets the nth answer (the last one
// repeats). "" never answers in time, "jarvisd" answers as jarvisd.
type answers map[int][]string

// fakeListeners are open listeners on the given ports whose /health answers as scripted, with
// no network and no timing involved. It records whether each request carried a deadline.
type fakeListeners struct {
	mu        sync.Mutex
	script    answers
	calls     map[int]int
	deadlines map[int][]bool
}

func newFakeListeners(a answers) *fakeListeners {
	return &fakeListeners{script: a, calls: map[int]int{}, deadlines: map[int][]bool{}}
}

func (f *fakeListeners) dial(_ context.Context, _, addr string) (net.Conn, error) {
	_, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	if _, ok := f.script[n]; !ok {
		return nil, errors.New("connection refused")
	}
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, nil
}

func (f *fakeListeners) serverHeader(ctx context.Context, url string) (string, error) {
	var port int
	if _, err := fmt.Sscanf(url, "http://127.0.0.1:%d/health", &port); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, hasDeadline := ctx.Deadline()
	f.deadlines[port] = append(f.deadlines[port], hasDeadline)
	s := f.script[port]
	a := s[min(f.calls[port], len(s)-1)]
	f.calls[port]++
	if a == "" {
		return "", context.DeadlineExceeded
	}
	if a == "jarvisd" {
		return httpx.ServerName, nil
	}
	return a, nil
}

func (f *fakeListeners) opts(owner string, ports ...Port) Options {
	return Options{Ports: ports, GOOS: "linux",
		Dial:          f.dial,
		ServerHeader:  f.serverHeader,
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
	ctx := context.Background()
	const configP, slowP, hungP, brokerP = 7700, 7704, 7705, 1884
	config := Port{"config", configP, "tcp"}

	// Slow, answers on the second try (asked with more time): all good.
	f := newFakeListeners(answers{configP: {"jarvisd"}, slowP: {"", "jarvisd"}})
	checks := listening(ctx, f.opts("", config, Port{"llm", slowP, "tcp"}))
	if c := checkNamed(checks, "listening"); c == nil || c.Status != OK || checkNamed(checks, "ports") != nil {
		t.Fatalf("slow but answering: %+v", checks)
	}
	if d := f.deadlines[slowP]; len(d) != 2 || !d[1] {
		t.Fatalf("second try on the slow port: %v, want one with its own deadline", d)
	}

	hung := Port{"llm", hungP, "tcp"}
	broker := Port{"mqtt", brokerP, "tcp"} // accepts; not asked
	script := answers{configP: {"jarvisd"}, hungP: {""}, brokerP: {"never asked"}}

	// Never answers, owner unknown (no root for ss/lsof), next to a port answering as jarvisd:
	// jarvisd binds all its listeners before serving, so it is jarvisd's, only slow.
	f = newFakeListeners(script)
	checks = listening(ctx, f.opts("", config, hung, broker))
	c := checkNamed(checks, "listening")
	if c == nil || c.Status != Warn || !strings.Contains(c.Detail, "llm (") || checkNamed(checks, "ports") != nil {
		t.Fatalf("hung next to jarvisd: %+v", checks)
	}
	if f.calls[brokerP] != 0 {
		t.Fatal("asked the broker for /health")
	}

	// The only HTTP listener hangs, but the process behind the socket is jarvisd.
	checks = listening(ctx, newFakeListeners(script).opts("jarvisd", hung, broker))
	if c := checkNamed(checks, "listening"); c == nil || c.Status != Warn || checkNamed(checks, "ports") != nil {
		t.Fatalf("hung, owner jarvisd: %+v", checks)
	}

	// Another program behind the socket stays another program, next to jarvisd or not.
	checks = listening(ctx, newFakeListeners(script).opts("python3", config, hung))
	if c := checkNamed(checks, "ports"); c == nil || c.Status != Fail || !strings.Contains(c.Detail, "llm (") || strings.Contains(c.Detail, "config") {
		t.Fatalf("owner python3: %+v", checks)
	}
	// And so does a hung listener nothing vouches for (a wedged legacy service).
	checks = listening(ctx, newFakeListeners(script).opts("", hung, broker))
	if c := checkNamed(checks, "ports"); c == nil || c.Status != Fail || !strings.Contains(c.Detail, "mqtt (") {
		t.Fatalf("hung, unknown owner, no jarvisd: %+v", checks)
	}
}

// serverHeader reads the Server header, and gives up when its context does.
func TestServerHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("hang") {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Server", httpx.ServerName)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // hang guard
	defer cancel()
	if got, err := serverHeader(ctx, srv.URL+"/health"); err != nil || got != httpx.ServerName {
		t.Fatalf("serverHeader = %q, %v", got, err)
	}
	short, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShort()
	if _, err := serverHeader(short, srv.URL+"/health?hang"); err == nil {
		t.Fatal("a listener that never answers answered")
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
