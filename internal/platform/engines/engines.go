// Package engines supervises inference-engine subprocesses: llama-server, whisper-server and
// the like (PLAN §3.3).
//
// A Supervisor runs one Spec as a child process. It sets the GPU environment, waits for the
// engine's health endpoint, restarts it with exponential backoff when it exits or keeps failing
// health checks, and gives up (state Failed) after too many restarts in a window. A missing
// GGUF makes llama-server exit at once, so a crash loop must end rather than spin forever.
// Stop and Restart support GPU hand-off (unload, then reload). A Manager holds the named
// supervisors and reports their status for `jarvisd engines` and doctor.
//
// Engine output is logged line by line at debug level, and the last lines are kept in Status so
// doctor can show why an engine died.
//
// This package only runs engines that are already on disk. Downloading and version-pinning
// them per GPU flavour is a separate step (PLAN Phase 3).
package engines

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// State is a supervised engine's lifecycle state.
type State string

const (
	Starting   State = "starting"   // process launched, not yet healthy
	Healthy    State = "healthy"    // passing health checks (or running, with no check)
	Unhealthy  State = "unhealthy"  // was healthy, now failing checks below the threshold
	Draining   State = "draining"   // failed the threshold; restarting after DrainGrace unless it recovers
	Restarting State = "restarting" // down, waiting out the backoff
	Stopped    State = "stopped"    // not running, by request (or never started)
	Failed     State = "failed"     // gave up: too many restarts, or it could not be launched
)

// ErrStopped is returned by WaitHealthy when the engine is stopped.
var ErrStopped = errors.New("engine stopped")

// ErrFailed is returned by WaitHealthy when the supervisor gave up on the engine.
var ErrFailed = errors.New("engine failed")

// GPUBackend names a GPU runtime, which decides the device-visibility variable.
type GPUBackend string

const (
	CUDA   GPUBackend = "cuda"   // CUDA_VISIBLE_DEVICES
	ROCm   GPUBackend = "rocm"   // HIP_VISIBLE_DEVICES (not ROCR, which would remap twice)
	Vulkan GPUBackend = "vulkan" // GGML_VK_VISIBLE_DEVICES
	Metal  GPUBackend = "metal"  // no visibility variable
)

// GPU pins an engine to devices. The zero value leaves the environment alone.
type GPU struct {
	Backend GPUBackend
	// VisibleDevices is the backend's device list, e.g. "0" or "0,1". Empty sets nothing.
	VisibleDevices string
}

// envVar returns the visibility variable this GPU sets, if any.
func (g GPU) envVar() (key, value string, ok bool) {
	if g.VisibleDevices == "" {
		return "", "", false
	}
	switch g.Backend {
	case CUDA:
		return "CUDA_VISIBLE_DEVICES", g.VisibleDevices, true
	case ROCm:
		return "HIP_VISIBLE_DEVICES", g.VisibleDevices, true
	case Vulkan:
		return "GGML_VK_VISIBLE_DEVICES", g.VisibleDevices, true
	}
	return "", "", false
}

// HealthCheck polls an HTTP endpoint and expects a 2xx. llama-server's /health answers 503
// while the model loads, so failures before the first success only count against StartTimeout.
type HealthCheck struct {
	URL              string
	Interval         time.Duration // default 5s
	Timeout          time.Duration // per request; default 2s
	FailureThreshold int           // consecutive failures, once healthy, that force a restart; default 3
	// DrainGrace delays a health-failure restart so requests already in flight can finish.
	// The engine is marked Draining, so routers stop sending it new work; if it passes a
	// health check during the grace, the restart is called off. 0 restarts at once.
	DrainGrace time.Duration
}

// RestartPolicy bounds restarts. The delay before restart n within Window is
// BackoffMin·2^(n-1), capped at BackoffMax.
type RestartPolicy struct {
	BackoffMin  time.Duration // default 1s
	BackoffMax  time.Duration // default 1m
	MaxRestarts int           // within Window before giving up; default 5, negative = never give up
	Window      time.Duration // default 10m
}

// Spec describes one engine.
type Spec struct {
	Name string
	Path string // binary
	Args []string
	Env  map[string]string // merged onto os.Environ; wins over everything
	Dir  string
	GPU  GPU
	// Health is optional. Without it the engine counts as healthy as soon as it is running.
	Health *HealthCheck
	// StartTimeout is how long a fresh start may take to pass its first health check.
	// Default 3m: a big GGUF can take minutes to load.
	StartTimeout time.Duration
	// StopTimeout is the grace between SIGTERM and SIGKILL. Default 10s. On Windows the
	// process tree is killed at once.
	StopTimeout time.Duration
	Restart     RestartPolicy
}

