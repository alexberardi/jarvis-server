package computer

import (
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
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
)

// Desktop recording (plan P3 piece 36): the platform's listen-only tap (record_darwin.go) hands
// over the user's raw clicks, keystrokes and scrolls; DesktopSource turns them into the browser
// trace's events (kind click, input, key, scroll; element role and name) plus the desktop's
// app (bundle id) and the element's AX path, with a keyframe of the screen at most every
// KeyframeEvery. Keystrokes become one input event per focused element. A keystroke typed while
// secure input is on, or into a secure text field, never leaves the tap: the input event says
// masked instead.

// KeyframeEvery is the shortest gap between two keyframes of a desktop recording.
const KeyframeEvery = 2 * time.Second

const (
	deskQueue       = 512             // raw events waiting for the worker; more are dropped
	deskScrollGap   = time.Second     // scrolls closer than this are one scroll event
	deskWorkerAwait = 5 * time.Second // how long Stop waits for the queue to drain
)

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
}

// DeskPlatform is what a desktop recording asks the OS (ax on macOS).
type DeskPlatform struct {
	ElementAt func(x, y float64) (*ax.Recorded, error)
	Focused   func() (*ax.Recorded, error)
	// Keyframe is a JPEG of the screen; nil: no keyframes.
	Keyframe func(ctx context.Context) ([]byte, error)
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

	mu   sync.Mutex
	cur  *deskSession
	done map[string][]Keyframe // stopped recordings' keyframes, until Keyframes takes them
}

type deskSession struct {
	id      string
	rec     *deskRecorder
	ch      chan DeskInput
	stopTap func()
	worker  chan struct{}
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
	stop, err := s.Tap(func(in DeskInput) {
		select {
		case ses.ch <- in:
		default: // the worker is far behind (an app not answering AX): drop rather than block the tap
		}
	})
	if err != nil {
		return err
	}
	ses.stopTap = stop
	go func() {
		defer close(ses.worker)
		for in := range ses.ch {
			ses.rec.handle(in)
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
// once the worker is done).
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

	kmu      sync.Mutex
	kf       []Keyframe
	lastShot time.Time
}

// deskTyping is the text typed into one element so far.
type deskTyping struct {
	el     *ax.Recorded
	text   []rune
	masked bool
	at     time.Time
}

func (d *deskRecorder) handle(in DeskInput) {
	if in.At.IsZero() {
		in.At = d.now()
	}
	switch in.Kind {
	case InClick:
		d.flush()
		el := d.lookup(d.p.ElementAt, in.X, in.Y)
		e := d.event(InClick, el, in.At)
		e.X, e.Y = round(in.X), round(in.Y)
		if in.Button == "right" {
			e.Button = "right"
		}
		d.send(e)
	case InScroll:
		d.flushTyping()
		if d.scroll != nil && in.At.Sub(d.scrolled) < deskScrollGap {
			d.scroll.DX += in.DX
			d.scroll.DY += in.DY
			d.scrolled = in.At
			return
		}
		d.flushScroll()
		el := d.lookup(d.p.ElementAt, in.X, in.Y)
		e := d.event(InScroll, el, in.At)
		e.X, e.Y, e.DX, e.DY = round(in.X), round(in.Y), in.DX, in.DY
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
		d.typing = &deskTyping{el: el, at: in.At}
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
	e := DesktopEvent{RecordingID: d.id, Kind: kind, TS: at.UnixMilli()}
	if el != nil {
		e.App = el.App
		e.Element = &DesktopElement{Role: el.Role, Name: el.Name, AXPath: el.Path}
	}
	return e
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
	e := d.event("input", t.el, t.at)
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
func (d *deskRecorder) send(e DesktopEvent) {
	d.seq++
	e.Seq = d.seq
	d.keyframe(e.Seq)
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	d.emit(raw)
}

func (d *deskRecorder) keyframe(seq int64) {
	if d.p.Keyframe == nil {
		return
	}
	now := d.now()
	if !d.lastShot.IsZero() && now.Sub(d.lastShot) < KeyframeEvery {
		return
	}
	d.lastShot = now
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	b, err := d.p.Keyframe(ctx)
	cancel()
	if err != nil {
		d.log.Debug("desktop recording: no keyframe", "seq", seq, "err", err)
		return
	}
	path := filepath.Join(d.dir, d.id+"."+strconv.FormatInt(seq, 10)+".jpg")
	if err := writePrivate(path, b); err != nil {
		d.log.Warn("desktop recording: couldn't keep a keyframe", "err", err)
		return
	}
	d.kmu.Lock()
	d.kf = append(d.kf, Keyframe{Seq: int(seq), Path: path})
	d.kmu.Unlock()
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
