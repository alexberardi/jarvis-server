package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

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

func (f *fakeWin32) allCommands() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

// --- canned hosts ---

const (
	linuxWinID = "0x03a00007"
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

func linuxEmpty() *fakeSys { return &fakeSys{bins: map[string]bool{}, env: map[string]string{}} }

// macFull is a Mac with Accessibility granted and one window open.
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
			case script == accessProbe:
				return "Terminal", nil
			case strings.Contains(script, "AXMinimized\" of w\n") || strings.Contains(script, "set out to"):
				return macWinID + "\tSafari\tApple\tfalse\n", nil
			case strings.Contains(script, "unix id is 4242"):
				if strings.Contains(script, "< 1 then") {
					return "ok", nil
				}
				return "missing", nil
			}
			return "missing", nil
		},
	}
}

func macEmpty() *fakeSys { return &fakeSys{bins: map[string]bool{}, env: map[string]string{}} }

func winFull() *fakeWin32 {
	return &fakeWin32{audio: true, desktop: true, volume: 0.4,
		windows: map[uintptr]Window{1311: {ID: winWinID, Title: "Notepad", App: "Notepad"}}}
}

func winEmpty() *fakeWin32 { return &fakeWin32{} }
