package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/evdev"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// evdev key codes the tests type (linux/input-event-codes.h).
const (
	evKeyE     = 18
	evKeyR     = 19
	evKeyT     = 20
	evKeyI     = 23
	evKeyS     = 31
	evKeyH     = 35
	evKeyN     = 49
	evKey1     = 2
	evKeySpace = 57
)

var evT0 = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

// evStream is a fake evdev stream: key and button presses and releases, wheel turns.
type evStream struct {
	at  time.Time
	out []evdev.Event
}

func (s *evStream) ev(typ, code uint16, v int32) {
	s.at = s.at.Add(50 * time.Millisecond)
	s.out = append(s.out, evdev.Event{Time: s.at, Type: typ, Code: code, Value: v})
}
func (s *evStream) down(code uint16) { s.ev(evdev.EvKey, code, 1) }
func (s *evStream) up(code uint16)   { s.ev(evdev.EvKey, code, 0) }
func (s *evStream) tap(code uint16)  { s.down(code); s.up(code) }
func (s *evStream) with(mod uint16, codes ...uint16) {
	s.down(mod)
	for _, c := range codes {
		s.tap(c)
	}
	s.up(mod)
}

func decodeAll(k *wlKeys, evs []evdev.Event) []DeskInput {
	var out []DeskInput
	for _, e := range evs {
		if in, ok := k.input(e); ok {
			out = append(out, in)
		}
	}
	return out
}

func describe(ins []DeskInput) string {
	var b []string
	for _, in := range ins {
		switch {
		case in.Kind == InClick:
			b = append(b, "click "+in.Button)
		case in.Kind == InScroll:
			b = append(b, "scroll "+itoa(in.DX)+","+itoa(in.DY))
		case in.Text == " ":
			b = append(b, "space")
		case in.Text != "":
			b = append(b, in.Text)
		default:
			b = append(b, "["+in.Key+"]")
		}
	}
	return strings.Join(b, " ")
}

// The evdev decoder: text through the layout (Shift and Caps Lock levels, autorepeat), named
// keys, shortcuts as keys (ctrl+n), clicks as buttons, the wheel as scrolls; releases, the
// middle button and modifier presses alone are nothing.
func TestWaylandKeysDecoded(t *testing.T) {
	s := &evStream{at: evT0}
	s.tap(evKeyH)
	s.with(evdev.KeyLeftShift, evKeyI)
	s.with(evdev.KeyRightShift, evKey1)
	s.tap(evKeySpace)
	s.tap(evdev.KeyCapsLock)
	s.tap(evKeyS)
	s.with(evdev.KeyLeftShift, evKeyS) // Shift under Caps Lock: lower case
	s.tap(evdev.KeyCapsLock)
	s.ev(evdev.EvKey, evKeyE, 1)
	s.ev(evdev.EvKey, evKeyE, 2) // held: autorepeat types again
	s.up(evKeyE)
	s.tap(evdev.KeyBackspace)
	s.with(evdev.KeyLeftCtrl, evKeyN)
	s.with(evdev.KeyRightCtrl, evdev.KeyEnter)
	s.down(evdev.KeyLeftCtrl)
	s.with(evdev.KeyLeftShift, evKeyT)
	s.up(evdev.KeyLeftCtrl)
	s.with(evdev.KeyLeftMeta, evKeyR)
	s.with(evdev.KeyLeftAlt, evdev.KeyTab)
	s.with(evdev.KeyLeftShift, evdev.KeyTab)
	s.tap(evdev.KeyTab)
	s.tap(evdev.KeyEnter)
	s.tap(evdev.KeyKPEnter)
	s.tap(evdev.KeyEsc)
	s.tap(evdev.BtnLeft)
	s.tap(evdev.BtnRight)
	s.tap(evdev.BtnMiddle)
	s.ev(evdev.EvRel, evdev.RelWheel, 1)
	s.ev(evdev.EvRel, evdev.RelWheel, -2)
	s.ev(evdev.EvRel, evdev.RelHWheel, 1)
	s.ev(evdev.EvRel, 0 /* REL_X: motion */, 5)
	s.tap(103) // KEY_UP: types nothing
	got := describe(decodeAll(&wlKeys{layout: xkb.US()}, s.out))
	want := "h I ! space S s e e [Backspace] [ctrl+n] [ctrl+enter] [ctrl+shift+t] [super+r] [alt+tab] [shift+tab] [Tab] [Enter] [Enter] [Escape] " +
		"click left click right scroll 0,-1 scroll 0,2 scroll 1,0 [other]"
	if got != want {
		t.Fatalf("decoded\n %s\nwant\n %s", got, want)
	}
	// Each input keeps the device's timestamp.
	ins := decodeAll(&wlKeys{layout: xkb.US()}, s.out[:2])
	if len(ins) != 1 || !ins[0].At.Equal(s.out[0].Time) {
		t.Fatalf("timestamp: %+v", ins)
	}
}

