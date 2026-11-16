package computer

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

func msgText(m llm.Message) string {
	if m.Content == nil {
		return ""
	}
	if m.Content.Text != nil {
		return *m.Content.Text
	}
	var b strings.Builder
	for _, p := range m.Content.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// In a conversation the task runs one action an answer; each answer stays in view with its
// thought, and after it comes what it changed on the screen.
func TestTurnsKeepEachAnswerAndItsOutcome(t *testing.T) {
	cart := shopPage()
	cart.Gen = 2
	cart.Elements = append(cart.Elements, Element{Ref: "e7", Role: "button", Name: "Remove from cart"})
	e := newEnv(t, shopPage(), cart)
	e.setts[SettingLoop] = LoopTurns
	e.script(`{"thought": "add it, then look at the cart", "actions": [`+press("e3", "Add the soap to the cart")+`,`+press("e4", "Open the cart")+`]}`,
		doneAnswer)
	tk := task()
	tk.Plan = "-> 1. add the soap\n   2. tell the user\n"
	out := e.loop.Run(t.Context(), tk)
	if out.State != StateDone {
		t.Fatalf("outcome %+v", out)
	}
	if len(e.exec.batches) != 1 || len(e.exec.batches[0].Actions) != 1 || e.exec.batches[0].Actions[0].Ref != "e3" {
		t.Fatalf("one action an answer: batches %+v", e.exec.batches)
	}
	first, second := e.llm.reqs[0].Messages, e.llm.reqs[1].Messages
	if len(first) != 2 || !strings.Contains(msgText(first[0]), "Send exactly one action per answer") ||
		!strings.Contains(msgText(first[1]), "-> 1. add the soap") || !strings.Contains(msgText(first[1]), `e3 button "Add to cart"`) {
		t.Fatalf("first call:\n%s\n---\n%s", msgText(first[0]), msgText(first[1]))
	}
	if len(second) != 4 || second[2].Role != "assistant" || second[3].Role != "user" {
		t.Fatalf("second call has %d messages", len(second))
	}
	if strings.Contains(msgText(second[1]), "Add to cart") || !strings.Contains(msgText(second[1]), "Task: Add the lavender soap") {
		t.Fatalf("the header carries a screen:\n%s", msgText(second[1]))
	}
	if a := msgText(second[2]); !strings.Contains(a, "add it, then look at the cart") || !strings.Contains(a, `"ref":"e3"`) || strings.Contains(a, "e4") {
		t.Fatalf("the answer in view: %s", a)
	}
	o := msgText(second[3])
	for _, want := range []string{`Outcome: Sent. On the screen: new: button "Remove from cart".`, "Only your first action ran: the other 1 did not", `e7 button "Remove from cart"`, "Your JSON answer:"} {
		if !strings.Contains(o, want) {
			t.Fatalf("the outcome lacks %q:\n%s", want, o)
		}
	}
}

// An answer that ran nothing stays in the conversation with what the model was told.
func TestTurnsKeepAnAnswerThatWasSentBack(t *testing.T) {
	e := newEnv(t)
	e.setts[SettingLoop] = LoopTurns
	e.script(`{"done": true, "summary": "Ordered the soap.", "verify": {"url_contains": "/soap"}}`,
		`{"ask": "Should I place the order?", "reason": "unclear"}`)
	tk := task()
	tk.Goal = "Order the lavender soap"
	if out := e.loop.Run(t.Context(), tk); out.State != StateAsk {
		t.Fatalf("outcome %+v", out)
	}
	m := e.llm.reqs[1].Messages
	if len(m) != 4 || !strings.Contains(msgText(m[2]), `"done":true`) || !strings.Contains(msgText(m[3]), "Not run: you said done, but nothing is ordered yet") {
		t.Fatalf("messages %d:\n%s\n---\n%s", len(m), msgText(m[len(m)-2]), msgText(m[len(m)-1]))
	}
	if strings.Count(msgText(m[3]), "nothing is ordered yet") != 1 {
		t.Fatalf("told twice:\n%s", msgText(m[3]))
	}
}

func TestDescribeChange(t *testing.T) {
	before := Observation{Title: "Untitled 1 — Writer", Elements: []Element{
		{Ref: "a1", Role: "textbox", Name: "Name", Value: ""}, {Ref: "a2", Role: "button", Name: "Save"}}, Text: "x"}
	same := before
	if got := describeChange(before, same); got != "nothing visibly changed." {
		t.Errorf("same screen: %q", got)
	}
	typed := before
	typed.Elements = []Element{{Ref: "a5", Role: "textbox", Name: "Name", Value: "Velmora"}, {Ref: "a6", Role: "button", Name: "Save"}}
	if got := describeChange(before, typed); got != `textbox "Name" now holds "Velmora".` {
		t.Errorf("a typed field: %q", got)
	}
	saved := Observation{Title: "Velmora.odt — Writer", Elements: before.Elements, Text: "x"}
	if got := describeChange(before, saved); got != `the window is now titled "Velmora.odt — Writer" (it was "Untitled 1 — Writer").` {
		t.Errorf("a saved document: %q", got)
	}
	page := Observation{URL: "https://shop.example.com/thanks", Title: "Thank you", Elements: []Element{{Ref: "e1", Role: "link", Name: "Home"}}}
	if got := describeChange(Observation{URL: "https://shop.example.com/checkout"}, page); !strings.HasPrefix(got, `the page is now https://shop.example.com/thanks ("Thank you")`) {
		t.Errorf("a new page: %q", got)
	}
}

func shot(t *testing.T, mark image.Rectangle) *Image {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 300, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 300; x++ {
			c := color.RGBA{230, 230, 230, 255}
			if image.Pt(x, y).In(mark) {
				c = color.RGBA{0, 0, 0, 255}
			}
			im.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	return &Image{MIME: "image/png", Data: buf.Bytes(), Width: 300, Height: 300}
}

// An app that lists nothing is compared by its screenshots: a missed click changes nothing, a
// blinking cursor a few pixels, a saved note a part of the screen.
func TestImageChange(t *testing.T) {
	blank := shot(t, image.Rectangle{})
	if got := imageChange(blank, shot(t, image.Rectangle{})); !strings.HasPrefix(got, "nothing visibly changed") {
		t.Errorf("same: %q", got)
	}
	if got := imageChange(blank, shot(t, image.Rect(10, 30, 12, 46))); !strings.Contains(got, "almost nothing changed") || !strings.Contains(got, "top left") {
		t.Errorf("a cursor: %q", got)
	}
	if got := imageChange(blank, shot(t, image.Rect(20, 220, 90, 260))); got != "the screenshot changed, mostly near the bottom left." {
		t.Errorf("a new note in the list: %q", got)
	}
	if got := imageChange(blank, shot(t, image.Rect(100, 2, 200, 18))); !strings.HasPrefix(got, "nothing visibly changed") {
		t.Errorf("Jarvis's own indicator counts as a change: %q", got)
	}
	if got := imageChange(blank, nil); got != "" {
		t.Errorf("nothing to compare: %q", got)
	}
}

// auto is a conversation for a large model and rounds for a small one, and a small model's
// lost task says the model may be why.
func TestLoopFollowsTheModelsSize(t *testing.T) {
	for _, c := range []struct {
		model     string
		wantTurns bool
	}{{"qwen3.8-27b", true}, {"qwen3.5-9b", false}} {
		e := newEnv(t, shopPage())
		delete(e.setts, SettingLoop)
		e.setts[SettingMaxRounds] = 1
		e.loop.Models.Endpoint = func(_ context.Context, _ string) (llm.Endpoint, error) {
			return llm.Endpoint{Model: c.model, Vision: true}, nil
		}
		go1 := `{"actions": [` + press("e3", "Add the soap to the cart") + `]}`
		e.script(go1, go1, go1)
		out := e.loop.Run(t.Context(), task())
		if out.State != StateFailed {
			t.Fatalf("%s: outcome %+v, want it out of rounds", c.model, out)
		}
		sys := msgText(e.llm.reqs[0].Messages[0])
		if got := sys == turnsSystemPrompt; got != c.wantTurns {
			t.Errorf("%s: a conversation = %v, want %v", c.model, got, c.wantTurns)
		}
		if got := out.ModelNote != ""; got == c.wantTurns {
			t.Errorf("%s: model note %q", c.model, out.ModelNote)
		}
	}
}
