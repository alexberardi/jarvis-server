package screen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// MacWindow is one entry of CGWindowListCopyWindowInfo, in its order (front to back).
type MacWindow struct {
	ID       uint32
	Title    string // empty without the Screen Recording grant
	App      string // owner name
	Layer    int
	W, H     float64
	OnScreen bool
}

// minSide: smaller windows are status items, tooltips and helpers, never what the user means.
const minSide = 50

// normal: a layer-0 window big enough to be one the user looks at.
func (w MacWindow) normal() bool {
	return w.ID != 0 && w.Layer == 0 && w.W >= minSide && w.H >= minSide
}

// frontOf is the frontmost normal on-screen window.
func frontOf(ws []MacWindow) (MacWindow, bool) {
	for _, w := range ws {
		if w.normal() && w.OnScreen {
			return w, true
		}
	}
	return MacWindow{}, false
}

// namedOf is the window the user named: screencapture -l captures a window even when it is
// covered, so any on-screen window works; one that isn't on screen (minimised, another Space)
// is window_not_visible. The list is front to back, so the frontmost match wins.
func namedOf(ws []MacWindow, query string) (MacWindow, error) {
	var keep []MacWindow
	var cands []candidate
	for _, w := range ws {
		if !w.normal() {
			continue
		}
		cands = append(cands, candidate{Apps: []string{w.App}, Title: w.Title, Recency: len(keep), Visible: w.OnScreen})
		keep = append(keep, w)
	}
	i, err := pickWindow(cands, query)
	if err != nil {
		return MacWindow{}, err
	}
	return keep[i], nil
}

// Mac captures with /usr/sbin/screencapture. The agent runs as Jarvis Automation Agent.app under launchd,
// so the child inherits the app's Screen Recording grant (TCC attributes to the responsible
// app); started from a terminal it would use the terminal's.
type Mac struct {
	X       Exec
	Trusted func() bool                 // the Screen Recording grant (macax.ScreenTrusted)
	Windows func() ([]MacWindow, error) // the window list, front to back (CGWindowListCopyWindowInfo)
}

const screencapture = "/usr/sbin/screencapture"

func (m *Mac) Available(context.Context) (bool, string) {
	if m.Trusted == nil || !m.Trusted() {
		return false, "Jarvis Automation Agent isn't allowed to record the screen: turn it on in System Settings > Privacy & Security > " +
			"Screen & System Audio Recording, then restart the agent"
	}
	return true, ""
}

func (m *Mac) Capture(ctx context.Context, scope, window string) (Shot, error) {
	var (
		win    MacWindow
		hasWin bool
	)
	if named := normalizeQuery(window) != ""; named || scope == ScopeWindow {
		var ws []MacWindow
		if m.Windows != nil {
			var err error
			if ws, err = m.Windows(); err != nil && named {
				return Shot{}, err
			}
		}
		if named {
			w, err := namedOf(ws, window)
			if err != nil {
				return Shot{}, err
			}
			win, hasWin = w, true
		} else {
			win, hasWin = frontOf(ws)
		}
	}
	// screencapture writes only to a file: a private directory, removed before returning.
	dir, err := os.MkdirTemp("", "jarvis-screen-")
	if err != nil {
		return Shot{}, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "shot.png")
	args := []string{"-x", "-t", "png"} // -x: no sound
	var shot Shot
	if hasWin {
		args = append(args, "-o", "-l", strconv.FormatUint(uint64(win.ID), 10)) // -o: no shadow
		shot.Title, shot.App = win.Title, win.App
	} else {
		args = append(args, "-m") // the main display
	}
	if _, err := m.X.Output(ctx, screencapture, append(args, file)...); err != nil {
		return Shot{}, err
	}
	b, err := os.ReadFile(file)
	_ = os.Remove(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Shot{}, errors.New("screencapture produced no image (is Screen Recording allowed for Jarvis Automation Agent?)")
		}
		return Shot{}, err
	}
	img, err := decode(b)
	if err != nil {
		return Shot{}, err
	}
	shot.Image = img
	return shot, nil
}
