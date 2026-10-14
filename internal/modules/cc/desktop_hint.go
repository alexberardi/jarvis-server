package cc

import (
	"strings"
	"unicode"
)

// The desktop app hint (docs/agent/DESIGN.md §14 "A name before a request"). On a follow-up
// after "Can you open the oven?", the user said "NeoVim, can you open it?" and the model took
// "NeoVim" for someone being spoken to (the not-for-me rule "names a different addressee")
// and stayed silent. When a desktop node's own app names (stt_vocabulary) include the name the
// utterance starts with, or the one it asks to open, the turn gets one line saying that name is
// an app on this computer. Only the user's own word is repeated: the catalogue never reaches
// the LLM.

// desktopOpenTool marks a conversation with a desktop node's tools.
const desktopOpenTool = "desktop_open"

// openVerbs start an "open X" request.
var openVerbs = map[string]bool{"open": true, "launch": true, "start": true, "run": true}

// minHintName: shorter names ("X", "HEY") are ordinary words too often.
const minHintName = 4

// desktopAppHint is the hint for utterance, or "".
func (m *Module) desktopAppHint(conv *conversation, utterance string) string {
	if !conv.hasTool(desktopOpenTool) {
		return ""
	}
	vocab := m.vocab.get(conv.nodeID)
	if vocab == "" {
		return ""
	}
	name := spokenAppName(utterance, strings.Split(vocab, ","))
	if name == "" {
		return ""
	}
	return "[desktop: \"" + name + "\" is an app on this computer, not a person; \"" + name +
		", …\" or \"open " + name + "\" asks you to act on that app with the desktop tools.]"
}

// spokenAppName is the app name the utterance starts with ("NeoVim, can you open it?") or
// asks to open ("can you open Steam"), as the user said it, when it is one of names.
func spokenAppName(utterance string, names []string) string {
	known := map[string]bool{}
	for _, n := range names {
		if c := compactName(n); len([]rune(c)) >= minHintName {
			known[c] = true
		}
	}
	ws := strings.Fields(utterance)
	match := func(i int) string { // one or two words from i, as said
		for n := 2; n >= 1; n-- {
			if i+n > len(ws) {
				continue
			}
			said := strings.Join(ws[i:i+n], " ")
			if known[compactName(said)] {
				return strings.TrimFunc(said, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			}
		}
		return ""
	}
	// "NeoVim, can you …": a name, then a comma, first.
	for n := 1; n <= 2 && n <= len(ws); n++ {
		if strings.HasSuffix(ws[n-1], ",") {
			if s := match(0); s != "" {
				return s
			}
			break
		}
	}
	// "… open (up) (the) X …"
	for i, w := range ws {
		if !openVerbs[compactName(w)] {
			continue
		}
		j := i + 1
		for j < len(ws) && (compactName(ws[j]) == "up" || compactName(ws[j]) == "the" || compactName(ws[j]) == "my") {
			j++
		}
		if s := match(j); s != "" {
			return s
		}
	}
	return ""
}

func compactName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hasTool reports whether the conversation offers the named tool.
func (c *conversation) hasTool(name string) bool {
	for _, t := range c.tools {
		if toolName(t) == name {
			return true
		}
	}
	return false
}

// The same-device hint (web app plan §3.6, DESIGN.md §14 "The Jarvis page in front"): someone
// chatting in the Jarvis web page on the computer whose agent they are talking to. The window
// in front is that page, so the turn says to name the app for typing and keys; the agent
// itself never types into the page.

// desktopTypeTool is the desktop agent's typing tool.
const desktopTypeTool = "desktop_type_text"

// SameDeviceHint is the line added to such a turn.
const SameDeviceHint = "[web: the user is writing this in the Jarvis web page on this computer, which is the window in front. " +
	"For desktop_type_text and desktop_press_key, an app the user names goes in app (\"type hi into slack\" → app: \"slack\"); " +
	"with none named, leave app out and the text goes to the window they used before this page.]"

// sameDeviceHint is SameDeviceHint for a same-device web chat with the typing tool, or "".
func sameDeviceHint(conv *conversation, in turnInput) string {
	if !in.SameDevice || conv.chatUserID == 0 || !conv.hasTool(desktopTypeTool) {
		return ""
	}
	return SameDeviceHint
}
