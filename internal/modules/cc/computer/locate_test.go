package computer

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// locateLLM answers the locate questions in turn and keeps what it was asked.
type locateLLM struct {
	answers []string
	asked   []string
}

func (f *locateLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.asked = append(f.asked, req.Messages[0].Content.Parts[0].Text)
	ans := `{"absent": true}`
	if len(f.answers) > 0 {
		ans, f.answers = f.answers[0], f.answers[1:]
	}
	return &llm.ChatResponse{Content: ans}, nil
}

func locateRound(t *testing.T) *round {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1280, 800))); err != nil {
		t.Fatal(err)
	}
	return &round{
		run: &Run{Rounds: 3, History: []Entry{
			{Round: 1, Kind: "click", Intent: "Click on the Title field to focus it", Result: "ok"},
			{Round: 2, Kind: "click", Intent: "Save the note", Result: "ok"},
		}},
		task:  Task{HouseholdID: "h"},
		pick:  Pick{Label: "live", Adapter: computeruse.Norm1000},
		shots: []*Image{{MIME: "image/png", Data: buf.Bytes(), CaptureID: "c1", Width: 1280, Height: 800}},
	}
}

func saveClick(x, y int) computeruse.Action {
	return computeruse.Action{Kind: "click", X: &x, Y: &y, Intent: "Click the Save button to save the note"}
}

// A screenshot click is placed by its own question, then by a close-up of that spot: the 9B put
// Save 15 to 70 px too high in a round's answer and within a few pixels when asked alone.
func TestLocatePlacesAClickWithACloseUp(t *testing.T) {
	// First answer: (212, 940) of 1000 = pixel (271, 752). The close-up is the 320 px square
	// around it, clamped to the screenshot: x 111..431, y 480..800. Its (500, 890) of 1000 is
	// pixel (400, 712) of 800 = 160, 284 of the square = (271, 764) on the screenshot.
	f := &locateLLM{answers: []string{`{"x": 212, "y": 940}`, "```json\n[{\"point_2d\": [500, 890], \"label\": \"Save\"}]\n```"}}
	l := &Loop{LLM: f, Settings: fakeSettings{SettingLocateClicks: "on"}}
	x, y, ok := l.locate(context.Background(), locateRound(t), saveClick(188, 870))
	if !ok || x != 271 || y != 764 {
		t.Fatalf("locate = (%d, %d), %v; want (271, 764)", x, y, ok)
	}
	if len(f.asked) != 2 || !strings.Contains(f.asked[0], `"Click the Save button to save the note"`) || !strings.Contains(f.asked[1], "part of a screenshot") {
		t.Fatalf("asked %q", f.asked)
	}
}

// A close-up that doesn't show the control (a wide text area) leaves the first answer.
func TestLocateKeepsTheFirstAnswerWhenTheCloseUpHasNone(t *testing.T) {
	f := &locateLLM{answers: []string{`{"x": 500, "y": 500}`, `{"absent": true}`}}
	l := &Loop{LLM: f, Settings: fakeSettings{SettingLocateClicks: "on"}}
	if x, y, ok := l.locate(context.Background(), locateRound(t), saveClick(1, 1)); !ok || x != 640 || y != 400 {
		t.Fatalf("locate = (%d, %d), %v; want (640, 400)", x, y, ok)
	}
}

// Off by default, and never for an element action, a drag, a click made for the first time
// (the body field, after the title field), or a control the model can't find:
// the round's own coordinates stand.
func TestLocateLeavesOtherActionsAlone(t *testing.T) {
	on := fakeSettings{SettingLocateClicks: "on"}
	ref := saveClick(1, 1)
	ref.Ref = "e3"
	drag := saveClick(1, 1)
	drag.Kind = "drag"
	first := saveClick(1, 1)
	first.Intent = "Click on the Body field to focus it"
	for name, c := range map[string]struct {
		s fakeSettings
		a computeruse.Action
	}{"off": {fakeSettings{}, saveClick(1, 1)}, "ref": {on, ref}, "drag": {on, drag}, "first": {on, first}, "absent": {on, saveClick(1, 1)}} {
		f := &locateLLM{}
		l := &Loop{LLM: f, Settings: c.s}
		if _, _, ok := l.locate(context.Background(), locateRound(t), c.a); ok {
			t.Errorf("%s: placed", name)
		}
		if name != "absent" && len(f.asked) != 0 {
			t.Errorf("%s: the model was asked", name)
		}
	}
}
