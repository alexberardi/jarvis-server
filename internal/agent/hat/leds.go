// Package hat drives the non-audio parts of the ReSpeaker 2-mics Pi HAT v2 the Pi node runs
// on: the three APA102 LEDs on SPI and the user button on GPIO17. It ports the Python node's
// services/respeaker_led_service.py and services/button_service.py (N: 0.3.4) with their
// colours, timings and two-layer pattern model, from pure Go (CGO_ENABLED=0; see
// docs/agent/go-pi-node.md §3.4 and D5).
package hat

import (
	"cmp"
	"log/slog"
	"math"
	"sync"
	"time"
)

// NumLEDs is the HAT's APA102 count.
const NumLEDs = 3

// RenderFPS is the Python service's render rate.
const RenderFPS = 20

// Pixel is one LED: colour plus the per-pattern brightness in percent (apa102_pi's
// bright_percent), 0-100.
type Pixel struct{ R, G, B, Bright uint8 }

// Pattern names, as the Python node (and the mobile "Test LEDs" picker) spell them.
const (
	Off             = "off"
	Normal          = "normal" // idle: dim white
	Alert           = "alert"  // middle LED purple: alerts queued
	WakeDetected    = "wake_detected"
	Listening       = "listening"
	Thinking        = "thinking"
	Speaking        = "speaking"
	Error           = "error"
	NotForMe        = "not_for_me"
	Muted           = "muted"
	ShutdownWarning = "shutdown_warning"
	Setup           = "setup" // not enrolled: the setup hotspot is up (the Go node's own; #292)
)

func all(p Pixel) []Pixel { return []Pixel{p, p, p} }

var dark = Pixel{}

// patterns are respeaker_led_service.py's _PATTERNS: t is seconds since the renderer started.
var patterns = map[string]func(t float64) []Pixel{
	Off:          func(float64) []Pixel { return all(dark) },
	Normal:       func(float64) []Pixel { return all(Pixel{255, 255, 255, 15}) },
	Alert:        func(float64) []Pixel { return []Pixel{dark, {180, 0, 255, 45}, dark} },
	WakeDetected: func(float64) []Pixel { return all(Pixel{180, 0, 255, 45}) },
	Listening:    func(float64) []Pixel { return all(Pixel{0, 255, 0, 40}) },
	Thinking: func(t float64) []Pixel {
		// Pinwheel: 1, 2, 3 LEDs lit, a step every 1/3 s.
		lit := int(t*3)%NumLEDs + 1
		out := all(dark)
		for i := range lit {
			out[i] = Pixel{255, 140, 0, 35}
		}
		return out
	},
	Speaking: func(float64) []Pixel { return all(Pixel{0, 200, 220, 35}) },
	Error:    func(float64) []Pixel { return all(Pixel{255, 0, 0, 50}) },
	NotForMe: func(float64) []Pixel { return all(Pixel{255, 90, 0, 45}) },
	Muted:    func(float64) []Pixel { return all(Pixel{255, 0, 0, 30}) },
	// A slow blue breath, 3 s a cycle: no other state is blue or breathes.
	Setup: func(t float64) []Pixel {
		return all(Pixel{0, 60, 255, uint8(10 + 30*(1-math.Cos(2*math.Pi*t/3))/2)})
	},
	ShutdownWarning: func(t float64) []Pixel {
		if math.Mod(t, 0.2) < 0.1 {
			return all(Pixel{255, 0, 0, 60})
		}
		return all(dark)
	},
}

// Known reports whether name is a pattern.
func Known(name string) bool { _, ok := patterns[name]; return ok }

// Render returns pattern name's pixels at t with brightness scaled by scale percent (an unknown
// name renders off, as Python's _PATTERNS.get(pattern, _p_off)).
func Render(name string, t float64, scale int) []Pixel {
	f, ok := patterns[name]
	if !ok {
		f = patterns[Off]
	}
	px := f(t)
	for i := range px {
		px[i].Bright = uint8(max(0, min(100, int(px[i].Bright)*scale/100)))
	}
	return px
}

// globalBrightness is the Python service's APA102(global_brightness=31).
const globalBrightness = 31

// Frame encodes pixels as apa102_pi's show() sends them: a 4-byte start frame, per LED
// 0xE0|brightness then blue, green, red (order "rgb"), a 4-byte reset frame for SK9822 parts and
// (n+15)/16 trailing zero bytes. Brightness is ceil(bright_percent * 31 / 100).
func Frame(px []Pixel) []byte {
	f := make([]byte, 4, 4+4*len(px)+4+(len(px)+15)/16)
	for _, p := range px {
		br := byte(math.Ceil(float64(p.Bright) * globalBrightness / 100))
		f = append(f, 0xE0|br&0x1F, p.B, p.G, p.R)
	}
	f = append(f, 0, 0, 0, 0)
	for range (len(px) + 15) / 16 {
		f = append(f, 0)
	}
	return f
}

// Strip receives encoded frames (the SPI device, or a fake).
type Strip interface {
	Write(frame []byte) (int, error)
	Close() error
}

// output is what LEDs draws on: the HAT's APA102 chain, or the Pi's own ACT LED (act.go).
type output interface {
	// pick is the pattern shown for the stable pattern and the overlay ("" = none).
	pick(stable, transient string) string
	// encode is what to write for pattern name at t seconds, at scale percent.
	encode(name string, t float64, scale int) []byte
	// animated reports whether name changes with time (the others are drawn once).
	animated(name string) bool
	write(b []byte) error
	// close releases the output, after encode(Off) was written.
	close() error
}

// chain is the HAT's three APA102 LEDs.
type chain struct{ Strip }

