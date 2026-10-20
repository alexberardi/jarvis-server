package cc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// fakeSightLLM answers the learn call with learn, and a call with images (naming clicks by
// sight) with a label for each "Event N" it is shown: labels[N], else "Label N".
type fakeSightLLM struct {
	mu     sync.Mutex
	learn  string
	labels map[int]string
	fail   error // the answer to a call with images
	bare   bool  // answer the events as a bare list, as some models do
	reqs   []llm.ChatRequest
}

var sightStepRE = regexp.MustCompile(`(?m)^Event (\d+):`)

func (f *fakeSightLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	parts := req.Messages[len(req.Messages)-1].Content.Parts
	if len(parts) == 0 {
		return &llm.ChatResponse{Content: f.learn}, nil
	}
	if f.fail != nil {
		return nil, f.fail
	}
	var evs []map[string]any
	for _, p := range parts {
		for _, m := range sightStepRE.FindAllStringSubmatch(p.Text, -1) {
			n, _ := strconv.Atoi(m[1])
			label := f.labels[n]
			if label == "" {
				label = "Label " + m[1]
			}
			evs = append(evs, map[string]any{"event": n, "app": "Text Editor", "role": "button", "label": label,
				"did": "clicked " + label, "confidence": 0.8})
		}
	}
	// One the model made up, and one it couldn't name: both ignored.
	evs = append(evs, map[string]any{"event": 99, "role": "button", "label": "Ghost", "confidence": 1},
		map[string]any{"event": 1, "role": "", "label": "", "confidence": 0.1})
	if f.bare {
		b, _ := json.Marshal(evs)
		return &llm.ChatResponse{Content: string(b)}, nil
	}
	b, _ := json.Marshal(map[string]any{"events": evs})
	return &llm.ChatResponse{Content: "```json\n" + string(b) + "\n```"}, nil
}

func (f *fakeSightLLM) Stream(context.Context, llm.ChatRequest) (<-chan llm.Frame, error) {
	return nil, fmt.Errorf("no stream")
}

func (f *fakeSightLLM) Embed(context.Context, []string) (llm.Embeddings, error) {
	return llm.Embeddings{}, fmt.Errorf("no embeddings")
}

// sightCalls are the requests that carried images.
func (f *fakeSightLLM) sightCalls() []llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []llm.ChatRequest
	for _, r := range f.reqs {
		if len(r.Messages[len(r.Messages)-1].Content.Parts) > 0 {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeSightLLM) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

type sightEnv struct {
	*cbEnv
	model *fakeSightLLM
}

// newSightEnv is a module whose slots have vision (or not) and the given context window.
func newSightEnv(t *testing.T, learn string, vision bool, window int) *sightEnv {
	t.Helper()
	withEnvExtra(t, envExtra{queue: true})
	model := &fakeSightLLM{learn: learn}
	ce := newCBEnv(t, envOpts{noMQTT: true, configure: func(m *Module) {
		m.LLM = model
		m.Endpoints = func(_ context.Context, label string) (llm.Endpoint, error) {
			return llm.Endpoint{Vision: vision, ContextLength: window}, nil
		}
	}})
	return &sightEnv{cbEnv: ce, model: model}
}

func (se *sightEnv) record(id string, trace json.RawMessage) {
	se.t.Helper()
	if _, err := se.m.createRecording(context.Background(), Recording{ID: id, HouseholdID: cbHH, OwnerID: 5,
		NodeID: "node-mac", Mode: "desktop", Trace: trace}); err != nil {
		se.t.Fatal(err)
	}
}

// keyframe stores event seq's keyframe (and crop) as the agent's upload does; each image is
// a distinct JPEG, returned as its data URL.
func (se *sightEnv) keyframe(id string, seq int, crop bool) (frame, cropURL string) {
	se.t.Helper()
	put := func(c bool, w int) string {
		data := testJPEG(se.t, w, 8, 0)
		if _, err := se.m.deps.Blobs.Put(context.Background(), keyframeKey(id, seq, c), strings.NewReader(data), "image/jpeg"); err != nil {
			se.t.Fatal(err)
		}
		return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte(data))
	}
	frame = put(false, 16+seq)
	if crop {
		cropURL = put(true, 4+seq)
	}
	return frame, cropURL
}

func (se *sightEnv) learn(id string) TaughtWorkflow {
	se.t.Helper()
	ctx := context.Background()
	if err := se.m.learnFromRecording(ctx, id); err != nil {
		se.t.Fatal(err)
	}
	ws, err := se.m.listWorkflows(ctx, cbHH, 5)
	if err != nil {
		se.t.Fatal(err)
	}
	for _, w := range ws {
		if w.RecordingID == id {
			return w
		}
	}
	se.t.Fatal("no workflow learnt from", id)
	return TaughtWorkflow{}
}

