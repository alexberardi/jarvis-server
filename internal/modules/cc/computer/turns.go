package computer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// SettingLoop is how a task talks to its model. "rounds" gives it one fresh prompt a round: the
// task, a line for each of its last actions, and the screen. "turns" is a conversation kept all
// task long: the task and the errand's plan first, then each of the model's answers followed by
// what that answer did (what changed on the screen, not just that it was sent), one action an
// answer. A small model given only "ok" for an action repeated it or called an order done.
const SettingLoop = "computer.loop"

// SettingLoop's values.
const (
	LoopRounds = "rounds"
	LoopTurns  = "turns"
)

// KindSaid is the history entry of an answer that ran nothing (a done that was sent back, a
// batch that was refused): Thought holds the answer and Outcome what the model was told.
const KindSaid = "said"

// What a turn's answer and typed text keep in the history, in runes.
const (
	saidMax    = 400
	actTextMax = 300
	detailMax  = 120
)

// turnsSystemPrompt is systemPrompt for a conversation: one action an answer, and the outcome
// of each told before the next.
var turnsSystemPrompt = turnsSystem(systemPrompt)

func turnsSystem(s string) string {
	for _, sw := range [][2]string{
		{"Each turn you see the task, what you did so far, and the current page or app:",
			"This is a conversation: first the task (and the plan it is a step of), then, after each of your answers, what that answer did and the current page or app:"},
		{"Send at most 12 actions per turn; you see the result of every action before the next turn.",
			"Send exactly one action per answer. After it you are told what happened: what changed on the screen, or that nothing visibly did. An action that changed nothing either missed, or had nothing to change (a New button on a form that is already empty): look at the screen, and if it is already as the step needs, go on to the next one. Never send the same action over and over."},
		{`1. Next actions: {"thought": "<one short sentence>", "actions": [<action>, ...]}`,
			`1. The next action: {"thought": "<one short sentence: what the last outcome means and what comes next>", "actions": [<one action>]}`},
	} {
		if !strings.Contains(s, sw[0]) {
			panic("computer: turnsSystem: the system prompt no longer has: " + sw[0])
		}
		s = strings.Replace(s, sw[0], sw[1], 1)
	}
	return s
}

// turnsMessages is the task as a conversation: the header, each past round as the model's
// answer and its outcome, and last the outcome of the newest answer with the screen as it is.
func turnsMessages(in promptInput, shots []*Image) []llm.Message {
	var head strings.Builder
	stable := renderHeader(&head, in)
	var screen strings.Builder
	renderScreen(&screen, in)

	rounds := groupRounds(in.History)
	msgs := []llm.Message{{Role: "system", Content: llm.TextContent(turnsSystemPrompt)}}
	last := func(text string) llm.Message {
		parts := []llm.Part{{Type: "text", Text: text}}
		for _, im := range shots {
			url := "data:" + im.MIME + ";base64," + base64.StdEncoding.EncodeToString(im.Data)
			parts = append(parts, llm.Part{Type: "image_url", ImageURL: &llm.ImageURL{URL: url}})
		}
		return llm.Message{Role: "user", Content: &llm.Content{Parts: parts}}
	}
	if len(rounds) == 0 {
		text := head.String()
		if in.Feedback != "" {
			text += "Last turn: " + oneLine(in.Feedback) + "\n"
		}
		m := last(text + screen.String())
		m.Content.Parts[0].CachePrefix = stable
		return append(msgs, m)
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: &llm.Content{Parts: []llm.Part{{Type: "text", Text: head.String(), CachePrefix: stable}}}})
	for i := 0; i < len(rounds); i++ {
		es := rounds[i]
		answer := answerOf(es)
		// The same answer several times running is shown once, with how often: a small model
		// that sees itself repeat an answer five times gives it a sixth.
		same := 1
		for i+1 < len(rounds) && answerOf(rounds[i+1]) == answer {
			i++
			same++
		}
		es = rounds[i]
		msgs = append(msgs, llm.Message{Role: "assistant", Content: llm.TextContent(answer)})
		out := outcomeOf(es)
		if same > 1 {
			out += fmt.Sprintf("\nYou have now given this same answer %d times in a row, and it has not got you further. Answer differently.", same)
		}
		if i < len(rounds)-1 {
			msgs = append(msgs, llm.Message{Role: "user", Content: llm.TextContent(out)})
			continue
		}
		// What the loop tells the model about its newest answer, unless that answer's entry
		// already holds it (an answer that ran nothing).
		if in.Feedback != "" && es[0].Kind != KindSaid {
			out += "\n" + oneLine(in.Feedback)
		}
		if in.Desktop && justSaved(es[len(es)-1]) && !saveDialog(in.Obs.Title) {
			out += "\n" + saveWentThrough
		}
		msgs = append(msgs, last(out+"\n"+screen.String()))
	}
	return msgs
}

