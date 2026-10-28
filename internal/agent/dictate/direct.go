package dictate

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Funnel stage 1 for dictation (VOICE-FLOWS.md Q27): "type X", "type X in the message box",
// "type X in Slack and press enter", "press enter", "send it" are carried out here through the
// same tool calls the model would make, so every check stays (focus confirmed, Sanitize, the
// Jarvis page guard, the confirmation). Only "type": "write X" may mean making text up (Q15),
// which is the model's.

var (
	reType = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+|can you\s+|could you\s+)?type\s+(?:out\s+)?(.+?)[\s.]*$`)
	// "type in the message box: hello", the place first.
	rePlaceFirst = regexp.MustCompile(`(?i)^(?:in|into)\s+(?:the\s+|my\s+)?([^.:,]+?)\s*[.:,]\s+(.+)$`)
	rePlaceLast  = regexp.MustCompile(`(?i)^(.+?)\s+(?:in|into)\s+(?:the\s+|my\s+)?([\w' -]+?)$`)
	reSubmit     = regexp.MustCompile(`(?i),?\s+and\s+(?:then\s+)?(?:(?:press|hit)\s+(?:enter|return)|send(?:\s+it)?|submit(?:\s+it)?)$`)
	reQuoted     = regexp.MustCompile(`^["“'‘](.+?)["”'’][.!?,]?$`) // quoted text keeps its own punctuation
	// reNumberPlace is a place named by a number on screen: "29", "number 4", "box 7".
	reNumberPlace = regexp.MustCompile(`(?i)^(?:(?:number|box|field)\s+)?\d+$`)
	reEnter       = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?(?:(?:press|hit)\s+(?:enter|return)|send\s+it|submit(?:\s+it)?)[\s.!?]*$`)
)

// fieldWords name a place in the window in front, not an app: typing goes where the caret is.
var fieldWords = []string{"message", "message box", "chat", "chat box", "box", "field", "text box", "textbox",
	"input", "prompt", "search", "search box", "search bar", "comment", "reply", "composer", "page", "this",
	"here", "it", "window", "the window", "editor"}

// typeRequest is a parsed "type X".
type typeRequest struct {
	text, place string
	submit      bool
}

func parseType(said string) (typeRequest, bool) {
	m := reType.FindStringSubmatch(said)
	if m == nil {
		return typeRequest{}, false
	}
	body := strings.TrimSpace(m[1])
	var r typeRequest
	if loc := reSubmit.FindStringIndex(body); loc != nil {
		r.submit, body = true, strings.TrimSpace(body[:loc[0]])
	}
	if pm := rePlaceLast.FindStringSubmatch(body); pm != nil && reNumberPlace.MatchString(strings.TrimSpace(pm[2])) {
		return typeRequest{}, false // "type ABC into 29": a number on the page, the browser's (browser/hints.go)
	}
	if pm := rePlaceFirst.FindStringSubmatch(body); pm != nil {
		r.place, body = strings.TrimSpace(pm[1]), strings.TrimSpace(pm[2])
	} else if pm := rePlaceLast.FindStringSubmatch(body); pm != nil && (quoted(pm[1]) || isField(pm[2])) {
		// "type I'm in the car" keeps its "in": a place is taken only after quoted text or
		// when it names a field; an app name there is the model's to resolve.
		r.place, body = strings.TrimSpace(pm[2]), strings.TrimSpace(pm[1])
	}
	if qm := reQuoted.FindStringSubmatch(body); qm != nil {
		body = qm[1]
	}
	r.text = strings.TrimSpace(body)
	if r.text == "" {
		return typeRequest{}, false
	}
	return r, true
}

func quoted(s string) bool { return reQuoted.MatchString(strings.TrimSpace(s)) }

func isField(place string) bool {
	p := strings.ToLower(strings.TrimSpace(place))
	for _, suffix := range []string{" box", " field", " bar"} {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	// "the cloud message" (Claude's message box, misheard): ends in a field word.
	ws := strings.Fields(p)
	return slices.Contains(fieldWords, p) || (len(ws) > 0 && slices.Contains(fieldWords, ws[len(ws)-1]))
}

// Direct carries out "type X …" and "press enter" itself (funnel stage 1); ok false passes it on.
func (t *Tools) Direct(ctx context.Context, said string) (map[string]any, bool) {
	req := node.Request{VoiceCommand: said}
	if reEnter.MatchString(said) {
		return t.direct(ctx, ToolPress, map[string]any{"key": string(KeyEnter)}, req)
	}
	r, ok := parseType(said)
	if !ok {
		return nil, false
	}
	args := map[string]any{"text": r.text, "submit": r.submit}
	if r.place != "" && !isField(r.place) {
		args["app"] = r.place
	}
	return t.direct(ctx, ToolType, args, req)
}

// direct runs one tool call for stage 1. An app that isn't open, or that names several windows,
// goes on to the model (it can open the app, or ask); a confirmation question is put on the
// island as a choice, so the spoken yes or no comes back to AnswerChoice.
func (t *Tools) direct(ctx context.Context, tool string, args map[string]any, req node.Request) (map[string]any, bool) {
	if !node.Enabled(ctx, tool) {
		return nil, false // turned off in this computer's settings: the model says so
	}
	out, ok := t.Call(ctx, tool, args, req)
	if !ok {
		return nil, false
	}
	switch out["error_code"] {
	case CodeNotOpen, CodeNotReady, "window_not_found", "ambiguous_window":
		return nil, false
	}
	if out["status"] == "awaiting_confirmation" {
		q, _ := out["question"].(string)
		out[node.IslandKey], out[node.IslandOnlyKey], out[node.ChoiceKey] = WaitingPrefix+q, true, true
		t.mu.Lock()
		t.directConfirm, _ = out["confirmation_id"].(string)
		t.mu.Unlock()
		return out, true
	}
	out[node.IslandOnlyKey] = true
	return out, true
}

// AnswerChoice takes the spoken answer to a confirmation stage 1 asked ("yes", "send it", "no").
func (t *Tools) AnswerChoice(ctx context.Context, said string) (map[string]any, bool) {
	t.mu.Lock()
	id := t.directConfirm
	t.directConfirm = ""
	t.mu.Unlock()
	if id == "" || !t.Confirm.Pending(id) {
		return nil, false
	}
	out, ok := t.Call(ctx, ToolConfirm, map[string]any{"confirmation_id": id, "answer": said}, node.Request{VoiceCommand: said})
	if ok {
		out[node.IslandOnlyKey] = true
	}
	return out, ok
}
