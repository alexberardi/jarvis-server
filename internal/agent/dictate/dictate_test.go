package dictate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{
		"  hello world ":              "hello world",
		"line one\nline two\tend":     "line one line two end",
		"rm -rf /\x1b[2J\x07ok":       "rm -rf /[2Jok",
		"abc‮def​":                    "abcdef", // bidi override, zero width space
		"héllo wörld ✓ 日本語 😀":         "héllo wörld ✓ 日本語 😀",
		"submit\r\n":                  "submit",
		"tell me \"quotes\" & <tags>": "tell me \"quotes\" & <tags>",
	} {
		got, err := Sanitize(in)
		if err != nil || got != want {
			t.Errorf("Sanitize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  \n\t ", "\x00\x1b", strings.Repeat("x", MaxText+1)} {
		if _, err := Sanitize(bad); !errors.Is(err, errBadArgs) {
			t.Errorf("Sanitize(%.20q) accepted", bad)
		}
	}
	if s, err := Sanitize(strings.Repeat("é", MaxText)); err != nil || len([]rune(s)) != MaxText {
		t.Error("MaxText characters (not bytes) must be accepted")
	}
}

func TestParseKey(t *testing.T) {
	for in, want := range map[string]Key{
		"Enter": KeyEnter, "return": KeyEnter, "the enter key": KeyEnter, "ESC": KeyEscape, "Shift+Tab": KeyShiftTab,
		"shift tab": KeyShiftTab, "shift-tab": KeyShiftTab, "Shift + Tab": KeyShiftTab, "space": KeySpace,
		"up arrow": KeyUp, "Left": KeyLeft, "arrow down": KeyDown,
	} {
		if got, err := ParseKey(in); err != nil || got != want {
			t.Errorf("ParseKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ctrl+c", "alt+f4", "delete", "super", "a"} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) accepted", bad)
		}
	}
}

func TestClassifyAnswer(t *testing.T) {
	for in, want := range map[string]Answer{
		"OK": AnswerYes, "yes": AnswerYes, "Yes, send it.": AnswerYes, "confirm": AnswerYes, "go ahead": AnswerYes,
		"okay": AnswerYes, "sure": AnswerYes, "yep do it": AnswerYes,
		"cancel": AnswerNo, "no": AnswerNo, "stop": AnswerNo, "no, don't send it": AnswerNo, "don’t": AnswerNo,
		"yes... no wait": AnswerNo, "never mind": AnswerNo,
		"": AnswerUnclear, "I didn't catch that, sorry.": AnswerUnclear, "banana": AnswerUnclear, "notes": AnswerUnclear,
	} {
		if got := ClassifyAnswer(in); got != want {
			t.Errorf("ClassifyAnswer(%q) = %d, want %d", in, got, want)
		}
	}
}

// --- fakes ---

type fakeExec struct {
	env   map[string]string
	bins  map[string]bool
	files map[string]bool
	ran   []Cmd
	out   func(c Cmd) (string, error)
}

func (f *fakeExec) LookPath(n string) bool { return f.bins[n] }
func (f *fakeExec) Getenv(k string) string { return f.env[k] }
func (f *fakeExec) Exists(p string) bool   { return f.files[p] }
func (f *fakeExec) Run(_ context.Context, c Cmd) (string, error) {
	f.ran = append(f.ran, c)
	if f.out != nil {
		return f.out(c)
	}
	return "", nil
}

