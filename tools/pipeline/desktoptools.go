package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/clock"
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
)

// RenderDesktopToolset is the client_tools a desktop agent sends at /conversation/start, rendered
// by the agent's own toolsets in its order (cmd/jarvis-agent toolFlags.build: desktop, screen,
// dictation, browser, clock). It is the tool list of a typical Linux agent, the one a real test
// pass ran on: apps, volume, screen capture and typing, the browser extension connected, no
// window control (a compositor the agent can't drive windows on), no sidecar commands, and the
// dictation confirm tool off (the default).
//
// toolsets/desktop.json is this output (`pipeline toolset desktop`), and a test keeps it equal,
// so a desktop scenario offers the model what a real agent does: descriptions, examples and
// keywords included. The keywords matter: command-center's must-call guard only fires on an
// utterance that matches one (textfilter.ForceToolsGate).
func RenderDesktopToolset() ([]byte, error) { return renderDesktop(allCaps{}) }

// RenderDesktopWindowsToolset is RenderDesktopToolset on a desktop that can also list, focus,
// minimize and close windows (macOS with the Accessibility grant, X11): toolsets/desktop-windows.json
// (`pipeline toolset desktop-windows`).
func RenderDesktopWindowsToolset() ([]byte, error) { return renderDesktop(windowCaps{}) }

func renderDesktop(caps host.Backend) ([]byte, error) {
	ctx := context.Background()
	ts := node.Multi{
		desktop.New(host.New(caps), desktop.OpenPolicy{}, nil),
		&screen.Tools{C: allCaps{}},
		&dictate.Tools{KB: allCaps{}},
	}
	defs := ts.Available(ctx)
	defs = append(defs, browser.NewTools(nil).Defs()...)
	defs = append(defs, (&clock.Tool{}).Available(ctx)...)
	var tools []any
	for _, d := range defs {
		tools = append(tools, d.ClientTool)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(tools); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// allCaps is a desktop that does nothing: it is only asked what it can do (apps, volume, screen
// capture and typing; no window control).
type allCaps struct{}

var errRenderOnly = errors.New("pipeline: render only")

func (allCaps) Platform() string { return "linux" }
func (allCaps) Capabilities(context.Context) []string {
	return []string{host.CapOpen, host.CapVolumeGet, host.CapVolumeSet}
}

// windowCaps is allCaps with window control.
type windowCaps struct{ allCaps }

func (windowCaps) Capabilities(ctx context.Context) []string {
	return append(allCaps{}.Capabilities(ctx), host.AllWindowCaps...)
}

func (allCaps) GetVolume(context.Context) (int, error)             { return 0, errRenderOnly }
func (allCaps) ApplyVolume(context.Context, int) error             { return errRenderOnly }
func (allCaps) ListWindows(context.Context) ([]host.Window, error) { return nil, errRenderOnly }
func (allCaps) MinimizeWindow(context.Context, string) error       { return errRenderOnly }
func (allCaps) FocusWindow(context.Context, string) error          { return errRenderOnly }
func (allCaps) CloseWindow(context.Context, string) error          { return errRenderOnly }
func (allCaps) Open(context.Context, string) error                 { return errRenderOnly }
func (allCaps) LaunchApp(context.Context, string) error            { return errRenderOnly }

// screen.Capturer and dictate.Keyboard.
func (allCaps) Available(context.Context) (bool, string) { return true, "" }
func (allCaps) Capture(context.Context, string, string) (screen.Shot, error) {
	return screen.Shot{}, errRenderOnly
}
func (allCaps) Name() string                             { return "render" }
func (allCaps) Type(context.Context, string) error       { return errRenderOnly }
func (allCaps) Press(context.Context, dictate.Key) error { return errRenderOnly }