func (s Spec) withDefaults() Spec {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	if s.Health != nil {
		h := *s.Health
		def(&h.Interval, 5*time.Second)
		def(&h.Timeout, 2*time.Second)
		if h.FailureThreshold <= 0 {
			h.FailureThreshold = 3
		}
		s.Health = &h
	}
	def(&s.StartTimeout, 3*time.Minute)
	def(&s.StopTimeout, 10*time.Second)
	def(&s.Restart.BackoffMin, time.Second)
	def(&s.Restart.BackoffMax, time.Minute)
	def(&s.Restart.Window, 10*time.Minute)
	if s.Restart.MaxRestarts == 0 {
		s.Restart.MaxRestarts = 5
	}
	return s
}

func (s Spec) validate() error {
	switch {
	case s.Name == "":
		return errors.New("engines: spec has no name")
	case s.Path == "":
		return fmt.Errorf("engines: %s: spec has no binary path", s.Name)
	case s.Health != nil && s.Health.URL == "":
		return fmt.Errorf("engines: %s: health check has no URL", s.Name)
	}
	return nil
}

// environ builds the child's environment: base, then the GPU variable, then Env. An operator
// who already set the GPU variable in base keeps it, as gpu_select.py does.
func (s Spec) environ(base []string) []string {
	norm := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	env := slices.Clone(base)
	idx := make(map[string]int, len(env))
	for i, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		idx[norm(k)] = i
	}
	set := func(k, v string) {
		if i, ok := idx[norm(k)]; ok {
			env[i] = k + "=" + v
			return
		}
		idx[norm(k)] = len(env)
		env = append(env, k+"="+v)
	}
	if k, v, ok := s.GPU.envVar(); ok {
		var cur string
		if i, ok := idx[norm(k)]; ok {
			_, cur, _ = strings.Cut(env[i], "=")
		}
		if cur == "" {
			set(k, v)
		}
	}
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		set(k, s.Env[k])
	}
	return env
}

// backoff is the delay before restart n (1-based) within the window.
func (p RestartPolicy) backoff(n int) time.Duration {
	d := p.BackoffMin
	for i := 1; i < n && d < p.BackoffMax; i++ {
		d *= 2
	}
	return min(d, p.BackoffMax)
}

// FreePort returns a TCP port on 127.0.0.1 that was free a moment ago, for engines that
// need one. Another process can take it before the engine binds; restarts reuse it.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Status is a snapshot of a supervised engine.
type Status struct {
	Name      string
	State     State
	PID       int       // 0 when not running
	Restarts  int       // since the last Start
	Since     time.Time // when State was entered
	Started   time.Time // when the running process was launched; zero when none
	LastError string    // why it last went down or unhealthy
	Output    []string  // last lines of stdout and stderr
}

const outputTail = 20

// Supervisor runs one engine. Its methods are safe for concurrent use.
type Supervisor struct {
	spec   Spec
	log    *slog.Logger
	client *http.Client

	mu       sync.Mutex
	state    State
	since    time.Time
	changed  chan struct{} // closed and replaced on every state change
	pid      int
	started  time.Time
	restarts int
	lastErr  string
	tail     []string
	cur      *run // nil when the loop is not running
}

// run is one Start→Stop lifetime of the supervision loop.
type run struct {
	stop     chan struct{} // closed by Stop
	force    chan struct{} // closed when Stop's ctx expires: skip the grace period
	done     chan struct{} // closed when the loop exits
	stopping bool
}

// New creates a stopped supervisor. Call Start to launch the engine.
func New(spec Spec, log *slog.Logger) (*Supervisor, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	spec = spec.withDefaults()
	return &Supervisor{
		spec: spec,
		log:  log.With("engine", spec.Name),
		// No keep-alives: a pooled connection to a dead engine would fail one check spuriously.
		client:  &http.Client{Transport: &http.Transport{DisableKeepAlives: true}},
		state:   Stopped,
		since:   time.Now(),
		changed: make(chan struct{}),
	}, nil
}

// Name returns the engine's name.
func (s *Supervisor) Name() string { return s.spec.Name }

// State returns the current state.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Status returns a snapshot.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		Name: s.spec.Name, State: s.state, PID: s.pid, Restarts: s.restarts,
		Since: s.since, LastError: s.lastErr, Output: slices.Clone(s.tail),
	}
	if s.pid != 0 {
		st.Started = s.started
	}
	return st
}

