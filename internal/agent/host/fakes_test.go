package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// fakeSys is a scriptable Sys: which binaries exist, the environment, and canned command output.
type fakeSys struct {
	mu      sync.Mutex
	bins    map[string]bool
	env     map[string]string
	handler func(name string, args []string) (string, error)
	calls   []string // every Run/Start/LookPath/Getenv, in order
	runs    []string // Run command lines
	starts  []string // Start command lines
}

func (f *fakeSys) record(kind, s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, kind+" "+s)
}

func (f *fakeSys) LookPath(name string) bool { f.record("lookpath", name); return f.bins[name] }
func (f *fakeSys) Getenv(k string) string    { f.record("getenv", k); return f.env[k] }

func (f *fakeSys) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.record("run", line)
	f.mu.Lock()
	f.runs = append(f.runs, line)
	f.mu.Unlock()
	if !f.bins[name] {
		return "", &RunError{Name: name, Msg: "not installed"}
	}
	if f.handler == nil {
		return "", nil
	}
	return f.handler(name, args)
}

func (f *fakeSys) Start(name string, args ...string) error {
	line := strings.Join(append([]string{name}, args...), " ")
	f.record("start", line)
	f.mu.Lock()
	f.starts = append(f.starts, line)
	f.mu.Unlock()
	if !f.bins[name] {
		return errors.New("not installed")
	}
	return nil
}

func (f *fakeSys) allCommands() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(append(append([]string{}, f.runs...), f.starts...), "\n")
}

// fakeWin32 is a scriptable Win32.
type fakeWin32 struct {
	mu      sync.Mutex
	audio   bool
	desktop bool
	volume  float32
	windows map[uintptr]Window
	calls   []string
	zoomed  bool
	iconic  bool
	areas   []Rect // nil: two side-by-side monitors
	rect    Rect   // the window's rect; zero: 800x600 at 100,100
}

func (f *fakeWin32) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }

func (f *fakeWin32) AudioAvailable() bool { f.rec("audio?"); return f.audio }
func (f *fakeWin32) VolumeScalar() (float32, error) {
	f.rec("getvol")
	if !f.audio {
		return 0, errors.New("no endpoint")
	}
	return f.volume, nil
}
func (f *fakeWin32) SetVolumeScalar(v float32) error {
	f.rec(fmt.Sprintf("setvol %.2f", v))
	if !f.audio {
		return errors.New("no endpoint")
	}
	f.volume = v
	return nil
}
func (f *fakeWin32) DesktopAvailable() bool { f.rec("desktop?"); return f.desktop }
func (f *fakeWin32) EnumWindows() ([]Window, error) {
	f.rec("enum")
	var out []Window
	for _, w := range f.windows {
		out = append(out, w)
	}
	return out, nil
}
func (f *fakeWin32) IsWindow(h uintptr) bool {
	f.rec(fmt.Sprintf("iswindow %d", h))
	_, ok := f.windows[h]
	return ok
}
func (f *fakeWin32) Minimize(h uintptr) error { f.rec(fmt.Sprintf("minimize %d", h)); return nil }
func (f *fakeWin32) Focus(h uintptr) error    { f.rec(fmt.Sprintf("focus %d", h)); return nil }
func (f *fakeWin32) PostClose(h uintptr) error {
	f.rec(fmt.Sprintf("postclose %d", h))
	return nil
}
func (f *fakeWin32) ShellOpen(t string) error { f.rec("shellopen " + t); return nil }
func (f *fakeWin32) Maximize(h uintptr) error {
	f.rec(fmt.Sprintf("maximize %d", h))
	f.zoomed = true
	return nil
}
func (f *fakeWin32) Restore(h uintptr) error {
	f.rec(fmt.Sprintf("restore %d", h))
	f.zoomed, f.iconic = false, false
	return nil
}
func (f *fakeWin32) IsZoomed(uintptr) bool { return f.zoomed }
func (f *fakeWin32) IsIconic(uintptr) bool { return f.iconic }
func (f *fakeWin32) WorkAreas() ([]Rect, error) {
	if f.areas == nil {
		// Two 1920x1080 monitors side by side, a 40 px taskbar on each.
		return []Rect{{0, 0, 1920, 1040}, {1920, 0, 3840, 1040}}, nil
	}
	return f.areas, nil
}
func (f *fakeWin32) WindowRect(uintptr) (Rect, error) {
	if f.rect == (Rect{}) {
		return Rect{100, 100, 900, 700}, nil
	}
	return f.rect, nil
}
func (f *fakeWin32) SetRect(h uintptr, r Rect) error {
	f.rec(fmt.Sprintf("setrect %d %d,%d %dx%d", h, r.L, r.T, r.w(), r.h()))
	f.rect = r
	return nil
}

