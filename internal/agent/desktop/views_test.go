package desktop

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Tonight's Firefox (2026-10-25), as AT-SPI listed it.
var firefoxTabs = []View{
	{0, "New session - Claude", false, ""},
	{1, "Web analytics | Alexmberardi@gmail.com's Account | Cloudflare", false, ""},
	{2, "Jarvis Automation", false, ""},
	{3, "Usage - OpenAI API", false, ""},
	{4, "Discord | #general | Jarvis Automation", false, ""},
	{5, "YouTube", true, ""},
}

type fakeViews struct {
	views    []View
	switched []string
}

func (f *fakeViews) Views(context.Context, string) (string, []View, error) {
	return "Firefox", f.views, nil
}

func (f *fakeViews) Switch(_ context.Context, _ string, v View) error {
	f.switched = append(f.switched, v.Name)
	return nil
}

// What STT heard tonight, and the tab each one meant.
func TestMatchViewHeardNames(t *testing.T) {
	for heard, want := range map[string]string{
		"Claude":         "New session - Claude",
		"the Claude tab": "New session - Claude",
		"Claude pad":     "New session - Claude",
		"Cloud Fair":     "Web analytics | Alexmberardi@gmail.com's Account | Cloudflare",
		"web analytics":  "Web analytics | Alexmberardi@gmail.com's Account | Cloudflare",
		"Discord":        "Discord | #general | Jarvis Automation",
		"YouTube":        "YouTube",
		"open AI":        "Usage - OpenAI API",
	} {
		v, cands := matchView(firefoxTabs, heard)
		switch {
		case v != nil && v.Name == want:
		case v == nil && len(cands) > 0 && cands[0].Name == want:
			t.Logf("%q: asked, %q first", heard, want) // a choice with the right one first is fine
		default:
			t.Errorf("%q: got %+v / %v, want %q", heard, v, cands, want)
		}
	}
	// Claude and Cloudflare sound alike: "Claude pad" asks, Claude first.
	if v, cands := matchView(firefoxTabs, "Claude pad"); v != nil || len(cands) != 2 || cands[0].Name != "New session - Claude" {
		t.Errorf("Claude pad: got %+v / %v", v, cands)
	}
	if v, cands := matchView(firefoxTabs, "zebra"); v != nil || cands != nil {
		t.Errorf("zebra: got %+v / %v", v, cands)
	}
	// Two tabs carry "Jarvis Automation": ask, never pick one.
	if v, cands := matchView(firefoxTabs, "Jarvis Automation"); v != nil || len(cands) < 2 {
		t.Errorf("ambiguous: got %+v / %v", v, cands)
	}
}

func TestViewToolsSwitchAskAndAnswer(t *testing.T) {
	ts := New(host.New(&fakeBackend{caps: hypr}), OpenPolicy{}, catalogue)
	if slices.ContainsFunc(ts.Available(context.Background()), func(d node.ToolDef) bool { return d.Name == ToolView }) {
		t.Fatal("view tools offered with no view source")
	}
	fv := &fakeViews{views: firefoxTabs}
	ts.Views = fv
	ctx := context.Background()
	if !slices.ContainsFunc(ts.Available(ctx), func(d node.ToolDef) bool { return d.Name == ToolView }) {
		t.Fatal("view tools not offered")
	}

	out, _ := ts.Call(ctx, ToolView, map[string]any{"name": "Cloud Fair"}, node.Request{})
	if out["success"] != true || !slices.Equal(fv.switched, []string{firefoxTabs[1].Name}) || out[node.IslandOnlyKey] != true {
		t.Fatalf("switch: %v, switched %v", out, fv.switched)
	}

	out, _ = ts.Call(ctx, ToolView, map[string]any{"name": "Jarvis Automation"}, node.Request{})
	if out[node.ChoiceKey] != true || out["error_code"] != "ambiguous_view" || !strings.Contains(out[node.IslandKey].(string), "2.") {
		t.Fatalf("ambiguous: %v", out)
	}
	if _, ok := ts.AnswerChoice(ctx, "pizza"); ok {
		t.Fatal("not an answer, taken as one")
	}
	out, ok := ts.AnswerChoice(ctx, "the second one")
	if !ok || out["success"] != true || len(fv.switched) != 2 || !strings.Contains(fv.switched[1], "Jarvis Automation") {
		t.Fatalf("answer: %v %v, switched %v", out, ok, fv.switched)
	}

	// Nothing near: every tab is offered, since the name may be misheard.
	out, _ = ts.Call(ctx, ToolView, map[string]any{"name": "zebra"}, node.Request{})
	if out["error_code"] != "view_not_found" || out[node.ChoiceKey] != true {
		t.Fatalf("not found: %v", out)
	}

	out, _ = ts.Call(ctx, ToolViews, map[string]any{}, node.Request{})
	if out["count"] != len(firefoxTabs) || out["app"] != "Firefox" {
		t.Fatalf("list: %v", out)
	}
}
