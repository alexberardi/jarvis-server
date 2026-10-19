package computer

import (
	"testing"
	"time"
)

// A macOS key down decodes to text, a named key or a shortcut; while secure input is on the
// characters are never read.
func TestMacKeyInput(t *testing.T) {
	at := time.Unix(0, 0)
	cases := []struct {
		name   string
		code   uint16
		flags  uint64
		secure bool
		text   string
		want   DeskInput
	}{
		{"letter", 0, macFlagShift, false, "M", DeskInput{Text: "M"}},
		{"return", vkReturn, 0, false, "\r", DeskInput{Key: KeyEnter}},
		{"keypad enter", vkKeypadEnter, 0, false, "\x03", DeskInput{Key: KeyEnter}},
		{"backspace", vkDelete, 0, false, "\x7f", DeskInput{Key: KeyBackspace}},
		{"arrow", 123, 0, false, "", DeskInput{Key: keyOther}},
		{"cmd+n", 45, macFlagCmd, false, "n", DeskInput{Key: "cmd+n"}},
		{"cmd+shift+N", 45, macFlagCmd | macFlagShift, false, "N", DeskInput{Key: "shift+cmd+n"}},
		{"ctrl+a", 0, macFlagCtrl, false, "\x01", DeskInput{Key: "ctrl+a"}},
		{"cmd+return", vkReturn, macFlagCmd, false, "", DeskInput{Key: "cmd+enter"}},
		{"alt types", 2, macFlagAlt, false, "∂", DeskInput{Text: "∂"}},
		{"secure letter", 0, 0, true, "p", DeskInput{Secure: true}},
		{"secure return", vkReturn, 0, true, "", DeskInput{Key: KeyEnter, Secure: true}},
		{"secure shortcut", 9, macFlagCmd, true, "v", DeskInput{Secure: true}},
	}
	for _, c := range cases {
		read := false
		got := macKeyInput(c.code, c.flags, c.secure, at, func() string { read = true; return c.text })
		c.want.Kind, c.want.At = InKey, at
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if c.secure && read {
			t.Errorf("%s: read the characters while secure input was on", c.name)
		}
	}
}
