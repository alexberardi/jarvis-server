// Package island is the desktop agent's on-screen voice indicator: a small pill at the top
// centre of the screen (under the notch on a Mac) that drops in when the wake word is heard,
// shows sound bars that follow the user's voice and then Jarvis's, pulses while Jarvis
// thinks, and counts down the follow-up window with a ring. It never takes focus or clicks
// and has no Dock or taskbar entry.
//
// One pure-Go renderer (render.go) draws every frame into an RGBA buffer; a small platform
// backend only puts that buffer on screen: an NSPanel on macOS (purego, no cgo), a
// wlr-layer-shell overlay surface on Wayland (a minimal pure-Go client). Anything the
// platform can't do (no display, GNOME without layer-shell, X11, Windows for now) turns the
// island off with one log line; it never fails the agent.
package island

import (
	"image"
	"log/slog"
	"math"
	"sync"
	"time"
)

// State is what the island shows.
type State int

const (
	Hidden    State = iota
	Listening       // recording the user: bars follow the microphone
	Thinking        // waiting for Jarvis: a slow travelling pulse
	Speaking        // Jarvis is talking: bars follow the reply
	FollowUp        // the follow-up window is open: a countdown ring
	Error           // a short red flash, then hidden
)

func (s State) String() string {
	return [...]string{"hidden", "listening", "thinking", "speaking", "follow_up", "error"}[s]
}

const (
	frameEvery = time.Second / 30
	slideSecs  = 0.22 // in and out
	errorHold  = 1200 * time.Millisecond
)

// backend puts frames on screen. Calls come from the island's own goroutine, one at a time.
type backend interface {
	// show maps a CanvasW × CanvasH (logical) surface and returns its buffer scale.
	show() (scale float64, err error)
	// present displays img (CanvasW·scale × CanvasH·scale, premultiplied RGBA).
	present(img *image.RGBA) error
	hide()
	close()
}

// Island animates the indicator. A nil *Island is valid and shows nothing.
type Island struct {
	be  backend
	log *slog.Logger
	now func() time.Time

	mu     sync.Mutex
	m      model
	wake   chan struct{}
	done   chan struct{}
	closed bool
	off    bool // the backend failed: stay hidden for good
}

func newIsland(be backend, log *slog.Logger) *Island {
	i := &Island{be: be, log: log, now: time.Now, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go i.loop()
	return i
}

// Set shows s. window is the time left in the follow-up window (FollowUp only).
func (i *Island) Set(s State, window time.Duration) {
	if i == nil {
		return
	}
	i.mu.Lock()
	if i.off || i.closed {
		i.mu.Unlock()
		return
	}
	i.m.set(s, window, i.now())
	select { // under mu: Close can't close wake meanwhile
	case i.wake <- struct{}{}:
	default:
	}
	i.mu.Unlock()
}

// Level reports the loudness of what is heard or said: RMS amplitude, 0..1.
func (i *Island) Level(v float32) {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.m.level(float64(v))
	i.mu.Unlock()
}

// Close hides the island and stops its goroutine.
func (i *Island) Close() {
	if i == nil {
		return
	}
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return
	}
	i.closed = true
	close(i.wake)
	i.mu.Unlock()
	<-i.done
	i.be.close()
}

// loop sleeps while hidden and renders at ~30 fps while anything is on screen.
func (i *Island) loop() {
	defer close(i.done)
	var img *image.RGBA
	var scale float64
	shown := false
	for range i.wake {
		t := time.NewTicker(frameEvery)
		for {
			i.mu.Lock()
			f, visible := i.m.step(i.now())
			stop := i.closed || i.off
			i.mu.Unlock()
			if stop || !visible {
				break
			}
			if !shown {
				s, err := i.be.show()
				if err != nil {
					i.disable("show", err)
					break
				}
				shown, scale = true, s
				w, h := int(math.Round(CanvasW*s)), int(math.Round(CanvasH*s))
				if img == nil || img.Bounds().Dx() != w || img.Bounds().Dy() != h {
					img = image.NewRGBA(image.Rect(0, 0, w, h))
				}
			}
			Render(img, f, scale)
			if err := i.be.present(img); err != nil {
				i.disable("present", err)
				break
			}
			<-t.C
		}
		t.Stop()
		if shown {
			i.be.hide()
			shown = false
		}
	}
}