func (f *fakeWin32) allCommands() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

// --- canned hosts ---

const (
	linuxWinID = "0x03a00007"
	hyprWinID  = "0x55aa01"
	swayWinID  = "17"
	macWinID   = "4242/1"
	winWinID   = "1311"
)

// linuxFull is an X11 desktop with every tool installed and one window open.
func linuxFull() *fakeSys {
	vol := 40
	return &fakeSys{
		bins: map[string]bool{"xdg-open": true, "gio": true, "pactl": true, "wmctrl": true, "xdotool": true},
		env:  map[string]string{"DISPLAY": ":0"},
		handler: func(name string, args []string) (string, error) {
			switch {
			case name == "pactl" && args[0] == "get-sink-volume":
				return fmt.Sprintf("Volume: front-left: 26214 /  %d%% / -23.88 dB,   front-right: 26214 /  %d%% / -23.88 dB", vol, vol), nil
			case name == "pactl" && args[0] == "set-sink-volume":
				_, _ = fmt.Sscanf(args[2], "%d%%", &vol)
				return "", nil
			case name == "wmctrl" && args[0] == "-l":
				return linuxWinID + "  0 firefox.Firefox  box Mozilla Firefox — a  title\n0x01200003 -1 xfce4-panel.Xfce4-panel  box xfce4-panel", nil
			}
			return "", nil
		},
	}
}

// linuxEWMH is an X11 desktop with an EWMH window manager and none of the window helpers:
// windows go through the built-in client (a fake X11 recording into the same Sys).
func linuxEWMH() (*fakeSys, *fakeX) {
	s := linuxFull()
	delete(s.bins, "wmctrl")
	delete(s.bins, "xdotool")
	return s, &fakeX{sys: s, wm: true, windows: []x11.Window{
		{ID: 0x03a00007, Title: "Mozilla Firefox — a  title", Class: "firefox.Firefox"},
		{ID: 0x01200003, Title: "xfce4-panel", Class: "xfce4-panel.Xfce4-panel", Minimized: true}}}
}

// fakeX is the built-in X11 window control, recording "x11 <op> <id>" among the fakeSys's
// commands.
type fakeX struct {
	sys     *fakeSys
	wm      bool
	why     string
	windows []x11.Window
	err     error
	// features and desktops: what Features reports; nil features: everything, 4 desktops.
	features map[string]bool
	desktops int
}

func (f *fakeX) rec(line string) {
	f.sys.record("x11", line)
	f.sys.mu.Lock()
	f.sys.runs = append(f.sys.runs, "x11 "+line)
	f.sys.mu.Unlock()
}

func (f *fakeX) WMAvailable(context.Context) (bool, string) {
	f.rec("probe")
	return f.wm, f.why
}

func (f *fakeX) Windows(context.Context) ([]x11.Window, error) {
	f.rec("list")
	return f.windows, f.err
}

func (f *fakeX) op(name string, id uint32) error {
	f.rec(name + " " + x11.FormatID(id))
	return f.err
}

func (f *fakeX) Activate(_ context.Context, id uint32) error    { return f.op("activate", id) }
func (f *fakeX) CloseWindow(_ context.Context, id uint32) error { return f.op("close", id) }
func (f *fakeX) Iconify(_ context.Context, id uint32) error     { return f.op("iconify", id) }

