package computer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// usKeymap: keycodes 8.. as a small US layout (two levels).
var usKeymap = x11.Keymap{Min: 8, Per: 2, Syms: []uint32{
	'a', 'A', // 8
	'b', 'B', // 9
	'n', 'N', // 10
	's', 'S', // 11
	'1', '!', // 12
	' ', 0, // 13
	symReturn, 0, // 14
	symTab, symISOLeftTab, // 15
	symBackSpace, 0, // 16
	symShiftL, 0, // 17
	symControlL, 0, // 18
	symAltL, 0, // 19
	symSuperL, 0, // 20
	symCapsLock, 0, // 21
	xkb.KeyLeft, 0, // 22
	symISOLevel3, 0, // 23
	'e', 'E', // 24
	symEscape, 0, // 25
}}

// keycodes in usKeymap.
const (
	kcA, kcB, kcN, kcS, kc1, kcSpace, kcReturn, kcTab, kcBackSpace = 8, 9, 10, 11, 12, 13, 14, 15, 16
	kcShift, kcCtrl, kcAlt, kcSuper, kcCaps, kcLeft, kcAltGr, kcE  = 17, 18, 19, 20, 21, 22, 23, 24
)

// fakeX11 is an X11 session: the RECORD stream the test feeds, the windows under points, the
// active window and AT-SPI's elements.
type fakeX11 struct {
	mu      sync.Mutex
	wayland bool
	noX     bool
	send    func(x11.RecordedInput)
	stopped bool
	windows map[[2]int]x11.Owner
	active  x11.Owner
	at      map[[2]int]*computeruse.Element
	focused map[int]*computeruse.Element // by pid
}

func newFakeX11() *fakeX11 {
	return &fakeX11{windows: map[[2]int]x11.Owner{}, at: map[[2]int]*computeruse.Element{}, focused: map[int]*computeruse.Element{}}
}

func (f *fakeX11) Wayland() bool { return f.wayland }
func (f *fakeX11) Session() (bool, string) {
	if f.wayland || f.noX {
		return false, "no X11"
	}
	return true, ""
}

func (f *fakeX11) Record(send func(x11.RecordedInput)) (x11.Keymap, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.send = send
	return usKeymap, func() { f.mu.Lock(); f.stopped = true; f.mu.Unlock() }, nil
}

func (f *fakeX11) WindowAt(_ context.Context, x, y int) (x11.Owner, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.windows[[2]int{x, y}], nil
}

func (f *fakeX11) ActiveOwner(context.Context) (x11.Owner, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, nil
}

func (f *fakeX11) access() x11Access {
	return x11Access{
		ElementAt: func(pid, x, y int) (*computeruse.Element, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.at[[2]int{x, y}], nil
		},
		Focused: func(pid int) (*computeruse.Element, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.focused[pid], nil
		},
	}
}

func (f *fakeX11) focus(pid int, el *computeruse.Element) {
	f.mu.Lock()
	f.focused[pid] = el
	f.mu.Unlock()
}

// ev feeds the RECORD stream one device event.
func (f *fakeX11) ev(typ, detail byte, x, y int) {
	f.mu.Lock()
	send := f.send
	f.mu.Unlock()
	send(x11.RecordedInput{Type: typ, Detail: detail, RootX: x, RootY: y})
}

func (f *fakeX11) click(x, y int)  { f.ev(x11.ButtonPress, x11ButtonLeft, x, y) }
func (f *fakeX11) tap(code byte)   { f.ev(x11.KeyPress, code, 0, 0); f.ev(x11.KeyRelease, code, 0, 0) }
func (f *fakeX11) down(code byte)  { f.ev(x11.KeyPress, code, 0, 0) }
func (f *fakeX11) up(code byte)    { f.ev(x11.KeyRelease, code, 0, 0) }
func (f *fakeX11) chord(m, k byte) { f.down(m); f.tap(k); f.up(m) }
func (f *fakeX11) typeCodes(c ...byte) {
	for _, k := range c {
		f.tap(k)
	}
}

var (
	notesWin   = x11.Owner{ID: 0x400001, PID: 77, Class: "Notes"}
	bankWin    = x11.Owner{ID: 0x400002, PID: 88, Class: "Bank"}
	x11NewNote = &computeruse.Element{Role: "button", Name: "New note"}
	x11Title   = &computeruse.Element{Role: "textbox", Name: "Title"}
	x11Pass    = &computeruse.Element{Role: "passwordtext", Name: "Password", Secure: true}
)

