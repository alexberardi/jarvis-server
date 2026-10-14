package dictate

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// The Jarvis page in front (DESIGN.md §14): typing with no app named goes to the window used
// before it, with focus confirmed; without a focus history nothing is typed and the model is
// told to ask which app.

// pageIs is a JarvisPage guard that recognises the window with this id (nil front: blind).
func pageIs(id string, blind bool) func(context.Context, *Window) bool {
	return func(_ context.Context, w *Window) bool {
		if w == nil {
			return blind
		}
		return w.ID == id
	}
}

// historyWin is fakeWin with a focus history (FocusOrdered).
type historyWin struct {
	*fakeWin
	history bool
}

func (h *historyWin) ByFocus(ctx context.Context) ([]Window, bool, error) {
	ws, err := h.List(ctx)
	return ws, h.history, err
}

func TestJarvisPageTypesIntoThePreviousWindow(t *testing.T) {
	for _, c := range []struct {
		name string
		win  func() (Windows, *string)
		page string
		want string // the window typed into
		into string
	}{
		{"hyprland focus history", func() (Windows, *string) {
			active := "0x55aa02"
			return &LinuxWindows{X: hyprExec(&active)}, &active
		}, "0x55aa02", "0x55aa01", "Firefox"},
		{"sway focus lists", func() (Windows, *string) {
			active := "5"
			f := &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w", "SWAYSOCK": "/s"}, bins: bins("swaymsg")}
			f.out = func(c Cmd) (string, error) {
				switch {
				case c.line() == "swaymsg -t get_tree":
					return strings.Replace(swayTree, `"focused": true`, `"focused": `+map[bool]string{true: "true", false: "false"}[active == "5"], 1), nil
				case strings.HasSuffix(c.line(), "] focus"):
					active = strings.TrimSuffix(strings.TrimPrefix(c.Args[0], "[con_id="), "]")
				}
				return "", nil
			}
			return &swayActive{LinuxWindows: &LinuxWindows{X: f}, active: &active}, &active
		}, "5", "4", ""},
		{"X11 stacking / Windows Z order / macOS front to back", func() (Windows, *string) {
			w := &historyWin{fakeWin: newWin(), history: true}
			w.ws[2].Recency, w.ws[1].Recency = 1, 2 // kitty "notes" was used just before the page
			return w, &w.active
		}, "term", "k1", "Kitty"},
	} {
		t.Run(c.name, func(t *testing.T) {
			win, active := c.win()
			kb := &fakeKB{ok: true}
			var shown []string
			kb.onType = func() {
				if *active != c.want {
					t.Errorf("typed into %q before the focus moved to %q", *active, c.want)
				}
			}
			ts := &Tools{KB: kb, Win: win, Sleep: noSleep, JarvisPage: pageIs(c.page, false),
				Show: func(s string, _ time.Duration) { shown = append(shown, s) }}
			out := call(t, ts, ToolType, map[string]any{"text": "walk the dogs"})
			if out["success"] != true || out["instead_of_jarvis_page"] != true || !slices.Equal(kb.typed, []string{"walk the dogs"}) {
				t.Fatalf("out %v typed %q", out, kb.typed)
			}
			if c.into != "" && out["into"] != c.into {
				t.Errorf("into %v, want %q", out["into"], c.into)
			}
			if note, _ := out["note"].(string); !strings.Contains(note, "Jarvis page was in front") || !strings.Contains(note, out["into"].(string)) {
				t.Errorf("note %q", note)
			}
			if len(shown) != 1 || !strings.HasPrefix(shown[0], "Typing into ") {
				t.Errorf("island %q", shown)
			}
		})
	}
}

// swayActive lets the sway fake follow focus (the tree's "focused" flag is on con 5 only).
type swayActive struct {
	*LinuxWindows
	active *string
}

func (s *swayActive) Active(context.Context) (string, error) { return *s.active, nil }

func TestJarvisPageFocusMustBeConfirmed(t *testing.T) {
	kb := &fakeKB{ok: true}
	w := &historyWin{fakeWin: newWin(), history: true}
	w.focusLag = -1 // the compositor never moves focus
	ts := &Tools{KB: kb, Win: w, Sleep: noSleep, JarvisPage: pageIs("term", false)}
	out := call(t, ts, ToolType, map[string]any{"text": "hi"})
	if out["success"] != false || out["error_code"] != "focus_failed" || len(kb.typed) != 0 {
		t.Fatalf("out %v typed %q", out, kb.typed)
	}
}

