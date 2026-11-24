package dictate

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// The key between lines comes from the app with the focus, never from the model: Enter in a
// chat sends half a message, in a terminal runs a command (2026-11-13: a typed summary lost its
// paragraphs because every line break became a space).
func TestLineBreakKeyByApp(t *testing.T) {
	for _, c := range []struct {
		apps []string
		want Key
	}{
		{[]string{"org.gnome.TextEditor"}, KeyEnter},
		{[]string{"gedit"}, KeyEnter},
		{[]string{"libreoffice-writer"}, KeyEnter},
		{[]string{"soffice"}, KeyEnter},
		{[]string{"org.kde.kate"}, KeyEnter},
		{[]string{"com.apple.TextEdit"}, KeyEnter},
		{[]string{"WINWORD.EXE"}, KeyEnter},
		{[]string{"Slack"}, KeyShiftEnter},
		{[]string{"discord"}, KeyShiftEnter},
		{[]string{"brave-browser"}, KeyShiftEnter},
		{[]string{"Claude"}, KeyShiftEnter},
		{[]string{"some-app-nobody-listed"}, KeyShiftEnter},
		{[]string{"foot"}, ""},
		{[]string{"com.mitchellh.ghostty"}, ""},
		{[]string{"org.gnome.Console"}, ""},
		{[]string{"gnome-terminal-server"}, ""},
		{[]string{"xfce4-terminal"}, ""},
		{[]string{"com.apple.Terminal"}, ""},
		{[]string{"WindowsTerminal.exe"}, ""},
		{[]string{"gedit", "kitty"}, ""}, // a terminal by any name is a terminal
		{nil, ""},                        // can't tell: spaces
		{[]string{""}, ""},
	} {
		if got := lineBreakKey(c.apps); got != c.want {
			t.Errorf("%q: %q, want %q", c.apps, got, c.want)
		}
	}
}

func TestSanitizeLines(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"one line", []string{"one line"}},
		{"a\nb", []string{"a", "b"}},
		{"a\r\n\r\n\r\n\nb\n\n", []string{"a", "", "b"}}, // blank lines collapse to one paragraph break
		{"\n\n  title\t\x1b[31m\nbody  ", []string{"title [31m", "body"}},
	} {
		got, err := SanitizeLines(c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%q: %q %v, want %q", c.in, got, err, c.want)
		}
	}
	if _, err := SanitizeLines("\n \n"); !errors.Is(err, errBadArgs) {
		t.Errorf("blank text: %v", err)
	}
	// The limit counts line breaks.
	if _, err := SanitizeLines(strings.Repeat("a\n", MaxText/2) + "a"); !errors.Is(err, errBadArgs) {
		t.Errorf("over the limit: %v", err)
	}
	if _, err := SanitizeLines(strings.Repeat("a\n", MaxText/2-1) + "a"); err != nil {
		t.Errorf("at the limit: %v", err)
	}
}

// End to end through the tool: the focused window picks the key; the result says which.
func TestTypedLinesFollowTheFocusedApp(t *testing.T) {
	text := "Title\n\nFirst paragraph.\nSecond line."
	for _, c := range []struct {
		app    string
		keys   []Key
		typed  []string
		breaks string
	}{
		{"org.gnome.TextEditor", []Key{KeyEnter, KeyEnter, KeyEnter}, []string{"Title", "First paragraph.", "Second line."}, "enter"},
		{"Slack", []Key{KeyShiftEnter, KeyShiftEnter, KeyShiftEnter}, []string{"Title", "First paragraph.", "Second line."}, "shift+enter"},
		{"foot", nil, []string{"Title First paragraph. Second line."}, "spaces"},
	} {
		w := &fakeWin{active: "w", ws: []Window{{ID: "w", Apps: []string{c.app}, Title: "x", Visible: true}}}
		kb := &fakeKB{ok: true}
		ts := &Tools{KB: kb, Win: w, Sleep: noSleep}
		out := call(t, ts, ToolType, map[string]any{"text": text})
		if out["success"] != true || out["line_breaks"] != c.breaks {
			t.Errorf("%s: %v", c.app, out)
		}
		if !slices.Equal(kb.keys, c.keys) || !slices.Equal(kb.typed, c.typed) {
			t.Errorf("%s: keys %q typed %q", c.app, kb.keys, kb.typed)
		}
	}
}

