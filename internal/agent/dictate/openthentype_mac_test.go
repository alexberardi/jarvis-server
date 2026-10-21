package dictate

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/launches"
)

// macWin is fakeWin with macOS's ids: pid/index, index 1 the app's front window.
type macWin struct{ *fakeWin }

func (m macWin) Same(id, active string) bool { return SameMacPid(id, active) }

// Active is the front app's pid, as MacFrontPid has it.
func (m macWin) Active(ctx context.Context) (string, error) {
	a, err := m.fakeWin.Active(ctx)
	pid, _, _ := strings.Cut(a, "/")
	return pid, err
}

// On macOS the launch snapshot is the window server's (CoreGraphics window numbers by pid),
// not System Events' pid/index ids. "open Neovim and type hello": the terminal (pid 300,
// already open) gets a new window; the window server shows a new window of pid 300, and the
// typing goes to that app's front window.
func TestTypeIntoTheNewWindowFoundByTheWindowServer(t *testing.T) {
	fw := &fakeWin{active: "100/1", ws: []Window{
		{ID: "100/1", Apps: []string{"Safari"}, Title: "News", Visible: true},
		{ID: "300/1", Apps: []string{"Terminal"}, Title: "zsh", Visible: true},
	}}
	win := macWin{fw}
	cg := []launches.Owned{{ID: "11", Owner: "100"}, {ID: "31", Owner: "300"}}
	tr := &launches.Tracker{Owners: func(context.Context) ([]launches.Owned, error) { return cg, nil }}
	tr.Record(context.Background(), "Neovim", "nvim")
	waits := 0
	sleep := func(context.Context, time.Duration) error {
		waits++
		if waits == 2 {
			cg = append([]launches.Owned{{ID: "32", Owner: "300"}}, cg...)
			fw.ws = []Window{{ID: "300/1", Apps: []string{"Terminal"}, Title: "nvim", Visible: true}, fw.ws[0],
				{ID: "300/2", Apps: []string{"Terminal"}, Title: "zsh", Visible: true}}
		}
		return nil
	}
	kb := &fakeKB{ok: true}
	ts := &Tools{KB: kb, Win: win, Sleep: sleep, Launched: tr, Apps: installed}
	out := call(t, ts, ToolType, map[string]any{"app": "NeoVim", "text": "hello"})
	if out["success"] != true || out["into"] != "Neovim" || !slices.Equal(fw.focused, []string{"300/1"}) {
		t.Fatalf("result %v focused %v", out, fw.focused)
	}
	if waits < 2 {
		t.Fatalf("typed after %d waits, before the window existed", waits)
	}
}
