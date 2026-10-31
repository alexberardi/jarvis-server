package appclick

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/targets"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

type fakeWindows []computer.DesktopWindow

func (f *fakeWindows) List(context.Context) ([]computer.DesktopWindow, error) { return *f, nil }

// rig is a desktop with LibreOffice Writer in front: its controls, what was pressed, and the
// snapshot generation the fake accessibility tree is on.
type rig struct {
	wins    fakeWindows
	els     []computeruse.SnapElement
	gen     int
	pressed []string
	on      bool
	src     *Source
}

func newRig(t *testing.T) *rig {
	r := &rig{on: true,
		wins: fakeWindows{
			{ID: "w2", App: "soffice", PID: 42, Title: "Untitled 1 - LibreOffice Writer", Focused: true},
			{ID: "w1", App: "foot", PID: 7, Title: "~"},
		},
		els: []computeruse.SnapElement{
			{Ref: "a1", Role: "togglebutton", Name: "Bold", Context: "Formatting", Position: "top-left"},
			{Ref: "a2", Role: "togglebutton", Name: "Italic", Context: "Formatting", Position: "top-left"},
			{Ref: "a3", Role: "button", Name: "Delete Comment", Context: "Comments", Position: "top-right"},
			{Ref: "a4", Role: "button", Name: "OK", Context: "Character", Position: "bottom-right"},
			{Ref: "a5", Role: "button", Name: "OK", Context: "Paragraph", Position: "bottom-right"},
		}}
	r.src = &Source{
		Windows: &r.wins,
		Snapshot: func(_ context.Context, pid, max int) (computeruse.Snapshot, error) {
			if pid != 42 {
				t.Errorf("snapshot of pid %d, want the focused window's 42", pid)
			}
			r.gen++
			return computeruse.Snapshot{Gen: r.gen, Elements: slices.Clone(r.els)}, nil
		},
		Press: func(gen int, ref string) error {
			if gen != r.gen {
				return &staleErr{}
			}
			r.pressed = append(r.pressed, ref)
			return nil
		},
		Allowed: func() bool { return r.on },
		Confirm: &dictate.Confirmer{Mode: dictate.ConfirmSubmit},
	}
	return r
}

type staleErr struct{}

func (*staleErr) Error() string { return "ref is from an older snapshot" }

func TestClickByNameInTheFrontApp(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for said, ref := range map[string]string{"Click Bold.": "a1", "tap italic": "a2", "Quick bold.": "a1", "click on the bold button": "a1"} {
		out, ok := r.src.Direct(ctx, said)
		if !ok || out["success"] != true || out[node.IslandOnlyKey] != true || r.pressed[len(r.pressed)-1] != ref {
			t.Errorf("%q: %v %v, pressed %v; want %s", said, out, ok, r.pressed, ref)
		}
	}
}

func TestPassesOnWhatIsntAnAppClick(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for _, said := range []string{"press enter", "open Calc", "switch to Firefox", "what time is it", "Good morning."} {
		if out, ok := r.src.Direct(ctx, said); ok {
			t.Errorf("%q taken: %v", said, out)
		}
	}
	// The browser in front is the browser's (its own Direct runs first).
	r.wins[0] = computer.DesktopWindow{ID: "w3", App: "firefox", PID: 9, Title: "Google", Focused: true}
	if _, ok := r.src.Direct(ctx, "click Bold"); ok {
		t.Error("a click in the browser's window was taken")
	}
	// Computer control off: nothing on this computer is clicked by voice.
	r = newRig(t)
	r.on = false
	if _, ok := r.src.Direct(ctx, "click Bold"); ok || len(r.pressed) > 0 {
		t.Errorf("clicked with computer control off: %v", r.pressed)
	}
	// An app with no accessible controls: the model's (a computer task can still use the screen).
	r = newRig(t)
	r.els = nil
	if _, ok := r.src.Direct(ctx, "click Bold"); ok {
		t.Error("a click in an app with no controls was kept")
	}
}

// A literal "click" that matches nothing is said back, never a free model turn; a misheard one
// goes on as speech. Two controls with one name go to the pick, which here says none.
func TestMissesStayOnTheIsland(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	out, ok := r.src.Direct(ctx, "click the purple elephant")
	if !ok || out["success"] != false || out["error_code"] != "target_not_found" || len(r.pressed) > 0 {
		t.Fatalf("unmatched click: %v %v", out, ok)
	}
	if _, ok := r.src.Direct(ctx, "Good elephant."); ok {
		t.Error("a misheard click with nothing like it was kept")
	}
	if out, ok := r.src.Direct(ctx, "click OK"); !ok || out["success"] != false || len(r.pressed) > 0 {
		t.Errorf("two OK buttons without a pick: %v %v, pressed %v", out, ok, r.pressed)
	}
}

// The model's pick tells the two OK buttons apart by their context, in an app window.
func TestPickByContext(t *testing.T) {
	r := newRig(t)
	var prompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var b struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(req.Body).Decode(&b)
		prompt = b.Messages[0].Content
		_ = json.NewEncoder(w).Encode(map[string]any{"content": `{"ref":"a5","confidence":0.9}`})
	}))
	defer srv.Close()
	cc := node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k1"})
	r.src.Picker = targets.Picker{CC: func() *node.CC { return cc }, Wrap: func(s string) string { return "<<<" + s + ">>>" }}
	out, ok := r.src.Direct(context.Background(), "click OK in the paragraph dialog")
	if !ok || out["success"] != true || !slices.Equal(r.pressed, []string{"a5"}) {
		t.Fatalf("pick: %v %v, pressed %v", out, ok, r.pressed)
	}
	for _, want := range []string{"app window", "Paragraph", "Character"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

// A risky control ("Delete Comment") asks first; the yes presses it, unless the front window
// changed in between, which is a stale snapshot, not a retarget (§3).
func TestRiskyClickAsksAndChecksTheWindow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	out, ok := r.src.Direct(ctx, "click delete comment")
	if !ok || out["status"] != "awaiting_confirmation" || out[node.ChoiceKey] != true || len(r.pressed) > 0 {
		t.Fatalf("risky click: %v %v, pressed %v", out, ok, r.pressed)
	}
	out, ok = r.src.AnswerChoice(ctx, "yes")
	if !ok || out["success"] != true || !slices.Equal(r.pressed, []string{"a3"}) {
		t.Fatalf("yes: %v %v, pressed %v", out, ok, r.pressed)
	}

	r = newRig(t)
	r.src.Direct(ctx, "click delete comment")
	r.wins[0].Focused, r.wins[1].Focused = false, true // the user went to the terminal
	out, ok = r.src.AnswerChoice(ctx, "yes")
	if !ok || out["success"] != false || len(r.pressed) > 0 {
		t.Fatalf("yes after the window changed: %v %v, pressed %v", out, ok, r.pressed)
	}
}

// A press against a snapshot that a newer one replaced fails cleanly, nothing pressed.
func TestStaleRefFails(t *testing.T) {
	r := newRig(t)
	r.src.Press = func(int, string) error { return &staleErr{} }
	out, ok := r.src.Direct(context.Background(), "click bold")
	if !ok || out["success"] != false || out[node.IslandKey] == nil {
		t.Fatalf("stale: %v %v", out, ok)
	}
}
