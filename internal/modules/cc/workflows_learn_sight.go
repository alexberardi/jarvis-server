package cc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// Naming desktop clicks by sight (#319). A desktop click with no accessibility element (Wayland
// without AT-SPI, an X11 or macOS lookup that came back empty) has only its point, app and
// window. Before the learn call, the vision model sees each such click's keyframe (the click
// ringed on Wayland) and crop, with the window and the events around it, and its answer becomes
// the event's element as the trace would have had it: role and visible label. The learn prompt
// then says what was clicked, and the step records the element with source "vision" and the
// model's confidence. A replay finds it by sight (the recorded element is the hint of its model
// round), never by an AX path. Decision 2026-10-20-naming-steps-by-sight.md.

const (
	elementSourceVision = "vision"

	sightMaxClicks = 4 // clicks one call names: at most 2*sightMaxClicks images
	// What one click takes of the model's context (its keyframe, its crop, its text), and what
	// the instructions and the answer need; a small window names fewer clicks per call.
	sightClickTokens   = 1600 + 400 + 300
	sightReserveTokens = 2000
	sightMaxTokens     = 1500
	sightMaxLabel      = 120 // runes of a role, label, app or "did"

	// keyframeWait is how long the learn job waits for a recording's keyframes, which the agent
	// sends after the trace (at once, else on its 5-minute sweep).
	keyframeWait = 10 * time.Minute
	// learnMaxAttempts with learnBackoff (2 s doubling, at most 5 min) retry for about 13
	// minutes, past keyframeWait.
	learnMaxAttempts = 10
)

func learnBackoff(attempt int) time.Duration {
	return min(2*time.Second<<min(attempt-1, 10), 5*time.Minute)
}

// errKeyframesPending is a desktop recording whose clicks' keyframes aren't all in yet: the
// learn job retries.
var errKeyframesPending = errors.New("waiting for the recording's keyframes")

// awaitKeyframes is errKeyframesPending while a click without an element has a keyframe the
// trace says was taken but the server doesn't keep yet, for keyframeWait after the recording
// was stored; nil when nothing would look at them (no queue to retry on, no blob store, no
// vision).
func (m *Module) awaitKeyframes(ctx context.Context, rec Recording, evs []desktopEvent) error {
	if m.deps.Queue == nil || m.deps.Blobs == nil || m.now().Sub(rec.CreatedAt) >= keyframeWait {
		return nil
	}
	var want []int64
	for _, e := range evs {
		if e.Kind == "click" && e.Element == nil && e.KeyframePoint != nil {
			want = append(want, e.Seq)
		}
	}
	if len(want) == 0 {
		return nil
	}
	if _, ok := m.visionLabel(ctx); !ok {
		return nil
	}
	kept, err := m.keyframeImages(ctx, rec.ID)
	if err != nil {
		return err
	}
	have := map[int64]bool{}
	for _, k := range kept {
		have[int64(k.Seq)] = true
	}
	missing := 0
	for _, s := range want {
		if !have[s] {
			missing++
		}
	}
	if missing > 0 {
		return fmt.Errorf("%w: %d of %d", errKeyframesPending, missing, len(want))
	}
	return nil
}

// sightClick is a click to name: its event number (1-based in evs), its keyframe and crop.
type sightClick struct {
	n           int
	frame, crop []byte
}