func bins(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func (c Cmd) line() string { return c.Name + " " + strings.Join(c.Args, " ") }

func TestLinuxKeyboardBackends(t *testing.T) {
	hypr := map[string]string{"WAYLAND_DISPLAY": "wayland-1", "XDG_CURRENT_DESKTOP": "Hyprland", "XDG_RUNTIME_DIR": "/run/user/1000"}
	gnome := map[string]string{"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME", "XDG_RUNTIME_DIR": "/run/user/1000"}
	x11 := map[string]string{"DISPLAY": ":0"}
	ysock := map[string]bool{"/run/user/1000/.ydotool_socket": true}
	for _, c := range []struct {
		name  string
		env   map[string]string
		bins  map[string]bool
		files map[string]bool
		want  string
	}{
		{"hyprland wtype (no virtual keyboard)", hypr, bins("wtype", "ydotool"), ysock, beWtype},
		{"hyprland no wtype, ydotool", hypr, bins("ydotool"), ysock, beYdotool},
		{"ydotool without its daemon", hypr, bins("ydotool"), nil, ""},
		{"gnome ignores wtype", gnome, bins("wtype"), nil, ""},
		{"gnome ydotool", gnome, bins("wtype", "ydotool"), ysock, beYdotool},
		{"x11 xdotool", x11, bins("xdotool", "wtype"), nil, beXdotool},
		{"x11 nothing", x11, bins("wtype"), nil, ""},
		{"headless", map[string]string{}, bins("wtype", "xdotool"), nil, ""},
	} {
		k := &LinuxKeyboard{X: &fakeExec{env: c.env, bins: c.bins, files: c.files}, Wayland: &fakeInjector{why: "no protocol"}}
		got, why := k.backend(context.Background())
		if got != c.want || (got == "" && why == "") {
			t.Errorf("%s: backend %q (%q), want %q", c.name, got, why, c.want)
		}
		if ok, _ := k.Available(context.Background()); ok != (c.want != "") {
			t.Errorf("%s: Available %v", c.name, ok)
		}
	}
}

// fakeInjector is a built-in backend (the Wayland virtual keyboard) that records strokes.
type fakeInjector struct {
	ok   bool
	why  string
	err  error
	sent [][]xkb.Stroke
}

func (f *fakeInjector) Name() string { return "fake-native" }
func (f *fakeInjector) Available(context.Context) (bool, string) {
	return f.ok, f.why
}
func (f *fakeInjector) Send(_ context.Context, s []xkb.Stroke) error {
	f.sent = append(f.sent, s)
	return f.err
}

// The built-in virtual keyboard wins over every helper whenever the compositor offers it, and
// only on Wayland.
func TestLinuxKeyboardPrefersTheBuiltInVirtualKeyboard(t *testing.T) {
	ctx := context.Background()
	hypr := map[string]string{"WAYLAND_DISPLAY": "wayland-1", "XDG_CURRENT_DESKTOP": "Hyprland"}
	nat := &fakeInjector{ok: true}
	f := &fakeExec{env: hypr, bins: bins("wtype", "ydotool")}
	k := &LinuxKeyboard{X: f, Wayland: nat}
	if b, _ := k.backend(ctx); b != beWayland || k.Name() != "fake-native" {
		t.Fatalf("backend %q name %q", b, k.Name())
	}
	if err := k.Type(ctx, "hé 日"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []Key{KeyEnter, KeyEscape, KeyTab, KeyShiftTab, KeySpace, KeyUp, KeyDown, KeyLeft, KeyRight} {
		if err := k.Press(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.ran) != 0 {
		t.Fatalf("ran helpers: %v", f.ran)
	}
	if got := nat.sent[0]; len(got) != 4 || got[0].Sym != 'h' || got[1].Sym != 0xe9 || got[2].Sym != xkb.KeySpace || got[3].Sym != 0x10065e5 {
		t.Fatalf("text strokes %v", got)
	}
	want := []xkb.Stroke{{Sym: xkb.KeyReturn}, {Sym: xkb.KeyEscape}, {Sym: xkb.KeyTab}, {Sym: xkb.KeyTab, Shift: true},
		{Sym: xkb.KeySpace}, {Sym: xkb.KeyUp}, {Sym: xkb.KeyDown}, {Sym: xkb.KeyLeft}, {Sym: xkb.KeyRight}}
	for i, w := range want {
		if got := nat.sent[i+1]; len(got) != 1 || got[0] != w {
			t.Errorf("key %d: %v, want %v", i, got, w)
		}
	}
	if err := k.Press(ctx, Key("ctrl+c")); !errors.Is(err, errBadArgs) {
		t.Fatalf("unknown key: %v", err)
	}

	// A failure is reported, not retried through a helper (the keys may have half-arrived).
	nat.err = errors.New("compositor said no")
	if err := k.Type(ctx, "x"); err == nil || len(f.ran) != 0 {
		t.Fatalf("err %v, helpers %v", err, f.ran)
	}

	// Not offered: the helpers as before. Not Wayland: never asked.
	k.Wayland = &fakeInjector{why: "no protocol"}
	if b, _ := k.backend(ctx); b != beWtype {
		t.Fatalf("fallback %q", b)
	}
	x := &LinuxKeyboard{X: &fakeExec{env: map[string]string{"DISPLAY": ":0"}, bins: bins("xdotool")}, Wayland: &fakeInjector{ok: true}}
	if b, _ := x.backend(ctx); b != beXdotool {
		t.Fatalf("x11 picked %q", b)
	}
	none := &LinuxKeyboard{X: &fakeExec{env: hypr}, Wayland: &fakeInjector{why: "no protocol"}}
	if ok, why := none.Available(ctx); ok || !strings.Contains(why, "virtual-keyboard") {
		t.Fatalf("%v %q", ok, why)
	}
}

func TestLinuxKeyboardCommands(t *testing.T) {
	ctx := context.Background()
	f := &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w"}, bins: bins("wtype")}
	k := &LinuxKeyboard{X: f, Wayland: &fakeInjector{why: "no protocol"}}
	if err := k.Type(ctx, "héllo -k Return"); err != nil {
		t.Fatal(err)
	}
	_ = k.Press(ctx, KeyEnter)
	_ = k.Press(ctx, KeyShiftTab)
	want := []string{"wtype -s 40 -", "wtype -s 40 -k Return", "wtype -s 40 -M shift -k Tab -m shift"}
	for i, c := range f.ran {
		if c.line() != want[i] {
			t.Errorf("wtype %d: %q, want %q", i, c.line(), want[i])
		}
	}
	// The text goes on stdin, never argv (a "-k Return" in dictation stays text), with a UTF-8 locale.
	if f.ran[0].Stdin != "héllo -k Return" || !slices.Contains(f.ran[0].Env, "LC_ALL=C.UTF-8") {
		t.Errorf("wtype stdin %q env %v", f.ran[0].Stdin, f.ran[0].Env)
	}

	f = &fakeExec{env: map[string]string{"DISPLAY": ":0"}, bins: bins("xdotool")}
	k = &LinuxKeyboard{X: f}
	_ = k.Type(ctx, "x")
	_ = k.Press(ctx, KeyShiftTab)
	_ = k.Press(ctx, KeyEscape)
	if got := []string{f.ran[0].line(), f.ran[1].line(), f.ran[2].line()}; !slices.Equal(got, []string{
		"xdotool type --clearmodifiers --delay 6 --file -", "xdotool key --clearmodifiers shift+Tab", "xdotool key --clearmodifiers Escape"}) {
		t.Errorf("xdotool %q", got)
	}

	f = &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w", "YDOTOOL_SOCKET": "/s"}, bins: bins("ydotool"), files: map[string]bool{"/s": true}}
	k = &LinuxKeyboard{X: f}
	if err := k.Type(ctx, "héllo"); !errors.Is(err, ErrUnavailable) || len(f.ran) != 0 {
		t.Errorf("ydotool typed non-ASCII: %v", err)
	}
	_ = k.Type(ctx, "git status")
	_ = k.Press(ctx, KeyShiftTab)
	if f.ran[0].line() != "ydotool type --file -" || f.ran[0].Stdin != "git status" || f.ran[1].line() != "ydotool key 42:1 15:1 15:0 42:0" {
		t.Errorf("ydotool %q", f.ran)
	}

	k = &LinuxKeyboard{X: &fakeExec{}}
	if err := k.Type(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("headless type: %v", err)
	}
}

const hyprClients = `[
 {"address":"0x55aa01","title":"Docs — Mozilla Firefox","class":"firefox","initialClass":"firefox","workspace":{"id":1},"mapped":true,"hidden":false,"focusHistoryID":1},
 {"address":"0x55aa02","title":"✳ claude","class":"foot","initialClass":"foot","workspace":{"id":2},"mapped":true,"hidden":false,"focusHistoryID":0},
 {"address":"0x55aa03","title":"Claude","class":"Claude","initialClass":"Claude","workspace":{"id":3},"mapped":true,"hidden":false,"focusHistoryID":2},
 {"address":"bogus","title":"x","class":"evil","workspace":{"id":1},"mapped":true},
 {"address":"0x55aa04","title":"unmapped","class":"slack","workspace":{"id":1},"mapped":false}
]`

func hyprExec(active *string) *fakeExec {
	return &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w", "HYPRLAND_INSTANCE_SIGNATURE": "sig"}, bins: bins("hyprctl", "wtype"),
		out: func(c Cmd) (string, error) {
			switch c.line() {
			case "hyprctl -j clients":
				return hyprClients, nil
			case "hyprctl -j monitors":
				return `[{"activeWorkspace":{"id":1},"specialWorkspace":{"id":0}},{"activeWorkspace":{"id":2},"specialWorkspace":{"id":0}}]`, nil
			case "hyprctl -j activewindow":
				if *active == "" {
					return "{}", nil
				}
				return fmt.Sprintf(`{"address":%q}`, *active), nil
			}
			if strings.HasPrefix(c.line(), "hyprctl dispatch focuswindow address:") {
				*active = strings.TrimPrefix(c.Args[2], "address:")
				return "ok", nil
			}
			return "", fmt.Errorf("unexpected %q", c.line())
		}}
}

func TestHyprlandWindows(t *testing.T) {
	ctx := context.Background()
	active := "0x55aa02"
	f := hyprExec(&active)
	w := &LinuxWindows{X: f}
	ws, err := w.List(ctx)
	if err != nil || len(ws) != 3 {
		t.Fatalf("list %v %v", ws, err)
	}
	if !ws[0].Visible || !ws[1].Visible || ws[2].Visible || ws[1].Recency != 0 {
		t.Errorf("visibility/recency %+v", ws)
	}
	if a, _ := w.Active(ctx); !w.Same("0x55AA02", a) {
		t.Errorf("active %q", a)
	}
	if err := w.Focus(ctx, "0x55aa03"); err != nil || active != "0x55aa03" {
		t.Errorf("focus %v %q", err, active)
	}
	if err := w.Focus(ctx, "0x1; exec rm"); err == nil {
		t.Error("an unvalidated address reached hyprctl")
	}
}

// A Hyprland configured in Lua reads `hyprctl dispatch` as Lua: the classic focuswindow is a
// syntax error there (exit 7), hl.dsp.focus works.
func TestHyprlandFocusWithALuaConfig(t *testing.T) {
	ctx := context.Background()
	active := "0x55aa02"
	f := hyprExec(&active)
	classic := f.out
	f.out = func(c Cmd) (string, error) {
		if strings.HasPrefix(c.line(), "hyprctl dispatch focuswindow") {
			return "", errors.New(`hyprctl: Error: [string "return hl.dispatch(focuswindow address:0x55..."]:1: ')' expected near 'address'`)
		}
		if c.line() == `hyprctl dispatch hl.dsp.focus({ window = "address:0x55aa03" })` {
			active = "0x55aa03"
			return "ok", nil
		}
		return classic(c)
	}
	w := &LinuxWindows{X: f}
	if err := w.Focus(ctx, "0x55aa03"); err != nil || active != "0x55aa03" {
		t.Fatalf("focus %v %q (ran %v)", err, active, f.ran)
	}
	// Both refused: the classic error is the one reported.
	f.out = func(c Cmd) (string, error) {
		if strings.HasPrefix(c.line(), "hyprctl dispatch") {
			return "", errors.New("hyprctl: exit status 7")
		}
		return classic(c)
	}
	if err := w.Focus(ctx, "0x55aa01"); err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("err %v", err)
	}
}

func TestHyprX11Focused(t *testing.T) {
	ctx := context.Background()
	win := `{"address":"0x1","xwayland":true}`
	f := &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w", "HYPRLAND_INSTANCE_SIGNATURE": "sig"}, bins: bins("hyprctl"),
		out: func(c Cmd) (string, error) {
			if c.line() == "hyprctl -j activewindow" {
				return win, nil
			}
			return "", errors.New("unexpected")
		}}
	if !HyprX11Focused(f)(ctx) {
		t.Error("xwayland window not seen")
	}
	win = `{"address":"0x1","xwayland":false}`
	if HyprX11Focused(f)(ctx) {
		t.Error("wayland window taken for X11")
	}
	win = "{}"
	if HyprX11Focused(f)(ctx) {
		t.Error("no window taken for X11")
	}
	if HyprX11Focused(&fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w", "SWAYSOCK": "/s"}, bins: bins("hyprctl")})(ctx) {
		t.Error("not Hyprland")
	}
}

