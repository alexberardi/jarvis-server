package dictate

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/launches"
)

// "open omawrite and jot down a note to update Steam" (heard "Can you open Omerite and drop
// down a note to update Steam?"): desktop_open launches it, and desktop_type_text with the
// same misheard name follows at once, before the window exists. It waits for the window.

func installed() ([]apps.App, error) {
	return []apps.App{{Name: "Omawrite", ID: "omawrite"}, {Name: "Neovim", ID: "nvim"}, {Name: "Steam", ID: "steam"}}, nil
}

// appearAfter makes w appear in win after n waits; it returns the Sleep and a wait counter.
func appearAfter(win *fakeWin, n int, w Window) (func(context.Context, time.Duration) error, *int) {
	waits := 0
	return func(context.Context, time.Duration) error {
		waits++
		if waits == n {
			win.ws = append(win.ws, w)
		}
		return nil
	}, &waits
}

func launched(win *fakeWin, name, id string) *launches.Tracker {
	tr := &launches.Tracker{Windows: func(ctx context.Context) ([]string, error) {
		ws, _ := win.List(ctx)
		var ids []string
		for _, w := range ws {
			ids = append(ids, w.ID)
		}
		return ids, nil
	}}
	tr.Record(context.Background(), name, id)
	return tr
}

func TestTypeWaitsForTheLaunchedAppsWindow(t *testing.T) {
	win := omawriteDesktop(false)
	tr := launched(win, "Omawrite", "omawrite")
	sleep, waits := appearAfter(win, 3, Window{ID: "ow", Apps: []string{"omawrite"}, Title: "untitled", Visible: true})
	kb := &fakeKB{ok: true}
	ts := &Tools{KB: kb, Win: win, Sleep: sleep, Launched: tr, Apps: installed}
	out := call(t, ts, ToolType, map[string]any{"app": "Omerite", "text": "update Steam"})
	if out["success"] != true || out["matched_app"] != "omawrite" || out["heard_as"] != "Omerite" {
		t.Fatalf("result %v", out)
	}
	if !slices.Equal(kb.typed, []string{"update Steam"}) || !slices.Equal(win.focused, []string{"ow"}) {
		t.Fatalf("typed %q focused %v", kb.typed, win.focused)
	}
	if *waits < 3 {
		t.Fatalf("typed after %d waits, before the window existed", *waits)
	}
}

// Neovim runs in a terminal: its window is the terminal's, so no name matches it. The one
// window that appeared since the launch is it.
func TestTypeIntoATerminalAppsNewWindow(t *testing.T) {
	win := omawriteDesktop(false)
	tr := launched(win, "Neovim", "nvim")
	sleep, _ := appearAfter(win, 2, Window{ID: "nv", Apps: []string{"Alacritty"}, Title: "nvim", Visible: true})
	kb := &fakeKB{ok: true}
	ts := &Tools{KB: kb, Win: win, Sleep: sleep, Launched: tr, Apps: installed}
	out := call(t, ts, ToolType, map[string]any{"app": "NeoVim", "text": "hello"})
	if out["success"] != true || out["into"] != "Neovim" || !slices.Equal(win.focused, []string{"nv"}) {
		t.Fatalf("result %v focused %v", out, win.focused)
	}
}

func TestLaunchedWindowThatNeverAppearsTypesNothing(t *testing.T) {
	win := omawriteDesktop(false)
	tr := launched(win, "Omawrite", "omawrite")
	waits := 0
	kb := &fakeKB{ok: true}
	ts := &Tools{KB: kb, Win: win, Launched: tr, Apps: installed, WindowWait: time.Second,
		Sleep: func(context.Context, time.Duration) error { waits++; return nil }}
	out := call(t, ts, ToolType, map[string]any{"app": "omawrite", "text": "x"})
	if out["success"] != false || out["error_code"] != CodeNotReady || len(kb.typed) != 0 {
		t.Fatalf("result %v typed %q", out, kb.typed)
	}
	if waits != 3 { // 1 s / 250 ms = 4 looks, 3 waits between them
		t.Fatalf("waited %d times", waits)
	}
	want := "Nothing was typed: Omawrite was opened, but its window hasn't appeared yet. Tell the user nothing was typed and to ask again once Omawrite is open."
	if out["error"] != want {
		t.Fatalf("error %q", out["error"])
	}
}

// Another app launched last: no waiting, the usual not-found / not-open answer.
func TestNoWaitForAnAppThatWasntLaunched(t *testing.T) {
	win := omawriteDesktop(false)
	tr := launched(win, "Steam", "steam")
	waits := 0
	kb := &fakeKB{ok: true}
	ts := &Tools{KB: kb, Win: win, Launched: tr, Apps: installed,
		Sleep: func(context.Context, time.Duration) error { waits++; return nil }}
	out := call(t, ts, ToolType, map[string]any{"app": "Omerite", "text": "x"})
	if waits != 0 || out["error_code"] != CodeNotOpen || len(kb.typed) != 0 {
		t.Fatalf("waits %d result %v", waits, out)
	}
	if out["error"] != "Nothing was typed: Omawrite is installed but not open. Open it with desktop_open, then type." {
		t.Fatalf("error %q", out["error"])
	}
}