// setLocked changes state; s.mu must be held.
func (s *Supervisor) setLocked(st State, lastErr string) {
	if lastErr != "" {
		s.lastErr = lastErr
	}
	if st == s.state {
		return
	}
	s.state, s.since = st, time.Now()
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Supervisor) set(st State, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setLocked(st, lastErr)
}

// Start launches the engine and returns once the process is running; use WaitHealthy to
// wait for it to serve. A launch failure (e.g. a missing binary) is returned and the state
// becomes Failed. Starting a running supervisor is an error.
func (s *Supervisor) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		return fmt.Errorf("engines: %s already running", s.spec.Name)
	}
	p, err := s.spawn()
	if err != nil {
		s.setLocked(Failed, err.Error())
		return err
	}
	r := &run{stop: make(chan struct{}), force: make(chan struct{}), done: make(chan struct{})}
	s.cur, s.pid, s.started, s.restarts, s.lastErr = r, p.cmd.Process.Pid, time.Now(), 0, ""
	s.setLocked(Starting, "")
	if s.spec.Health == nil {
		s.setLocked(Healthy, "")
	}
	go s.loop(r, p)
	return nil
}

// Stop stops the engine and waits for it to exit: SIGTERM, then SIGKILL after StopTimeout
// (or as soon as ctx is done). Its whole process group goes with it on unix.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	r := s.cur
	if r == nil {
		s.mu.Unlock()
		return nil
	}
	if !r.stopping {
		r.stopping = true
		close(r.stop)
	}
	s.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
	}
	s.mu.Lock()
	select {
	case <-r.force:
	default:
		close(r.force)
	}
	s.mu.Unlock()
	<-r.done
	return ctx.Err()
}

// Restart stops the engine and starts it again at once, with a fresh restart budget. It
// returns once the new process is running. This is the GPU hand-off reload.
func (s *Supervisor) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start()
}

// WaitHealthy blocks until the engine is healthy, it stops or fails, or ctx is done.
func (s *Supervisor) WaitHealthy(ctx context.Context) error {
	for {
		s.mu.Lock()
		st, ch, lastErr := s.state, s.changed, s.lastErr
		s.mu.Unlock()
		switch st {
		case Healthy:
			return nil
		case Stopped:
			return fmt.Errorf("engines: %s: %w", s.spec.Name, ErrStopped)
		case Failed:
			return fmt.Errorf("engines: %s: %w: %s", s.spec.Name, ErrFailed, lastErr)
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// loop supervises p and its successors until stopped or out of restarts.
func (s *Supervisor) loop(r *run, p *proc) {
	defer close(r.done)
	finish := func(st State, lastErr string) {
		s.mu.Lock()
		s.cur, s.pid = nil, 0
		s.setLocked(st, lastErr)
		s.mu.Unlock()
	}
	pol := s.spec.Restart
	var recent []time.Time // restart times within the window
	var cause error
	for {
		if p != nil {
			stop, c := s.watch(r, p)
			s.terminate(r, p)
			if stop {
				s.log.Info("engine stopped")
				finish(Stopped, "")
				return
			}
			cause = c
		}
		s.log.Warn("engine down", "cause", cause)

		now := time.Now()
		recent = slices.DeleteFunc(recent, func(t time.Time) bool { return now.Sub(t) > pol.Window })
		if pol.MaxRestarts >= 0 && len(recent) >= pol.MaxRestarts {
			s.log.Error("engine failed: too many restarts", "restarts", len(recent), "window", pol.Window, "cause", cause)
			finish(Failed, cause.Error())
			return
		}
		recent = append(recent, now)
		delay := pol.backoff(len(recent))
		s.mu.Lock()
		s.restarts++
		s.pid = 0
		s.setLocked(Restarting, cause.Error())
		s.mu.Unlock()

		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-r.stop:
			t.Stop()
			finish(Stopped, "")
			return
		}

		s.mu.Lock()
		np, err := s.spawn()
		if err != nil {
			p, cause = nil, err
		} else {
			p = np
			s.pid, s.started = p.cmd.Process.Pid, time.Now()
			s.setLocked(Starting, "")
			if s.spec.Health == nil {
				s.setLocked(Healthy, "")
			}
		}
		s.mu.Unlock()
	}
}

// watch returns when p must go: stop requested, process exited, or health failed.
func (s *Supervisor) watch(r *run, p *proc) (stop bool, cause error) {
	hc := s.spec.Health
	if hc == nil {
		select {
		case <-p.exited:
			return false, p.exitErr()
		case <-r.stop:
			return true, nil
		}
	}
	startup := time.NewTimer(s.spec.StartTimeout)
	defer startup.Stop()
	next := time.NewTimer(0)
	defer next.Stop()
	healthy, fails := false, 0
	var drain <-chan time.Time // non-nil while draining
	var drainCause error
	for {
		select {
		case <-p.exited:
			return false, p.exitErr()
		case <-r.stop:
			return true, nil
		case <-startup.C:
			return false, fmt.Errorf("not healthy within %s", s.spec.StartTimeout)
		case <-drain:
			return false, drainCause
		case <-next.C:
		}
		err := s.check()
		next.Reset(hc.Interval)
		switch {
		case err == nil && drain != nil:
			s.log.Info("engine recovered while draining; restart called off")
			drain, fails = nil, 0
			s.set(Healthy, "")
		case drain != nil:
			// Still failing; the grace timer decides.
		case err == nil:
			fails = 0
			if !healthy {
				healthy = true
				startup.Stop()
				s.log.Info("engine healthy", "pid", p.cmd.Process.Pid)
			}
			s.set(Healthy, "")
		case !healthy:
			// Still loading; StartTimeout decides.
		default:
			fails++
			s.log.Warn("engine health check failed", "err", err, "consecutive", fails)
			if fails >= hc.FailureThreshold {
				cause := fmt.Errorf("%d consecutive failed health checks: %w", fails, err)
				if hc.DrainGrace <= 0 {
					return false, cause
				}
				s.log.Warn("engine draining before restart", "grace", hc.DrainGrace)
				s.set(Draining, cause.Error())
				drain, drainCause = time.After(hc.DrainGrace), cause
				continue
			}
			s.set(Unhealthy, err.Error())
		}
	}
}

func (s *Supervisor) check() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.spec.Health.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.spec.Health.URL, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("health: %s", resp.Status)
	}
	return nil
}