// ewmhAll is what openbox advertises: every arranging feature, four desktops.
var ewmhAll = map[string]bool{"_NET_WM_STATE": true, "_NET_WM_STATE_MAXIMIZED_VERT": true, "_NET_WM_STATE_MAXIMIZED_HORZ": true,
	"_NET_WM_STATE_FULLSCREEN": true, "_NET_MOVERESIZE_WINDOW": true, "_NET_WORKAREA": true, "_NET_WM_DESKTOP": true}

func (f *fakeX) Features(context.Context) (map[string]bool, int, error) {
	if f.features == nil {
		return ewmhAll, 4, f.err
	}
	return f.features, f.desktops, f.err
}
func (f *fakeX) SetState(_ context.Context, id uint32, add bool, states ...string) error {
	f.rec(fmt.Sprintf("state %s add=%v %s", x11.FormatID(id), add, strings.Join(states, ",")))
	return f.err
}
func (f *fakeX) MoveToDesktop(_ context.Context, id uint32, n int) error {
	f.rec(fmt.Sprintf("desktop %s %d", x11.FormatID(id), n))
	return f.err
}
func (f *fakeX) Snap(_ context.Context, id uint32, left bool) error {
	f.rec(fmt.Sprintf("snap %s left=%v", x11.FormatID(id), left))
	return f.err
}

// hyprClients is `hyprctl -j clients`: the focused terminal, Firefox behind it, and an unmapped
// client that must not be listed.
const hyprClients = `[
 {"address":"0x55aa02","mapped":true,"class":"foot","title":"~","focusHistoryID":0,"fullscreen":0,"workspace":{"id":1,"name":"1"}},
 {"address":"` + hyprWinID + `","mapped":true,"class":"firefox","title":"Mozilla Firefox","focusHistoryID":1,"fullscreen":1,"workspace":{"id":2,"name":"2"}},
 {"address":"0x55aa03","mapped":false,"class":"ghost","title":"","focusHistoryID":2}]`

// linuxHyprland is a Hyprland session: hyprctl answers, and every dispatch in the classic
// form succeeds (luaOnly makes the classic form fail, as on a Lua-configured Hyprland).
func linuxHyprland() *fakeSys { return hyprSys(false) }

func hyprSys(luaOnly bool) *fakeSys {
	s := linuxFull()
	delete(s.bins, "wmctrl")
	delete(s.bins, "xdotool")
	s.bins["hyprctl"] = true
	s.env = map[string]string{"WAYLAND_DISPLAY": "wayland-1", "HYPRLAND_INSTANCE_SIGNATURE": "sig"}
	base := s.handler
	s.handler = func(name string, args []string) (string, error) {
		if name != "hyprctl" {
			return base(name, args)
		}
		switch {
		case args[0] == "-j" && args[1] == "clients":
			return hyprClients, nil
		case args[0] == "-j" && args[1] == "activewindow":
			return `{"address":"0x55aa02"}`, nil
		case args[0] == "-j" && args[1] == "monitors":
			return `[{"name":"DP-1"},{"name":"HDMI-A-1"}]`, nil
		case args[0] == "dispatch" && strings.HasPrefix(args[1], "hl.dsp."):
			return "ok", nil
		case (args[0] == "dispatch" || args[0] == "--batch") && luaOnly:
			return "", &RunError{Name: "hyprctl", Msg: "')' expected near 'address'"}
		case args[0] == "dispatch":
			return "ok", nil
		case args[0] == "--batch":
			return "ok\n\nok", nil
		}
		return "", nil
	}
	return s
}

// swayTree is `swaymsg -t get_tree`: Firefox (focused) and a terminal on workspace 1, where the
// terminal was focused before it.
const swayTree = `{"id":1,"type":"root","focus":[2],"nodes":[
 {"id":2,"type":"output","name":"DP-1","focus":[3],"nodes":[
  {"id":3,"type":"workspace","name":"1","focus":[` + swayWinID + `,18],"nodes":[
   {"id":18,"type":"con","name":"~","app_id":"foot","pid":10},
   {"id":` + swayWinID + `,"type":"con","name":"Mozilla Firefox","app_id":"firefox","pid":11,"focused":true}]}]}]}`

