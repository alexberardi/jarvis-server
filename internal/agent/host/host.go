// Package host is "how to do a thing on this OS": volume, windows and open, behind one
// contract with one backend per platform.
//
// It owns no policy, no routing and no voice logic. If you catch yourself deciding *which*
// window the user meant, or *whether* they are allowed to, that belongs in command-center or
// in the tool layer. This package answers "minimise window 0x1234", never "minimise the thing
// I was just talking about".
//
// # How drift between the three platforms is prevented
//
//  1. Host is the only caller of a Backend. Capability gating, volume clamping and
//     capability-name validation live here, once, so three backends cannot disagree about them.
//  2. Backend.ApplyVolume receives an already-clamped level. There is one clamp implementation
//     (Host.SetVolume), so there is nothing to diverge.
//  3. One contract suite (contract_test.go) runs against every backend on every OS. That is
//     what catches a Windows-only mistake on the Linux runner.
package host

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Capability names are a wire contract: CC decides whether to offer a tool from them. A typo
// would silently disable a feature, so the set is closed and Host rejects unknown names.
const (
	CapVolumeGet       = "volume.get"
	CapVolumeSet       = "volume.set"
	CapWindowsList     = "windows.list"
	CapWindowsMinimize = "windows.minimize"
	CapWindowsFocus    = "windows.focus"
	CapWindowsClose    = "windows.close"
	CapOpen            = "open"

	// The arranging verbs (DESKTOP-COMMANDS.md L1). Each is offered only where the platform has
	// a native version (Q4): no stand-ins.
	CapWindowsUnminimize = "windows.unminimize"
	CapWindowsMaximize   = "windows.maximize"
	CapWindowsUnmaximize = "windows.unmaximize" // leave maximize or fullscreen
	CapWindowsFullscreen = "windows.fullscreen"
	CapWindowsSnap       = "windows.snap"
	CapWindowsMonitor    = "windows.monitor"   // only while there is a second monitor
	CapWindowsWorkspace  = "windows.workspace" // a numbered workspace
)

// KnownCapabilities is the closed set.
var KnownCapabilities = map[string]bool{
	CapVolumeGet: true, CapVolumeSet: true,
	CapWindowsList: true, CapWindowsMinimize: true, CapWindowsFocus: true, CapWindowsClose: true,
	CapOpen:              true,
	CapWindowsUnminimize: true, CapWindowsMaximize: true, CapWindowsUnmaximize: true,
	CapWindowsFullscreen: true, CapWindowsSnap: true, CapWindowsMonitor: true, CapWindowsWorkspace: true,
}

// AllWindowCaps is the basic window capabilities, for backends that gain or lose them as a unit
// (X11 present, Accessibility granted). The arranging ones are added per platform.
var AllWindowCaps = []string{CapWindowsList, CapWindowsMinimize, CapWindowsFocus, CapWindowsClose}

const (
	VolumeMin = 0
	VolumeMax = 100
)

// Window is one top-level window. ID is an opaque, platform-defined handle (HWND, X11 window
// id, macOS pid/index): callers treat it as a cookie from ListWindows, never parse or persist
// it, and expect it to go stale when the window closes.
type Window struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	App       string `json:"app"`
	Minimized bool   `json:"minimized"`
	// Focused: the window with keyboard focus, where the backend can tell (Hyprland, sway);
	// false everywhere else for now.
	Focused bool `json:"focused"`
	// Workspace names the window's workspace where the backend knows it (Hyprland, sway), so
	// two windows with the same title can be told apart; "" elsewhere.
	Workspace string `json:"workspace,omitempty"`
}

// Errors. Every deliberate failure is one of these, so callers map conditions identically
// whatever the platform.
var (
	// ErrNotSupported: this host cannot do this, and retrying won't change that (Wayland forbids
	// cross-app window control; macOS lacks an Accessibility grant; a headless box has no
	// session). Tools degrade gracefully on it.
	ErrNotSupported = errors.New("not supported on this host")
	// ErrWindowNotFound: the window id is unknown, usually because it closed mid-request.
	ErrWindowNotFound = errors.New("window not found")
	// ErrOperationFailed: the platform call was attempted and failed unexpectedly.
	ErrOperationFailed = errors.New("host operation failed")
	// ErrInvalidTarget: open was asked for something the open policy refuses.
	ErrInvalidTarget = errors.New("target refused")
)

// Errorf wraps one of the sentinel errors with detail, keeping errors.Is working.
func Errorf(kind error, format string, a ...any) error {
	return fmt.Errorf("%w: %s", kind, fmt.Sprintf(format, a...))
}

