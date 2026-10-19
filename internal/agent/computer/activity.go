// Package computer is the agent's side of computer tasks: Jarvis using this computer's mouse,
// keyboard and screen for an errand, or watching the user to learn one (docs/agent/
// COMPUTER_CONTROL.md).
package computer

import (
	"maps"
	"slices"
	"sync"
)

// Kind is what Jarvis is doing with the computer.
type Kind string

const (
	Idle        Kind = ""
	Controlling Kind = "controlling" // driving the mouse and keyboard
	Paused      Kind = "paused"      // a task is paused (the user took over)
	Recording   Kind = "recording"   // watching the user, to learn a workflow
)

// Activity is what Jarvis is doing with the computer, and what shows it: while it controls the
// computer or records, the island and the status icon always say so, and a stop from the tray,
// the CLI or the control socket ends it. The executor and the recorder call Set; their runs
// register with OnStop. The zero value is ready; a nil *Activity does nothing.
type Activity struct {
	mu    sync.Mutex
	kind  Kind
	text  string
	shows []func(Kind, string)
	stops map[int]func()
	next  int

	// showMu orders the indicators' updates: each one shows the state as it is when it runs,
	// so the last update always shows the latest state.
	showMu sync.Mutex
}

// Show adds an indicator: f is called now with what is going on, then on every change. It
// must not block for long.
func (a *Activity) Show(f func(k Kind, text string)) {
	a.mu.Lock()
	a.shows = append(a.shows, f)
	a.mu.Unlock()
	a.showMu.Lock()
	defer a.showMu.Unlock()
	k, text := a.Status()
	f(k, text)
}

// Set says what Jarvis is doing: k with text, the task's intent ("Ordering the blue mug").
// Idle clears it.
func (a *Activity) Set(k Kind, text string) {
	if a == nil {
		return
	}
	if k == Idle {
		text = ""
	}
	a.mu.Lock()
	a.kind, a.text = k, text
	a.mu.Unlock()
	a.notify()
}

// Pause shows a task going on as paused, keeping its intent. Nothing going on: nothing.
func (a *Activity) Pause() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.kind == Idle {
		a.mu.Unlock()
		return
	}
	a.kind = Paused
	a.mu.Unlock()
	a.notify()
}

// Status is what Jarvis is doing and the task's intent.
func (a *Activity) Status() (Kind, string) {
	if a == nil {
		return Idle, ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.kind, a.text
}

// String is the status as the control socket's computer-status answers it: "idle",
// "controlling", or "controlling: Ordering the blue mug".
func (a *Activity) String() string {
	k, text := a.Status()
	switch {
	case k == Idle:
		return "idle"
	case text == "":
		return string(k)
	}
	return string(k) + ": " + text
}

// OnStop registers what Stop ends (an executor's or recorder's Runs.Stop); remove
// unregisters it.
func (a *Activity) OnStop(f func()) (remove func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stops == nil {
		a.stops = map[int]func(){}
	}
	id := a.next
	a.next++
	a.stops[id] = f
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.stops, id)
	}
}

// Stop ends what Jarvis is doing with the computer: every registered run is stopped (also
// when nothing shows yet: a run may be starting) and the indicators clear. It reports whether
// anything was going on.
func (a *Activity) Stop() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	was := a.kind != Idle
	a.kind, a.text = Idle, ""
	var stops []func() // in the order they registered
	for _, id := range slices.Sorted(maps.Keys(a.stops)) {
		stops = append(stops, a.stops[id])
	}
	a.mu.Unlock()
	a.notify()
	for _, f := range stops {
		f()
	}
	return was
}

// notify shows the current state on every indicator.
func (a *Activity) notify() {
	a.showMu.Lock()
	defer a.showMu.Unlock()
	a.mu.Lock()
	k, text, shows := a.kind, a.text, a.shows
	a.mu.Unlock()
	for _, f := range shows {
		f(k, text)
	}
}
