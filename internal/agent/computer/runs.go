package computer

import (
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// runIdle is how long a run nobody touched is kept: cc's runs last at most computer.max_minutes
// (20 by default), and a run held for a confirmation keeps its window for the user's answer.
const runIdle = 6 * time.Hour

// Run is the executor's state of one computer task (cc's run id): its task window and tab, the
// tab's address as last seen, the last snapshot (an action's ref is only good for its gen) and
// whether it was stopped.
type Run struct {
	ID       string
	Tab      string // "KEY:N", the task window's tab
	Window   string // "KEY:N", the task window
	URL      string
	Snap     *computeruse.Snapshot
	Stopped  bool
	captures int
	touched  time.Time
}

// Runs keeps the executor's runs by id. A stopped run stays stopped: every later action on it
// is refused (code stopped). The zero value is ready.
type Runs struct {
	mu   sync.Mutex
	runs map[string]*Run
	now  func() time.Time
}

func (r *Runs) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// With calls f with run id's state (a new run on first use), under the lock: f must not block.
func (r *Runs) With(id string, f func(*Run)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	if r.runs == nil {
		r.runs = map[string]*Run{}
	}
	for k, run := range r.runs {
		if now.Sub(run.touched) > runIdle {
			delete(r.runs, k)
		}
	}
	run := r.runs[id]
	if run == nil {
		run = &Run{ID: id}
		r.runs[id] = run
	}
	run.touched = now
	f(run)
}

// Get is a copy of run id's state (a new, empty one when unknown).
func (r *Runs) Get(id string) Run {
	var out Run
	r.With(id, func(run *Run) { out = *run })
	return out
}

// Stop marks run id stopped: the action going on finishes, the next one is refused.
func (r *Runs) Stop(id string) {
	r.With(id, func(run *Run) { run.Stopped = true })
}

// StopAll stops every run (the tray's, the CLI's or voice's "stop": Activity.Stop).
func (r *Runs) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.runs {
		run.Stopped = true
	}
}

// Stopped reports whether run id was stopped.
func (r *Runs) Stopped(id string) bool { return r.Get(id).Stopped }

// Live reports whether any run is going on (known and not stopped).
func (r *Runs) Live() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.runs {
		if !run.Stopped {
			return true
		}
	}
	return false
}
