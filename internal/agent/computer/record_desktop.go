package computer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
)

// Desktop recording (plan P3 piece 36): the platform's listen-only tap (record_darwin.go;
// record_x11.go and record_wayland.go on Linux) hands over the user's raw clicks, keystrokes
// and scrolls; DesktopSource turns them into the browser
// trace's events (kind click, input, key, scroll; element role and name) plus the desktop's
// app (bundle id) and the element's AX path, with a keyframe of the screen at most every
// KeyframeEvery. Keystrokes become one input event per focused element. A keystroke typed while
// secure input is on, or into a secure text field, never leaves the tap: the input event says
// masked instead.

// KeyframeEvery is the shortest gap between two keyframes of a desktop recording (unless the
// platform says otherwise: DeskPlatform.KeyframeEvery).
const KeyframeEvery = 2 * time.Second

const (
	deskQueue     = 512         // raw events waiting for the worker; more are dropped
	deskScrollGap = time.Second // scrolls closer than this are one scroll event
)

var deskWorkerAwait = 5 * time.Second // how long Stop waits for the queue to drain (tests shorten it)

// Raw input kinds (DeskInput.Kind).
const (
	InClick  = "click"
	InKey    = "key"
	InScroll = "scroll"
)

// Named keys (DeskInput.Key). Enter, Tab and Escape are key events as in the browser's trace.
const (
	KeyEnter     = "Enter"
	KeyTab       = "Tab"
	KeyEscape    = "Escape"
	KeyBackspace = "Backspace"
)

// DeskInput is one raw user input event, as the platform's tap saw it.
type DeskInput struct {
	Kind   string
	At     time.Time
	X, Y   float64 // click, scroll: the global point
	Button string  // click: "left" or "right"
	// Key: the characters typed (never set when Secure), or a named key (KeyEnter…) or a
	// shortcut ("cmd+n") in Key.
	Text   string
	Key    string
	Secure bool // key: secure event input was on
	DX, DY int  // scroll: lines
	// NoPoint: a click or scroll whose position isn't known (sway has no IPC for the cursor).
	NoPoint bool
}

// DeskPlatform is what a desktop recording asks the OS (ax on macOS).
type DeskPlatform struct {
	ElementAt func(x, y float64) (*ax.Recorded, error)
	Focused   func() (*ax.Recorded, error)
	// Keyframe is a JPEG of the screen; nil: no keyframes.
	Keyframe func(ctx context.Context) ([]byte, error)
	// Frame, when set, takes the keyframes instead (Wayland): the screen with pt marked (a
	// click's point; nil: the cursor's, if known), where pt is on it, and a crop around it.
	Frame func(ctx context.Context, pt *Point) (Frame, error)
	// KeyframeEvery is the shortest gap between keyframes (0: KeyframeEvery).
	KeyframeEvery time.Duration
	// Window is the focused window, kept on each event (nil: none kept).
	Window func() *EventWindow
	// Context is asked at each start: "" when elements can be looked up, "none" when they
	// can't (no accessibility bus): every event then says so, nothing can be masked, and the
	// start warns that typed text is kept (Warning).
	Context func() string
}

// ContextNone: the recording has no accessibility context (DesktopEvent.Context).
const ContextNone = "none"

// Point is a global point in layout (logical) coordinates.
type Point struct{ X, Y float64 }

// Frame is a keyframe from DeskPlatform.Frame: the JPEG, and when the point is on it, the
// point in the JPEG's pixels and a JPEG of the 256 px around it.
type Frame struct {
	JPEG  []byte
	Point *KeyframePoint
	Crop  []byte
}

