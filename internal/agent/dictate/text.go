// Package dictate is dictation on the desktop (docs/agent/DESIGN.md §14): type text into the
// focused window or into an app the user named ("tell claude to …"), press a few keys
// ("press enter" answers most OK/Cancel dialogs), and the optional confirmation step that
// asks before anything is sent.
//
// Mechanism per OS lives behind two small interfaces: Keyboard (wtype / ydotool / xdotool,
// CGEvent, SendInput) and Windows (hyprctl, swaymsg, and the host backend's window control
// for X11, macOS and Windows). Policy (which window, whether to ask first, what may be typed)
// is in tools.go. Nothing is typed into a window that couldn't be confirmed to have focus.
package dictate

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// MaxText is the most a single call types, in characters. Dictation is a sentence or a
// paragraph; a longer text is refused, never cut (half a command sent is worse than none).
const MaxText = 2000

var errBadArgs = errors.New("bad arguments")

// Sanitize makes dictated text safe to type: tabs and line breaks become spaces (a line break
// would press Enter: sending is only ever the separate submit step), every other control or
// format character is dropped (escape sequences, bidi overrides, zero-width tricks), and
// surrounding space is trimmed. Empty or over MaxText characters is an error.
func Sanitize(s string) (string, error) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r' || r == ' ' || r == ' ':
			b.WriteRune(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar:
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("%w: text is empty", errBadArgs)
	}
	if n := len([]rune(out)); n > MaxText {
		return "", fmt.Errorf("%w: text is %d characters; at most %d can be typed at once", errBadArgs, n, MaxText)
	}
	return out, nil
}

// Key is a key desktop_press_key may press: a closed set, enough to answer a dialog or move
// between its buttons, never shortcuts.
type Key string

const (
	KeyEnter    Key = "enter"
	KeyEscape   Key = "escape"
	KeyTab      Key = "tab"
	KeyShiftTab Key = "shift+tab"
	KeySpace    Key = "space"
	KeyUp       Key = "up"
	KeyDown     Key = "down"
	KeyLeft     Key = "left"
	KeyRight    Key = "right"
)

// Keys is every Key, in the order the tool lists them.
var Keys = []Key{KeyEnter, KeyEscape, KeyTab, KeyShiftTab, KeySpace, KeyUp, KeyDown, KeyLeft, KeyRight}

var keyAliases = map[string]Key{
	"return": KeyEnter, "ok": KeyEnter, "esc": KeyEscape, "shift tab": KeyShiftTab, "backtab": KeyShiftTab,
	"spacebar": KeySpace, "space bar": KeySpace,
	"up arrow": KeyUp, "arrow up": KeyUp, "down arrow": KeyDown, "arrow down": KeyDown,
	"left arrow": KeyLeft, "arrow left": KeyLeft, "right arrow": KeyRight, "arrow right": KeyRight,
}

// ParseKey reads a key name the way a model writes it ("Enter", "esc", "Shift+Tab").
func ParseKey(s string) (Key, error) {
	k := strings.ToLower(strings.TrimSpace(s))
	k = strings.TrimSuffix(strings.TrimPrefix(k, "the "), " key")
	k = strings.Join(strings.Fields(k), " ")
	k = strings.ReplaceAll(strings.ReplaceAll(k, " + ", "+"), "-", "+")
	for _, key := range Keys {
		if k == string(key) {
			return key, nil
		}
	}
	if key, ok := keyAliases[k]; ok {
		return key, nil
	}
	names := make([]string, len(Keys))
	for i, key := range Keys {
		names[i] = string(key)
	}
	return "", fmt.Errorf("%w: key must be one of %s", errBadArgs, strings.Join(names, ", "))
}

// Words that answer a confirmation. A "no" word anywhere wins ("no, don't send it"); an answer
// with neither, or nothing caught, cancels.
var (
	yesWords = []string{"yes", "yeah", "yep", "yup", "ok", "okay", "sure", "confirm", "confirmed", "send", "send it",
		"do it", "go", "go ahead", "submit", "correct", "right", "affirmative", "please do", "click it", "type it", "press it", "proceed"}
	noWords = []string{"no", "nope", "nah", "cancel", "stop", "don't", "dont", "do not", "abort", "never mind", "nevermind",
		"wait", "wrong", "hold on", "not"}
)

// Answer is how a confirmation was answered.
type Answer int

const (
	AnswerUnclear Answer = iota // neither: treated as cancel
	AnswerYes
	AnswerNo
)

// ClassifyAnswer reads the user's own words ("OK", "yes send it", "cancel", "no").
func ClassifyAnswer(s string) Answer {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.ReplaceAll(s, "’", "'")) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	norm := " " + strings.Join(strings.Fields(b.String()), " ") + " "
	has := func(words []string) bool {
		for _, w := range words {
			if strings.Contains(norm, " "+w+" ") {
				return true
			}
		}
		return false
	}
	switch {
	case has(noWords):
		return AnswerNo
	case has(yesWords):
		return AnswerYes
	}
	return AnswerUnclear
}