// Backend is one platform's implementation. Implementations:
//
//   - must be constructible on any OS: no platform calls, probes or subprocesses at
//     construction, so the contract suite can reach every backend from every runner;
//   - compute Capabilities per call, never cached: a macOS Accessibility grant can be revoked
//     and a Linux box can gain a session;
//   - never kill in CloseWindow: it is a graceful request (WM_CLOSE, wmctrl -c);
//   - return from Open as soon as the OS accepted the request (fire-and-forget).
type Backend interface {
	Platform() string // "linux", "darwin" or "windows" (runtime.GOOS spelling)
	Capabilities(ctx context.Context) []string
	GetVolume(ctx context.Context) (int, error)
	// ApplyVolume receives a level already clamped into [0, 100]. Don't re-clamp.
	ApplyVolume(ctx context.Context, level int) error
	ListWindows(ctx context.Context) ([]Window, error)
	MinimizeWindow(ctx context.Context, id string) error
	FocusWindow(ctx context.Context, id string) error
	CloseWindow(ctx context.Context, id string) error
	Open(ctx context.Context, target string) error
	// LaunchApp starts an installed application from its catalogue path (a .app bundle, a
	// .desktop file or a .lnk shortcut). Fire-and-forget, like Open. The path always comes
	// from the app catalogue, never from the model.
	LaunchApp(ctx context.Context, path string) error
	// ArrangeWindow runs the actions past minimise, focus and close (unminimize, maximize,
	// unmaximize, fullscreen, snap, move to a monitor or workspace). target is the monitor or
	// workspace of a move, already validated by Host; "" otherwise. Setting a state the window
	// is already in is not an error.
	ArrangeWindow(ctx context.Context, id string, a WindowAction, target string) error
}

// Host gates, clamps and validates in front of exactly one Backend. It is the only thing that
// calls a backend.
type Host struct {
	b Backend
}

// New wraps a backend.
func New(b Backend) *Host { return &Host{b: b} }

// Platform is the backend's platform name.
func (h *Host) Platform() string { return h.b.Platform() }

