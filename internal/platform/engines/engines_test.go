package engines

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The fake engine is this test binary re-executed with ENGINES_HELPER=1. Its health
// endpoint is served by the test process itself (fakeHealth), so a test decides when the
// engine is healthy rather than racing the helper's own clock, and no port is handed from
// FreePort to a child that might lose it. Knobs:
//
//	HELPER_STARTS           append one line per start, once the knobs below are set up
//	HELPER_CRASH_AFTER      exit 3 after this duration
//	HELPER_ERROR_LINE       print this line (as a traceback's root cause)
//	HELPER_ERROR_AFTER      wait this long before printing HELPER_ERROR_LINE
//	HELPER_EARLY_LINE       print this line at once (an exception vLLM only warns about at load)
//	HELPER_ERROR_ON         wait for this file to exist before printing HELPER_ERROR_LINE
//	HELPER_ENV_FILE         write the environment here
//	HELPER_CHILD_PID_FILE   spawn a sleeping grandchild and write its pid here
//	HELPER_IGNORE_TERM      ignore SIGTERM
//	HELPER_IGNORE_PIPE      ignore SIGPIPE, as llama-server (cpp-httplib) does, so it outlives its pipes
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ENGINES_HELPER") != "1" {
		t.Skip("helper process")
	}
	runHelper()
	os.Exit(0)
}

func runHelper() {
	if os.Getenv("HELPER_SLEEP") == "1" {
		time.Sleep(time.Minute)
		return
	}
	if os.Getenv("HELPER_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	if os.Getenv("HELPER_IGNORE_PIPE") == "1" {
		signal.Ignore(syscall.SIGPIPE)
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
	// Written last: a test that sees this start counted knows the knobs above took effect.
	if f := os.Getenv("HELPER_STARTS"); f != "" {
		fh, _ := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintln(fh, os.Getpid())
		fh.Close()
	}
	fmt.Println("hello stdout")
	fmt.Fprintln(os.Stderr, "hello stderr")
	if v := os.Getenv("HELPER_EARLY_LINE"); v != "" {
		fmt.Fprintln(os.Stderr, v)
	}
	if v := os.Getenv("HELPER_ERROR_LINE"); v != "" {
		if d := os.Getenv("HELPER_ERROR_AFTER"); d != "" {
			wait, err := time.ParseDuration(d)
			if err != nil {
				panic(err)
			}
			time.Sleep(wait)
		}
		if f := os.Getenv("HELPER_ERROR_ON"); f != "" {
			for _, err := os.Stat(f); err != nil; _, err = os.Stat(f) {
				time.Sleep(10 * time.Millisecond)
			}
		}
		// A Python traceback: the root cause, then lines that push it out of the tail.
		fmt.Fprintln(os.Stderr, "(EngineCore pid=123) ERROR 10-20 04:40:09 [core.py:1510] "+v)
		for i := 0; i < 40; i++ {
			fmt.Fprintf(os.Stderr, "  File \"x.py\", line %d, in f\n", i)
		}
		fmt.Fprintln(os.Stderr, "RuntimeError: Engine core initialization failed. See root cause above.")
	}
	if v := os.Getenv("HELPER_CRASH_AFTER"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			panic(err)
		}
		time.Sleep(d)
		os.Exit(3)
	}
	// Run until stopped. Not select{}: with nothing else running, the runtime would call that
	// a deadlock and exit 2.
	for {
		time.Sleep(time.Hour)
	}
}

// hangGuard bounds every wait in these tests. It only turns a hang into a failure; nothing
// is expected to take anywhere near it, even on a loaded CI runner.
const hangGuard = 30 * time.Second

// fakeHealth is a fake engine's /health, answered in the test process.
type fakeHealth struct {
	srv    *httptest.Server
	status atomic.Int32 // HTTP status to answer
	checks atomic.Int64 // requests served
}

func newFakeHealth(t *testing.T) *fakeHealth {
	h := &fakeHealth{}
	h.status.Store(http.StatusOK)
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.checks.Add(1)
		w.WriteHeader(int(h.status.Load()))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHealth) set(ok bool) {
	if ok {
		h.status.Store(http.StatusOK)
	} else {
		h.status.Store(http.StatusServiceUnavailable)
	}
}

