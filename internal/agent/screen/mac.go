package screen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// FrontWindow is macOS's frontmost normal window (layer 0): its CGWindowID, title and app.
type FrontWindow struct {
	ID    uint32
	Title string
	App   string
}

// Mac captures with /usr/sbin/screencapture. The agent runs as Jarvis Agent.app under launchd,
// so the child inherits the app's Screen Recording grant (TCC attributes to the responsible
// app); started from a terminal it would use the terminal's.
type Mac struct {
	X       Exec
	Trusted func() bool                 // the Screen Recording grant (macax.ScreenTrusted)
	Front   func() (FrontWindow, error) // the frontmost window (CGWindowListCopyWindowInfo)
}

const screencapture = "/usr/sbin/screencapture"

func (m *Mac) Available(context.Context) (bool, string) {
	if m.Trusted == nil || !m.Trusted() {
		return false, "Jarvis Agent isn't allowed to record the screen: turn it on in System Settings > Privacy & Security > " +
			"Screen & System Audio Recording, then restart the agent"
	}
	return true, ""
}

func (m *Mac) Capture(ctx context.Context, scope string) (Shot, error) {
	// screencapture writes only to a file: a private directory, removed before returning.
	dir, err := os.MkdirTemp("", "jarvis-screen-")
	if err != nil {
		return Shot{}, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "shot.png")
	args := []string{"-x", "-t", "png"} // -x: no sound
	var shot Shot
	if scope == ScopeWindow && m.Front != nil {
		if w, err := m.Front(); err == nil && w.ID != 0 {
			args = append(args, "-o", "-l", strconv.FormatUint(uint64(w.ID), 10)) // -o: no shadow
			shot.Title, shot.App = w.Title, w.App
		}
	}
	if len(args) == 3 {
		args = append(args, "-m") // the main display
	}
	if _, err := m.X.Output(ctx, screencapture, append(args, file)...); err != nil {
		return Shot{}, err
	}
	b, err := os.ReadFile(file)
	_ = os.Remove(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Shot{}, errors.New("screencapture produced no image (is Screen Recording allowed for Jarvis Agent?)")
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