// nameClicksBySight names evs' clicks that have no element from their keyframes, before the
// learn call (so its prompt says what was clicked), and returns what the card should say about
// the ones it couldn't ("" when nothing). A click whose keyframe isn't kept keeps its point.
// Nothing here fails the learning.
func (m *Module) nameClicksBySight(ctx context.Context, rec Recording, evs []desktopEvent) string {
	var todo []int
	for i, e := range evs {
		if e.Kind == "click" && e.Element == nil {
			todo = append(todo, i+1)
		}
	}
	if len(todo) == 0 {
		return ""
	}
	label, ok := m.visionLabel(ctx)
	if !ok {
		m.deps.Log.Warn("cc: no model with vision to name a recording's clicks", "recording", rec.ID, "clicks", len(todo))
		return fmt.Sprintf("The model has no vision, so I couldn't name %d click%s from the screen: %s by %s recorded point.",
			len(todo), plural(len(todo)), pick(len(todo), "it replays", "they replay"), pick(len(todo), "its", "their"))
	}
	if m.deps.Blobs == nil {
		return ""
	}
	kept, err := m.keyframeImages(ctx, rec.ID)
	if err != nil {
		m.deps.Log.Warn("cc: couldn't list a recording's keyframes", "recording", rec.ID, "err", err)
		return ""
	}
	crops := map[int]bool{}
	for _, k := range kept {
		crops[k.Seq] = k.Crop
	}
	var ready []sightClick
	for _, n := range todo {
		seq := int(evs[n-1].Seq)
		crop, ok := crops[seq]
		if !ok {
			continue
		}
		c := sightClick{n: n}
		if c.frame, err = m.readKeyframe(ctx, keyframeKey(rec.ID, seq, false), keyframeMaxBytes); err != nil {
			m.deps.Log.Warn("cc: couldn't read a keyframe", "recording", rec.ID, "seq", seq, "err", err)
			continue
		}
		if crop {
			c.crop, _ = m.readKeyframe(ctx, keyframeKey(rec.ID, seq, true), keyframeCropMaxBytes)
		}
		ready = append(ready, c)
	}

	per := sightMaxClicks
	if ep, ok := m.endpoint(ctx, label); ok && ep.ContextLength > 0 {
		per = min(max((ep.ContextLength-sightReserveTokens)/sightClickTokens, 1), sightMaxClicks)
	}
	named, failed := 0, 0
	var lastErr error
	for i := 0; i < len(ready); i += per {
		batch := ready[i:min(i+per, len(ready))]
		n, err := m.nameBatch(ctx, label, evs, batch)
		named += n
		if err != nil {
			failed += len(batch)
			lastErr = err
		}
	}
	m.deps.Log.Info("cc: named a recording's clicks by sight", "recording", rec.ID, "label", label,
		"clicks", len(todo), "with_keyframe", len(ready), "named", named)
	if lastErr != nil {
		m.deps.Log.Warn("cc: couldn't name a recording's clicks by sight", "recording", rec.ID, "clicks", failed, "err", lastErr)
		return fmt.Sprintf("I couldn't name %d click%s from the screen (the model failed), so %s by %s recorded point.",
			failed, plural(failed), pick(failed, "it replays", "they replay"), pick(failed, "its", "their"))
	}
	return ""
}

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (m *Module) readKeyframe(ctx context.Context, key string, limit int64) ([]byte, error) {
	rc, _, err := m.deps.Blobs.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

const sightSystemPrompt = `You look at screenshots of someone using the apps on their computer, one for each click they made, and say what each click was on, as the app's accessibility tree would have recorded it. Answer only JSON.`

const sightInstructions = `For each event, answer what was clicked:
{"events": [{"event": 1, "app": "the app's name", "role": "button", "label": "Send", "did": "clicked Send", "confidence": 0.9}]}
- role: the control's accessibility role (button, link, menu item, tab, text field, checkbox, list item, icon…).
- label: its visible text, or for an icon what it means ("Settings"); "" when there is none.
- did: what the click did, in a few words ("clicked Send", "opened the Inbox folder", "clicked into the subject").
- confidence: 0 to 1, how sure you are the label names what was clicked. Leave out an event you can't tell.`

// nameBatch asks the model to name batch's clicks and writes its answers into evs as their
// elements; the number named.
func (m *Module) nameBatch(ctx context.Context, label string, evs []desktopEvent, batch []sightClick) (int, error) {
	parts := []llm.Part{{Type: "text", Text: "Clicks from a recorded task, each with the screen when it happened:"}}
	image := func(b []byte) llm.Part {
		return llm.Part{Type: "image_url", ImageURL: &llm.ImageURL{URL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)}}
	}
	for _, c := range batch {
		parts = append(parts, llm.Part{Type: "text", Text: sightClickText(evs, c.n)}, image(c.frame))
		if c.crop != nil {
			parts = append(parts, llm.Part{Type: "text", Text: "The same click up close:"}, image(c.crop))
		}
	}
	parts = append(parts, llm.Part{Type: "text", Text: sightInstructions})
	temp, maxTokens, think := 0.0, sightMaxTokens, 0
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{
		Label:           label,
		Temperature:     &temp,
		MaxTokens:       &maxTokens,
		ReasoningBudget: &think,
		ResponseFormat:  &llm.ResponseFormat{Type: "json_object"},
		Messages: []llm.Message{
			{Role: "system", Content: llm.TextContent(sightSystemPrompt)},
			{Role: "user", Content: &llm.Content{Parts: parts}},
		},
	})
	if err != nil {
		return 0, err
	}
	var out struct {
		Events []struct {
			Event      int     `json:"event"`
			App        string  `json:"app"`
			Role       string  `json:"role"`
			Label      string  `json:"label"`
			Did        string  `json:"did"`
			Confidence float64 `json:"confidence"`
		} `json:"events"`
	}
	i, j := strings.Index(resp.Content, "{"), strings.LastIndex(resp.Content, "}")
	if i < 0 || j < i {
		return 0, errors.New("the model's answer isn't JSON")
	}
	if err := json.Unmarshal([]byte(resp.Content[i:j+1]), &out); err != nil {
		return 0, fmt.Errorf("the model's answer isn't the events JSON: %w", err)
	}
	clean := func(s string) string { return truncateRunes(oneLine(s), sightMaxLabel) }
	named := 0
	for _, a := range out.Events {
		role, name := clean(a.Role), clean(a.Label)
		if !slices.ContainsFunc(batch, func(c sightClick) bool { return c.n == a.Event }) ||
			(role == "" && name == "") || evs[a.Event-1].Element != nil {
			continue
		}
		e := &evs[a.Event-1]
		e.Element = &desktopElement{Role: role, Name: name, Vision: true, Confidence: min(max(a.Confidence, 0), 1),
			Did: truncateRunes(clean(a.Did), computeruse.MaxIntent)}
		if e.App == "" {
			e.App = clean(a.App)
		}
		named++
	}
	return named, nil
}

