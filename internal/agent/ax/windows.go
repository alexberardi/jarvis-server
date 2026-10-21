package ax

// Window control through the Accessibility API, for the host backend's macOS window tools. It
// replaced System Events scripts (#359): System Events waits out each unresponsive app's AX
// timeout in turn, so one app that doesn't answer made every window list take ~10 s. Here each
// app element gets a short messaging timeout of its own (AppTimeout), and the caller queries
// apps concurrently.

// AppTimeout is the messaging timeout set on each app element and its windows: an app that
// doesn't answer within it is skipped (AXError -25204).
const AppTimeout = 0.25 // seconds

// App is a running app with a regular activation policy (one that has a Dock icon).
type App struct {
	PID  int
	Name string // localizedName
}

// AppWindow is one of an app's AXWindows, in their order (the frontmost first).
type AppWindow struct {
	Title     string
	Minimized bool
}

// The window actions Act performs.
const (
	ActMinimize = "minimize"
	ActFocus    = "focus"
	ActClose    = "close"
)

// ErrNoWindow: the app or the window index is gone.
var ErrNoWindow = &Error{Code: CodeNotFound, Msg: "no such window"}

// Windows is the macOS window control (Accessibility). On other platforms it is untrusted and
// every call is ErrUnsupported.
type Windows struct{}

// Trusted reports whether this process holds the Accessibility grant. It never prompts.
func (Windows) Trusted() bool { return trusted() }

// Apps lists the running regular apps.
func (Windows) Apps() ([]App, error) { return runningApps() }

// AppWindows lists one app's windows; an app that doesn't answer fails within AppTimeout.
func (Windows) AppWindows(pid int) ([]AppWindow, error) { return appWindows(pid) }

// Act performs action (ActMinimize, ActFocus, ActClose) on window index (1-based, AXWindows
// order) of the app with this pid. Close presses the window's close button: the same graceful
// request a click makes, never a quit or a kill.
func (Windows) Act(pid, index int, action string) error { return windowAct(pid, index, action) }