// AltGr: on a layout whose right Alt is ISO_Level3_Shift it's a level (the built-in layout has
// no third level: other), never a shortcut; elsewhere right Alt is Alt.
func TestWaylandAltGr(t *testing.T) {
	km := strings.Replace(usLevel3Keymap, "RALT_SYM", "0xfe03", 1)
	l, err := xkb.ParseKeymap(km)
	if err != nil {
		t.Fatal(err)
	}
	s := &evStream{at: evT0}
	s.with(evdev.KeyRightAlt, evKeyE)
	if got := describe(decodeAll(&wlKeys{layout: l}, s.out)); got != "[other]" {
		t.Fatalf("AltGr+e: %s", got)
	}
	if got := describe(decodeAll(&wlKeys{layout: xkb.US()}, s.out)); got != "[alt+e]" {
		t.Fatalf("right Alt on US: %s", got)
	}
}

const usLevel3Keymap = `xkb_keymap {
xkb_keycodes "evdev" {
	<AD03> = 26;
	<RALT> = 108;
};
xkb_symbols "pc_de" {
	key <AD03> { [ 0x65, 0x45 ] };
	key <RALT> { [ RALT_SYM ] };
};
};`

// fakeHypr answers hyprctl like Hyprland: the focused window and the cursor, both changeable.
type fakeHypr struct {
	mu     sync.Mutex
	window string // hyprctl -j activewindow
	cursor string // hyprctl cursorpos
	env    map[string]string
	ran    []string
}

func newFakeHypr() *fakeHypr {
	return &fakeHypr{window: editorWindow, cursor: "1100, 300",
		env: map[string]string{"WAYLAND_DISPLAY": "wayland-1", "HYPRLAND_INSTANCE_SIGNATURE": "sig"}}
}

const (
	editorWindow = `{"address":"0x1","at":[1000,0],"size":[800,600],"class":"org.gnome.TextEditor","title":"notes.txt","pid":42}`
	bankWindow   = `{"address":"0x2","at":[0,0],"size":[1000,1080],"class":"firefox","title":"Sign in - Bank","pid":7}`
)

func (f *fakeHypr) set(window, cursor string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if window != "" {
		f.window = window
	}
	if cursor != "" {
		f.cursor = cursor
	}
}

func (f *fakeHypr) LookPath(n string) bool { return n == "hyprctl" || n == "grim" }
func (f *fakeHypr) Getenv(k string) string { f.mu.Lock(); defer f.mu.Unlock(); return f.env[k] }
func (f *fakeHypr) Exists(string) bool     { return false }
func (f *fakeHypr) Run(_ context.Context, c dictate.Cmd) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	args := strings.Join(c.Args, " ")
	f.ran = append(f.ran, args)
	switch args {
	case "-j activewindow":
		return f.window, nil
	case "cursorpos":
		return f.cursor, nil
	}
	return "", errors.New("unexpected hyprctl " + args)
}

