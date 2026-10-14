package dictate

import (
	"context"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// Focus history (DESIGN.md §14 "The Jarvis page in front"): which window the user had in front
// before the one in front now. Hyprland (focusHistoryID) and sway (each container's focus
// list) keep it; on X11 the window manager's stacking order stands in for it (it raises what it
// focuses), on Windows the Z order, on macOS the front-to-back order of the on-screen windows.
// GNOME and KDE on Wayland let no ordinary program list windows at all.

// FocusOrdered is a Windows backend that can list windows by when they last had focus.
type FocusOrdered interface {
	// ByFocus lists the windows with Recency in focus order (0: focused most recently). ok is
	// false when this desktop keeps no such history; the list is then List's.
	ByFocus(ctx context.Context) (ws []Window, ok bool, err error)
}

// ByFocus: Hyprland's and sway's lists are already in focus order; X11 asks its host backend.
func (l *LinuxWindows) ByFocus(ctx context.Context) ([]Window, bool, error) {
	switch l.compositor() {
	case "hyprland", "sway":
		ws, err := l.List(ctx)
		return ws, err == nil, err
	}
	if fo, ok := l.X11.(FocusOrdered); ok && l.X.Getenv("WAYLAND_DISPLAY") == "" {
		return fo.ByFocus(ctx)
	}
	ws, err := l.List(ctx)
	return ws, false, err
}

// ByFocus is List with Recency from HistoryFn, when there is one and it works.
func (w *HostWindows) ByFocus(ctx context.Context) ([]Window, bool, error) {
	ws, err := w.List(ctx)
	if err != nil || w.HistoryFn == nil {
		return ws, false, err
	}
	return ws, w.HistoryFn(ctx, ws), nil
}

// rankBy sets each window's Recency from its key's place in order (most recent first); windows
// whose key isn't there come after, in list order.
func rankBy(ws []Window, order []string, key func(Window) string) {
	pos := make(map[string]int, len(order))
	for i, k := range order {
		if _, dup := pos[k]; !dup {
			pos[k] = i
		}
	}
	for i := range ws {
		if p, ok := pos[key(ws[i])]; ok {
			ws[i].Recency = p
		} else {
			ws[i].Recency = len(order) + i
		}
	}
}

// X11Stacking is X11's focus history: the EWMH stacking order, top first.
func X11Stacking(wm interface {
	Stacking(ctx context.Context) ([]uint32, error)
}) func(context.Context, []Window) bool {
	return func(ctx context.Context, ws []Window) bool {
		ids, err := wm.Stacking(ctx)
		if err != nil {
			return false
		}
		order := make([]string, len(ids))
		for i, id := range ids {
			order[i] = strconv.FormatUint(uint64(id), 10)
		}
		rankBy(ws, order, func(w Window) string {
			id, ok := x11.ParseID(w.ID)
			if !ok {
				return ""
			}
			return strconv.FormatUint(uint64(id), 10)
		})
		return true
	}
}

// ZOrder: Windows' EnumWindows lists top-level windows top first, which List keeps.
func ZOrder(context.Context, []Window) bool { return true }

// macFrontToBack lists the pids of the on-screen app windows front to back (layer 0, the
// normal window layer), from CGWindowListCopyWindowInfo through JavaScript for Automation:
// owner pids need no Screen Recording grant.
const macFrontToBack = `ObjC.import('CoreGraphics');
var l = ObjC.castRefToObject($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly | $.kCGWindowListExcludeDesktopElements, $.kCGNullWindowID));
var out = [];
for (var i = 0; i < l.count; i++) {
  var w = l.objectAtIndex(i);
  if (ObjC.unwrap(w.objectForKey('kCGWindowLayer')) == 0) out.push(ObjC.unwrap(w.objectForKey('kCGWindowOwnerPID')));
}
out.join(' ');`

// MacFrontToBack is macOS's focus history: apps in front-to-back order of their windows, and
// within an app System Events' own order (window 1 is its frontmost). A window id is pid/index.
func MacFrontToBack(x Exec) func(context.Context, []Window) bool {
	return func(ctx context.Context, ws []Window) bool {
		out, err := x.Run(ctx, Cmd{Name: "osascript", Args: []string{"-l", "JavaScript", "-e", macFrontToBack}})
		if err != nil {
			return false
		}
		var pids []string
		for _, p := range strings.Fields(out) {
			if _, err := strconv.Atoi(p); err == nil {
				pids = append(pids, p)
			}
		}
		if len(pids) == 0 {
			return false
		}
		appRank := map[string]int{}
		for _, p := range pids {
			if _, ok := appRank[p]; !ok {
				appRank[p] = len(appRank)
			}
		}
		for i := range ws {
			pid, idx, _ := strings.Cut(ws[i].ID, "/")
			n, _ := strconv.Atoi(idx)
			r, ok := appRank[pid]
			if !ok {
				r = len(appRank) + i
			}
			ws[i].Recency = r*1000 + n
		}
		return true
	}
}
