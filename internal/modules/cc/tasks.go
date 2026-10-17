package cc

import "sync"

// taskGroup tracks the module's fire-and-forget goroutines (trace writes, inbox pushes, wake
// verification…) so shutdown can wait for them before the DB closes (module.Waiter). Unlike a
// bare WaitGroup, Go never races Wait: a running task may still start another while the group
// drains, but once it has drained, Go refuses. The zero value is ready to use.
type taskGroup struct {
	mu      sync.Mutex
	idle    *sync.Cond // signalled when n drops to 0
	n       int
	closing bool
}

// Go runs fn on its own goroutine and reports true, or reports false without running it when
// the group has already drained.
func (g *taskGroup) Go(fn func()) bool {
	g.mu.Lock()
	if g.closing && g.n == 0 {
		g.mu.Unlock()
		return false
	}
	g.n++
	g.mu.Unlock()
	go func() {
		defer g.done()
		fn()
	}()
	return true
}

func (g *taskGroup) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n--; g.n == 0 && g.idle != nil {
		g.idle.Broadcast()
	}
}

// Wait stops the group taking new work once it is idle and blocks until every task (including
// those started by running tasks meanwhile) has returned. It may be called more than once.
func (g *taskGroup) Wait() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closing = true
	g.settleLocked()
}

// settle blocks until no task is running, without closing the group (tests).
func (g *taskGroup) settle() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.settleLocked()
}

func (g *taskGroup) settleLocked() {
	if g.idle == nil {
		g.idle = sync.NewCond(&g.mu)
	}
	for g.n > 0 {
		g.idle.Wait()
	}
}

// background runs fn on the module's task group; once the group has drained (shutdown) it
// drops fn, logs what was dropped and reports false.
func (m *Module) background(what string, fn func()) bool {
	if m.tasks.Go(fn) {
		return true
	}
	m.deps.Log.Debug("cc: background task dropped at shutdown", "task", what)
	return false
}

// Wait is module.Waiter: the runner calls it once the run context is cancelled and the
// listeners are shut down. Live calls and plans finish first (a call ending at shutdown may
// still resume its errand on the task group), then the task group drains.
func (m *Module) Wait() {
	if m.phone != nil {
		m.phone.Wait()
	}
	m.tasks.Wait()
}