// wlEnv is a Wayland desktop under a recording: Hyprland (fake hyprctl), evdev devices fed by
// the test, AT-SPI's focused element per process and a screen to take keyframes of.
type wlEnv struct {
	*recEnv
	hypr     *fakeHypr
	src      *DesktopSource
	clock    *fakeDesk // its clock only
	mu       sync.Mutex
	send     func(evdev.Event)
	openErr  error
	a11yErr  error
	focus    map[int]*computeruse.Element
	at       map[[3]int]*computeruse.Element // ElementAt's answers by pid and window point
	offBus   map[int]bool                    // processes not on the accessibility bus
	logs     bytes.Buffer
	shots    int
	stopped  bool
	startOut map[string]any
}

func newWLEnv(t *testing.T) *wlEnv {
	t.Helper()
	e := &wlEnv{recEnv: newRecEnv(t), hypr: newFakeHypr(), clock: newFakeDesk(),
		focus: map[int]*computeruse.Element{42: {Role: "text", Name: "Document"}},
		at:    map[[3]int]*computeruse.Element{}, offBus: map[int]bool{}}
	e.src = newWaylandSource(e.r.Dir, e.system(), slog.New(slog.NewTextHandler(&lockedWriter{w: &e.logs, mu: &e.mu}, nil)))
	e.src.now = e.clock.now
	e.r.Sources[ModeDesktop] = e.src
	return e
}

func (e *wlEnv) system() wlSystem {
	return wlSystem{
		Windows: &dictate.LinuxWindows{X: e.hypr},
		Devices: func() ([]evdev.Device, error) {
			return []evdev.Device{{Path: "/dev/input/event3", Keyboard: true}, {Path: "/dev/input/event7", Pointer: true}}, nil
		},
		Open: func(devs []evdev.Device, send func(evdev.Event)) (func(), error) {
			if e.openErr != nil {
				return nil, e.openErr
			}
			e.mu.Lock()
			e.send = send
			e.mu.Unlock()
			return func() { e.mu.Lock(); e.stopped = true; e.mu.Unlock() }, nil
		},
		Layout: func(context.Context) *xkb.Layout { return xkb.US() },
		Access: func(context.Context) atspi.State {
			return atspi.State{SessionBus: true, From: "org.a11y.Bus", Enabled: true, Err: e.a11yErr}
		},
		Focused: func(pid int) (*computeruse.Element, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.offBus[pid] {
				return nil, &atspi.Error{Code: atspi.CodeNotFound, Msg: "no accessible app"}
			}
			return e.focus[pid], nil
		},
		ElementAt: func(pid, x, y int) (*computeruse.Element, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.offBus[pid] {
				return nil, &atspi.Error{Code: atspi.CodeNotFound, Msg: "no accessible app"}
			}
			return e.at[[3]int{pid, x, y}], nil
		},
		Shot: func(context.Context) (screen.Shot, error) {
			e.mu.Lock()
			e.shots++
			e.mu.Unlock()
			// The focused monitor: DP-2 right of a 1000 px one, at scale 1.
			return screen.Shot{Image: image.NewRGBA(image.Rect(0, 0, 1920, 1080)),
				Geometry: computeruse.Geometry{OriginX: 1000, LogicalW: 1920, LogicalH: 1080, Scale: 1}}, nil
		},
	}
}

func (e *wlEnv) feed(s *evStream) {
	e.mu.Lock()
	send := e.send
	e.mu.Unlock()
	for _, ev := range s.out {
		e.clock.tick(50 * time.Millisecond)
		send(ev)
	}
	// The recorder asks the compositor and AT-SPI on its worker: let it catch up before the
	// test changes what they answer.
	deadline := time.Now().Add(5 * time.Second)
	for !e.src.settled() {
		if time.Now().After(deadline) {
			panic("the desktop recording's worker never caught up")
		}
		time.Sleep(time.Millisecond)
	}
}

func (e *wlEnv) setFocus(pid int, el *computeruse.Element) {
	e.mu.Lock()
	e.focus[pid] = el
	e.mu.Unlock()
}