func (i *Island) disable(what string, err error) {
	i.mu.Lock()
	i.off = true
	i.mu.Unlock()
	i.log.Info("island: turned off", "step", what, "err", err)
}

// logOff says, once, why there is no island.
func logOff(log *slog.Logger, why any) {
	log.Info("island: no on-screen indicator", "reason", why)
}

// --- the animation model (pure, so tests drive it with a fake clock) ---

type model struct {
	state  State
	since  time.Time
	window time.Duration
	last   time.Time // the previous step
	t      float64   // seconds of animation, for the oscillations

	appear float64 // 0..1, linear; Render eases it
	seen   State   // the last state shown, for the colours while sliding out
	loud   float64 // the newest level, decaying
	bars   [nBars]float64
}

func (m *model) set(s State, window time.Duration, now time.Time) {
	if s == m.state && s != FollowUp {
		return
	}
	m.state, m.since, m.window = s, now, window
}

func (m *model) level(v float64) { m.loud = math.Max(m.loud*0.5, v) }

// shown is the state on screen once an error flash has run its course.
func (m *model) shown(now time.Time) State {
	if m.state == Error && now.Sub(m.since) > errorHold {
		return Hidden
	}
	return m.state
}

// step advances the animation to now. visible is false once the island has slid away.
func (m *model) step(now time.Time) (Frame, bool) {
	dt := 0.0
	if !m.last.IsZero() {
		dt = math.Min(0.1, now.Sub(m.last).Seconds())
	}
	m.last = now
	m.t += dt
	s := m.shown(now)

	if s == Hidden {
		m.appear = math.Max(0, m.appear-dt/slideSecs)
	} else {
		m.appear = math.Min(1, m.appear+dt/slideSecs)
	}
	if m.appear == 0 && s == Hidden {
		m.last = time.Time{}
		m.bars = [nBars]float64{}
		return Frame{}, false
	}

	m.loud *= math.Exp(-dt * 5)
	n := loudness(m.loud)
	shape := [nBars]float64{0.55, 0.85, 1, 0.8, 0.6}
	for i := range m.bars {
		var target float64
		switch s {
		case Listening, Speaking:
			wob := 0.75 + 0.25*math.Sin(m.t*(7+1.3*float64(i))+float64(i)*1.7)
			target = math.Max(0.06, n*shape[i]*wob)
		case Thinking:
			target = 0.18 + 0.22*(0.5+0.5*math.Sin(2*math.Pi*0.9*m.t-float64(i)*0.7))
		case FollowUp:
			target = 0.1 + 0.04*math.Sin(2*math.Pi*0.5*m.t)
		default:
			target = 0.08
		}
		k := 8.0
		if target > m.bars[i] {
			k = 18
		}
		m.bars[i] += (target - m.bars[i]) * (1 - math.Exp(-dt*k))
	}

	if s != Hidden {
		m.seen = s
	}
	f := Frame{State: m.seen, Appear: m.appear, Bars: m.bars}
	switch s {
	case Listening, Speaking:
		f.Glow = n
	case FollowUp:
		if m.window > 0 {
			f.Ring = clamp01(1 - now.Sub(m.since).Seconds()/m.window.Seconds())
		}
	}
	return f, true
}

// loudness maps an RMS amplitude onto 0..1 on a dB scale: -50 dBFS (a quiet room) is 0,
// -12 dBFS (loud speech) is 1.
func loudness(rms float64) float64 {
	if rms <= 0 {
		return 0
	}
	return clamp01((20*math.Log10(rms) + 50) / 38)
}