const swayTree = `{"id":1,"type":"root","nodes":[
 {"id":2,"type":"output","name":"HEADLESS-1","current_workspace":"1","focus":[3,9],"nodes":[
  {"id":3,"type":"workspace","name":"1","focus":[5,4],"nodes":[
   {"id":4,"type":"con","name":"alpha","app_id":"footA","pid":10,"visible":true},
   {"id":5,"type":"con","name":"beta","app_id":"footB","pid":11,"visible":true,"focused":true}]},
  {"id":9,"type":"workspace","name":"2","nodes":[{"id":6,"type":"con","name":"Slack","app_id":"Slack","pid":12}]}]}]}`

func TestSwayWindows(t *testing.T) {
	ctx := context.Background()
	f := &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w", "SWAYSOCK": "/s"}, bins: bins("swaymsg"),
		out: func(c Cmd) (string, error) {
			if c.line() == "swaymsg -t get_tree" {
				return swayTree, nil
			}
			return "", nil
		}}
	w := &LinuxWindows{X: f}
	ws, err := w.List(ctx)
	if err != nil || len(ws) != 3 || ws[0].ID != "5" || ws[1].ID != "4" || ws[2].Visible {
		t.Fatalf("list %+v %v", ws, err)
	}
	if a, _ := w.Active(ctx); a != "5" {
		t.Errorf("active %q", a)
	}
	_ = w.Focus(ctx, "4")
	if got := f.ran[len(f.ran)-1].line(); got != "swaymsg [con_id=4] focus" {
		t.Errorf("focus %q", got)
	}
	if err := w.Focus(ctx, "4] kill; [con_id=5"); err == nil {
		t.Error("an unvalidated id reached swaymsg")
	}
}

