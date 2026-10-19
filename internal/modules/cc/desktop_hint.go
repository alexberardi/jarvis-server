package cc

import (
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
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
	"For desktop_type_text and desktop_press_key, an app the user names always goes in app, exactly as they said it, even " +
	"one you don't recognise (\"type hi into slack\" → app: \"slack\"; \"type into quillpad that …\" → app: \"quillpad\"); " +
	"with none named, leave app out and the text goes to the window they used before this page.]"

// sameDeviceHint is SameDeviceHint for a same-device web chat with the typing tool, or "".
func sameDeviceHint(conv *conversation, in turnInput) string {
	if !in.SameDevice || conv.chatUserID == 0 || !conv.hasTool(desktopTypeTool) {
		return ""
	}
	return SameDeviceHint
}

// The app a typing request names (DESIGN.md §14 "Typing keeps the app the user named"). The
// 9B sometimes calls desktop_type_text without app for "type into omawrite that …", and the
// text lands in the window in front. When the call has no app and the utterance says "into X"
// or "in X" after a typing verb, X (one or two words, not part of the text) goes in app.

// typeVerbs start a typing request.
var typeVerbs = map[string]bool{"type": true, "write": true, "enter": true, "put": true, "paste": true, "dictate": true}

// appEnds end the app name after "into X": "type into omawrite that …".
var appEnds = map[string]bool{"that": true, "saying": true, "and": true, "please": true, "then": true, "so": true}

// notApps follow "in" without naming an app: "in all caps", "in spanish", "in here".
var notApps = map[string]bool{"caps": true, "capitals": true, "uppercase": true, "lowercase": true, "bold": true,
	"italics": true, "quotes": true, "english": true, "spanish": true, "french": true, "german": true, "here": true,
	"there": true, "it": true, "this": true, "window": true, "field": true, "box": true}

// typedAppNamed is the app the typing request utterance names for text, as the user said it,
// or "".
func typedAppNamed(utterance, text string) string {
	ws := strings.Fields(utterance)
	verb := -1
	for i, w := range ws {
		c := compactName(w)
		if openVerbs[c] {
			return "" // "open X and type Y": typing follows the launch (LaunchImplicit)
		}
		if typeVerbs[c] {
			verb = i
			break
		}
	}
	if verb < 0 {
		return ""
	}
	inText := map[string]bool{}
	for _, w := range strings.Fields(text) {
		inText[compactName(w)] = true
	}
	for i := verb + 1; i < len(ws); i++ {
		if c := compactName(ws[i]); c != "in" && c != "into" {
			continue
		}
		j := i + 1
		if j < len(ws) && (compactName(ws[j]) == "the" || compactName(ws[j]) == "my") {
			j++
		}
		k, ended := j, false
		for ; k < len(ws) && k-j < 3; k++ {
			if appEnds[compactName(ws[k])] {
				ended = true
				break
			}
			if strings.ContainsAny(ws[k][len(ws[k])-1:], ",.:;!?") {
				k++
				ended = true
				break
			}
		}
		if k == len(ws) {
			ended = true
		}
		if !ended || k == j || k-j > 2 {
			continue
		}
		ok := true
		for _, w := range ws[j:k] {
			if c := compactName(w); c == "" || notApps[c] || inText[c] {
				ok = false
			}
		}
		if ok {
			return strings.TrimRight(strings.Join(ws[i+1:k], " "), ",.:;!?")
		}
	}
	return ""
}

// fillTypedApp is call's arguments with the app the utterance names, when call is a
// desktop_type_text that names none.
func fillTypedApp(utterance string, call parse.ToolCall) (string, bool) {
	if call.Function.Name != desktopTypeTool {
		return "", false
	}
	v, err := pyjson.Loads(call.Function.Arguments)
	o, isObj := v.(*pyjson.Object)
	if err != nil || !isObj {
		return "", false
	}
	if app, ok := o.Get("app"); ok && app != nil && app != "" {
		return "", false
	}
	text, _ := o.Get("text")
	ts, _ := text.(string)
	app := typedAppNamed(utterance, ts)
	if app == "" {
		return "", false
	}
	o.Set("app", app)
	return pyjson.Dumps(o, true), true
}