func (chain) pick(stable, transient string) string { return cmp.Or(transient, stable) }
func (chain) encode(name string, t float64, scale int) []byte {
	return Frame(Render(name, t, scale))
}
func (chain) animated(name string) bool { return animated[name] }
func (c chain) write(b []byte) error    { _, err := c.Write(b); return err }
func (c chain) close() error            { return c.Close() }

// LEDs is the Python service's two-layer model: a stable pattern (idle / alert) with a
// transient command-flow overlay on top, rendered at RenderFPS on its own goroutine. Every method
// is safe to call from any goroutine and returns at once.
type LEDs struct {
	out output
	log *slog.Logger
	now func() time.Time
	// afterFunc schedules Preview's auto-clear (time.AfterFunc; tests fire it themselves).
	afterFunc func(time.Duration, func()) *time.Timer

	mu        sync.Mutex
	stable    string
	transient string // "" = none
	enabled   bool
	idleDark  bool // the idle "normal" pattern shows dark: the LEDs light only while responding
	scale     int
	timer     *time.Timer // a pending auto-clear (Preview, Flash)
	lastErr   string

	stop    chan struct{}
	done    chan struct{}
	changed chan struct{} // wakes the renderer when a static pattern is replaced
}

// animated are the patterns that change with time; the others are drawn once and the renderer
// sleeps until the next change (Python redraws at 20 Hz regardless: ~3% of a Pi 4 core).
var animated = map[string]bool{Thinking: true, ShutdownWarning: true, Setup: true}

// NewLEDs starts rendering to strip: "normal" (dim white) until told otherwise.
func NewLEDs(strip Strip, log *slog.Logger) *LEDs { return newLEDs(chain{strip}, log) }

func newLEDs(out output, log *slog.Logger) *LEDs {
	l := &LEDs{out: out, log: log, now: time.Now, afterFunc: time.AfterFunc, stable: Normal, enabled: true, scale: 100,
		stop: make(chan struct{}), done: make(chan struct{}), changed: make(chan struct{}, 1)}
	go l.loop()
	return l
}

// SetPattern sets the stable pattern.
func (l *LEDs) SetPattern(name string) {
	l.mu.Lock()
	l.stable = name
	l.mu.Unlock()
	l.poke()
}

// poke wakes the renderer (never blocks).
func (l *LEDs) poke() {
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

// SetTransient overlays a command-flow pattern; "" clears it so the stable pattern shows. Any
// call cancels a pending auto-clear (the Python fix for a preview timer clearing a real wake).
func (l *LEDs) SetTransient(name string) {
	l.mu.Lock()
	l.cancelLocked()
	l.transient = name
	l.mu.Unlock()
	l.poke()
}

// Preview shows name as the overlay for d, then clears it unless something else took over (the
// mobile "Test LEDs" picker, preview_pattern).
func (l *LEDs) Preview(name string, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cancelLocked()
	l.transient = name
	var t *time.Timer
	t = l.afterFunc(d, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.timer == t { // superseded otherwise
			l.timer = nil
			l.transient = ""
			l.poke()
		}
	})
	l.timer = t
	l.poke()
}

func (l *LEDs) cancelLocked() {
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
}

// SetEnabled turns output on or off (led_enabled); rendering continues, forced dark.
func (l *LEDs) SetEnabled(on bool) {
	l.mu.Lock()
	l.enabled = on
	l.mu.Unlock()
	l.poke()
}

// SetIdleDark keeps the idle "normal" pattern dark, so the LEDs light only while the node responds
// (wake, listening, thinking, speaking, errors) or an alert is queued (led_idle_enabled false).
func (l *LEDs) SetIdleDark(dark bool) {
	l.mu.Lock()
	l.idleDark = dark
	l.mu.Unlock()
	l.poke()
}

// SetBrightness scales every pattern's brightness, 0-100 (led_brightness_percent).
func (l *LEDs) SetBrightness(percent int) {
	l.mu.Lock()
	l.scale = max(0, min(100, percent))
	l.mu.Unlock()
	l.poke()
}

// Current is the pattern being shown.
func (l *LEDs) Current() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.transient != "" {
		return l.transient
	}
	return l.stable
}

func (l *LEDs) loop() {
	defer close(l.done)
	t0 := l.now()
	tick := time.NewTicker(time.Second / RenderFPS)
	defer tick.Stop()
	var last []byte
	for {
		l.mu.Lock()
		name, on, scale := l.out.pick(l.stable, l.transient), l.enabled, l.scale
		if name == Normal && l.idleDark {
			name = Off
		}
		l.mu.Unlock()
		if !on {
			name, scale = Off, 0
		}
		f := l.out.encode(name, l.now().Sub(t0).Seconds(), scale)
		// Python rewrites every frame; skipping unchanged ones saves the SPI write and its CPU
		// on a Zero, and steady patterns are most of the time.
		if string(f) != string(last) {
			if err := l.out.write(f); err != nil {
				if msg := err.Error(); msg != l.lastErr {
					l.lastErr = msg
					l.log.Warn("hat: LED write failed", "pattern", name, "err", err)
				}
				last = nil
			} else {
				last = f
			}
		}
		var next <-chan time.Time // nil: a static pattern waits for a change
		if l.out.animated(name) {
			next = tick.C
		}
		select {
		case <-l.stop:
			return
		case <-l.changed:
		case <-next:
		}
	}
}

// Close stops rendering, turns the LEDs off and closes the strip (the ACT LED gets its kernel
// trigger back).
func (l *LEDs) Close() error {
	select {
	case <-l.stop:
		return nil
	default:
	}
	close(l.stop)
	<-l.done
	l.mu.Lock()
	l.cancelLocked()
	l.mu.Unlock()
	_ = l.out.write(l.out.encode(Off, 0, 0))
	return l.out.close()
}
