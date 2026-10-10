package host

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Darwin shells out to osascript (volume, windows via System Events) and open(1).
//
// Window control needs an Accessibility (TCC) grant for the binary hosting the agent, plus an
// Automation grant to drive System Events. TCC grants are per binary, which is one reason this
// is a single Go binary rather than a Python script: the grant goes to jarvis-agent, not to a
// Homebrew Python that a patch upgrade silently replaces.
//
// Not named *_darwin.go: it compiles everywhere so the contract suite can run it everywhere.
type Darwin struct {
	Sys Sys
}

// NewDarwin builds the macOS backend. It touches nothing.
func NewDarwin(s Sys) *Darwin { return &Darwin{Sys: s} }

func (*Darwin) Platform() string { return "darwin" }

func (d *Darwin) Capabilities(ctx context.Context) []string {
	var caps []string
	if d.Sys.LookPath("open") {
		caps = append(caps, CapOpen)
	}
	if d.Sys.LookPath("osascript") {
		caps = append(caps, CapVolumeGet, CapVolumeSet)
		if d.accessibilityGranted(ctx) {
			caps = append(caps, AllWindowCaps...)
		}
	}
	return caps
}

// accessProbe really touches windows. The earlier Python probe ("count processes") succeeds
// without assistive access on current macOS, so window control was advertised and then failed.
const accessProbe = `tell application "System Events" to get name of every window of (first process whose frontmost is true)`

func (d *Darwin) accessibilityGranted(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := d.osascript(ctx, accessProbe)
	return err == nil
}

func (d *Darwin) osascript(ctx context.Context, script string) (string, error) {
	if !d.Sys.LookPath("osascript") {
		return "", Errorf(ErrNotSupported, "osascript unavailable")
	}
	out, err := d.Sys.Run(ctx, "osascript", "-e", script)
	if err != nil {
		msg := err.Error()
		// -1719 / -25211 / -1743 are TCC denials (assistive access, automation).
		if strings.Contains(msg, "-1719") || strings.Contains(msg, "-25211") || strings.Contains(msg, "-1743") || strings.Contains(msg, "-1728") || strings.Contains(msg, "not allowed") {
			return "", Errorf(ErrNotSupported, "Accessibility/Automation permission required: %s", msg)
		}
		return "", Errorf(ErrOperationFailed, "%s", msg)
	}
	return out, nil
}

func (d *Darwin) GetVolume(ctx context.Context) (int, error) {
	raw, err := d.osascript(ctx, "output volume of (get volume settings)")
	if err != nil {
		return 0, err
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		// "missing value": the current output device has no software volume (e.g. HDMI).
		return 0, Errorf(ErrOperationFailed, "output device reports no volume (%q)", raw)
	}
	return v, nil
}

func (d *Darwin) ApplyVolume(ctx context.Context, level int) error {
	_, err := d.osascript(ctx, fmt.Sprintf("set volume output volume %d", level))
	return err
}

// Window ids are "<pid>/<index>": the owning process and the window's position in its window
// list. Opaque to callers, and only as stable as the window list, which is what the contract
// promises (valid until the window set changes). System Events windows have no usable id.
var reMacID = regexp.MustCompile(`^(\d{1,10})/(\d{1,4})$`)

const listScript = `tell application "System Events"
	set out to ""
	repeat with proc in (every process whose background only is false)
		set procID to (unix id of proc) as string
		set procName to name of proc
		set i to 0
		repeat with w in (every window of proc)
			set i to i + 1
			try
				set m to false
				try
					set m to value of attribute "AXMinimized" of w
				end try
				set out to out & procID & "/" & (i as string) & tab & procName & tab & (name of w as string) & tab & (m as string) & linefeed
			end try
		end repeat
	end repeat
	return out
end tell`

func (d *Darwin) ListWindows(ctx context.Context) ([]Window, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := d.osascript(ctx, listScript)
	if err != nil {
		return nil, err
	}
	out := []Window{}
	for _, line := range strings.Split(raw, "\n") {
		p := strings.Split(line, "\t")
		if len(p) != 4 || !reMacID.MatchString(p[0]) {
			continue
		}
		out = append(out, Window{ID: p[0], App: p[1], Title: p[2], Minimized: strings.EqualFold(p[3], "true")})
	}
	return out, nil
}

// The id is validated against reMacID before it is spliced into AppleScript: only digits reach
// the script, so a window id from an LLM cannot inject code.
const actionScript = `tell application "System Events"
	set procs to (every process whose unix id is %s)
	if (count of procs) is 0 then return "missing"
	set proc to item 1 of procs
	if (count of windows of proc) < %s then return "missing"
	set w to window %s of proc
	%s
	return "ok"
end tell`

var macActions = map[WindowAction]string{
	ActionMinimize: `set value of attribute "AXMinimized" of w to true`,
	ActionFocus:    "perform action \"AXRaise\" of w\n\tset frontmost of proc to true",
	// The window's own close button: the same graceful request a click makes, so the app can
	// prompt to save. Never quit or kill the process.
	ActionClose: `click (first button of w whose subrole is "AXCloseButton")`,
}

func (d *Darwin) act(ctx context.Context, action WindowAction, id string) error {
	m := reMacID.FindStringSubmatch(id)
	if m == nil {
		return Errorf(ErrWindowNotFound, "not a window id: %q", id)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := d.osascript(ctx, fmt.Sprintf(actionScript, m[1], m[2], m[2], macActions[action]))
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "missing" {
		return Errorf(ErrWindowNotFound, "window %s no longer exists", id)
	}
	return nil
}

func (d *Darwin) MinimizeWindow(ctx context.Context, id string) error {
	return d.act(ctx, ActionMinimize, id)
}
func (d *Darwin) FocusWindow(ctx context.Context, id string) error {
	return d.act(ctx, ActionFocus, id)
}
func (d *Darwin) CloseWindow(ctx context.Context, id string) error {
	return d.act(ctx, ActionClose, id)
}

func (d *Darwin) Open(_ context.Context, target string) error {
	if !d.Sys.LookPath("open") {
		return Errorf(ErrNotSupported, "the open binary is unavailable")
	}
	if err := d.Sys.Start("open", target); err != nil {
		return Errorf(ErrOperationFailed, "could not open %q: %v", target, err)
	}
	return nil
}

// LaunchApp opens a .app bundle with open(1), which starts it or brings it forward.
func (d *Darwin) LaunchApp(_ context.Context, path string) error {
	if !d.Sys.LookPath("open") {
		return Errorf(ErrNotSupported, "the open binary is unavailable")
	}
	if err := d.Sys.Start("open", path); err != nil {
		return Errorf(ErrOperationFailed, "could not launch %q: %v", path, err)
	}
	return nil
}