func (se *sightEnv) cardBody() string {
	se.notify.mu.Lock()
	defer se.notify.mu.Unlock()
	for _, it := range se.notify.items {
		if it.Category == learnCardCategory {
			return it.Body
		}
	}
	se.t.Fatal("no learnt card")
	return ""
}

// sightImages are a request's image data URLs, in order.
func sightImages(req llm.ChatRequest) []string {
	var out []string
	for _, p := range req.Messages[len(req.Messages)-1].Content.Parts {
		if p.ImageURL != nil {
			out = append(out, p.ImageURL.URL)
		}
	}
	return out
}

func sightText(req llm.ChatRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Content.Text != nil {
			b.WriteString(*m.Content.Text + "\n")
		}
		for _, p := range m.Content.Parts {
			b.WriteString(p.Text + "\n")
		}
	}
	return b.String()
}

// waylandSightModel makes the Wayland trace's click its own step, without an intent.
const waylandSightModel = `{"name": "Add to the shopping list",
 "steps": [
  {"intent": "", "event_seqs": [1]},
  {"intent": "Write the item", "event_seqs": [2]},
  {"intent": "Save it", "event_seqs": [3]}
 ]}`

// TestLearnNamesWaylandStepsBySight: a Wayland click has no element, only its point; before the
// learn call, the vision model sees its marked keyframe and crop, the window and the events
// around it. Its answer is what the learn prompt says was clicked, the step's element (source
// vision, its confidence), and the step's intent when the learn call gave none. Steps without a
// click are left alone.
func TestLearnNamesWaylandStepsBySight(t *testing.T) {
	se := newSightEnv(t, waylandSightModel, true, 32768)
	se.record("rec_wl_sight", desktopTrace(t, "desktop_wayland_trace.json"))
	frame, crop := se.keyframe("rec_wl_sight", 1, true)
	w := se.learn("rec_wl_sight")

	if len(w.Steps) != 3 {
		t.Fatalf("steps %s", mustJSON(t, w.Steps))
	}
	want := &RecordedElement{Role: "button", Name: "Label 1", Source: "vision", Confidence: 0.8}
	if g := mustJSON(t, w.Steps[0].Element); g != mustJSON(t, want) {
		t.Fatalf("step 1's element %s", g)
	}
	if w.Steps[0].Intent != "clicked Label 1" || w.Steps[0].Action.Intent != "clicked Label 1" {
		t.Fatalf("step 1's intent %q / %q", w.Steps[0].Intent, w.Steps[0].Action.Intent)
	}
	if w.Steps[0].App != "org.gnome.TextEditor" || w.Steps[0].Action.Kind != "click" || *w.Steps[0].Action.X != 1200 {
		t.Fatalf("step 1 %s", mustJSON(t, w.Steps[0]))
	}
	for _, s := range w.Steps[1:] {
		if s.Element != nil {
			t.Fatalf("a step without a click was named: %s", mustJSON(t, s))
		}
	}
	calls := se.model.sightCalls()
	if len(calls) != 1 {
		t.Fatalf("%d calls with images, want 1", len(calls))
	}
	if g := sightImages(calls[0]); len(g) != 2 || g[0] != frame || g[1] != crop {
		t.Fatalf("images %d, want the keyframe then its crop", len(g))
	}
	text := sightText(calls[0])
	for _, s := range []string{
		"Event 1: click in org.gnome.TextEditor",
		`window: org.gnome.TextEditor, "notes.txt"`,
		"after: type into org.gnome.TextEditor",
		"(200,340)", // the click on the keyframe
		"red ring",
	} {
		if !strings.Contains(text, s) {
			t.Fatalf("the request lacks %q:\n%s", s, text)
		}
	}
	if strings.Contains(text, "hunter2") {
		t.Fatalf("a masked value reached the model:\n%s", text)
	}
	se.model.mu.Lock()
	learnPrompt := *se.model.reqs[len(se.model.reqs)-1].Messages[1].Content.Text
	se.model.mu.Unlock()
	if want := `1. click button "Label 1" (seen on the screen) in org.gnome.TextEditor — "notes.txt" at (1200,340)`; !strings.Contains(learnPrompt, want) {
		t.Fatalf("the learn prompt lacks %q:\n%s", want, learnPrompt)
	}
	if body := se.cardBody(); strings.Contains(body, "vision") {
		t.Fatalf("the card has a vision note:\n%s", body)
	}
}