func (e *wlEnv) record(t *testing.T, do func()) (Trace, []DesktopEvent) {
	t.Helper()
	tools := &RecordTools{R: e.r}
	out, _ := tools.Call(context.Background(), "computer_record_start", map[string]any{"mode": "desktop"}, node.Request{})
	if out["success"] != true {
		t.Fatalf("start: %v", out)
	}
	e.startOut = out
	do()
	tr, err := e.r.Stop(out["recording_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	evs := make([]DesktopEvent, len(tr.Events))
	for i, raw := range tr.Events {
		if err := json.Unmarshal(raw, &evs[i]); err != nil {
			t.Fatal(err)
		}
	}
	return tr, evs
}

// lockedWriter is a log's writer under the env's lock (the worker logs while the test reads).
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

func (e *wlEnv) logged() string { e.mu.Lock(); defer e.mu.Unlock(); return e.logs.String() }

// The whole path on Hyprland: typing grouped per focused element (AT-SPI's role and name) in
// the focused window, a shortcut as a key, clicks with the cursor's point and the window, a
// keyframe at the click with the point marked and a crop, all 0600, removed with the recording.
func TestWaylandRecording(t *testing.T) {
	e := newWLEnv(t)
	tr, evs := e.record(t, func() {
		s := &evStream{at: evT0}
		s.tap(evKeyH)
		s.tap(evKeyI)
		e.feed(s)
		e.clock.tick(2 * time.Second)
		e.hypr.set("", "1500, 250")
		s = &evStream{at: evT0.Add(3 * time.Second)}
		s.tap(evdev.BtnLeft)
		e.feed(s)
		e.clock.tick(2 * time.Second)
		s = &evStream{at: evT0.Add(6 * time.Second)}
		s.with(evdev.KeyLeftCtrl, evKeyS)
		e.feed(s)
	})
	if got := summary(evs); got != `input Document "hi"; click; key Document [ctrl+s]` {
		t.Fatalf("trace: %s", got)
	}
	in, click := evs[0], evs[1]
	if in.App != "org.gnome.TextEditor" || in.Element.Role != "text" || in.Window == nil || in.Window.Title != "notes.txt" ||
		in.Context != "" || in.TS != evT0.Add(50*time.Millisecond).UnixMilli() {
		t.Fatalf("input event: %+v %+v", in, in.Window)
	}
	if click.X != 1500 || click.Y != 250 || click.Button != "" || click.App != "org.gnome.TextEditor" || click.Element != nil ||
		*click.Window != (EventWindow{Class: "org.gnome.TextEditor", Title: "notes.txt", X: 1000, W: 800, H: 600}) {
		t.Fatalf("click: %+v %+v", click, click.Window)
	}
	// The click's keyframe: its point on the image (the monitor starts at x 1000; the 1920 px
	// capture is sent at 1568) and a crop.
	if click.KeyframePoint == nil || *click.KeyframePoint != (KeyframePoint{X: 408, Y: 204}) {
		t.Fatalf("click keyframe point %+v", click.KeyframePoint)
	}
	var kc *Keyframe
	for i := range tr.Keyframes {
		if tr.Keyframes[i].Seq == int(click.Seq) {
			kc = &tr.Keyframes[i]
		}
	}
	if kc == nil || kc.Crop == "" {
		t.Fatalf("no keyframe with a crop for the click: %+v", tr.Keyframes)
	}
	for _, p := range []string{kc.Path, kc.Crop} {
		st, err := os.Stat(p)
		if err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, err, st)
		}
	}
	crop, err := jpeg.DecodeConfig(bytes.NewReader(mustRead(t, kc.Crop)))
	if err != nil || crop.Width != 256 || crop.Height != 256 {
		t.Fatalf("crop %+v %v", crop, err)
	}
	// LoadTrace (a recording sent after a restart) finds the crop again.
	back, err := e.r.LoadTrace(tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range back.Keyframes {
		found = found || k.Seq == kc.Seq && k.Crop == kc.Crop && k.Path == kc.Path
	}
	if len(back.Keyframes) != len(tr.Keyframes) || !found {
		t.Fatalf("loaded keyframes %+v, stopped %+v", back.Keyframes, tr.Keyframes)
	}
	if err := e.r.Delete(tr.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(e.r.Dir, tr.ID+"*")); len(left) != 0 {
		t.Fatalf("delete left %v", left)
	}
	if _, warned := e.startOut["warning"]; warned {
		t.Fatalf("no warning with AT-SPI: %v", e.startOut)
	}
	if l := e.logged(); !strings.Contains(l, "desktop recording: accessibility") || !strings.Contains(l, "address_from=org.a11y.Bus") {
		t.Fatalf("the start doesn't log the accessibility setup: %s", l)
	}
}

// A click is named by AT-SPI's element under it, asked from the focused window's corner (GTK
// and Qt on Wayland don't know where their window is); outside that window nothing is asked.
func TestWaylandClickElement(t *testing.T) {
	e := newWLEnv(t)
	e.at[[3]int{42, 500, 250}] = &computeruse.Element{Role: "button", Name: "Save"}
	_, evs := e.record(t, func() {
		e.hypr.set("", "1500, 250")
		s := &evStream{at: evT0}
		s.tap(evdev.BtnLeft)
		e.feed(s)
		e.hypr.set("", "200, 250") // left of the window at x 1000
		s = &evStream{at: evT0.Add(time.Second)}
		s.tap(evdev.BtnLeft)
		e.feed(s)
	})
	if got := summary(evs); got != `click Save; click` {
		t.Fatalf("trace: %s", got)
	}
	if el := evs[0].Element; el.Role != "button" || evs[0].Context != "" || evs[1].Context != "" {
		t.Fatalf("clicks: %+v %+v", evs[0], evs[1])
	}
}

// An app that isn't on the accessibility bus (foot) marks its events context none, so learning
// knows there was no element to name (and keeps none of its typing); its failure is logged
// once. The recording's other apps keep their elements.
func TestWaylandAppOffBus(t *testing.T) {
	e := newWLEnv(t)
	e.offBus[99] = true
	_, evs := e.record(t, func() {
		e.hypr.set(`{"address":"0x3","at":[0,0],"size":[1000,1080],"class":"foot","title":"~","pid":99}`, "500, 500")
		s := &evStream{at: evT0}
		s.tap(evdev.BtnLeft)
		s.tap(evKeyH)
		s.tap(evKeyI)
		s.tap(evdev.KeyEnter)
		e.feed(s)
		e.hypr.set(editorWindow, "")
		s = &evStream{at: evT0.Add(time.Second)}
		s.tap(evKeyT)
		e.feed(s)
	})
	if got := summary(evs); got != `click; input "hi"; key [Enter]; input Document "t"` {
		t.Fatalf("trace: %s", got)
	}
	for _, ev := range evs[:3] {
		if ev.Context != "none" || ev.App != "foot" {
			t.Fatalf("foot's event: %+v", ev)
		}
	}
	if evs[3].Context != "" {
		t.Fatalf("the editor's event: %+v", evs[3])
	}
	if n := strings.Count(e.logged(), "no accessibility for an app"); n != 1 {
		t.Fatalf("logged foot's failure %d times: %s", n, e.logged())
	}
	if _, warned := e.startOut["warning"]; warned {
		t.Fatalf("no start warning with the bus up: %v", e.startOut)
	}
}

// Typing goes in one group per focus: another element or another window flushes it.
func TestWaylandGroupsPerFocus(t *testing.T) {
	e := newWLEnv(t)
	_, evs := e.record(t, func() {
		s := &evStream{at: evT0}
		s.tap(evKeyH)
		e.feed(s)
		e.setFocus(42, &computeruse.Element{Role: "entry", Name: "Search"})
		s = &evStream{at: s.at}
		s.tap(evKeyI)
		e.feed(s)
		e.hypr.set(`{"address":"0x3","class":"foot","title":"~","pid":99}`, "")
		s = &evStream{at: s.at}
		s.tap(evKeyT)
		e.feed(s)
	})
	if got := summary(evs); got != `input Document "h"; input Search "i"; input "t"` {
		t.Fatalf("trace: %s", got)
	}
	if evs[2].App != "foot" || evs[2].Element != nil {
		t.Fatalf("a window AT-SPI knows nothing about: %+v", evs[2])
	}
}

// Text typed while the focused element is a password text never reaches the trace: masked.
func TestWaylandPasswordMasked(t *testing.T) {
	e := newWLEnv(t)
	e.hypr.set(bankWindow, "")
	e.setFocus(7, &computeruse.Element{Role: "password text", Name: "Password", Secure: true})
	tr, evs := e.record(t, func() {
		s := &evStream{at: evT0}
		for _, c := range []uint16{evKeyS, evKeyE, evKeyE, evKeyS, evKeyH} {
			s.tap(c)
		}
		s.tap(evdev.KeyEnter)
		e.feed(s)
	})
	if got := summary(evs); got != `input Password masked; key Password [Enter]` {
		t.Fatalf("trace: %s", got)
	}
	file := mustRead(t, e.r.Path(tr.ID))
	if bytes.Contains(file, []byte("seesh")) || bytes.Contains(file, []byte(`"value"`)) {
		t.Fatalf("the password is in the trace: %s", file)
	}
}

// Without AT-SPI every event says context none, nothing can be masked, and the start says the
// typed text is kept.
func TestWaylandWithoutATSPI(t *testing.T) {
	e := newWLEnv(t)
	e.a11yErr = errors.New("no accessibility bus")
	_, evs := e.record(t, func() {
		s := &evStream{at: evT0}
		s.tap(evKeyH)
		s.tap(evKeyI)
		s.tap(evdev.BtnLeft)
		e.feed(s)
	})
	if got := summary(evs); got != `input "hi"; click` {
		t.Fatalf("trace: %s", got)
	}
	for _, ev := range evs {
		if ev.Context != "none" || ev.App != "org.gnome.TextEditor" {
			t.Fatalf("event without AT-SPI: %+v", ev)
		}
	}
	w, _ := e.startOut["warning"].(string)
	if !strings.Contains(w, "typed text is kept") {
		t.Fatalf("start warning %q", w)
	}
}

// Keyframes: at most one a second; a click that ends typing takes the keyframe (the typing's
// would be the same moment); without a cursor position (sway) there is no point and no crop.
func TestWaylandKeyframes(t *testing.T) {
	e := newWLEnv(t)
	tr, evs := e.record(t, func() {
		s := &evStream{at: evT0}
		s.tap(evKeyH)
		s.tap(evdev.BtnLeft) // flushes "h": one keyframe, the click's
		e.feed(s)
		s = &evStream{at: s.at}
		s.tap(evdev.BtnLeft) // 100 ms later: none
		e.feed(s)
		s = &evStream{at: s.at.Add(time.Second)}
		s.tap(evdev.BtnLeft) // over a second later: one
		e.feed(s)
	})
	if got := summary(evs); got != `input Document "h"; click; click; click` {
		t.Fatalf("trace: %s", got)
	}
	var seqs []int
	for _, k := range tr.Keyframes {
		seqs = append(seqs, k.Seq)
	}
	if len(seqs) != 2 || seqs[0] != 2 || seqs[1] != 4 {
		t.Fatalf("keyframes at %v, want [2 4]", seqs)
	}
	if evs[0].KeyframePoint != nil || evs[2].KeyframePoint != nil || evs[1].KeyframePoint == nil {
		t.Fatalf("keyframe points: %+v", evs)
	}
}

func TestWaylandSwayHasNoCursor(t *testing.T) {
	e := newWLEnv(t)
	tree := `{"type":"root","nodes":[{"type":"output","nodes":[{"type":"workspace","nodes":[
		{"type":"con","id":8,"name":"Inbox","app_id":"thunderbird","pid":42,"focused":true,"rect":{"x":0,"y":0,"width":960,"height":1080}}]}]}]}`
	sway := &swayExec{tree: tree}
	sys := e.system()
	sys.Windows = &dictate.LinuxWindows{X: sway}
	e.src = newWaylandSource(e.r.Dir, sys, slogDiscard())
	e.src.now = e.clock.now
	e.r.Sources[ModeDesktop] = e.src
	tr, evs := e.record(t, func() {
		s := &evStream{at: evT0}
		s.tap(evdev.BtnLeft)
		e.feed(s)
	})
	if len(evs) != 1 || evs[0].X != 0 || evs[0].Y != 0 || evs[0].KeyframePoint != nil || evs[0].App != "thunderbird" {
		t.Fatalf("sway click: %+v", evs)
	}
	if len(tr.Keyframes) != 1 || tr.Keyframes[0].Crop != "" {
		t.Fatalf("sway keyframe: %+v", tr.Keyframes)
	}
}

type swayExec struct{ tree string }

func (s *swayExec) LookPath(n string) bool { return n == "swaymsg" }
func (s *swayExec) Getenv(k string) string {
	return map[string]string{"WAYLAND_DISPLAY": "wayland-1", "SWAYSOCK": "/run/sway"}[k]
}
func (s *swayExec) Exists(string) bool { return false }
func (s *swayExec) Run(_ context.Context, c dictate.Cmd) (string, error) {
	if c.Name == "swaymsg" {
		return s.tree, nil
	}
	return "", errors.New("unexpected " + c.Name)
}

// Refusals: GNOME/KDE (no hyprctl or swaymsg) refuse as unsupported; without the input group
// the start says exactly how to add it.
func TestWaylandRefusals(t *testing.T) {
	gnome := newWLEnv(t)
	gnome.hypr.env = map[string]string{"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "GNOME"}
	tools := &RecordTools{R: gnome.r}
	out, _ := tools.Call(context.Background(), "computer_record_start", map[string]any{"mode": "desktop"}, node.Request{})
	if out["success"] != false || out["error_code"] != "unsupported" || !strings.Contains(out["error"].(string), "GNOME and KDE") {
		t.Fatalf("GNOME: %v", out)
	}

	noGroup := newWLEnv(t)
	noGroup.openErr = evdev.ErrNoAccess
	tools = &RecordTools{R: noGroup.r}
	out, _ = tools.Call(context.Background(), "computer_record_start", map[string]any{"mode": "desktop"}, node.Request{})
	msg, _ := out["error"].(string)
	if out["success"] != false || !strings.Contains(msg, "sudo usermod -aG input $USER") ||
		!strings.Contains(msg, "log out") {
		t.Fatalf("no input group: %v", out)
	}
	if noGroup.r.Current() != "" {
		t.Fatal("a refused start is recording")
	}
	if left, _ := os.ReadDir(noGroup.r.Dir); len(left) != 0 {
		t.Fatalf("a refused start left %v", left)
	}
}

// markFrame: the point (global layout coordinates) lands on the image through the capture's
// geometry, a ring marks it, the crop is 256 px around it clamped to the image; outside the
// image there is neither point nor crop.
func TestMarkFrame(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 400))
	geo := computeruse.Geometry{OriginX: 1000, OriginY: 0, LogicalW: 400, LogicalH: 200, Scale: 2}
	f, err := markFrame(img, geo, &Point{X: 1010, Y: 190})
	if err != nil {
		t.Fatal(err)
	}
	if f.Point == nil || *f.Point != (KeyframePoint{X: 20, Y: 380}) {
		t.Fatalf("point %+v", f.Point)
	}
	full, err := jpeg.Decode(bytes.NewReader(f.JPEG))
	if err != nil {
		t.Fatal(err)
	}
	if !reddish(full.At(20+markRadius, 380)) || reddish(full.At(400, 200)) {
		t.Fatal("the ring isn't drawn around the point")
	}
	crop, err := jpeg.DecodeConfig(bytes.NewReader(f.Crop))
	if err != nil || crop.Width != 256 || crop.Height != 256 {
		t.Fatalf("crop %+v %v", crop, err)
	}
	if reddish(full.At(700, 50)) {
		t.Fatal("marked away from the point")
	}

	out, err := markFrame(img, geo, &Point{X: 10, Y: 10}) // on another monitor
	if err != nil || out.Point != nil || out.Crop != nil || len(out.JPEG) == 0 {
		t.Fatalf("outside: %+v %v", out.Point, err)
	}
	none, err := markFrame(img, geo, nil)
	if err != nil || none.Point != nil || none.Crop != nil {
		t.Fatalf("no point: %+v %v", none.Point, err)
	}
}

func reddish(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xa000 && g < 0x6000 && b < 0x6000
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