// A chat message with lines still sends only on submit, after the last line.
func TestSubmitAfterLinesInAChat(t *testing.T) {
	w := &fakeWin{active: "s", ws: []Window{{ID: "s", Apps: []string{"Slack"}, Title: "general", Visible: true}}}
	kb := &fakeKB{ok: true}
	ts := &Tools{KB: kb, Win: w, Sleep: noSleep}
	out := call(t, ts, ToolType, map[string]any{"text": "hi\nsee you at five", "app": "slack", "submit": true})
	if out["success"] != true || out["submitted"] != true {
		t.Fatalf("%v", out)
	}
	if !slices.Equal(kb.keys, []Key{KeyShiftEnter, KeyEnter}) {
		t.Fatalf("keys %q", kb.keys)
	}
}

// Without a window backend nothing says which app has the focus: lines are joined with spaces.
func TestNoWindowBackendJoinsLines(t *testing.T) {
	kb := &fakeKB{ok: true}
	out := call(t, &Tools{KB: kb, Sleep: noSleep}, ToolType, map[string]any{"text": "a\nb"})
	if out["success"] != true || len(kb.keys) != 0 || !slices.Equal(kb.typed, []string{"a b"}) {
		t.Fatalf("%v keys %q typed %q", out, kb.keys, kb.typed)
	}
}

// Shift+Enter is typing's own key: desktop_press_key's closed set doesn't offer it.
func TestShiftEnterIsNotAPressKey(t *testing.T) {
	if slices.Contains(Keys, KeyShiftEnter) {
		t.Fatal("shift+enter offered to desktop_press_key")
	}
	if _, err := ParseKey("shift+enter"); err == nil {
		t.Fatal("shift+enter parsed as a key")
	}
}

// Shift+Enter holds Shift on every keyboard: without it, it is Enter (a chat message sent).
func TestShiftEnterHoldsShiftOnEveryKeyboard(t *testing.T) {
	ctx := context.Background()
	nat := &fakeInjector{ok: true}
	k := &LinuxKeyboard{X: &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w", "HYPRLAND_INSTANCE_SIGNATURE": "x"}}, Wayland: nat}
	if err := k.Press(ctx, KeyShiftEnter); err != nil || len(nat.sent) != 1 || nat.sent[0][0] != (xkb.Stroke{Sym: xkb.KeyReturn, Shift: true}) {
		t.Errorf("built-in: %v %v", err, nat.sent)
	}
	for _, c := range []struct {
		f    *fakeExec
		want string
	}{
		{&fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w"}, bins: bins("wtype")}, "wtype -s 40 -M shift -k Return -m shift"},
		{&fakeExec{env: map[string]string{"DISPLAY": ":0"}, bins: bins("xdotool")}, "xdotool key --clearmodifiers shift+Return"},
		{&fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "w", "YDOTOOL_SOCKET": "/s"}, bins: bins("ydotool"), files: map[string]bool{"/s": true}},
			"ydotool key 42:1 28:1 28:0 42:0"},
	} {
		k := &LinuxKeyboard{X: c.f, Wayland: &fakeInjector{why: "no protocol"}, X11: &fakeInjector{why: "no XTEST"}}
		if err := k.Press(ctx, KeyShiftEnter); err != nil || len(c.f.ran) != 1 || c.f.ran[0].line() != c.want {
			t.Errorf("%v: %q, want %q", err, c.f.ran, c.want)
		}
	}
	in, _ := keyInputs(KeyShiftEnter)
	if len(in) != 4 || in[0].VK != vkShift || in[1].VK != vkReturn || in[3].VK != vkShift || in[3].Flags != keyeventfKeyUp {
		t.Errorf("windows %+v", in)
	}
	p := &fakePoster{}
	m := &MacKeyboard{Trusted: func() bool { return true }, Post: p}
	if err := m.Press(ctx, KeyShiftEnter); err != nil || !slices.Equal(p.keys, []string{"36 true"}) {
		t.Errorf("mac %v %v", err, p.keys)
	}
}
