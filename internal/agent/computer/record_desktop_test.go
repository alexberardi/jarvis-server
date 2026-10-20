package computer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
)

// fakeDesk is the OS under a desktop recording: elements by point, the focused element and a
// screen to take keyframes of.
type fakeDesk struct {
	mu      sync.Mutex
	at      map[[2]float64]*ax.Recorded
	focused *ax.Recorded
	shots   int
	clock   time.Time
}

func newFakeDesk() *fakeDesk {
	return &fakeDesk{at: map[[2]float64]*ax.Recorded{}, clock: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
}

func (f *fakeDesk) focus(el *ax.Recorded) { f.mu.Lock(); f.focused = el; f.mu.Unlock() }

// tick moves the clock and returns the new time.
func (f *fakeDesk) tick(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(d)
	return f.clock
}

func (f *fakeDesk) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock }

func (f *fakeDesk) platform() DeskPlatform {
	return DeskPlatform{
		ElementAt: func(x, y float64) (*ax.Recorded, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.at[[2]float64{x, y}], nil
		},
		Focused: func() (*ax.Recorded, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.focused, nil },
		Keyframe: func(context.Context) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.shots++
			return []byte{0xff, 0xd8, byte(f.shots)}, nil
		},
	}
}

var (
	notesBody  = &ax.Recorded{Role: "AXTextArea", Name: "Note body", Path: "AXWindow/AXScrollArea/AXTextArea", PID: 42, App: "com.apple.Notes"}
	notesTitle = &ax.Recorded{Role: "AXTextField", Name: "Title", Path: "AXWindow/AXTextField", PID: 42, App: "com.apple.Notes"}
	pwField    = &ax.Recorded{Role: "AXSecureTextField", Name: "Password", Secure: true, Path: "AXWindow/AXSecureTextField", PID: 7, App: "com.example.bank"}
	newNoteBtn = &ax.Recorded{Role: "AXButton", Name: "New Note", Path: "AXWindow/AXToolbar/AXButton#new", PID: 42, App: "com.apple.Notes"}
)

// deskEnv drives a deskRecorder directly and collects what it emits.
type deskEnv struct {
	desk *fakeDesk
	rec  *deskRecorder
	raw  []string
}

func newDeskEnv(t *testing.T) *deskEnv {
	e := &deskEnv{desk: newFakeDesk()}
	e.rec = &deskRecorder{id: "rec_1", dir: t.TempDir(), p: e.desk.platform(), now: e.desk.now,
		log: slogDiscard(), emit: func(b json.RawMessage) { e.raw = append(e.raw, string(b)) }}
	return e
}

func (e *deskEnv) typeText(s string) {
	for _, r := range s {
		e.rec.handle(DeskInput{Kind: InKey, Text: string(r), At: e.desk.tick(100 * time.Millisecond)})
	}
}

func (e *deskEnv) press(key string) {
	e.rec.handle(DeskInput{Kind: InKey, Key: key, At: e.desk.tick(100 * time.Millisecond)})
}