// fakeWM is the built-in EWMH client as dictation sees it.
type fakeWM struct {
	ok     bool
	active uint32
}

func (f *fakeWM) WMAvailable(context.Context) (bool, string) { return f.ok, "no EWMH window manager" }
func (f *fakeWM) Active(context.Context) (uint32, error)     { return f.active, nil }

// On X11 the built-in XTEST keyboard wins over xdotool, and is never asked on Wayland.
func TestLinuxKeyboardPrefersXTEST(t *testing.T) {
	ctx := context.Background()
	nat := &fakeInjector{ok: true}
	f := &fakeExec{env: map[string]string{"DISPLAY": ":0"}, bins: bins("xdotool", "ydotool")}
	k := &LinuxKeyboard{X: f, X11: nat, Wayland: &fakeInjector{ok: true}}
	if b, _ := k.backend(ctx); b != beX11 || k.Name() != "fake-native" {
		t.Fatalf("backend %q", b)
	}
	_ = k.Type(ctx, "ñ")
	_ = k.Press(ctx, KeyShiftTab)
	if len(f.ran) != 0 || len(nat.sent) != 2 || nat.sent[0][0].Sym != 0xf1 || nat.sent[1][0] != (xkb.Stroke{Sym: xkb.KeyTab, Shift: true}) {
		t.Fatalf("ran %v sent %v", f.ran, nat.sent)
	}
	k.X11 = &fakeInjector{why: "no XTEST"}
	if b, _ := k.backend(ctx); b != beXdotool {
		t.Fatalf("fallback %q", b)
	}
	w := &LinuxKeyboard{X: &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w", "DISPLAY": ":0"}, bins: bins("ydotool")},
		X11: &fakeInjector{ok: true}, Wayland: &fakeInjector{why: "no"}}
	if b, _ := w.backend(ctx); b == beX11 {
		t.Fatal("XTEST on Wayland (it would only reach XWayland apps)")
	}
}

// probeCounter is an Injector that counts how often it was asked.
type probeCounter struct {
	fakeInjector
	probes int
}

func (p *probeCounter) Available(ctx context.Context) (bool, string) {
	p.probes++
	return p.fakeInjector.Available(ctx)
}

// GNOME and KDE type through the RemoteDesktop portal once the user allowed it; until then
// status carries the portal's reason (the one command to run). It's never asked where a
// virtual keyboard or wtype works.
func TestLinuxKeyboardPortal(t *testing.T) {
	ctx := context.Background()
	gnome := map[string]string{"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME"}
	por := &probeCounter{fakeInjector: fakeInjector{ok: true}}
	f := &fakeExec{env: gnome, bins: bins("ydotool"), files: map[string]bool{"/tmp/.ydotool_socket": true}}
	k := &LinuxKeyboard{X: f, Wayland: &fakeInjector{why: "GNOME has no virtual-keyboard protocol"}, X11: &fakeInjector{ok: true}, Portal: por}
	if b, _ := k.backend(ctx); b != bePortal || k.Name() != "fake-native" {
		t.Fatalf("backend %q", b)
	}
	_ = k.Type(ctx, "日本")
	_ = k.Press(ctx, KeyEnter)
	if len(f.ran) != 0 || len(por.sent) != 2 || por.sent[1][0].Sym != xkb.KeyReturn {
		t.Fatalf("ran %v sent %v", f.ran, por.sent)
	}

	// Not allowed yet: off, with the portal's reason (ydotool absent).
	k = &LinuxKeyboard{X: &fakeExec{env: gnome}, Wayland: &fakeInjector{why: "no"},
		Portal: &fakeInjector{why: "typing on this desktop needs a one-time permission: run `jarvis-agent dictation allow`"}}
	if ok, why := k.Available(ctx); ok || !strings.Contains(why, "dictation allow") {
		t.Fatalf("%v %q", ok, why)
	}
	// ...but ydotool with its daemon still works meanwhile.
	k.X = &fakeExec{env: gnome, bins: bins("ydotool"), files: map[string]bool{"/tmp/.ydotool_socket": true}}
	if b, _ := k.backend(ctx); b != beYdotool {
		t.Fatalf("got %q", b)
	}

	// The virtual keyboard or wtype wins, and the portal (a D-Bus round trip) isn't even asked.
	hypr := map[string]string{"WAYLAND_DISPLAY": "wayland-1", "XDG_CURRENT_DESKTOP": "Hyprland"}
	por = &probeCounter{fakeInjector: fakeInjector{ok: true}}
	k = &LinuxKeyboard{X: &fakeExec{env: hypr, bins: bins("wtype")}, Wayland: &fakeInjector{ok: true}, Portal: por}
	_, _ = k.backend(ctx)
	k.Wayland = &fakeInjector{why: "no"}
	if b, _ := k.backend(ctx); b != beWtype || por.probes != 0 {
		t.Fatalf("backend %q, portal probed %d times", b, por.probes)
	}
	// X11 never uses it.
	x := &LinuxKeyboard{X: &fakeExec{env: map[string]string{"DISPLAY": ":0"}}, Portal: &fakeInjector{ok: true}}
	if b, _ := x.backend(ctx); b == bePortal {
		t.Fatal("portal on X11")
	}
}