// linuxSway is a sway session.
func linuxSway() *fakeSys {
	s := linuxFull()
	delete(s.bins, "wmctrl")
	delete(s.bins, "xdotool")
	s.bins["swaymsg"] = true
	s.env = map[string]string{"WAYLAND_DISPLAY": "wayland-1", "SWAYSOCK": "/run/sway.sock"}
	base := s.handler
	s.handler = func(name string, args []string) (string, error) {
		if name != "swaymsg" {
			return base(name, args)
		}
		if args[0] == "-t" && args[1] == "get_tree" {
			return swayTree, nil
		}
		if args[0] == "-t" && args[1] == "get_outputs" {
			return `[{"name":"DP-1","active":true},{"name":"HDMI-A-1","active":true},{"name":"eDP-1","active":false}]`, nil
		}
		return `[{"success":true}]`, nil
	}
	return s
}

func linuxEmpty() *fakeSys { return &fakeSys{bins: map[string]bool{}, env: map[string]string{}} }

// macFull is a Mac with osascript and open(1): volume and open. Windows come from macAX.
func macFull() *fakeSys {
	vol := 40
	return &fakeSys{
		bins: map[string]bool{"open": true, "osascript": true},
		env:  map[string]string{},
		handler: func(name string, args []string) (string, error) {
			script := args[len(args)-1]
			switch {
			case strings.Contains(script, "get volume settings"):
				return fmt.Sprint(vol), nil
			case strings.HasPrefix(script, "set volume output volume"):
				_, _ = fmt.Sscanf(script, "set volume output volume %d", &vol)
				return "", nil
			}
			return "", nil
		},
	}
}

// macAX is Accessibility granted with one window open (macWinID, Safari's).
func macAX(s *fakeSys) *fakeAX {
	return &fakeAX{sys: s, trusted: true, apps: []ax.App{{PID: 4242, Name: "Safari"}},
		windows: map[int][]ax.AppWindow{4242: {{Title: "Apple"}}}}
}

// macDesktop is a Darwin backend on macFull and macAX.
func macDesktop() (*Darwin, *fakeSys, *fakeAX) {
	s := macFull()
	a := macAX(s)
	return &Darwin{Sys: s, AX: a}, s, a
}

// fakeAX is the Accessibility window control, recording "ax <op> ..." among the fakeSys's
// commands. An app in hang blocks AppWindows until its channel is closed: one that never
// answers.
type fakeAX struct {
	sys     *fakeSys
	trusted bool
	apps    []ax.App
	windows map[int][]ax.AppWindow
	errs    map[int]error // AppWindows' error per pid
	hang    map[int]chan struct{}
}

func (f *fakeAX) rec(line string) {
	f.sys.record("ax", line)
	f.sys.mu.Lock()
	f.sys.runs = append(f.sys.runs, "ax "+line)
	f.sys.mu.Unlock()
}

func (f *fakeAX) Trusted() bool { f.rec("trusted"); return f.trusted }

func (f *fakeAX) Apps() ([]ax.App, error) {
	f.rec("apps")
	if !f.trusted {
		return nil, ax.ErrNotTrusted
	}
	return f.apps, nil
}

func (f *fakeAX) AppWindows(pid int) ([]ax.AppWindow, error) {
	f.rec(fmt.Sprintf("windows %d", pid))
	if ch, ok := f.hang[pid]; ok {
		<-ch
	}
	if err := f.errs[pid]; err != nil {
		return nil, err
	}
	return f.windows[pid], nil
}

func (f *fakeAX) Act(pid, index int, action string) error {
	f.rec(fmt.Sprintf("%s %d/%d", action, pid, index))
	if !f.trusted {
		return ax.ErrNotTrusted
	}
	if index < 1 || index > len(f.windows[pid]) {
		return ax.ErrNoWindow
	}
	return nil
}

func macEmpty() *fakeSys { return &fakeSys{bins: map[string]bool{}, env: map[string]string{}} }

func winFull() *fakeWin32 {
	return &fakeWin32{audio: true, desktop: true, volume: 0.4,
		windows: map[uintptr]Window{1311: {ID: winWinID, Title: "Notepad", App: "Notepad"}}}
}

func winEmpty() *fakeWin32 { return &fakeWin32{} }