// Capabilities is the sorted, validated capability list, computed now.
func (h *Host) Capabilities(ctx context.Context) ([]string, error) {
	caps := h.b.Capabilities(ctx)
	seen := map[string]bool{}
	var out []string
	for _, c := range caps {
		if !KnownCapabilities[c] {
			return nil, fmt.Errorf("host: backend %s reported unknown capability %q", h.b.Platform(), c)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Has reports whether a capability is available right now.
func (h *Host) Has(ctx context.Context, capability string) bool {
	caps, err := h.Capabilities(ctx)
	if err != nil {
		return false
	}
	for _, c := range caps {
		if c == capability {
			return true
		}
	}
	return false
}

func (h *Host) gate(ctx context.Context, capability string) error {
	if !h.Has(ctx, capability) {
		return Errorf(ErrNotSupported, "%s is unavailable on this %s host", capability, h.b.Platform())
	}
	return nil
}

// Clamp is the one volume clamp.
func Clamp(level int) int {
	return max(VolumeMin, min(VolumeMax, level))
}

// GetVolume is the current master volume, 0-100.
func (h *Host) GetVolume(ctx context.Context) (int, error) {
	if err := h.gate(ctx, CapVolumeGet); err != nil {
		return 0, err
	}
	v, err := h.b.GetVolume(ctx)
	if err != nil {
		return 0, err
	}
	return Clamp(v), nil
}

// SetVolume clamps out-of-range levels to the nearest bound (150 becomes 100; never an error)
// and returns the level actually applied.
func (h *Host) SetVolume(ctx context.Context, level int) (int, error) {
	if err := h.gate(ctx, CapVolumeSet); err != nil {
		return 0, err
	}
	applied := Clamp(level)
	if err := h.b.ApplyVolume(ctx, applied); err != nil {
		return 0, err
	}
	return applied, nil
}

// ListWindows lists top-level windows.
func (h *Host) ListWindows(ctx context.Context) ([]Window, error) {
	if err := h.gate(ctx, CapWindowsList); err != nil {
		return nil, err
	}
	ws, err := h.b.ListWindows(ctx)
	if ws == nil && err == nil {
		ws = []Window{}
	}
	return ws, err
}

// WindowAction is a window verb.
type WindowAction string

const (
	ActionMinimize        WindowAction = "minimize"
	ActionFocus           WindowAction = "focus"
	ActionClose           WindowAction = "close"
	ActionUnminimize      WindowAction = "unminimize"
	ActionMaximize        WindowAction = "maximize"
	ActionUnmaximize      WindowAction = "unmaximize"
	ActionFullscreen      WindowAction = "fullscreen"
	ActionSnapLeft        WindowAction = "snap_left"
	ActionSnapRight       WindowAction = "snap_right"
	ActionMoveToMonitor   WindowAction = "move_to_monitor"
	ActionMoveToWorkspace WindowAction = "move_to_workspace"
)

// ActionCapability maps a window action to the capability it needs.
var ActionCapability = map[WindowAction]string{
	ActionMinimize:        CapWindowsMinimize,
	ActionFocus:           CapWindowsFocus,
	ActionClose:           CapWindowsClose,
	ActionUnminimize:      CapWindowsUnminimize,
	ActionMaximize:        CapWindowsMaximize,
	ActionUnmaximize:      CapWindowsUnmaximize,
	ActionFullscreen:      CapWindowsFullscreen,
	ActionSnapLeft:        CapWindowsSnap,
	ActionSnapRight:       CapWindowsSnap,
	ActionMoveToMonitor:   CapWindowsMonitor,
	ActionMoveToWorkspace: CapWindowsWorkspace,
}

// WindowActions is every action, in the order a tool lists them.
var WindowActions = []WindowAction{ActionFocus, ActionMinimize, ActionUnminimize, ActionMaximize,
	ActionUnmaximize, ActionFullscreen, ActionSnapLeft, ActionSnapRight, ActionMoveToMonitor,
	ActionMoveToWorkspace, ActionClose}

// NeedsTarget: the move actions take a monitor or workspace (WindowMove).
func NeedsTarget(a WindowAction) bool { return a == ActionMoveToMonitor || a == ActionMoveToWorkspace }

// WindowDo runs a window action that takes no target. Close is always a graceful request.
func (h *Host) WindowDo(ctx context.Context, action WindowAction, id string) error {
	if NeedsTarget(action) {
		return fmt.Errorf("host: %s needs a target (WindowMove)", action)
	}
	return h.windowDo(ctx, action, id, "")
}

var (
	reWorkspace = regexp.MustCompile(`^[1-9][0-9]{0,2}$`)
	// A monitor is next, previous, its position (1-9) or its connector name ("DP-1").
	reMonitor = regexp.MustCompile(`^(next|previous|[1-9]|[A-Za-z][A-Za-z0-9_.-]{0,31})$`)
)

// WindowMove moves a window to a monitor (next, previous, 1-9 or a name) or a numbered
// workspace. The target is validated here, once, before any backend sees it.
func (h *Host) WindowMove(ctx context.Context, action WindowAction, id, target string) error {
	switch action {
	case ActionMoveToWorkspace:
		if !reWorkspace.MatchString(target) {
			return Errorf(ErrInvalidTarget, "workspace must be a number from 1 to 999 (got %q)", target)
		}
	case ActionMoveToMonitor:
		if !reMonitor.MatchString(target) {
			return Errorf(ErrInvalidTarget, "monitor must be next, previous, a number or a name like DP-1 (got %q)", target)
		}
	default:
		return fmt.Errorf("host: %s takes no target (WindowDo)", action)
	}
	return h.windowDo(ctx, action, id, target)
}

func (h *Host) windowDo(ctx context.Context, action WindowAction, id, target string) error {
	capability, ok := ActionCapability[action]
	if !ok {
		return fmt.Errorf("host: unknown window action %q", action)
	}
	if err := h.gate(ctx, capability); err != nil {
		return err
	}
	if id == "" {
		return Errorf(ErrWindowNotFound, "empty window id")
	}
	switch action {
	case ActionMinimize:
		return h.b.MinimizeWindow(ctx, id)
	case ActionFocus:
		return h.b.FocusWindow(ctx, id)
	case ActionClose:
		return h.b.CloseWindow(ctx, id)
	default:
		return h.b.ArrangeWindow(ctx, id, action, target)
	}
}

// Open hands a target to the OS handler and returns once the OS accepted it. There is no
// launch confirmation, by contract: it is unreliable on all three platforms. The caller is
// responsible for policy (what may be opened); see the tools package.
func (h *Host) Open(ctx context.Context, target string) error {
	if err := h.gate(ctx, CapOpen); err != nil {
		return err
	}
	if target == "" {
		return Errorf(ErrInvalidTarget, "empty target")
	}
	return h.b.Open(ctx, target)
}

// LaunchApp starts an installed app by its catalogue path. It is gated on CapOpen: launching an
// app and opening a link are the same user-visible ability.
func (h *Host) LaunchApp(ctx context.Context, path string) error {
	if err := h.gate(ctx, CapOpen); err != nil {
		return err
	}
	if !absFor(h.b.Platform(), path) {
		return Errorf(ErrInvalidTarget, "app path must be absolute: %q", path)
	}
	return h.b.LaunchApp(ctx, path)
}

// absFor judges absoluteness by the backend's platform, not the OS running the code, so the
// contract suite gives the same answer on every runner.
func absFor(platform, p string) bool {
	if platform != "windows" {
		return strings.HasPrefix(p, "/")
	}
	if strings.HasPrefix(p, `\\`) { // UNC
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}