// mixedTrace: a click with an AT-SPI element, a click without (its keyframe marked), and a
// click without whose keyframe the throttle skipped. Every click has a keyframe kept.
const mixedTrace = `[
{"seq":1,"ts":1760000000000,"kind":"click","app":"thunderbird","x":10,"y":20,"element":{"role":"push button","name":"Write"}},
{"seq":2,"ts":1760000001000,"kind":"click","app":"thunderbird","x":300,"y":40,"context":"none","keyframe_point":{"x":150,"y":20}},
{"seq":3,"ts":1760000001500,"kind":"click","app":"thunderbird","x":500,"y":600,"context":"none"}
]`

const mixedModel = `{"name": "Send a mail", "steps": [
  {"intent": "Write a new mail", "event_seqs": [1]},
  {"intent": "Send it", "event_seqs": [2]},
  {"intent": "Close it", "event_seqs": [3]}]}`

// TestLearnSightLeavesAXSteps: a step with an accessibility element keeps it and its keyframe
// isn't sent; an unmarked keyframe goes with the click's screen point; a label naming a risky
// control (Send) makes the step ask first. The model answers a bare list here.
func TestLearnSightLeavesAXSteps(t *testing.T) {
	se := newSightEnv(t, mixedModel, true, 0)
	se.model.labels = map[int]string{2: "Send"}
	se.model.bare = true
	se.record("rec_mixed_sight", json.RawMessage(mixedTrace))
	axFrame, _ := se.keyframe("rec_mixed_sight", 1, false)
	frame2, crop2 := se.keyframe("rec_mixed_sight", 2, true)
	frame3, _ := se.keyframe("rec_mixed_sight", 3, false)
	w := se.learn("rec_mixed_sight")

	if g := mustJSON(t, w.Steps[0].Element); g != mustJSON(t, &RecordedElement{Role: "push button", Name: "Write", Source: "ax"}) {
		t.Fatalf("the AX step's element %s", g)
	}
	if w.Steps[0].Intent != "Write a new mail" {
		t.Fatalf("the AX step's intent %q", w.Steps[0].Intent)
	}
	if e := w.Steps[1].Element; e == nil || e.Source != "vision" || e.Name != "Send" || w.Steps[1].Intent != "Send it" {
		t.Fatalf("step 2 %s", mustJSON(t, w.Steps[1]))
	}
	if w.Steps[1].Risk != "send" {
		t.Fatalf("step 2's risk %q, want send", w.Steps[1].Risk)
	}
	if e := w.Steps[2].Element; e == nil || e.Source != "vision" || e.Name != "Label 3" {
		t.Fatalf("step 3 %s", mustJSON(t, w.Steps[2]))
	}
	var urls []string
	var text string
	for _, c := range se.model.sightCalls() {
		urls = append(urls, sightImages(c)...)
		text += sightText(c)
	}
	if strings.Join(urls, " ") != strings.Join([]string{frame2, crop2, frame3}, " ") {
		t.Fatalf("%d images sent; want step 2's keyframe and crop, then step 3's keyframe", len(urls))
	}
	for _, u := range urls {
		if u == axFrame {
			t.Fatal("the AX step's keyframe was sent")
		}
	}
	if strings.Contains(text, "Event 1:") || !strings.Contains(text, "unmarked") || !strings.Contains(text, "(500,600)") {
		t.Fatalf("request text:\n%s", text)
	}
}

// TestLearnSightWithoutVision: with no slot that sees images, nothing is guessed: no request
// carries images, the draft keeps its points, and the card says why its steps weren't named.
func TestLearnSightWithoutVision(t *testing.T) {
	se := newSightEnv(t, mixedModel, false, 0)
	se.record("rec_blind", json.RawMessage(mixedTrace))
	se.keyframe("rec_blind", 2, true)
	w := se.learn("rec_blind")

	if n := len(se.model.sightCalls()); n != 0 || se.model.calls() != 1 {
		t.Fatalf("%d calls, %d with images", se.model.calls(), n)
	}
	if w.Steps[0].Element == nil || w.Steps[1].Element != nil || w.Steps[2].Element != nil {
		t.Fatalf("steps %s", mustJSON(t, w.Steps))
	}
	body := se.cardBody()
	if !strings.Contains(body, "no vision") || !strings.Contains(body, "2 clicks") {
		t.Fatalf("the card doesn't say the model has no vision:\n%s", body)
	}
}