func TestX11ActiveAsksTheWindowManagerFirst(t *testing.T) {
	ctx := context.Background()
	f := &fakeExec{bins: bins("xdotool"), out: func(Cmd) (string, error) { return "999", nil }}
	got, err := X11Active(f, &fakeWM{ok: true, active: 0x04000007})(ctx)
	if err != nil || got != "67108871" || len(f.ran) != 0 || !SameX11("0x04000007", got) {
		t.Fatalf("%q %v ran %v", got, err, f.ran)
	}
	got, _ = X11Active(f, &fakeWM{})(ctx)
	if got != "999" || len(f.ran) != 1 || f.ran[0].line() != "xdotool getactivewindow" {
		t.Fatalf("xdotool fallback: %q", got)
	}
	// With the built-in client, naming an app on X11 needs no xdotool.
	lw := &LinuxWindows{X: &fakeExec{env: map[string]string{"DISPLAY": ":0"}}, XWM: &fakeWM{ok: true},
		X11: &HostWindows{H: host.New(&fakeBackend{}), ActiveFn: func(context.Context) (string, error) { return "", nil }}}
	if _, why := lw.Available(ctx); strings.Contains(why, "xdotool") {
		t.Fatalf("still asks for xdotool: %q", why)
	}
}

func TestLinuxWindowsUnavailable(t *testing.T) {
	ctx := context.Background()
	gnome := &LinuxWindows{X: &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w"}, bins: bins("hyprctl", "swaymsg", "wmctrl", "xdotool")}}
	if ok, why := gnome.Available(ctx); ok || why == "" {
		t.Error("GNOME/KDE Wayland must not claim window control")
	}
	noXdotool := &LinuxWindows{X: &fakeExec{env: map[string]string{"DISPLAY": ":0"}, bins: bins("wmctrl")}, X11: &HostWindows{}}
	if ok, _ := noXdotool.Available(ctx); ok {
		t.Error("X11 without xdotool can't confirm focus")
	}
}

func TestSameIDs(t *testing.T) {
	if !SameX11("0x04000007", "67108871") || SameX11("0x04000007", "67108872") || SameX11("", "0") {
		t.Error("SameX11")
	}
	if !SameMacPid("412/2", "412") || SameMacPid("412/2", "41") || SameMacPid("412", "412") {
		t.Error("SameMacPid")
	}
}

// --- tools ---

type fakeKB struct {
	ok     bool
	typed  []string
	keys   []Key
	onType func()
}

func (f *fakeKB) Name() string                             { return "fake" }
func (f *fakeKB) Available(context.Context) (bool, string) { return f.ok, "no keyboard here" }
func (f *fakeKB) Press(_ context.Context, k Key) error     { f.keys = append(f.keys, k); return nil }
func (f *fakeKB) Type(_ context.Context, s string) error {
	f.typed = append(f.typed, s)
	if f.onType != nil {
		f.onType()
	}
	return nil
}

type fakeWin struct {
	ws       []Window
	active   string
	focusLag int // Active polls before a focus takes effect; -1: never
	pending  string
	focused  []string
}

func (f *fakeWin) Available(context.Context) (bool, string) { return true, "" }
func (f *fakeWin) List(context.Context) ([]Window, error)   { return f.ws, nil }
func (f *fakeWin) Same(id, active string) bool              { return id == active }
func (f *fakeWin) Focus(_ context.Context, id string) error {
	f.focused = append(f.focused, id)
	f.pending = id
	if f.focusLag == 0 {
		f.active = id
	}
	return nil
}
func (f *fakeWin) Active(context.Context) (string, error) {
	if f.pending != "" && f.focusLag > 0 {
		f.focusLag--
		if f.focusLag == 0 {
			f.active = f.pending
		}
	}
	return f.active, nil
}

func newWin() *fakeWin {
	return &fakeWin{active: "term", ws: []Window{
		{ID: "term", Apps: []string{"foot"}, Title: "~/jarvis", Recency: 0, Visible: true},
		{ID: "claude", Apps: []string{"Claude"}, Title: "Claude", Recency: 1, Visible: false},
		{ID: "k1", Apps: []string{"kitty"}, Title: "notes", Recency: 2, Visible: true},
		{ID: "k2", Apps: []string{"kitty"}, Title: "build", Recency: 3, Visible: true},
	}}
}

var noSleep = func(context.Context, time.Duration) error { return nil }

func call(t *testing.T, ts *Tools, name string, args map[string]any) map[string]any {
	t.Helper()
	out, ok := ts.Call(context.Background(), name, args, node.Request{})
	if !ok {
		t.Fatalf("%s not handled", name)
	}
	return out
}

func TestTypeIntoNamedApp(t *testing.T) {
	kb, win := &fakeKB{ok: true}, newWin()
	win.focusLag = 3
	var shown []string
	kb.onType = func() {
		if win.active != "claude" {
			t.Error("typed before the focus was confirmed")
		}
	}
	ts := &Tools{KB: kb, Win: win, Sleep: noSleep, Show: func(s string, _ time.Duration) { shown = append(shown, s) }}
	out := call(t, ts, ToolType, map[string]any{"text": "test def\n", "app": "claude", "submit": true})
	if out["success"] != true || out["submitted"] != true || out["into"] != "Claude" {
		t.Fatalf("out %v", out)
	}
	if !slices.Equal(kb.typed, []string{"test def"}) || !slices.Equal(kb.keys, []Key{KeyEnter}) || !slices.Equal(win.focused, []string{"claude"}) {
		t.Errorf("typed %q keys %v focused %v", kb.typed, kb.keys, win.focused)
	}
	if len(shown) != 1 || shown[0] != "Typing into Claude: test def" {
		t.Errorf("island %q", shown)
	}
	if _, has := out["text"]; has {
		t.Error("the output echoes the text")
	}
}

func TestTypeNeverIntoUnconfirmedFocus(t *testing.T) {
	kb, win := &fakeKB{ok: true}, newWin()
	win.focusLag = -1 // the compositor refuses to focus it
	ts := &Tools{KB: kb, Win: win, Sleep: noSleep}
	out := call(t, ts, ToolType, map[string]any{"text": "hello", "app": "claude"})
	if out["success"] != false || out["error_code"] != "focus_failed" || len(kb.typed) != 0 {
		t.Fatalf("out %v typed %q", out, kb.typed)
	}
}

func TestTypeTargetErrors(t *testing.T) {
	kb := &fakeKB{ok: true}
	ts := &Tools{KB: kb, Win: newWin(), Sleep: noSleep}
	out := call(t, ts, ToolType, map[string]any{"text": "x", "app": "kitty"})
	if out["error_code"] != "ambiguous_window" || len(out["candidates"].([]string)) != 2 {
		t.Errorf("ambiguous: %v", out)
	}
	out = call(t, ts, ToolType, map[string]any{"text": "x", "app": "spotify"})
	if out["error_code"] != "window_not_found" {
		t.Errorf("not found: %v", out)
	}
	ts.Win = nil
	out = call(t, ts, ToolType, map[string]any{"text": "x", "app": "claude"})
	if out["error_code"] != "not_supported" {
		t.Errorf("no window backend: %v", out)
	}
	out = call(t, ts, ToolType, map[string]any{"text": "\x1b\x07"})
	if out["error_code"] != "invalid_argument" {
		t.Errorf("empty text: %v", out)
	}
	if len(kb.typed) != 0 || len(kb.keys) != 0 {
		t.Errorf("typed %q keys %v on an error", kb.typed, kb.keys)
	}
	// No app: whatever is in front, no window backend needed; "the focused window" is no name.
	out = call(t, ts, ToolType, map[string]any{"text": "hello world", "app": "this window", "submit": "false"})
	if out["success"] != true || out["submitted"] != false || !slices.Equal(kb.typed, []string{"hello world"}) || len(kb.keys) != 0 {
		t.Errorf("plain type: %v %q %v", out, kb.typed, kb.keys)
	}
}

// Without an app the result names the window in front, and nothing is typed when the window
// backend can see that no window has the keyboard focus.
func TestTypeIntoTheWindowInFront(t *testing.T) {
	kb, win := &fakeKB{ok: true}, newWin()
	ts := &Tools{KB: kb, Win: win, Sleep: noSleep}
	out := call(t, ts, ToolType, map[string]any{"text": "hello"})
	if out["success"] != true || out["into"] != "Foot (the window in front)" || out["front_app"] != "foot" {
		t.Fatalf("out %v", out)
	}
	win.active = ""
	out = call(t, ts, ToolType, map[string]any{"text": "lost"})
	if out["success"] != false || out["error_code"] != "no_focused_window" || slices.Contains(kb.typed, "lost") {
		t.Fatalf("no focus: %v typed %q", out, kb.typed)
	}
	if out := call(t, ts, ToolPress, map[string]any{"key": "enter"}); out["error_code"] != "no_focused_window" || len(kb.keys) != 0 {
		t.Fatalf("press with no focus: %v keys %v", out, kb.keys)
	}
}

func TestPressKey(t *testing.T) {
	kb, win := &fakeKB{ok: true}, newWin()
	ts := &Tools{KB: kb, Win: win, Sleep: noSleep}
	if out := call(t, ts, ToolPress, map[string]any{"key": "Escape"}); out["success"] != true || out["pressed"] != "escape" {
		t.Errorf("escape: %v", out)
	}
	if out := call(t, ts, ToolPress, map[string]any{"key": "enter", "app": "notes"}); out["success"] != true || win.active != "k1" {
		t.Errorf("enter in notes: %v (active %s)", out, win.active)
	}
	if out := call(t, ts, ToolPress, map[string]any{"key": "alt+f4"}); out["error_code"] != "invalid_argument" {
		t.Errorf("alt+f4: %v", out)
	}
	if !slices.Equal(kb.keys, []Key{KeyEscape, KeyEnter}) {
		t.Errorf("keys %v", kb.keys)
	}
}

func TestAvailability(t *testing.T) {
	ts := &Tools{KB: &fakeKB{ok: false}}
	if len(ts.Available(context.Background())) != 0 {
		t.Error("tools offered without a keyboard")
	}
	ts.KB = &fakeKB{ok: true}
	names := func() []string {
		var n []string
		for _, d := range ts.Available(context.Background()) {
			n = append(n, d.Name)
		}
		return n
	}
	if got := names(); !slices.Equal(got, []string{ToolType, ToolPress}) {
		t.Errorf("tools %v", got)
	}
	ts.Confirm = &Confirmer{Mode: ConfirmSubmit}
	if got := names(); !slices.Equal(got, []string{ToolType, ToolPress, ToolConfirm}) {
		t.Errorf("tools with confirmation %v", got)
	}
	ts.Confirm.Mode = ConfirmOff
	if out, ok := ts.Call(context.Background(), ToolConfirm, map[string]any{}, node.Request{}); ok {
		t.Errorf("desktop_confirm served with confirmation off: %v", out)
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestConfirmSubmit(t *testing.T) {
	kb, win := &fakeKB{ok: true}, newWin()
	ck := &clock{t: time.Unix(1000, 0)}
	var island []string
	conf := &Confirmer{Mode: ConfirmSubmit, Timeout: 30 * time.Second, Now: ck.now,
		Show: func(s string, _ time.Duration) { island = append(island, s) }}
	ts := &Tools{KB: kb, Win: win, Confirm: conf, Sleep: noSleep, Show: conf.Show}

	// Typing without sending isn't asked about in "submit" mode.
	if out := call(t, ts, ToolType, map[string]any{"text": "draft"}); out["submitted"] != false || len(kb.typed) != 1 {
		t.Fatalf("plain typing was gated: %v", out)
	}
	island = nil
	// Sending is: nothing happens until a yes.
	out := call(t, ts, ToolType, map[string]any{"text": "test def", "app": "claude", "submit": true})
	id, _ := out["confirmation_id"].(string)
	if out["status"] != "awaiting_confirmation" || id == "" || out["question"] != `Send "test def" to Claude?` {
		t.Fatalf("ask: %v", out)
	}
	if len(kb.typed) != 1 || len(win.focused) != 0 {
		t.Fatal("acted before the answer")
	}
	if len(island) != 1 || !strings.Contains(island[0], "Send \"test def\" to Claude?") {
		t.Errorf("island %q", island)
	}
	out = call(t, ts, ToolConfirm, map[string]any{"confirmation_id": id, "answer": "OK, send it"})
	if out["submitted"] != true || !slices.Equal(kb.typed, []string{"draft", "test def"}) || !slices.Equal(win.focused, []string{"claude"}) {
		t.Fatalf("yes: %v typed %q", out, kb.typed)
	}
	if island[len(island)-1] != "Typing into Claude: test def" || !slices.Contains(island, "") {
		t.Errorf("island after yes %q", island)
	}
	// Single use.
	if out := call(t, ts, ToolConfirm, map[string]any{"confirmation_id": id, "answer": "yes"}); out["error_code"] != "invalid_argument" || len(kb.typed) != 2 {
		t.Errorf("reused: %v", out)
	}

	for _, c := range []struct {
		answer  string
		advance time.Duration
		reason  string
	}{
		{"cancel", 0, "said no"},
		{"no", 0, "said no"},
		{"hmm", 0, "clear yes"},
		{"I didn't catch that, sorry.", 0, "clear yes"},
		{"yes", 31 * time.Second, "no answer within 30 seconds"}, // timed out: a late yes is a cancel
	} {
		out := call(t, ts, ToolType, map[string]any{"text": "rm -rf", "submit": true})
		ck.t = ck.t.Add(c.advance)
		res := call(t, ts, ToolConfirm, map[string]any{"confirmation_id": out["confirmation_id"], "answer": c.answer})
		if res["cancelled"] != true || !strings.Contains(res["reason"].(string), c.reason) || len(kb.typed) != 2 {
			t.Errorf("%q after %v: %v (typed %q)", c.answer, c.advance, res, kb.typed)
		}
	}

	// A newer question replaces an older one: the old id can't be confirmed any more.
	first := call(t, ts, ToolType, map[string]any{"text": "one", "submit": true})
	second := call(t, ts, ToolType, map[string]any{"text": "two", "submit": true})
	if out := call(t, ts, ToolConfirm, map[string]any{"confirmation_id": first["confirmation_id"], "answer": "yes"}); out["success"] != false {
		t.Errorf("replaced question confirmed: %v", out)
	}
	if out := call(t, ts, ToolConfirm, map[string]any{"confirmation_id": second["confirmation_id"], "answer": "yes"}); out["submitted"] != true || kb.typed[len(kb.typed)-1] != "two" {
		t.Errorf("newest question: %v", out)
	}
}

func TestConfirmAllGatesKeys(t *testing.T) {
	kb := &fakeKB{ok: true}
	ts := &Tools{KB: kb, Confirm: &Confirmer{Mode: ConfirmAll}}
	out := call(t, ts, ToolPress, map[string]any{"key": "enter"})
	if out["status"] != "awaiting_confirmation" || len(kb.keys) != 0 {
		t.Fatalf("all: %v", out)
	}
	call(t, ts, ToolConfirm, map[string]any{"confirmation_id": out["confirmation_id"], "answer": "yes"})
	if !slices.Equal(kb.keys, []Key{KeyEnter}) {
		t.Errorf("keys %v", kb.keys)
	}
	ts.Confirm.Mode = ConfirmSubmit
	if out := call(t, ts, ToolPress, map[string]any{"key": "escape"}); out["pressed"] != "escape" {
		t.Errorf("submit mode gates keys: %v", out)
	}
	if _, err := ParseConfirmMode("sometimes"); err == nil {
		t.Error("bad mode accepted")
	}
}

func TestWindowsInputs(t *testing.T) {
	in := unicodeInputs("a😀")
	u := utf16.Encode([]rune("a😀"))
	if len(in) != 2*len(u) || in[0].Scan != 'a' || in[0].Flags != keyeventfUnicode || in[1].Flags != keyeventfUnicode|keyeventfKeyUp ||
		in[2].Scan != u[1] || in[4].Scan != u[2] {
		t.Errorf("unicode inputs %+v", in)
	}
	st, _ := keyInputs(KeyShiftTab)
	if len(st) != 4 || st[0].VK != vkShift || st[1].VK != vkTab || st[3].VK != vkShift || st[3].Flags != keyeventfKeyUp {
		t.Errorf("shift+tab %+v", st)
	}
	up, _ := keyInputs(KeyUp)
	if up[0].Flags != keyeventfExtended || up[1].Flags != keyeventfExtended|keyeventfKeyUp {
		t.Errorf("arrows must be extended keys: %+v", up)
	}
	var sent []KeyInput
	w := &WinKeyboard{Desktop: func() bool { return true }, Send: func(k []KeyInput) (int, error) { sent = k; return len(k) - 1, nil }}
	if err := w.Type(context.Background(), "hi"); err == nil || !strings.Contains(err.Error(), "administrator") || len(sent) != 4 {
		t.Errorf("a partial SendInput must be an error: %v", err)
	}
}

type fakePoster struct {
	units [][]uint16
	keys  []string
}

func (f *fakePoster) Unicode(u []uint16) error {
	f.units = append(f.units, append([]uint16(nil), u...))
	return nil
}
func (f *fakePoster) Key(c uint16, s bool) error {
	f.keys = append(f.keys, fmt.Sprint(c, s))
	return nil
}

func TestMacKeyboard(t *testing.T) {
	p := &fakePoster{}
	trusted := false
	m := &MacKeyboard{Trusted: func() bool { return trusted }, Post: p}
	if ok, _ := m.Available(context.Background()); ok {
		t.Fatal("available without Accessibility")
	}
	if err := m.Type(context.Background(), "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	trusted = true
	text := strings.Repeat("a", 19) + "😀" + strings.Repeat("b", 25)
	if err := m.Type(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	var all []uint16
	for _, c := range p.units {
		if len(c) > 20 || utf16.IsSurrogate(rune(c[len(c)-1])) && c[len(c)-1] < 0xDC00 {
			t.Errorf("chunk %d units ends on a high surrogate", len(c))
		}
		all = append(all, c...)
	}
	if string(utf16.Decode(all)) != text {
		t.Error("chunks don't reassemble the text")
	}
	_ = m.Press(context.Background(), KeyShiftTab)
	_ = m.Press(context.Background(), KeyEnter)
	if !slices.Equal(p.keys, []string{"48 true", "36 false"}) {
		t.Errorf("keys %v", p.keys)
	}
}

// fakeBackend is a host backend with windows, for HostWindows.
type fakeBackend struct {
	caps    []string
	focused string
}

func (*fakeBackend) Platform() string                                 { return "linux" }
func (f *fakeBackend) Capabilities(context.Context) []string          { return f.caps }
func (*fakeBackend) GetVolume(context.Context) (int, error)           { return 0, nil }
func (*fakeBackend) ApplyVolume(context.Context, int) error           { return nil }
func (*fakeBackend) MinimizeWindow(context.Context, string) error     { return nil }
func (*fakeBackend) CloseWindow(context.Context, string) error        { return nil }
func (*fakeBackend) Open(context.Context, string) error               { return nil }
func (*fakeBackend) LaunchApp(context.Context, string) error          { return nil }
func (f *fakeBackend) FocusWindow(_ context.Context, id string) error { f.focused = id; return nil }
func (*fakeBackend) ListWindows(context.Context) ([]host.Window, error) {
	return []host.Window{{ID: "0x04000007", App: "xterm.XTerm", Title: "shell"}, {ID: "0x05000001", App: "Navigator.firefox", Title: "Docs", Minimized: true}}, nil
}

func TestHostWindowsX11(t *testing.T) {
	be := &fakeBackend{caps: host.AllWindowCaps}
	w := &HostWindows{H: host.New(be), SameFn: SameX11, ActiveFn: func(context.Context) (string, error) {
		if be.focused == "" {
			return "67108871", nil
		}
		return "83886081", nil
	}}
	ctx := context.Background()
	if ok, _ := w.Available(ctx); !ok {
		t.Fatal("unavailable")
	}
	ts := &Tools{KB: &fakeKB{ok: true}, Win: w, Sleep: noSleep}
	out := call(t, ts, ToolType, map[string]any{"text": "hi", "app": "firefox"})
	if out["success"] != true || be.focused != "0x05000001" {
		t.Errorf("x11 type into firefox: %v (focused %s)", out, be.focused)
	}
	be.caps = nil
	if ok, why := w.Available(ctx); ok || why == "" {
		t.Error("available without window control")
	}
	if ok, _ := (&HostWindows{H: host.New(be)}).Available(ctx); ok {
		t.Error("available without an active-window check")
	}
}

func TestForOS(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		kb, win, err := ForOS(goos, host.New(&fakeBackend{}), &fakeExec{}, func() bool { return false })
		if err != nil || kb == nil || win == nil {
			t.Errorf("%s: %v", goos, err)
		}
		if ok, why := kb.Available(context.Background()); ok || why == "" {
			t.Errorf("%s: a fake host offers typing", goos)
		}
	}
	if _, _, err := ForOS("plan9", nil, nil, nil); err == nil {
		t.Error("unknown OS accepted")
	}
}
