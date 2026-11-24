package dictate

import (
	"context"
	"strings"
	"unicode"
)

// Line breaks in typed text (DESIGN.md §14 "Line breaks"). The key between lines is chosen here
// from the app that has the focus, never by the model: the model writes the text from pages it
// has just read, and a wrong guess presses Enter in a chat (a half message sent) or a terminal
// (a command run).
//
//   - Terminals: no key. Lines are joined with spaces (Shift+Enter is Enter there).
//   - Text editors and word processors: Enter, so paragraphs are real paragraphs.
//   - Anything else (chat apps, browsers, apps not listed): Shift+Enter, a new line that
//     doesn't send in Slack, Discord, Messages, web chats, and a line break in most other apps.
//   - Can't tell which app has the focus: no key, as for a terminal.
//
// Names are matched on the app id or class as the window system reports it, lower-cased with
// everything but letters and digits dropped ("org.gnome.TextEditor" → "orggnometexteditor"),
// and on its last dotted part ("texteditor").

// terminalApps are terminals, by app id, class or process name on Linux, macOS and Windows.
var terminalApps = []string{"foot", "footclient", "kitty", "alacritty", "ghostty", "wezterm", "weztermgui", "konsole",
	"gnometerminal", "gnometerminalserver", "xterm", "uxterm", "urxvt", "rxvt", "tilix", "terminator", "ptyxis",
	"console", "kgx", "xfce4terminal", "lxterminal", "qterminal", "mateterminal", "terminology", "sakura",
	"termite", "st", "stterm", "blackbox", "rio", "contour", "cooltretroterm", "yakuake", "guake", "tilda",
	"terminal", "iterm", "iterm2", "warp", "hyper", "tabby", "windowsterminal", "cmd", "conhost", "powershell", "pwsh",
	"mintty"}

// editorApps are text editors and word processors, where Enter starts a new paragraph.
var editorApps = []string{"gedit", "texteditor", "gnometexteditor", "kate", "kwrite", "mousepad", "pluma", "xed",
	"featherpad", "leafpad", "l3afpad", "notepadqq", "gnomesimple", "abiword", "libreoffice", "libreofficewriter",
	"soffice", "writer", "onlyoffice", "desktopeditors", "wpsoffice", "wps", "textedit", "pages", "notepad",
	"winword", "wordpad", "focuswriter", "ghostwriter", "apostrophe", "marktext", "obsidian", "joplin", "typora"}

var (
	terminalSet = toSet(terminalApps)
	editorSet   = toSet(editorApps)
)

func toSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// lineBreakKey is the key typed between lines into an app with these names (its app id, class
// or process name); "" means no key: join the lines with spaces.
func lineBreakKey(apps []string) Key {
	known := false
	editor := false
	for _, a := range apps {
		for _, n := range appKeys(a) {
			known = true
			if terminalSet[n] || strings.Contains(n, "terminal") {
				return "" // a terminal wins over anything else the window is called
			}
			if editorSet[n] {
				editor = true
			}
		}
	}
	switch {
	case !known:
		return ""
	case editor:
		return KeyEnter
	}
	return KeyShiftEnter
}

// LineBreakKey is lineBreakKey for computer tasks' typing (computer.Tools): the same rule, so
// a task's multi-line text gets paragraphs in an editor and never sends a chat message or runs
// a command.
func LineBreakKey(apps []string) Key { return lineBreakKey(apps) }

// appKeys are an app name's forms to match: the whole, and its last dotted part, each
// lower-cased to letters and digits ("com.mitchellh.ghostty" → "commitchellhghostty", "ghostty";
// "WINWORD.EXE" → "winwordexe", "winword").
func appKeys(app string) []string {
	app = strings.ToLower(strings.TrimSpace(app))
	app = strings.TrimSuffix(app, ".exe")
	app = strings.TrimSuffix(app, ".app")
	var out []string
	if k := alnum(app); k != "" {
		out = append(out, k)
	}
	if i := strings.LastIndex(app, "."); i >= 0 {
		if k := alnum(app[i+1:]); k != "" && k != out[0] {
			out = append(out, k)
		}
	}
	return out
}

func alnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (k Key) shifted() bool { return k == KeyShiftTab || k == KeyShiftEnter }

// typeLines types lines with key between them, or, with no key, as one line joined by spaces.
func typeLines(ctx context.Context, kb Keyboard, lines []string, key Key) error {
	if key == "" {
		var words []string
		for _, l := range lines {
			if l != "" {
				words = append(words, l)
			}
		}
		return kb.Type(ctx, strings.Join(words, " "))
	}
	for i, l := range lines {
		if i > 0 {
			if err := kb.Press(ctx, key); err != nil {
				return err
			}
		}
		if l != "" {
			if err := kb.Type(ctx, l); err != nil {
				return err
			}
		}
	}
	return nil
}

// focusedApps are the names of the window with the keyboard focus, nil when the window backend
// can't tell (then lines are joined with spaces).
func (t *Tools) focusedApps(ctx context.Context) []string {
	if t.Win == nil {
		return nil
	}
	if ok, _ := t.Win.Available(ctx); !ok {
		return nil
	}
	active, err := t.Win.Active(ctx)
	if err != nil || active == "" {
		return nil
	}
	ws, err := t.Win.List(ctx)
	if err != nil {
		return nil
	}
	for _, w := range ws {
		if t.Win.Same(w.ID, active) {
			return w.Apps
		}
	}
	return nil
}

// lineBreakName is the result's line_breaks: how the lines were joined.
func lineBreakName(k Key) string {
	if k == "" {
		return "spaces"
	}
	return string(k)
}
