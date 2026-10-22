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
// whether it was stopped. A desktop task also has its app, the captures its actions' points are
// on and the apps it has used.
type Run struct {
	ID      string
	Tab     string // "KEY:N", the task window's tab
	Window  string // "KEY:N", the task window; a desktop task's window id
	URL     string
	Snap    *computeruse.Snapshot
	Opening bool // its task window was just opened and its page not snapshotted yet
	Stopped bool
	App     string // desktop: the task's app, as its window names it ("Notes")
	PID     int    // desktop: its process

	captures int
	touched  time.Time
	attended bool                            // desktop: started with the user there; the guard watches it
	shots    map[string]computeruse.Geometry // desktop: the latest captures by id
	order    []string                        // their ids, oldest first
	apps     map[string]bool                 // desktop: the apps it has used, by appKeyOf
}

// keepCapture remembers capture id's geometry (the last maxCaptures).
func (r *Run) keepCapture(id string, g computeruse.Geometry) {
	if r.shots == nil {
		r.shots = map[string]computeruse.Geometry{}
	}
	r.shots[id] = g
	r.order = append(r.order, id)
	if len(r.order) > maxCaptures {
		delete(r.shots, r.order[0])
		r.order = r.order[1:]
	}
}

// capture is capture id's geometry; "" is the latest capture.
func (r Run) capture(id string) (computeruse.Geometry, bool) {
	if id == "" && len(r.order) > 0 {
		id = r.order[len(r.order)-1]
	}
	g, ok := r.shots[id]
	return g, ok
}

// knowApp notes an app the task uses.
func (r *Run) knowApp(name string) {
	if k := appKeyOf(name); k != "" {
		if r.apps == nil {
			r.apps = map[string]bool{}
		}
		r.apps[k] = true
	}
}

// knows reports whether the task has used the app named names before.
func (r *Run) knows(names []string) bool {
	for _, n := range names {
		if r.apps[appKeyOf(n)] {
			return true
		}
	}
	return false
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

// forEach calls f with every run, under the lock.
func (r *Runs) forEach(f func(*Run)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.runs {
		f(run)
	}
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
