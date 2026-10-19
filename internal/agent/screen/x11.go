package screen

import (
	"context"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/x11"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// X11Display is the slice of *x11.Display the X11 capturer uses.
type X11Display interface {
	Session() (bool, string)
	Active(ctx context.Context) (uint32, error)
	Windows(ctx context.Context) ([]x11.Window, error)
	Stacking(ctx context.Context) ([]uint32, error)
	Capture(ctx context.Context, win uint32) (x11.Capture, error)
}

// X11 captures an X11 session with GetImage on the root window: the screen, or the area of the
// active (_NET_ACTIVE_WINDOW) or named window. The pixels are what the screen shows there, so a
// window on top of a named one shows too. X11 has one pixel coordinate space: scale 1.
type X11 struct {
	D X11Display
}

func (x *X11) Available(context.Context) (bool, string) {
	if x.D == nil {
		return false, "reading the screen isn't supported in this build"
	}
	if ok, why := x.D.Session(); !ok {
		return false, "reading the screen needs an X11 session: " + why
	}
	return true, ""
}

// x11Apps is WM_CLASS ("instance.Class") as app names, Class first.
func x11Apps(class string) []string {
	parts := strings.Split(class, ".")
	slices.Reverse(parts)
	return slices.DeleteFunc(parts, func(s string) bool { return s == "" })
}

func (x *X11) Capture(ctx context.Context, scope, window string) (Shot, error) {
	named := normalizeQuery(window) != ""
	var (
		win  uint32
		info x11.Window
	)
	if named || scope == ScopeWindow {
		ws, err := x.D.Windows(ctx)
		if err != nil && named {
			return Shot{}, err
		}
		if named {
			stack, _ := x.D.Stacking(ctx)
			cands := make([]candidate, len(ws))
			for i, w := range ws {
				recency := slices.Index(stack, w.ID)
				if recency < 0 {
					recency = len(stack) + i
				}
				cands[i] = candidate{Apps: x11Apps(w.Class), Title: w.Title, Recency: recency, Visible: !w.Minimized}
			}
			i, err := pickWindow(cands, window)
			if err != nil {
				return Shot{}, err
			}
			info = ws[i]
		} else if id, err := x.D.Active(ctx); err == nil && id != 0 {
			info = x11.Window{ID: id}
			for _, w := range ws {
				if w.ID == id {
					info = w
				}
			}
		}
		// No active window: the screen instead.
		win = info.ID
	}
	c, err := x.D.Capture(ctx, win)
	if err != nil {
		return Shot{}, err
	}
	shot := Shot{Image: c.Image, Title: info.Title,
		Geometry: computeruse.Geometry{OriginX: c.X, OriginY: c.Y, LogicalW: c.W, LogicalH: c.H, Scale: 1}}
	if apps := x11Apps(info.Class); len(apps) > 0 {
		shot.App = apps[0]
	}
	return shot, nil
}