// TestLearnSightModelFails: a failed call with images doesn't fail the draft; its steps keep
// their points and the card says so.
func TestLearnSightModelFails(t *testing.T) {
	se := newSightEnv(t, mixedModel, true, 0)
	se.model.fail = fmt.Errorf("the engine is down")
	se.record("rec_fail_sight", json.RawMessage(mixedTrace))
	se.keyframe("rec_fail_sight", 2, true)
	w := se.learn("rec_fail_sight")
	if w.Steps[1].Element != nil || len(se.model.sightCalls()) != 1 {
		t.Fatalf("steps %s", mustJSON(t, w.Steps))
	}
	if body := se.cardBody(); !strings.Contains(body, "couldn't name 1 click from the screen") {
		t.Fatalf("card:\n%s", body)
	}
}

// TestLearnSightBatches: steps are named a few per call, fewer when the context is small, each
// call within the image cap.
func TestLearnSightBatches(t *testing.T) {
	var trace, steps []string
	for i := 1; i <= 10; i++ {
		trace = append(trace, fmt.Sprintf(`{"seq":%d,"kind":"click","app":"nautilus","x":%d,"y":10,"context":"none","keyframe_point":{"x":%d,"y":10}}`, i, i*10, i*10))
		steps = append(steps, fmt.Sprintf(`{"intent":"Open folder %d","event_seqs":[%d]}`, i, i))
	}
	model := `{"name": "Browse", "steps": [` + strings.Join(steps, ",") + `]}`
	for _, tc := range []struct {
		window, calls int
	}{{32768, 3}, {4000, 10}} {
		t.Run(strconv.Itoa(tc.window), func(t *testing.T) {
			se := newSightEnv(t, model, true, tc.window)
			id := "rec_many_" + strconv.Itoa(tc.window)
			se.record(id, json.RawMessage("["+strings.Join(trace, ",")+"]"))
			for i := 1; i <= 10; i++ {
				se.keyframe(id, i, true)
			}
			w := se.learn(id)
			calls := se.model.sightCalls()
			if len(calls) != tc.calls {
				t.Fatalf("%d calls with images, want %d", len(calls), tc.calls)
			}
			for _, c := range calls {
				if n := len(sightImages(c)); n > 2*sightMaxClicks {
					t.Fatalf("a call with %d images", n)
				}
			}
			for i, s := range w.Steps {
				if s.Element == nil || s.Element.Name != fmt.Sprintf("Label %d", i+1) {
					t.Fatalf("step %d %s", i+1, mustJSON(t, s))
				}
			}
		})
	}
}

// TestLearnSightWaitsForKeyframes: the trace is stored before its keyframes arrive. A click whose
// keyframe the trace says was taken, but isn't kept yet, has the job retry (no model call);
// once it is in, the step is named. After keyframeWait it learns without it.
func TestLearnSightWaitsForKeyframes(t *testing.T) {
	se := newSightEnv(t, waylandSightModel, true, 0)
	ctx := context.Background()
	se.record("rec_wl_wait", desktopTrace(t, "desktop_wayland_trace.json"))
	err := se.m.learnFromRecording(ctx, "rec_wl_wait")
	if err == nil || fmt.Sprintf("%T", err) == "queue.permanent" {
		t.Fatalf("err %T %v, want one to retry", err, err)
	}
	if se.model.calls() != 0 {
		t.Fatalf("%d model calls while waiting", se.model.calls())
	}
	se.keyframe("rec_wl_wait", 1, true)
	if w := se.learn("rec_wl_wait"); w.Steps[0].Element == nil || w.Steps[0].Element.Source != "vision" {
		t.Fatalf("step 1 %s", mustJSON(t, w.Steps[0]))
	}

	// One whose keyframe never comes: learnt with its point once keyframeWait has passed.
	se.record("rec_wl_late", desktopTrace(t, "desktop_wayland_trace.json"))
	later := time.Now().Add(keyframeWait + time.Minute)
	se.m.now = func() time.Time { return later }
	before := len(se.model.sightCalls())
	if w := se.learn("rec_wl_late"); w.Steps[0].Element != nil {
		t.Fatalf("step 1 %s", mustJSON(t, w.Steps[0]))
	}
	if len(se.model.sightCalls()) != before {
		t.Fatal("a call with images for a recording without keyframes")
	}
	// The job retries long enough to see keyframeWait pass.
	var waited time.Duration
	for n := 1; n < learnMaxAttempts; n++ {
		waited += learnBackoff(n)
	}
	if waited <= keyframeWait {
		t.Fatalf("%d attempts give up after %s, before keyframeWait (%s)", learnMaxAttempts, waited, keyframeWait)
	}
}