// groupRounds is the history as its rounds, in order.
func groupRounds(h []Entry) [][]Entry {
	var out [][]Entry
	for _, e := range h {
		if n := len(out); n > 0 && out[n-1][0].Round == e.Round && (out[n-1][0].Kind == KindSaid) == (e.Kind == KindSaid) {
			out[n-1] = append(out[n-1], e)
			continue
		}
		out = append(out, []Entry{e})
	}
	return out
}

// answerOf is a round's answer as the model gave it, as far as the history keeps it. The
// actions are in the shape the model must answer in: the 9B copied a history that showed its
// set_value without "text" and sent the next ones without any.
func answerOf(es []Entry) string {
	if es[0].Kind == KindSaid {
		return es[0].Thought
	}
	ans := struct {
		Thought string            `json:"thought,omitempty"`
		Actions []json.RawMessage `json:"actions"`
	}{Thought: es[0].Thought}
	for _, e := range es {
		act := json.RawMessage(e.Act)
		if !json.Valid(act) {
			act, _ = json.Marshal(map[string]string{"kind": e.Kind, "ref": e.Ref, "intent": e.Intent})
		}
		ans.Actions = append(ans.Actions, act)
	}
	b, _ := json.Marshal(ans)
	return string(b)
}

// actOf is an action as the model wrote it, for the conversation: its own coordinates, not the
// screenshot's pixels, and a long text cut.
func actOf(a computeruse.Action) string {
	a.Text = truncRunes(a.Text, actTextMax)
	a.CaptureID, a.Approval = "", ""
	b, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return string(b)
}

// outcomeOf is what a round's answer did, for the model: each action that didn't simply run,
// then what changed on the screen.
func outcomeOf(es []Entry) string {
	if es[0].Kind == KindSaid {
		return "Not run: " + oneLine(es[0].Outcome)
	}
	var parts []string
	sent := true
	for _, e := range es {
		if e.Result != "ok" {
			sent = false
			parts = append(parts, fmt.Sprintf("%q: %s.", e.Intent, e.Result))
		}
	}
	if sent {
		parts = append(parts, "Sent.")
	}
	if o := es[len(es)-1].Outcome; o != "" {
		parts = append(parts, "On the screen: "+o)
	}
	return "Outcome: " + strings.Join(parts, " ")
}