// sightClickText is a click as the model sees it next to its keyframe: the click, its window,
// the events either side, and where the click is on the screenshot.
func sightClickText(evs []desktopEvent, n int) string {
	e := evs[n-1]
	var b strings.Builder
	fmt.Fprintf(&b, "Event %d: %s\n", n, describeDesktopEvent(e))
	if e.Window != nil && (e.Window.Class != "" || e.Window.Title != "") {
		fmt.Fprintf(&b, "window: %s, %s\n", orDefault(e.Window.Class, "unknown"), strconv.Quote(truncateRunes(oneLine(e.Window.Title), learnMaxValue)))
	}
	if n > 1 {
		fmt.Fprintf(&b, "before: %s\n", describeDesktopEvent(evs[n-2]))
	} else {
		b.WriteString("before: nothing, the recording starts here\n")
	}
	if n < len(evs) {
		fmt.Fprintf(&b, "after: %s\n", describeDesktopEvent(evs[n]))
	}
	if p := e.KeyframePoint; p != nil {
		fmt.Fprintf(&b, "On the screenshot the click is marked with a red ring, at (%d,%d) in its pixels.", p.X, p.Y)
	} else {
		fmt.Fprintf(&b, "The screenshot is the whole screen, unmarked; the click was at (%d,%d) in screen points.", e.X, e.Y)
	}
	return b.String()
}
