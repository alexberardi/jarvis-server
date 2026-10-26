// Package views is the desktop's view source on Linux (desktop.ViewSource): the tabs and sheets
// of an app's front window from AT-SPI (VOICE-FLOWS.md Q26), found by the window's pid. The
// matching and the choice list are desktop's; this only lists and switches.
package views

import (
	"context"
	"errors"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

// Source lists views through AT-SPI. Tabs and Select are atspi's (injected in tests).
type Source struct {
	Windows computer.Windows
	Host    *host.Host // focuses a named app's window before switching (nil: not focused)
	Tabs    func(ctx context.Context, pid int) ([]atspi.Tab, error)
	Select  func(ctx context.Context, pid, index int, name string) error
}

// ErrNoWindow: no window of the app the user named (or none focused).
var ErrNoWindow = errors.New("no such window")

// target is the window views are read from: the focused one, or the named app's (its focused
// window first, else its front one).
func (s *Source) target(ctx context.Context, app string) (computer.DesktopWindow, error) {
	ws, err := s.Windows.List(ctx)
	if err != nil {
		return computer.DesktopWindow{}, err
	}
	var best *computer.DesktopWindow
	for i := range ws {
		w := &ws[i]
		if w.Layer != 0 || w.PID <= 0 {
			continue
		}
		if app == "" {
			if w.Focused || best == nil {
				best = w
			}
			if w.Focused {
				break
			}
			continue
		}
		if !sameApp(app, w.App) {
			continue
		}
		if best == nil || (w.Focused && !best.Focused) {
			best = w
		}
	}
	if best == nil {
		if app == "" {
			return computer.DesktopWindow{}, ErrNoWindow
		}
		return computer.DesktopWindow{}, errors.Join(ErrNoWindow, errors.New("no "+app+" window is open"))
	}
	return *best, nil
}

func sameApp(said, app string) bool {
	a, b := strings.ToLower(strings.TrimSpace(said)), strings.ToLower(app)
	return a == b || strings.Contains(b, a) || winmatch.Similarity(said, app) >= 0.8
}

func (s *Source) Views(ctx context.Context, app string) (string, []desktop.View, error) {
	w, err := s.target(ctx, app)
	if err != nil {
		return "", nil, err
	}
	tabs, err := s.Tabs(ctx, w.PID)
	if err != nil {
		return w.App, nil, err
	}
	out := make([]desktop.View, len(tabs))
	for i, t := range tabs {
		out[i] = desktop.View{Index: t.Index, Name: t.Name, Selected: t.Selected}
	}
	return w.App, out, nil
}

func (s *Source) Switch(ctx context.Context, app string, v desktop.View) error {
	w, err := s.target(ctx, app)
	if err != nil {
		return err
	}
	if err := s.Select(ctx, w.PID, v.Index, v.Name); err != nil {
		return err
	}
	if !w.Focused && s.Host != nil {
		s.focus(ctx, w)
	}
	return nil
}

// focus brings the window forward, matched in the host's list by app and title (best effort:
// the tab is already switched).
func (s *Source) focus(ctx context.Context, w computer.DesktopWindow) {
	hw, err := s.Host.ListWindows(ctx)
	if err != nil {
		return
	}
	for _, h := range hw {
		if strings.EqualFold(h.App, w.App) && h.Title == w.Title {
			_ = s.Host.WindowDo(ctx, host.ActionFocus, h.ID)
			return
		}
	}
}