// terminate ends p (gracefully if it is still running) and reaps anything left in its group.
func (s *Supervisor) terminate(r *run, p *proc) {
	select {
	case <-p.exited:
	default:
		if err := interrupt(p.cmd.Process); err == nil {
			t := time.NewTimer(s.spec.StopTimeout)
			select {
			case <-p.exited:
			case <-t.C:
				s.log.Warn("engine ignored SIGTERM; killing", "after", s.spec.StopTimeout)
			case <-r.force:
			}
			t.Stop()
		}
		select {
		case <-p.exited:
		default:
			killTree(p.cmd.Process)
			<-p.exited
		}
	}
	killLeftovers(p.cmd.Process.Pid)
}

// proc is one launched engine process.
type proc struct {
	cmd    *exec.Cmd
	exited chan struct{} // closed once Wait returns; err is then set
	err    error
}

func (p *proc) exitErr() error {
	if p.err == nil {
		return errors.New("exited: status 0")
	}
	return fmt.Errorf("exited: %w", p.err)
}

// spawn launches the engine. Callers hold s.mu; the output goroutines take it per line.
func (s *Supervisor) spawn() (*proc, error) {
	cmd := exec.Command(s.spec.Path, s.spec.Args...)
	cmd.Dir = s.spec.Dir
	cmd.Env = s.spec.environ(os.Environ())
	setProcAttrs(cmd)
	stdout, stderr := &lineWriter{s: s, stream: "stdout"}, &lineWriter{s: s, stream: "stderr"}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// A grandchild holding the pipes open must not block Wait forever.
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("engines: start %s: %w", s.spec.Name, err)
	}
	if err := contain(cmd.Process); err != nil {
		s.log.Warn("engine not tied to jarvisd's lifetime; it may outlive a crash", "err", err)
	}
	s.log.Info("engine started", "pid", cmd.Process.Pid, "path", s.spec.Path)
	p := &proc{cmd: cmd, exited: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		stdout.flush()
		stderr.flush()
		close(p.exited)
	}()
	return p, nil
}

// lineWriter logs an engine's output stream line by line and keeps the tail for Status.
type lineWriter struct {
	s      *Supervisor
	stream string
	mu     sync.Mutex
	buf    []byte
}

const maxLine = 64 << 10

func (w *lineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLine {
		w.emit(w.buf)
		w.buf = nil
	}
	w.buf = append([]byte(nil), w.buf...) // don't pin the consumed prefix
	return len(b), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
}

func (w *lineWriter) emit(line []byte) {
	l := string(bytes.TrimRight(line, "\r"))
	w.s.log.Debug("engine output", "stream", w.stream, "line", l)
	w.s.mu.Lock()
	w.s.tail = append(w.s.tail, l)
	if len(w.s.tail) > outputTail {
		w.s.tail = slices.Delete(w.s.tail, 0, len(w.s.tail)-outputTail)
	}
	w.s.mu.Unlock()
}