// KeyframePoint is where an event's point (the click, else the cursor) is on its keyframe.
type KeyframePoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// EventWindow is the focused window an event happened in (Wayland: the compositor's).
type EventWindow struct {
	Class string `json:"class,omitempty"`
	Title string `json:"title,omitempty"`
	X     int    `json:"x,omitempty"`
	Y     int    `json:"y,omitempty"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
}

// DesktopEvent is one event of a desktop trace: a browser.RecordEvent's fields, and the app and
// the element's AX path instead of the URL and selector.
type DesktopEvent struct {
	RecordingID string          `json:"recording_id"`
	Seq         int64           `json:"seq"`
	TS          int64           `json:"ts"`   // ms since the epoch
	Kind        string          `json:"kind"` // click, input, key, scroll
	App         string          `json:"app,omitempty"`
	Key         string          `json:"key,omitempty"`
	Element     *DesktopElement `json:"element,omitempty"`
	Value       string          `json:"value,omitempty"`
	Masked      bool            `json:"masked,omitempty"`
	Button      string          `json:"button,omitempty"` // click: right for a right click
	X           int             `json:"x,omitempty"`      // click, scroll: global point
	Y           int             `json:"y,omitempty"`
	DX          int             `json:"dx,omitempty"` // scroll: lines
	DY          int             `json:"dy,omitempty"`
	Window      *EventWindow    `json:"window,omitempty"`
	// Context is "none" when the recording has no accessibility context (no element, no mask).
	Context string `json:"context,omitempty"`
	// KeyframePoint: the event has a keyframe with its point marked, here.
	KeyframePoint *KeyframePoint `json:"keyframe_point,omitempty"`
}

// DesktopElement is the element an event happened on.
type DesktopElement struct {
	Role   string `json:"role,omitempty"`
	Name   string `json:"name,omitempty"`
	AXPath string `json:"ax_path,omitempty"`
}

// DeskTap starts the platform's listen-only tap, which calls send with each raw event from
// then on (on any goroutine, one at a time). Once stop returns, send is not called again.
type DeskTap func(send func(DeskInput)) (stop func(), err error)

// DesktopSource is the recorder's desktop mode.
type DesktopSource struct {
	Dir      string // the recordings directory: keyframes are <id>.<seq>.jpg there
	Tap      DeskTap
	Platform DeskPlatform
	Log      *slog.Logger

	now func() time.Time // tests replace it

	mu      sync.Mutex
	cur     *deskSession
	done    map[string][]Keyframe // stopped recordings' keyframes, until Keyframes takes them
	warning string                // the last start's (Warning)
}

type deskSession struct {
	id      string
	rec     *deskRecorder
	ch      chan DeskInput
	stopTap func()
	worker  chan struct{}
	pending atomic.Int64 // inputs queued or being handled
}

// noContextWarning is the start's warning when there is no accessibility context.
const noContextWarning = "I can't see which field you're typing in (the accessibility bus isn't running), so " +
	"typed text is kept as it is, passwords too: don't type secrets while I'm recording."

// Warning is what the last start wants the user to know ("" for nothing).
func (s *DesktopSource) Warning() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warning
}

// settled reports whether the recording going on has handled every input it got (tests).
func (s *DesktopSource) settled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur == nil || s.cur.pending.Load() == 0
}

func (s *DesktopSource) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

func (s *DesktopSource) Start(_ context.Context, id string, emit func(json.RawMessage)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		return ErrBusy
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	ses := &deskSession{id: id, ch: make(chan DeskInput, deskQueue), worker: make(chan struct{}),
		rec: &deskRecorder{id: id, dir: s.Dir, p: s.Platform, emit: emit, now: now, log: s.log()}}
	if s.Platform.Context != nil {
		ses.rec.context = s.Platform.Context()
	}
	stop, err := s.Tap(func(in DeskInput) {
		ses.pending.Add(1)
		select {
		case ses.ch <- in:
		default: // the worker is far behind (an app not answering AX): drop rather than block the tap
			ses.pending.Add(-1)
		}
	})
	if err != nil {
		return err
	}
	ses.stopTap = stop
	s.warning = ""
	if ses.rec.context == ContextNone {
		s.warning = noContextWarning
	}
	go func() {
		defer close(ses.worker)
		for in := range ses.ch {
			if !ses.rec.isAbandoned() {
				ses.rec.handle(in)
			}
			ses.pending.Add(-1)
		}
	}()
	s.cur = ses
	return nil
}

func (s *DesktopSource) Stop(ctx context.Context, id string) error {
	s.mu.Lock()
	ses := s.cur
	if ses == nil || ses.id != id {
		s.mu.Unlock()
		return ErrNotRecording
	}
	s.cur = nil
	s.mu.Unlock()

	ses.stopTap()
	close(ses.ch)
	var err error
	wait, cancel := context.WithTimeout(ctx, deskWorkerAwait)
	select {
	case <-ses.worker:
		ses.rec.finish()
	case <-wait.Done():
		// The worker is stuck (an app not answering AX): what it hasn't emitted yet is lost,
		// text being typed too, and it writes no keyframe once the trace is handed over.
		ses.rec.abandon()
		err = errors.New("desktop recording: events still waiting; dropped")
	}
	cancel()
	s.mu.Lock()
	if s.done == nil {
		s.done = map[string][]Keyframe{}
	}
	s.done[id] = ses.rec.keyframes()
	s.mu.Unlock()
	return err
}

// Keyframes hands over stopped recording id's keyframes (once).
func (s *DesktopSource) Keyframes(id string) []Keyframe {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.done[id]
	delete(s.done, id)
	return k
}

// deskRecorder groups one recording's raw events. Only the worker goroutine calls it (finish
// once the worker is done), but for abandon, from Stop.
type deskRecorder struct {
	id   string
	dir  string
	p    DeskPlatform
	emit func(json.RawMessage)
	now  func() time.Time
	log  *slog.Logger

	seq      int64
	typing   *deskTyping
	scroll   *DesktopEvent
	scrolled time.Time
	context  string    // DeskPlatform.Context's answer at the start
	at       time.Time // the input being handled: keyframes are throttled on its time
	quiet    bool      // flushing for a click that takes the keyframe itself

	kmu       sync.Mutex
	kf        []Keyframe
	abandoned bool // Stop gave up waiting: keep no more keyframes, skip what is queued
	lastShot  time.Time
}

// deskTyping is the text typed into one element so far.
type deskTyping struct {
	el     *ax.Recorded
	win    *EventWindow
	text   []rune
	masked bool
	at     time.Time
}

func (d *deskRecorder) handle(in DeskInput) {
	if in.At.IsZero() {
		in.At = d.now()
	}
	d.at = in.At
	switch in.Kind {
	case InClick:
		// With marked keyframes the click takes the keyframe of the moment typing ended.
		d.quiet = d.p.Frame != nil
		d.flush()
		d.quiet = false
		var el *ax.Recorded
		var pt *Point
		if !in.NoPoint {
			el = d.lookup(d.p.ElementAt, in.X, in.Y)
			pt = &Point{in.X, in.Y}
		}
		e := d.event(InClick, el, in.At)
		if pt != nil {
			e.X, e.Y = round(in.X), round(in.Y)
		}
		if in.Button == "right" {
			e.Button = "right"
		}
		d.sendAt(e, pt)
	case InScroll:
		d.flushTyping()
		if d.scroll != nil && in.At.Sub(d.scrolled) < deskScrollGap {
			d.scroll.DX += in.DX
			d.scroll.DY += in.DY
			d.scrolled = in.At
			return
		}
		d.flushScroll()
		var el *ax.Recorded
		if !in.NoPoint {
			el = d.lookup(d.p.ElementAt, in.X, in.Y)
		}
		e := d.event(InScroll, el, in.At)
		if !in.NoPoint {
			e.X, e.Y = round(in.X), round(in.Y)
		}
		e.DX, e.DY = in.DX, in.DY
		d.scroll, d.scrolled = &e, in.At
	case InKey:
		d.flushScroll()
		d.key(in)
	}
}

func (d *deskRecorder) key(in DeskInput) {
	switch {
	case in.Key == KeyEnter || in.Key == KeyTab || in.Key == KeyEscape || strings.Contains(in.Key, "+"):
		var el *ax.Recorded
		if d.typing != nil {
			el = d.typing.el
		} else {
			el = d.lookup(func(float64, float64) (*ax.Recorded, error) { return d.p.Focused() }, 0, 0)
		}
		d.flushTyping()
		e := d.event(InKey, el, in.At)
		e.Key = in.Key
		d.send(e)
		return
	case in.Key == KeyBackspace:
		if t := d.typing; t != nil && !t.masked && len(t.text) > 0 {
			t.text = t.text[:len(t.text)-1]
		}
		return
	case in.Key != "":
		return // arrows and other keys that type nothing
	}
	el := d.lookup(func(float64, float64) (*ax.Recorded, error) { return d.p.Focused() }, 0, 0)
	if d.typing != nil && !sameElement(d.typing.el, el) {
		d.flushTyping()
	}
	if d.typing == nil {
		d.typing = &deskTyping{el: el, win: d.window(), at: in.At}
	}
	t := d.typing
	if in.Secure || (el != nil && el.Secure) {
		t.masked, t.text = true, nil
		return
	}
	if !t.masked && len(t.text) < recordValueMax {
		t.text = append(t.text, []rune(in.Text)...)
		if len(t.text) > recordValueMax {
			t.text = t.text[:recordValueMax]
		}
	}
}

// recordValueMax is the browser recording's longest value.
const recordValueMax = 500

func (d *deskRecorder) lookup(f func(x, y float64) (*ax.Recorded, error), x, y float64) *ax.Recorded {
	if f == nil {
		return nil
	}
	el, err := f(x, y)
	if err != nil {
		d.log.Debug("desktop recording: no element", "err", err)
		return nil
	}
	return el
}

func sameElement(a, b *ax.Recorded) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.PID == b.PID && a.Path == b.Path && a.Role == b.Role && a.Name == b.Name
}

func (d *deskRecorder) event(kind string, el *ax.Recorded, at time.Time) DesktopEvent {
	return d.eventIn(kind, el, d.window(), at)
}

// eventIn is an event on el in window win (nil: none kept).
func (d *deskRecorder) eventIn(kind string, el *ax.Recorded, win *EventWindow, at time.Time) DesktopEvent {
	e := DesktopEvent{RecordingID: d.id, Kind: kind, TS: at.UnixMilli(), Window: win, Context: d.context}
	if el != nil {
		e.App = el.App
		if el.Role != "" || el.Name != "" || el.Path != "" {
			e.Element = &DesktopElement{Role: el.Role, Name: el.Name, AXPath: el.Path}
		}
	}
	if e.App == "" && win != nil {
		e.App = win.Class
	}
	return e
}

// window is the focused window when the platform keeps it.
func (d *deskRecorder) window() *EventWindow {
	if d.p.Window == nil {
		return nil
	}
	return d.p.Window()
}

func (d *deskRecorder) flush() {
	d.flushTyping()
	d.flushScroll()
}

func (d *deskRecorder) flushTyping() {
	t := d.typing
	if t == nil {
		return
	}
	d.typing = nil
	if !t.masked && len(t.text) == 0 {
		return // typed and deleted again
	}
	e := d.eventIn("input", t.el, t.win, t.at)
	if t.masked {
		e.Masked = true
	} else {
		e.Value = string(t.text)
	}
	d.send(e)
}

func (d *deskRecorder) flushScroll() {
	if e := d.scroll; e != nil {
		d.scroll = nil
		if e.DX != 0 || e.DY != 0 {
			d.send(*e)
		}
	}
}

// send numbers e, takes its keyframe when the last one is KeyframeEvery old, and emits it.
func (d *deskRecorder) send(e DesktopEvent) { d.sendAt(e, nil) }

// sendAt is send for an event at point pt (a click's), which a marked keyframe marks.
func (d *deskRecorder) sendAt(e DesktopEvent, pt *Point) {
	d.seq++
	e.Seq = d.seq
	if !d.quiet {
		e.KeyframePoint = d.keyframe(e.Seq, pt)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	d.emit(raw)
}

// keyframe takes event seq's keyframe unless the last one is too recent (on the inputs'
// clock), and returns where the point is on it when the platform marks it.
func (d *deskRecorder) keyframe(seq int64, pt *Point) *KeyframePoint {
	if d.p.Keyframe == nil && d.p.Frame == nil {
		return nil
	}
	now := d.at
	if now.IsZero() {
		now = d.now()
	}
	if !d.lastShot.IsZero() && now.Sub(d.lastShot) < cmp.Or(d.p.KeyframeEvery, KeyframeEvery) {
		return nil
	}
	d.lastShot = now
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var f Frame
	var err error
	if d.p.Frame != nil {
		f, err = d.p.Frame(ctx, pt)
	} else {
		f.JPEG, err = d.p.Keyframe(ctx)
	}
	cancel()
	if err != nil {
		d.log.Debug("desktop recording: no keyframe", "seq", seq, "err", err)
		return nil
	}
	base := filepath.Join(d.dir, d.id+"."+strconv.FormatInt(seq, 10))
	d.kmu.Lock()
	defer d.kmu.Unlock()
	if d.abandoned {
		return nil
	}
	if err := writePrivate(base+".jpg", f.JPEG); err != nil {
		d.log.Warn("desktop recording: couldn't keep a keyframe", "err", err)
		return nil
	}
	k := Keyframe{Seq: int(seq), Path: base + ".jpg"}
	if f.Crop != nil {
		if err := writePrivate(base+cropSuffix, f.Crop); err == nil {
			k.Crop = base + cropSuffix
		}
	}
	d.kf = append(d.kf, k)
	return f.Point
}

// cropSuffix ends a keyframe's crop around its point: <id>.<seq>.crop.jpg next to it.
const cropSuffix = ".crop.jpg"

// abandon is Stop giving up on the worker; once it returns, no keyframe is written.
func (d *deskRecorder) abandon() {
	d.kmu.Lock()
	d.abandoned = true
	d.kmu.Unlock()
}

func (d *deskRecorder) isAbandoned() bool {
	d.kmu.Lock()
	defer d.kmu.Unlock()
	return d.abandoned
}

func (d *deskRecorder) finish() { d.flush() }

func (d *deskRecorder) keyframes() []Keyframe {
	d.kmu.Lock()
	defer d.kmu.Unlock()
	return append([]Keyframe(nil), d.kf...)
}

// writePrivate writes a new 0600 file.
func writePrivate(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("keyframe %s: %w", filepath.Base(path), err)
	}
	return nil
}

func round(f float64) int { return int(math.Round(f)) }

// keyframer is a desktop recording's keyframe: the screen as a JPEG (nil without a capturer).
func keyframer(c screen.Capturer) func(context.Context) ([]byte, error) {
	if c == nil {
		return nil
	}
	return func(ctx context.Context) ([]byte, error) {
		if ok, why := c.Available(ctx); !ok {
			return nil, errors.New(why)
		}
		shot, err := c.Capture(ctx, screen.ScopeScreen, "")
		if err != nil {
			return nil, err
		}
		b, _, _, err := screen.Encode(shot.Image)
		return b, err
	}
}