// awaitChecks waits until the supervisor has made n more health checks.
func (h *fakeHealth) awaitChecks(t *testing.T, n int64) {
	t.Helper()
	target := h.checks.Load() + n
	eventually(t, fmt.Sprintf("%d more health checks", n), func() bool { return h.checks.Load() >= target })
}

type fake struct {
	t      *testing.T
	dir    string
	health *fakeHealth
	starts string
}

func newFake(t *testing.T) *fake {
	t.Helper()
	dir := t.TempDir()
	return &fake{t: t, dir: dir, health: newFakeHealth(t), starts: filepath.Join(dir, "starts")}
}

// spec returns a fast-cycling spec for the fake engine with the given knobs.
func (f *fake) spec(env map[string]string) Spec {
	e := map[string]string{
		"ENGINES_HELPER": "1",
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
			URL:              f.health.srv.URL + "/health",
			Interval:         20 * time.Millisecond,
			Timeout:          hangGuard,
			FailureThreshold: 3,
		},
		StartTimeout: hangGuard,
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

// awaitStarts waits until the helper has been started (and set itself up) n times.
func (f *fake) awaitStarts(t *testing.T, n int) {
	t.Helper()
	eventually(t, fmt.Sprintf("%d starts", n), func() bool { return f.startCount() >= n })
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func start(t *testing.T, spec Spec) *Supervisor {
	t.Helper()
	return startWith(t, spec, nil)
}

// startWith starts a supervisor whose drain-grace timer is after (nil: the real one).
func startWith(t *testing.T, spec Spec, after func(time.Duration) <-chan time.Time) *Supervisor {
	t.Helper()
	s, err := New(spec, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if after != nil {
		s.after = after
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s
}

// eventually polls cond until it holds; the deadline is only a hang guard.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hangGuard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitHealthy(t *testing.T, s *Supervisor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	if err := s.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v (status %+v)", err, s.Status())
	}
}

func TestBecomesHealthyAndStops(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.health.set(false) // loading the model
	before := time.Now()
	s := start(t, f.spec(nil))
	f.health.awaitChecks(t, 2)
	if st := s.State(); st != Starting {
		t.Fatalf("state = %s, want starting while loading", st)
	}
	launched := s.Status().Started
	f.health.set(true)
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
	f.awaitStarts(t, 3)
	// The third start follows the second restart, whose cause (the crash) is recorded first.
	st := s.Status()
	if st.Restarts < 2 || !strings.Contains(st.LastError, "exit status 3") {
		t.Fatalf("status = %+v", st)
	}
}

// ErrorLine keeps the engine's own reason for an exit (the root cause of a Python traceback),
// which the last output lines alone may not show.
func TestErrorLineExplainsExit(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.health.set(false)
	spec := f.spec(map[string]string{"HELPER_CRASH_AFTER": "0s",
		"HELPER_ERROR_LINE": "ValueError: Free memory on device cuda:0 (0.96/11.61 GiB) on startup is less than desired"})
	spec.Restart.MaxRestarts = 1
	spec.ErrorLine = regexp.MustCompile(`(?:^|\] )((?:\w+\.)*\w*(?:Error|Exception): .*)`)
	s := start(t, spec)
	eventually(t, "the exit explained", func() bool {
		return strings.Contains(s.Status().LastError, "exit status 3: ValueError: Free memory on device cuda:0")
	})
	if out := s.Status().Output; slices.ContainsFunc(out, func(l string) bool { return strings.HasPrefix(l, "ValueError") }) {
		t.Fatal("the test's root cause is still in the tail: it proves nothing")
	}
}

// An engine that served, then lost its EngineCore mid-request (#582): health fails, and the
// cause names the child's exception printed since it served, not a warning from its load. A
// longer OutputTail keeps the traceback.
func TestErrorLineExplainsDeathAfterServing(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.health.set(false)
	request := filepath.Join(f.dir, "request")
	spec := f.spec(map[string]string{"HELPER_EARLY_LINE": "ModuleNotFoundError: No module named 'flashinfer_cubin'",
		"HELPER_ERROR_ON": request, "HELPER_ERROR_LINE": "RuntimeError: Could not find nvcc"})
	spec.Restart.MaxRestarts = 1
	spec.OutputTail = 100
	spec.ErrorLine = regexp.MustCompile(`(?:^|\] )((?:\w+\.)*\w*(?:Error|Exception): .*)`)
	s := start(t, spec)
	eventually(t, "the load warning", func() bool {
		return slices.ContainsFunc(s.Status().Output, func(l string) bool { return strings.HasPrefix(l, "ModuleNotFoundError") })
	})
	f.health.set(true)
	waitHealthy(t, s)
	if err := os.WriteFile(request, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the traceback", func() bool {
		return slices.Contains(s.Status().Output, "RuntimeError: Engine core initialization failed. See root cause above.")
	})
	if out := s.Status().Output; !slices.ContainsFunc(out, func(l string) bool { return strings.HasSuffix(l, "Could not find nvcc") }) {
		t.Fatalf("the tail lost the root cause: %q", out)
	}
	f.health.set(false)
	eventually(t, "the health failure explained", func() bool {
		e := s.Status().LastError
		return strings.Contains(e, "consecutive failed health checks") && strings.HasSuffix(e, ": RuntimeError: Could not find nvcc")
	})
}

// A Fatal line (the GPU is out of memory) means restarting can't help: the engine fails at
// once, with the line, and is not restarted (#562).
func TestFatalLineFailsWithoutRestart(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.health.set(false)
	spec := f.spec(map[string]string{"HELPER_CRASH_AFTER": "0s",
		"HELPER_ERROR_LINE": "ggml_backend_cuda_buffer_type_alloc_buffer: allocating 5120.00 MiB on device 0: cudaMalloc failed: out of memory"})
	spec.Restart.MaxRestarts = 5
	spec.Fatal = regexp.MustCompile(`cudaMalloc failed: out of memory`)
	s := start(t, spec)
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	if err := s.WaitHealthy(ctx); !errors.Is(err, ErrFailed) {
		t.Fatalf("WaitHealthy = %v, want ErrFailed", err)
	}
	st := s.Status()
	if st.State != Failed || st.Restarts != 0 || !strings.Contains(st.Fatal, "cudaMalloc failed: out of memory") ||
		!strings.Contains(st.LastError, "cudaMalloc failed") {
		t.Fatalf("status = %+v", st)
	}
	if n := f.startCount(); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	// Started again (Retry after freeing the card): the old line no longer counts.
	f.health.set(true)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.Fatal != "" {
		t.Fatalf("fatal kept across a start: %+v", st)
	}
}

// A Fatal line only counts at load: an engine that served, then printed one on a big request
// and crashed, is restarted (the crash freed the card), as any other crash is.
func TestFatalLineAfterHealthyRestarts(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	spec := f.spec(map[string]string{"HELPER_ERROR_AFTER": "300ms", "HELPER_CRASH_AFTER": "300ms",
		"HELPER_ERROR_LINE": "ggml_gallocr_reserve_n: failed to allocate CUDA0 buffer of size 2147483648"})
	spec.Restart.MaxRestarts = 5
	spec.Fatal = regexp.MustCompile(`failed to allocate \S+ buffer`)
	s := start(t, spec)
	waitHealthy(t, s)
	eventually(t, "a restart or a failure", func() bool { return f.startCount() >= 2 || s.State() == Failed })
	st := s.Status()
	if n := f.startCount(); n < 2 || st.Fatal != "" || st.Restarts < 1 {
		t.Fatalf("starts = %d, status = %+v; want restarted, no fatal line", n, st)
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
	f.health.set(false)                                           // never serves
	spec := f.spec(map[string]string{"HELPER_CRASH_AFTER": "0s"}) // missing GGUF: exits at once
	spec.Restart.MaxRestarts = 2
	s := start(t, spec)
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
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
	s := start(t, f.spec(nil))
	waitHealthy(t, s)
	// Healthy only means the test's own health endpoint answered; the helper may not have
	// counted its start yet. Failing health now would kill it first, and only the restart
	// would ever be counted.
	f.awaitStarts(t, 1)
	f.health.set(false)
	f.awaitStarts(t, 2)
	// The restarted engine is still loading (failing checks) and only StartTimeout, a hang
	// guard, would end that, so nothing replaces the restart's cause.
	if st := s.Status(); !strings.Contains(st.LastError, "consecutive failed health checks") {
		t.Fatalf("status = %+v", st)
	}
}

func TestStartTimeout(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.health.set(false) // never finishes loading
	spec := f.spec(nil)
	spec.StartTimeout = 200 * time.Millisecond
	spec.Restart.MaxRestarts = 1
	s := start(t, spec)
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
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
	f.awaitStarts(t, 1)
	pid := s.Status().PID
	if err := s.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitHealthy(t, s)
	f.awaitStarts(t, 2)
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
	f.awaitStarts(t, 1) // SIGTERM is ignored from here on
	// The engine ignores SIGTERM, so Stop returns only if it escalates to a kill.
	stopped := make(chan error, 1)
	go func() { stopped <- s.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(hangGuard):
		t.Fatal("Stop did not escalate to a kill")
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
	f.awaitStarts(t, 1) // the env file is written before the start is counted
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

// manualTimer stands in for the drain-grace timer: the test sees the grace requested and
// decides when it runs out.
type manualTimer struct {
	requested chan time.Duration
	fire      chan time.Time
}

func newManualTimer() *manualTimer {
	return &manualTimer{requested: make(chan time.Duration, 4), fire: make(chan time.Time, 1)}
}

// await returns the next grace the supervisor asks for, i.e. waits for it to start draining.
func (m *manualTimer) await(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-m.requested:
		return d
	case <-time.After(hangGuard):
		t.Fatal("timed out waiting for the engine to start draining")
		return 0
	}
}

func (m *manualTimer) after(d time.Duration) <-chan time.Time {
	m.requested <- d
	return m.fire
}

func TestDrainGraceDelaysHealthRestart(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	spec := f.spec(nil)
	spec.Health.DrainGrace = 400 * time.Millisecond
	grace := newManualTimer()
	s := startWith(t, spec, grace.after)
	waitHealthy(t, s)
	f.awaitStarts(t, 1)
	f.health.set(false)
	if d := grace.await(t); d != 400*time.Millisecond {
		t.Fatalf("grace = %s, want 400ms", d)
	}
	// Still failing, but inside the grace: draining, not restarted.
	f.health.awaitChecks(t, 5)
	if st := s.State(); st != Draining || f.startCount() != 1 {
		t.Fatalf("state %s after %d starts; want draining, not restarted, before the grace ran out", st, f.startCount())
	}
	f.health.set(true)
	grace.fire <- time.Now()
	f.awaitStarts(t, 2)
	waitHealthy(t, s)
	if st := s.Status(); st.Restarts != 1 || !strings.Contains(st.LastError, "consecutive failed health checks") {
		t.Fatalf("status = %+v", st)
	}
}

func TestDrainCalledOffWhenEngineRecovers(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	spec := f.spec(nil)
	spec.Health.DrainGrace = time.Hour
	grace := newManualTimer()
	s := startWith(t, spec, grace.after)
	waitHealthy(t, s)
	f.awaitStarts(t, 1)
	f.health.set(false)
	grace.await(t) // draining
	f.health.set(true)
	eventually(t, "recovered", func() bool { return s.State() == Healthy })
	// The grace running out after the recovery must not restart the engine.
	grace.fire <- time.Now()
	f.health.awaitChecks(t, 5)
	if st := s.State(); st != Healthy || f.startCount() != 1 {
		t.Fatalf("state %s after %d starts; the recovery should have called the restart off", st, f.startCount())
	}
}

// OnHealthy runs once each time a start becomes healthy (decision 2026-10-20-prompt-diet: the
// llm layer warms the engine then), not while it is still loading, and again after a restart.
func TestOnHealthyEachStart(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.health.set(false) // loading
	calls := make(chan struct{}, 4)
	spec := f.spec(nil)
	spec.OnHealthy = func() { calls <- struct{}{} }
	s := start(t, spec)
	f.health.awaitChecks(t, 2)
	select {
	case <-calls:
		t.Fatal("OnHealthy while loading")
	default:
	}
	f.health.set(true)
	waitHealthy(t, s)
	awaitCall := func(n int) {
		select {
		case <-calls:
		case <-time.After(hangGuard):
			t.Fatalf("OnHealthy call %d never came", n)
		}
	}
	awaitCall(1)
	f.awaitStarts(t, 1)
	if err := s.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitHealthy(t, s)
	awaitCall(2)
	select {
	case <-calls:
		t.Fatal("OnHealthy ran more than once per start")
	default:
	}
}