// said is a model's answer for the history, cut to saidMax.
func said(reply Reply) string {
	b, _ := json.Marshal(reply)
	return truncRunes(string(b), saidMax)
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// describeChange is what differs between the screen an answer was decided on and the screen
// after it ran, in words. Everything named comes from the page or app: it is content.
func describeChange(prev, cur Observation) string {
	var parts []string
	switch {
	case cur.URL != prev.URL && cur.URL != "":
		parts = append(parts, fmt.Sprintf("the page is now %s (%q)", defang(cur.URL), defang(cur.Title)))
	case cur.Title != prev.Title:
		parts = append(parts, fmt.Sprintf("the window is now titled %q (it was %q)", defang(cur.Title), defang(prev.Title)))
	}
	if cur.App != prev.App && cur.App != "" && prev.App != "" {
		parts = append(parts, fmt.Sprintf("the app in front is now %q", defang(cur.App)))
	}
	key := func(e Element) string { return e.Role + "\x00" + e.Name }
	before, values := map[string]int{}, map[string]string{}
	for _, e := range prev.Elements {
		before[key(e)]++
		values[key(e)] = e.Value + "\x00" + strings.Join(e.State, ",")
	}
	after := map[string]int{}
	var fresh, changed []string
	for _, e := range cur.Elements {
		k := key(e)
		after[k]++
		switch {
		case before[k] == 0:
			if e.Name != "" && len(fresh) < 6 {
				fresh = append(fresh, fmt.Sprintf("%s %q", e.Role, defang(e.Name)))
			}
		case before[k] == 1 && after[k] == 1 && values[k] != e.Value+"\x00"+strings.Join(e.State, ",") && len(changed) < 4:
			was, _, _ := strings.Cut(values[k], "\x00")
			if e.Value != was && !e.Secure {
				changed = append(changed, fmt.Sprintf("%s %q now holds %q", e.Role, defang(e.Name), defang(truncRunes(e.Value, detailMax))))
			} else if e.Value == was {
				changed = append(changed, fmt.Sprintf("%s %q is now [%s]", e.Role, defang(e.Name), strings.Join(e.State, ", ")))
			}
		}
	}
	gone := 0
	for k, n := range before {
		if after[k] < n {
			gone += n - after[k]
		}
	}
	parts = append(parts, changed...)
	if len(fresh) > 0 {
		parts = append(parts, "new: "+strings.Join(fresh, ", "))
	}
	if gone > 0 && len(fresh) == 0 && len(changed) == 0 && len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d element(s) are gone", gone))
	}
	if len(parts) == 0 && cur.Text != prev.Text {
		parts = append(parts, "the text on it changed")
	}
	if len(parts) == 0 {
		if s := imageChange(prev.Image, cur.Image); s != "" {
			return s
		}
		if len(cur.Elements) > 0 || cur.Text != "" {
			return "nothing visibly changed."
		}
		return ""
	}
	return strings.Join(parts, "; ") + "."
}

// A screenshot pixel counts as changed past pixelDelta in a channel; fewer than cursorPixels of
// them is what a blinking text cursor makes.
const (
	pixelDelta   = 40
	cursorPixels = 120
)

// imageChange compares two screenshots of the same size: "" when they can't be compared, else
// that nothing changed, or where on the screen the change is.
func imageChange(a, b *Image) string {
	if a == nil || b == nil {
		return ""
	}
	if bytes.Equal(a.Data, b.Data) {
		return "nothing visibly changed: the screenshot is the same as before."
	}
	ia, _, errA := image.Decode(bytes.NewReader(a.Data))
	ib, _, errB := image.Decode(bytes.NewReader(b.Data))
	if errA != nil || errB != nil || ia.Bounds().Size() != ib.Bounds().Size() {
		return ""
	}
	r := ia.Bounds()
	n, sx, sy := 0, 0, 0
	// Jarvis's own indicator (the island: a pill at the top centre of the screen that names the
	// action under way) changes with every action and is no news about the app.
	island := image.Rect(r.Min.X+r.Dx()*3/10, r.Min.Y, r.Min.X+r.Dx()*7/10, r.Min.Y+r.Dy()*7/100)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if image.Pt(x, y).In(island) {
				continue
			}
			r1, g1, b1, _ := ia.At(x, y).RGBA()
			r2, g2, b2, _ := ib.At(x-r.Min.X+ib.Bounds().Min.X, y-r.Min.Y+ib.Bounds().Min.Y).RGBA()
			if absDiff(r1, r2) > pixelDelta<<8 || absDiff(g1, g2) > pixelDelta<<8 || absDiff(b1, b2) > pixelDelta<<8 {
				n++
				sx += x - r.Min.X
				sy += y - r.Min.Y
			}
		}
	}
	switch {
	case n == 0:
		return "nothing visibly changed: the screenshot looks the same as before."
	case n < cursorPixels:
		return fmt.Sprintf("almost nothing changed: a few pixels near the %s (a text cursor, perhaps).", whereOn(sx/n, sy/n, r.Dx(), r.Dy()))
	}
	return fmt.Sprintf("the screenshot changed, mostly near the %s.", whereOn(sx/n, sy/n, r.Dx(), r.Dy()))
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// whereOn names a point's place on a w x h screen: "top left", "middle", "bottom right".
func whereOn(x, y, w, h int) string {
	row := []string{"top", "middle", "bottom"}[min(2, y*3/max(h, 1))]
	col := []string{"left", "centre", "right"}[min(2, x*3/max(w, 1))]
	if row == "middle" && col == "centre" {
		return "middle"
	}
	return row + " " + col
}