func TestJarvisPageRefusals(t *testing.T) {
	for _, c := range []struct {
		name  string
		ts    func(kb *fakeKB) *Tools
		ask   string // the sentence must ask this
		press bool
	}{
		{"no focus history", func(kb *fakeKB) *Tools {
			return &Tools{KB: kb, Win: &historyWin{fakeWin: newWin()}, Sleep: noSleep, JarvisPage: pageIs("term", false)}
		}, "ask which app to type into, then call again with that app", false},
		{"a backend that can't order windows", func(kb *fakeKB) *Tools {
			return &Tools{KB: kb, Win: newWin(), Sleep: noSleep, JarvisPage: pageIs("term", false)}
		}, "call again with that app", false},
		{"only the page is open", func(kb *fakeKB) *Tools {
			w := &historyWin{fakeWin: newWin(), history: true}
			w.ws = w.ws[:1]
			return &Tools{KB: kb, Win: w, Sleep: noSleep, JarvisPage: pageIs("term", false)}
		}, "no other window is open", false},
		{"GNOME/KDE: no window control, the extension says the page has focus", func(kb *fakeKB) *Tools {
			return &Tools{KB: kb, Sleep: noSleep, JarvisPage: pageIs("", true)}
		}, "they need to click into that app, then ask again", false},
		{"press key, no focus history", func(kb *fakeKB) *Tools {
			return &Tools{KB: kb, Win: &historyWin{fakeWin: newWin()}, Sleep: noSleep, JarvisPage: pageIs("term", false)}
		}, "Tell the user no key was pressed and ask which app", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			kb := &fakeKB{ok: true}
			var shown []string
			ts := c.ts(kb)
			ts.Show = func(s string, _ time.Duration) { shown = append(shown, s) }
			var out map[string]any
			if c.press {
				out = call(t, ts, ToolPress, map[string]any{"key": "enter"})
			} else {
				out = call(t, ts, ToolType, map[string]any{"text": "hi"})
			}
			msg, _ := out["error"].(string)
			if out["success"] != false || out["error_code"] != CodeJarvisPage || len(kb.typed) != 0 || len(kb.keys) != 0 {
				t.Fatalf("out %v typed %q keys %v", out, kb.typed, kb.keys)
			}
			if !strings.Contains(msg, "Jarvis page is in front") || !strings.Contains(msg, c.ask) {
				t.Errorf("error %q", msg)
			}
			if len(shown) != 1 || !strings.HasPrefix(shown[0], "The Jarvis page is in front") {
				t.Errorf("island %q", shown)
			}
		})
	}
}

// Everything else is as before: a named app, a page not in front, no guard, GNOME with the
// page not in front.
func TestJarvisPageLeavesOtherTypingAlone(t *testing.T) {
	for _, c := range []struct {
		name  string
		ts    func(kb *fakeKB, w *historyWin) *Tools
		args  map[string]any
		into  string
		focus []string
	}{
		{"named app", func(kb *fakeKB, w *historyWin) *Tools {
			return &Tools{KB: kb, Win: w, Sleep: noSleep, JarvisPage: pageIs("term", false)}
		}, map[string]any{"text": "hi", "app": "claude"}, "Claude", []string{"claude"}},
		{"page not in front", func(kb *fakeKB, w *historyWin) *Tools {
			return &Tools{KB: kb, Win: w, Sleep: noSleep, JarvisPage: pageIs("k2", false)}
		}, map[string]any{"text": "hi"}, "Foot (the window in front)", nil},
		{"no guard", func(kb *fakeKB, w *historyWin) *Tools {
			return &Tools{KB: kb, Win: w, Sleep: noSleep}
		}, map[string]any{"text": "hi"}, "Foot (the window in front)", nil},
		{"GNOME, page not in front", func(kb *fakeKB, _ *historyWin) *Tools {
			return &Tools{KB: kb, Sleep: noSleep, JarvisPage: pageIs("", false)}
		}, map[string]any{"text": "hi"}, "the window in front", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			kb, w := &fakeKB{ok: true}, &historyWin{fakeWin: newWin(), history: true}
			out := call(t, c.ts(kb, w), ToolType, c.args)
			if out["success"] != true || out["into"] != c.into || out["instead_of_jarvis_page"] != nil || !slices.Equal(w.focused, c.focus) {
				t.Fatalf("out %v focused %v", out, w.focused)
			}
		})
	}
}

// The X11 and macOS histories rank windows; a failure says there is none.
func TestFocusHistoryRanking(t *testing.T) {
	ctx := context.Background()
	ws := []Window{{ID: "0x00400001"}, {ID: "0x00400002"}, {ID: "0x00400003"}}
	if !X11Stacking(fakeStacking{ids: []uint32{0x400003, 0x400001}})(ctx, ws) {
		t.Fatal("stacking")
	}
	if ws[2].Recency != 0 || ws[0].Recency != 1 || ws[1].Recency <= 1 {
		t.Errorf("x11 %+v", ws)
	}
	if X11Stacking(fakeStacking{err: errors.New("no EWMH")})(ctx, ws) {
		t.Error("stacking without a window manager")
	}

	mac := []Window{{ID: "10/1"}, {ID: "20/1"}, {ID: "20/2"}, {ID: "30/1"}}
	x := &fakeExec{out: func(c Cmd) (string, error) {
		if c.Name != "osascript" || c.Args[0] != "-l" || c.Args[1] != "JavaScript" {
			t.Errorf("ran %q", c.line())
		}
		return "20 20 10 99", nil
	}}
	if !MacFrontToBack(x)(ctx, mac) {
		t.Fatal("mac")
	}
	if !(mac[1].Recency < mac[2].Recency && mac[2].Recency < mac[0].Recency && mac[0].Recency < mac[3].Recency) {
		t.Errorf("mac %+v", mac)
	}
	if MacFrontToBack(&fakeExec{out: func(Cmd) (string, error) { return "", errors.New("denied") }})(ctx, mac) {
		t.Error("mac history from a failed script")
	}
}

type fakeStacking struct {
	ids []uint32
	err error
}

func (f fakeStacking) Stacking(context.Context) ([]uint32, error) { return f.ids, f.err }