func (e *deskEnv) events(t *testing.T) []DesktopEvent {
	t.Helper()
	out := make([]DesktopEvent, len(e.raw))
	for i, r := range e.raw {
		if err := json.Unmarshal([]byte(r), &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func summary(evs []DesktopEvent) string {
	var b []string
	for _, e := range evs {
		s := e.Kind
		if e.Element != nil {
			s += " " + e.Element.Name
		}
		if e.Key != "" {
			s += " [" + e.Key + "]"
		}
		if e.Value != "" {
			s += " " + strconvQuote(e.Value)
		}
		if e.Masked {
			s += " masked"
		}
		b = append(b, s)
	}
	return strings.Join(b, "; ")
}

func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// Keystrokes become one input event per focused element: flushed by Tab/Enter (a key event of
// their own), a focus change, a click or the end; Backspace edits the text; shortcuts are keys.
func TestKeystrokesGrouped(t *testing.T) {
	e := newDeskEnv(t)
	e.desk.focus(notesTitle)
	e.typeText("Shoping")
	e.press(KeyBackspace)
	e.press(KeyBackspace)
	e.press(KeyBackspace)
	e.typeText("ping")
	e.press(KeyTab)
	e.desk.focus(notesBody)
	e.typeText("milk")
	e.press(KeyEnter)
	e.typeText("eggs")
	e.desk.focus(notesTitle) // the focus moved without a key (the app moved it)
	e.typeText("!")
	e.press("cmd+s")
	e.typeText("x")
	e.press(KeyBackspace) // typed and deleted: no input event
	e.rec.finish()

	got := summary(e.events(t))
	want := `input Title "Shopping"; key Title [Tab]; input Note body "milk"; key Note body [Enter]; ` +
		`input Note body "eggs"; input Title "!"; key Title [cmd+s]`
	if got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	evs := e.events(t)
	for i, ev := range evs {
		if ev.Seq != int64(i+1) || ev.RecordingID != "rec_1" || ev.TS == 0 {
			t.Fatalf("event %d: seq %d id %q ts %d", i, ev.Seq, ev.RecordingID, ev.TS)
		}
	}
	if evs[2].App != "com.apple.Notes" || evs[2].Element.AXPath != notesBody.Path {
		t.Fatalf("input element: %+v app %q", evs[2].Element, evs[2].App)
	}
}

// Text typed into a secure text field, or while secure event input is on, never reaches the
// trace: the input event says masked and has no value.
func TestSecureInputDropped(t *testing.T) {
	e := newDeskEnv(t)
	e.desk.focus(pwField)
	e.typeText("hunter2")
	e.press(KeyEnter)
	e.desk.focus(notesBody)
	// Secure event input on (another app's password prompt): the tap sends no text, but a
	// keystroke that carried some anyway is dropped too.
	for _, r := range "s3cret" {
		e.rec.handle(DeskInput{Kind: InKey, Text: string(r), Secure: true, At: e.desk.tick(time.Millisecond)})
	}
	e.typeText("ok") // still the same element: the run stays masked
	e.rec.finish()

	got := summary(e.events(t))
	if want := `input Password masked; key Password [Enter]; input Note body masked`; got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	all := strings.Join(e.raw, "\n")
	for _, secret := range []string{"hunter", "s3cret", `"value"`} {
		if strings.Contains(all, secret) {
			t.Fatalf("%q reached the trace: %s", secret, all)
		}
	}
}

// A click is recorded with the element under it (role, name, AX path, app) and its point; a
// right click says so. Scrolls close together are one event.
func TestClickResolvesElement(t *testing.T) {
	e := newDeskEnv(t)
	e.desk.at[[2]float64{120.4, 48}] = newNoteBtn
	e.desk.at[[2]float64{400, 300}] = notesBody
	e.desk.focus(notesBody)
	e.typeText("tea")
	e.rec.handle(DeskInput{Kind: InClick, X: 120.4, Y: 48, Button: "left", At: e.desk.tick(time.Second)})
	e.rec.handle(DeskInput{Kind: InClick, X: 400, Y: 300, Button: "right", At: e.desk.tick(time.Second)})
	e.rec.handle(DeskInput{Kind: InClick, X: 5, Y: 5, Button: "left", At: e.desk.tick(time.Second)}) // nothing there
	e.rec.handle(DeskInput{Kind: InScroll, X: 400, Y: 300, DY: -3, At: e.desk.tick(time.Second)})
	e.rec.handle(DeskInput{Kind: InScroll, X: 400, Y: 310, DY: -2, At: e.desk.tick(300 * time.Millisecond)})
	e.rec.handle(DeskInput{Kind: InScroll, X: 400, Y: 300, DY: 1, At: e.desk.tick(3 * time.Second)})
	e.rec.finish()

	evs := e.events(t)
	if got, want := summary(evs), `input Note body "tea"; click New Note; click Note body; click; scroll Note body; scroll Note body`; got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	c := evs[1]
	if c.App != "com.apple.Notes" || c.Element.Role != "AXButton" || c.Element.AXPath != "AXWindow/AXToolbar/AXButton#new" ||
		c.X != 120 || c.Y != 48 || c.Button != "" {
		t.Fatalf("click: %+v %+v", c, c.Element)
	}
	if evs[2].Button != "right" {
		t.Fatalf("right click: %+v", evs[2])
	}
	if evs[3].Element != nil || evs[3].X != 5 {
		t.Fatalf("click on nothing: %+v", evs[3])
	}
	if evs[4].DY != -5 || evs[5].DY != 1 {
		t.Fatalf("scrolls: %+v %+v", evs[4], evs[5])
	}
}

// A keyframe is taken with an event at most every KeyframeEvery, as a 0600 file next to the trace.
func TestKeyframesThrottled(t *testing.T) {
	e := newDeskEnv(t)
	e.desk.at[[2]float64{1, 1}] = newNoteBtn
	click := func(d time.Duration) {
		e.desk.tick(d)
		e.rec.handle(DeskInput{Kind: InClick, X: 1, Y: 1})
	}
	click(0)
	click(500 * time.Millisecond)
	click(1400 * time.Millisecond) // 1.9 s after the first keyframe
	click(200 * time.Millisecond)  // 2.1 s
	click(3 * time.Second)
	kf := e.rec.keyframes()
	var seqs []int
	for _, k := range kf {
		seqs = append(seqs, k.Seq)
		st, err := os.Stat(k.Path)
		// Windows reports 0666 for any file it can write: no Unix modes there.
		if err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 || filepath.Dir(k.Path) != e.rec.dir {
			t.Fatalf("keyframe %s: %v %v", k.Path, err, st)
		}
	}
	if len(seqs) != 3 || seqs[0] != 1 || seqs[1] != 4 || seqs[2] != 5 {
		t.Fatalf("keyframes at events %v, want [1 4 5]", seqs)
	}
}

// fakeTap is the platform's event tap.
type fakeTap struct {
	mu      sync.Mutex
	send    func(DeskInput)
	stopped bool
	err     error
}

func (f *fakeTap) start(send func(DeskInput)) (func(), error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	f.send = send
	f.mu.Unlock()
	return func() { f.mu.Lock(); f.stopped = true; f.mu.Unlock() }, nil
}

func (f *fakeTap) input(in DeskInput) {
	f.mu.Lock()
	send := f.send
	f.mu.Unlock()
	send(in)
}

// The whole path: the recorder's desktop mode keeps the tap's events in the trace file with
// their keyframes, never a secure field's text, and Delete removes the keyframes too.
func TestDesktopRecording(t *testing.T) {
	e := newRecEnv(t)
	desk, tap := newFakeDesk(), &fakeTap{}
	desk.at[[2]float64{10, 10}] = pwField
	src := &DesktopSource{Dir: e.r.Dir, Tap: tap.start, Platform: desk.platform(), now: desk.now}
	e.r.Sources[ModeDesktop] = src
	id, err := e.r.Start(context.Background(), ModeDesktop)
	if err != nil {
		t.Fatal(err)
	}
	tap.input(DeskInput{Kind: InClick, X: 10, Y: 10, At: desk.tick(time.Second)})
	desk.focus(pwField)
	for _, r := range "hunter2" {
		tap.input(DeskInput{Kind: InKey, Text: string(r), At: desk.tick(time.Second)})
	}
	tr, err := e.r.Stop(id)
	if err != nil {
		t.Fatal(err)
	}
	if !tap.stopped {
		t.Fatal("the tap still runs")
	}
	if tr.Mode != ModeDesktop || len(tr.Events) != 2 || len(tr.Keyframes) == 0 {
		t.Fatalf("trace: mode %s, %d events, %d keyframes", tr.Mode, len(tr.Events), len(tr.Keyframes))
	}
	file, _ := os.ReadFile(e.r.Path(id))
	if strings.Contains(string(file), "hunter") || !strings.Contains(string(file), `"masked":true`) {
		t.Fatalf("trace file: %s", file)
	}
	if err := e.r.Delete(id); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(e.r.Dir); len(left) != 0 {
		t.Fatalf("left after delete: %v", left)
	}

	// A tap that can't start (no Input Monitoring grant) fails the start and leaves nothing.
	tap.err = errors.New("allow Input Monitoring")
	if _, err := e.r.Start(context.Background(), ModeDesktop); err == nil || !strings.Contains(err.Error(), "Input Monitoring") {
		t.Fatalf("start without the grant: %v", err)
	}
	if left, _ := os.ReadDir(e.r.Dir); len(left) != 0 || e.r.Current() != "" {
		t.Fatalf("a failed start left %v", left)
	}
}

// A worker stuck past Stop's wait (an app not answering AX) does nothing more once Stop
// returns: no keyframe written after the trace was handed over, the queued events skipped.
func TestStopAbandonsStuckWorker(t *testing.T) {
	defer func(d time.Duration) { deskWorkerAwait = d }(deskWorkerAwait)
	deskWorkerAwait = 50 * time.Millisecond
	desk, tap := newFakeDesk(), &fakeTap{}
	p := desk.platform()
	release, calls := make(chan struct{}), 0
	var mu sync.Mutex
	p.ElementAt = func(x, y float64) (*ax.Recorded, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return newNoteBtn, nil
	}
	dir := t.TempDir()
	src := &DesktopSource{Dir: dir, Tap: tap.start, Platform: p, now: desk.now}
	if err := src.Start(context.Background(), "rec_x", func(json.RawMessage) {}); err != nil {
		t.Fatal(err)
	}
	worker := src.cur.worker
	tap.input(DeskInput{Kind: InClick, X: 1, Y: 1, At: desk.tick(time.Second)})
	tap.input(DeskInput{Kind: InClick, X: 2, Y: 2, At: desk.tick(3 * time.Second)})
	for {
		mu.Lock()
		n := calls
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := src.Stop(context.Background(), "rec_x"); err == nil {
		t.Fatal("Stop didn't say events were dropped")
	}
	close(release)
	<-worker
	if left, _ := filepath.Glob(filepath.Join(dir, "*.jpg")); len(left) != 0 {
		t.Fatalf("keyframes written after Stop: %v", left)
	}
	if calls != 1 {
		t.Fatalf("the worker handled %d clicks after Stop, want the stuck one only", calls)
	}
}