// x11Env is a recorder whose desktop mode is an X11 session.
func x11Env(t *testing.T, f *fakeX11) *recEnv {
	e := newRecEnv(t)
	src := newX11Source(e.r.Dir, f, f.access(), nil, nil)
	e.r.Sources[ModeDesktop] = src
	return e
}

func traceEvents(t *testing.T, tr Trace) []DesktopEvent {
	t.Helper()
	out := make([]DesktopEvent, len(tr.Events))
	for i, r := range tr.Events {
		if err := json.Unmarshal(r, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// A recording on X11: RECORD's clicks resolve to the AT-SPI element under them in the window
// there (its app is the window's class), keystrokes are grouped into the focused element's text
// with the keymap's levels, and Enter, shortcuts and scrolls are events of their own.
func TestX11ClicksRecorded(t *testing.T) {
	f := newFakeX11()
	e := x11Env(t, f)
	f.windows[[2]int{120, 40}] = notesWin
	f.windows[[2]int{300, 200}] = notesWin
	f.at[[2]int{120, 40}] = x11NewNote
	f.active = notesWin
	id, err := e.r.Start(context.Background(), ModeDesktop)
	if err != nil {
		t.Fatal(err)
	}
	f.click(120, 40)
	f.focus(notesWin.PID, x11Title)
	f.chord(kcShift, kcB)
	f.typeCodes(kcA, kcN, kcN, kcSpace, kc1, kcBackSpace)
	f.chord(kcShift, kc1)
	f.tap(kcReturn)
	f.chord(kcCtrl, kcS)
	f.ev(x11.ButtonPress, x11WheelDown, 300, 200)
	f.ev(x11.ButtonPress, x11WheelDown, 300, 200)
	f.ev(x11.ButtonPress, x11ButtonRight, 300, 200)
	tr, err := e.r.Stop(id)
	if err != nil {
		t.Fatal(err)
	}
	if !f.stopped {
		t.Fatal("RECORD still runs")
	}
	evs := traceEvents(t, tr)
	if got, want := summary(evs), `click New note; input Title "Bann !"; key Title [Enter]; key Title [ctrl+s]; scroll; click`; got != want {
		t.Fatalf("trace:\n got %s\nwant %s", got, want)
	}
	if c := evs[0]; c.App != "Notes" || c.X != 120 || c.Y != 40 || c.Element.Role != "button" || c.Button != "" {
		t.Fatalf("click %+v", c)
	}
	if s := evs[4]; s.DY != 2 || s.X != 300 {
		t.Fatalf("scroll %+v", s)
	}
	if r := evs[5]; r.Button != "right" || r.Element != nil {
		t.Fatalf("right click on nothing AT-SPI knows %+v", r)
	}
}

// Typing into an AT-SPI password text is masked: no keystroke of it reaches the trace file.
func TestPasswordFocusDropped(t *testing.T) {
	f := newFakeX11()
	e := x11Env(t, f)
	f.active = bankWin
	id, err := e.r.Start(context.Background(), ModeDesktop)
	if err != nil {
		t.Fatal(err)
	}
	f.focus(bankWin.PID, x11Pass)
	f.typeCodes(kcS, kcE, kcA, kcB, kcBackSpace)
	f.tap(kcTab)
	tr, err := e.r.Stop(id)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summary(traceEvents(t, tr)), `input Password masked; key Password [Tab]`; got != want {
		t.Fatalf("trace:\n got %s\nwant %s", got, want)
	}
	file, _ := os.ReadFile(e.r.Path(id))
	if strings.Contains(string(file), "seab") || strings.Contains(string(file), `"sea`) {
		t.Fatalf("the password's keystrokes are in the trace: %s", file)
	}
}

// The X11 source in a Wayland session refuses as unsupported (record_linux.go hands Wayland to
// the Wayland source); without any X display it is the general "can't record the desktop".
func TestWaylandDesktopRecordingRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		desk x11Desk
		want string
	}{
		{"wayland", &x11.Display{Getenv: func(k string) string {
			return map[string]string{"WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":0"}[k]
		}}, ErrWaylandRecording.Error()},
		{"no display", &fakeX11{noX: true}, ErrDesktopRecording.Error()},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newRecEnv(t)
			e.r.Sources[ModeDesktop] = newX11Source(e.r.Dir, c.desk, x11Access{}, nil, nil)
			tools := &RecordTools{R: e.r}
			out, _ := tools.Call(context.Background(), "computer_record_start", map[string]any{"mode": "desktop"}, node.Request{})
			if out["success"] != false || out["error_code"] != "unsupported" || out["error"] != c.want {
				t.Fatalf("start %v", out)
			}
			if e.r.Current() != "" {
				t.Fatal("a refused start is recording")
			}
			if left, _ := os.ReadDir(e.r.Dir); len(left) != 0 {
				t.Fatalf("a refused start left %v", left)
			}
			// The browser still records (stopped, so Windows can remove the open trace file).
			id, err := e.r.Start(context.Background(), ModeBrowser)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.r.Stop(id); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := newX11Source("", &fakeX11{wayland: true}, x11Access{}, nil, nil).Tap(func(DeskInput) {}); !errors.Is(err, ErrWaylandRecording) {
		t.Fatalf("tap on Wayland: %v", err)
	}
}

// The key decoder: levels from Shift and Caps Lock, AltGr's level without a shortcut,
// modifiers tracked from their own presses (RECORD's state can be empty) or the state bits.
func TestX11KeysDecoded(t *testing.T) {
	k := &x11Keys{km: usKeymap}
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	press := func(code byte, state uint16) string {
		in, ok := k.input(x11.RecordedInput{Type: x11.KeyPress, Detail: code, State: state}, at)
		if !ok {
			return "-"
		}
		if in.Text != "" {
			return in.Text
		}
		return "[" + in.Key + "]"
	}
	rel := func(code byte) { k.input(x11.RecordedInput{Type: x11.KeyRelease, Detail: code}, at) }
	var got []string
	add := func(s ...string) { got = append(got, s...) }
	add(press(kcA, 0), press(kcA, x11ShiftMask), press(kcShift, 0), press(kcA, 0), press(kcTab, 0))
	rel(kcShift)
	add(press(kcA, 0), press(kcCaps, 0), press(kcA, 0), press(kc1, 0), press(kcShift, 0), press(kcA, 0))
	rel(kcShift)
	add(press(kcCaps, 0), press(kcAlt, 0), press(kcN, 0))
	rel(kcAlt)
	add(press(kcN, x11ControlMask|x11ShiftMask), press(kcSuper, 0), press(kcSpace, 0))
	rel(kcSuper)
	add(press(kcAltGr, 0), press(kcE, x11ShiftMask), press(kcLeft, 0), press(kcBackSpace, 0), press(25, 0), press(kcCtrl, 0), press(kcReturn, 0))
	rel(kcCtrl)
	want := []string{"a", "A", "-", "A", "[shift+tab]",
		"a", "-", "A", "1", "-", "a",
		"-", "-", "[alt+n]",
		"[ctrl+shift+n]", "-", "[other]",
		"-", "E", "[other]", "[Backspace]", "[Escape]", "-", "[ctrl+enter]"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("decoded:\n got %q\nwant %q", got, want)
	}
	// A layout with AltGr levels (core index 4: group 1 level 3): AltGr+E is the euro sign.
	k = &x11Keys{km: x11.Keymap{Min: 8, Per: 6, Syms: []uint32{'e', 'E', 'e', 'E', symEuroSign, 0, symISOLevel3, 0, 0, 0, 0, 0}}}
	got = nil
	add(press(9, 0), press(8, 0))
	rel(9)
	add(press(8, 0))
	if strings.Join(got, " ") != "- € e" {
		t.Fatalf("AltGr: %q", got)
	}
	if in, ok := k.input(x11.RecordedInput{Type: x11.ButtonPress, Detail: 2}, at); ok {
		t.Fatalf("the middle button is %+v", in)
	}
	if in, _ := k.input(x11.RecordedInput{Type: x11.ButtonPress, Detail: x11WheelLeft, RootX: 5, RootY: 6}, at); in.Kind != InScroll || in.DX != -1 || in.DY != 0 || in.X != 5 {
		t.Fatalf("wheel left %+v", in)
	}
}
