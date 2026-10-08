package engines

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The fake engine is this test binary re-executed with ENGINES_HELPER=1. Knobs:
//
//	HELPER_PORT             serve /health on 127.0.0.1:port
//	HELPER_STARTS           append one line per start to this file
//	HELPER_HEALTHY_AFTER    /health is 503 until this duration (model loading)
//	HELPER_UNHEALTHY_AFTER  /health turns 503 after this duration
//	HELPER_CRASH_AFTER      exit 3 after this duration
//	HELPER_ENV_FILE         write the environment here
//	HELPER_CHILD_PID_FILE   spawn a sleeping grandchild and write its pid here
//	HELPER_IGNORE_TERM      ignore SIGTERM
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ENGINES_HELPER") != "1" {
		t.Skip("helper process")
	}
	runHelper()
	os.Exit(0)
}

func runHelper() {
	dur := func(k string) (time.Duration, bool) {
		v := os.Getenv(k)
		if v == "" {
			return 0, false
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			panic(err)
		}
		return d, true
	}
	if os.Getenv("HELPER_SLEEP") == "1" {
		time.Sleep(time.Minute)
		return
	}
	start := time.Now()
	if f := os.Getenv("HELPER_STARTS"); f != "" {
		fh, _ := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintln(fh, os.Getpid())
		fh.Close()
	}
	fmt.Println("hello stdout")
	fmt.Fprintln(os.Stderr, "hello stderr")
	if os.Getenv("HELPER_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	if f := os.Getenv("HELPER_ENV_FILE"); f != "" {
		_ = os.WriteFile(f, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	}
	if f := os.Getenv("HELPER_CHILD_PID_FILE"); f != "" {
		c := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		c.Env = []string{"ENGINES_HELPER=1", "HELPER_SLEEP=1"}
		if err := c.Start(); err != nil {
			panic(err)
		}
		_ = os.WriteFile(f, []byte(fmt.Sprint(c.Process.Pid)), 0o600)
	}
	if d, ok := dur("HELPER_CRASH_AFTER"); ok {
		if d == 0 {
			// Before listening: a timer can lose the race to the first health check.
			os.Exit(3)
		}
		time.AfterFunc(d, func() { os.Exit(3) })
	}
	healthyAfter, _ := dur("HELPER_HEALTHY_AFTER")
	unhealthyAfter, sick := dur("HELPER_UNHEALTHY_AFTER")
	recoverAfter, recovers := dur("HELPER_RECOVER_AFTER")
	port := os.Getenv("HELPER_PORT")
	if port == "" {
		select {}
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		panic(err)
	}
	_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		age := time.Since(start)
		if age < healthyAfter || (sick && age >= unhealthyAfter && !(recovers && age >= recoverAfter)) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

type fake struct {
	t      *testing.T
	dir    string
	port   int
	starts string
}

// usedPorts keeps parallel tests from sharing a port: FreePort can hand the same free port
// to two tests before either engine binds it, and then one fake answers both health checks.
var (
	portsMu   sync.Mutex
	usedPorts = map[int]bool{}
)

func newFake(t *testing.T) *fake {
	t.Helper()
	portsMu.Lock()
	defer portsMu.Unlock()
	var port int
	for {
		p, err := FreePort()
		if err != nil {
			t.Fatal(err)
		}
		if !usedPorts[p] {
			usedPorts[p], port = true, p
			break
		}
	}
	dir := t.TempDir()
	return &fake{t: t, dir: dir, port: port, starts: filepath.Join(dir, "starts")}
}

// spec returns a fast-cycling spec for the fake engine with the given knobs.
func (f *fake) spec(env map[string]string) Spec {
	e := map[string]string{
		"ENGINES_HELPER": "1",
		"HELPER_PORT":    fmt.Sprint(f.port),
		"HELPER_STARTS":  f.starts,
	}
	for k, v := range env {
		e[k] = v
	}
	return Spec{
		Name: "fake",
		Path: os.Args[0],
		Args: []string{"-test.run=^TestHelperProcess$"},
		Env:  e,
		Health: &HealthCheck{
			URL:              fmt.Sprintf("http://127.0.0.1:%d/health", f.port),
			Interval:         20 * time.Millisecond,
			Timeout:          200 * time.Millisecond,
			FailureThreshold: 3,
		},
		StartTimeout: 5 * time.Second,
		StopTimeout:  2 * time.Second,
		Restart:      RestartPolicy{BackoffMin: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond, MaxRestarts: -1, Window: time.Minute},
	}
}

func (f *fake) startCount() int {
	b, err := os.ReadFile(f.starts)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func start(t *testing.T, spec Spec) *Supervisor {
	t.Helper()
	s, err := New(spec, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitHealthy(t *testing.T, s *Supervisor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v (status %+v)", err, s.Status())
	}
}

func TestBecomesHealthyAndStops(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	before := time.Now()
	s := start(t, f.spec(map[string]string{"HELPER_HEALTHY_AFTER": "150ms"}))
	if st := s.State(); st != Starting {
		t.Fatalf("state = %s, want starting while loading", st)
	}
	launched := s.Status().Started
	waitHealthy(t, s)
	st := s.Status()
	if st.PID == 0 || st.Restarts != 0 {
		t.Fatalf("status = %+v", st)
	}
	// Started is the process launch, unchanged by the state change; Since is the state's.
	if launched.Before(before) || !st.Started.Equal(launched) || !st.Since.After(launched) {
		t.Fatalf("started %v (launched %v), since %v", st.Started, launched, st.Since)
	}
	eventually(t, "output captured", func() bool {
		out := s.Status().Output
		return slices.Contains(out, "hello stdout") && slices.Contains(out, "hello stderr")
	})
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.State != Stopped || st.PID != 0 || !st.Started.IsZero() {
		t.Fatalf("after stop: %+v", st)
	}
	if err := s.WaitHealthy(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("WaitHealthy after stop = %v", err)
	}
}

func TestRestartsAfterCrash(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	s := start(t, f.spec(map[string]string{"HELPER_CRASH_AFTER": "100ms"}))
	waitHealthy(t, s)
	eventually(t, "3 starts", func() bool { return f.startCount() >= 3 })
	st := s.Status()
	// The last restart's reason is the crash, or (on a slow runner) the failed health checks
	// that noticed the dead process first; both are restarts of a crashing engine.
	if st.Restarts < 2 || !(strings.Contains(st.LastError, "exit status 3") || strings.Contains(st.LastError, "failed health checks")) {
		t.Fatalf("status = %+v", st)
	}
}

func TestBackoff(t *testing.T) {
	p := RestartPolicy{BackoffMin: time.Second, BackoffMax: 5 * time.Second}
	var got []time.Duration
	for n := 1; n <= 5; n++ {
		got = append(got, p.backoff(n))
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("backoff = %v, want %v", got, want)
	}
}

func TestGivesUpAfterMaxRestarts(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	spec := f.spec(map[string]string{"HELPER_CRASH_AFTER": "0s"}) // missing GGUF: exits at once
	spec.Restart.MaxRestarts = 2
	s := start(t, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.WaitHealthy(ctx); !errors.Is(err, ErrFailed) {
		t.Fatalf("WaitHealthy = %v, want ErrFailed", err)
	}
	st := s.Status()
	if st.State != Failed || st.Restarts != 2 || !strings.Contains(st.LastError, "exit status 3") {
		t.Fatalf("status = %+v", st)
	}
	if n := f.startCount(); n != 3 {
		t.Fatalf("starts = %d, want 3", n)
	}
	// A failed engine can be started again (e.g. after the operator fixes it).
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
}

func TestRestartsOnFailingHealthChecks(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	s := start(t, f.spec(map[string]string{"HELPER_UNHEALTHY_AFTER": "200ms"}))
	waitHealthy(t, s)
	eventually(t, "restart after failed checks", func() bool { return f.startCount() >= 2 })
	if st := s.Status(); !strings.Contains(st.LastError, "consecutive failed health checks") {
		t.Fatalf("status = %+v", st)
	}
}

func TestStartTimeout(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	spec := f.spec(map[string]string{"HELPER_HEALTHY_AFTER": "1h"})
	spec.StartTimeout = 200 * time.Millisecond
	spec.Restart.MaxRestarts = 1
	s := start(t, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.WaitHealthy(ctx); !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "not healthy within") {
		t.Fatalf("WaitHealthy = %v", err)
	}
}

func TestRestart(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	s := start(t, f.spec(nil))
	waitHealthy(t, s)
	pid := s.Status().PID
	if err := s.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitHealthy(t, s)
	st := s.Status()
	if st.PID == pid || st.PID == 0 || st.Restarts != 0 || f.startCount() != 2 {
		t.Fatalf("after Restart: %+v (old pid %d, starts %d)", st, pid, f.startCount())
	}
}

func TestStopEscalatesToKill(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	spec := f.spec(map[string]string{"HELPER_IGNORE_TERM": "1"})
	spec.StopTimeout = 200 * time.Millisecond
	s := start(t, spec)
	waitHealthy(t, s)
	began := time.Now()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(began); el > 3*time.Second {
		t.Fatalf("stop took %s", el)
	}
	if s.State() != Stopped {
		t.Fatalf("state = %s", s.State())
	}
}

func TestNoHealthCheckIsHealthyWhenRunning(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	spec := f.spec(nil)
	spec.Health = nil
	s := start(t, spec)
	waitHealthy(t, s)
}

func TestStartMissingBinary(t *testing.T) {
	t.Parallel()
	s, err := New(Spec{Name: "ghost", Path: filepath.Join(t.TempDir(), "nope")}, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err == nil {
		t.Fatal("Start succeeded")
	}
	if s.State() != Failed {
		t.Fatalf("state = %s", s.State())
	}
}

func TestEnviron(t *testing.T) {
	base := []string{"PATH=/bin", "FOO=base", "HIP_VISIBLE_DEVICES="}
	spec := Spec{
		GPU: GPU{Backend: CUDA, VisibleDevices: "1"},
		Env: map[string]string{"FOO": "spec", "BAR": "x"},
	}
	got := spec.environ(base)
	for _, want := range []string{"PATH=/bin", "FOO=spec", "BAR=x", "CUDA_VISIBLE_DEVICES=1"} {
		if !slices.Contains(got, want) {
			t.Errorf("env missing %s: %v", want, got)
		}
	}
	if slices.Contains(got, "FOO=base") {
		t.Errorf("Env did not override base: %v", got)
	}

	// An operator's own visibility setting wins over GPU; an empty one doesn't.
	spec = Spec{GPU: GPU{Backend: CUDA, VisibleDevices: "1"}}
	if got := spec.environ([]string{"CUDA_VISIBLE_DEVICES=7"}); !slices.Equal(got, []string{"CUDA_VISIBLE_DEVICES=7"}) {
		t.Errorf("operator override lost: %v", got)
	}
	spec = Spec{GPU: GPU{Backend: ROCm, VisibleDevices: "0"}}
	if got := spec.environ(base); !slices.Contains(got, "HIP_VISIBLE_DEVICES=0") {
		t.Errorf("empty operator var should be replaced: %v", got)
	}
	for b, want := range map[GPUBackend]string{Vulkan: "GGML_VK_VISIBLE_DEVICES", Metal: ""} {
		k, _, ok := GPU{Backend: b, VisibleDevices: "0"}.envVar()
		if k != want || ok != (want != "") {
			t.Errorf("%s: envVar = %q %v", b, k, ok)
		}
	}
}

func TestEnvReachesEngine(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	envFile := filepath.Join(f.dir, "env")
	spec := f.spec(map[string]string{"HELPER_ENV_FILE": envFile, "JARVIS_TEST_VAR": "yes"})
	spec.GPU = GPU{Backend: Vulkan, VisibleDevices: "2"}
	s := start(t, spec)
	waitHealthy(t, s)
	b, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Split(string(b), "\n")
	for _, want := range []string{"JARVIS_TEST_VAR=yes", "GGML_VK_VISIBLE_DEVICES=2"} {
		if !slices.Contains(env, want) {
			t.Errorf("engine env missing %s", want)
		}
	}
	if os.Getenv("PATH") != "" && !slices.Contains(env, "PATH="+os.Getenv("PATH")) {
		t.Error("engine env lost the parent's PATH")
	}
}

func TestManager(t *testing.T) {
	t.Parallel()
	m := NewManager(quietLog())
	a, b := newFake(t), newFake(t)
	sa, sb := a.spec(nil), b.spec(nil)
	sa.Name, sb.Name = "llm-live", "llm-bg"
	if _, err := m.Add(sa); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add(sb); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add(sa); err == nil {
		t.Fatal("duplicate Add succeeded")
	}
	t.Cleanup(func() { _ = m.StopAll(context.Background()) })
	if err := m.StartAll(); err != nil {
		t.Fatal(err)
	}
	waitHealthy(t, m.Get("llm-live"))
	waitHealthy(t, m.Get("llm-bg"))
	st := m.Status()
	if len(st) != 2 || st[0].Name != "llm-bg" || st[1].Name != "llm-live" || st[0].State != Healthy {
		t.Fatalf("status = %+v", st)
	}
	if err := m.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, s := range m.Status() {
		if s.State != Stopped {
			t.Fatalf("%s: %s", s.Name, s.State)
		}
	}
}

func TestFreePort(t *testing.T) {
	p, err := FreePort()
	if err != nil || p <= 0 {
		t.Fatalf("FreePort = %d, %v", p, err)
	}
}

func TestDrainGraceDelaysHealthRestart(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	spec := f.spec(map[string]string{"HELPER_UNHEALTHY_AFTER": "200ms"})
	spec.Health.DrainGrace = 400 * time.Millisecond
	s := start(t, spec)
	waitHealthy(t, s)
	eventually(t, "draining", func() bool { return s.State() == Draining })
	drainingAt := time.Now()
	if f.startCount() != 1 {
		t.Fatal("restarted before the grace ran out")
	}
	eventually(t, "restart after grace", func() bool { return f.startCount() >= 2 })
	if waited := time.Since(drainingAt); waited < 300*time.Millisecond {
		t.Fatalf("restarted after %s, want ~400ms grace", waited)
	}
}

func TestDrainCalledOffWhenEngineRecovers(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	// Sick from 200ms to 1.5s: long enough to start draining even on a slow runner (a 200ms
	// window was missed on windows-latest), then healthy again well inside the grace.
	spec := f.spec(map[string]string{"HELPER_UNHEALTHY_AFTER": "200ms", "HELPER_RECOVER_AFTER": "1500ms"})
	spec.Health.DrainGrace = 5 * time.Second
	s := start(t, spec)
	waitHealthy(t, s)
	eventually(t, "draining", func() bool { return s.State() == Draining })
	eventually(t, "recovered", func() bool { return s.State() == Healthy })
	time.Sleep(300 * time.Millisecond)
	if n := f.startCount(); n != 1 {
		t.Fatalf("engine restarted %d times; the recovery should have called it off", n-1)
	}
}
