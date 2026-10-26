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

// targets are the windows views may be read from, best first: the named app's (its focused
// window first), or with no app named the focused window, then the rest front to back. The
// focused window is often the terminal the user talks over (2026-10-25: "switch to the Claude
// tab in Firefox" read foot's tabs and failed), so the first one that has views wins.
func (s *Source) targets(ctx context.Context, app string) ([]computer.DesktopWindow, error) {
	ws, err := s.Windows.List(ctx)
	if err != nil {
		return nil, err
	}
	var focused, rest []computer.DesktopWindow
	for _, w := range ws {
		if w.Layer != 0 || w.PID <= 0 || (app != "" && !sameApp(app, w.App)) {
			continue
		}
		if w.Focused {
			focused = append(focused, w)
		} else {
			rest = append(rest, w)
		}
	}
	out := append(focused, rest...)
	if len(out) == 0 {
		if app == "" {
			return nil, ErrNoWindow
		}
		return nil, errors.Join(ErrNoWindow, errors.New("no "+app+" window is open"))
	}
	return out, nil
}

// target is the first of targets whose app shows views (its tabs come along); with none, the
// first window and the error reading it.
func (s *Source) target(ctx context.Context, app string) (computer.DesktopWindow, []atspi.Tab, error) {
	ws, err := s.targets(ctx, app)
	if err != nil {
		return computer.DesktopWindow{}, nil, err
	}
	var firstErr error
	seen := map[int]bool{}
	for _, w := range ws {
		if seen[w.PID] {
			continue
		}
		seen[w.PID] = true
		tabs, err := s.Tabs(ctx, w.PID)
		if err == nil && len(tabs) > 0 {
			return w, tabs, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return ws[0], nil, firstErr
}

func sameApp(said, app string) bool {
	a, b := strings.ToLower(strings.TrimSpace(said)), strings.ToLower(app)
	return a == b || strings.Contains(b, a) || winmatch.Similarity(said, app) >= 0.8
}

func (s *Source) Views(ctx context.Context, app string) (string, []desktop.View, error) {
	w, tabs, err := s.target(ctx, app)
	if err != nil {
		return w.App, nil, err
	}
	out := make([]desktop.View, len(tabs))
	for i, t := range tabs {
		out[i] = desktop.View{Index: t.Index, Name: t.Name, Selected: t.Selected, Window: t.Window}
	}
	return w.App, out, nil
}

func (s *Source) Switch(ctx context.Context, app string, v desktop.View) error {
	w, _, err := s.target(ctx, app)
	if err != nil {
		return err
	}
	if err := s.Select(ctx, w.PID, v.Index, v.Name); err != nil {
		return err
	}
	if s.Host != nil && (!w.Focused || v.Window != "") {
		s.focus(ctx, w, v)
	}
	return nil
}

// focus brings forward the window v is in (v.Window, its title before the switch; after it the
// title starts with v's name), matched in the host's list by app and title; w itself when v
// names no window. Best effort: the tab is already switched.
func (s *Source) focus(ctx context.Context, w computer.DesktopWindow, v desktop.View) {
	hw, err := s.Host.ListWindows(ctx)
	if err != nil {
		return
	}
	for _, h := range hw {
		if !strings.EqualFold(h.App, w.App) {
			continue
		}
		// The title may already be the switched-to tab's ("Claude — Mozilla Firefox").
		if (v.Window == "" && h.Title == w.Title) || (v.Window != "" && (h.Title == v.Window || strings.HasPrefix(h.Title, v.Name))) {
			if !h.Focused {
				_ = s.Host.WindowDo(ctx, host.ActionFocus, h.ID)
			}
			return
		}
	}
}
