package playback

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// ctlFake is a source that records the arguments of each control.
type ctlFake struct {
	fake
	args []map[string]any
}

func (c *ctlFake) Control(ctx context.Context, action string, args map[string]any) (*State, error) {
	c.args = append(c.args, args)
	switch action {
	case "volume":
		v := args["volume"].(int)
		c.st.Volume = &v
	case "next":
		c.st.MediaTitle = "Next song"
	}
	return c.fake.Control(ctx, action, args)
}

func toolRig(src Source) (*Tools, *rig) {
	r := newRig()
	if src != nil {
		r.sources = []Source{src}
	}
	return &Tools{Hub: r.hub}, r
}

func call(t *testing.T, tools *Tools, args map[string]any) map[string]any {
	t.Helper()
	out, ok := tools.Call(context.Background(), ToolName, args, node.Request{})
	if !ok {
		t.Fatal("not handled")
	}
	return out
}

func TestToolOfferedOnlyWithASource(t *testing.T) {
	tools, _ := toolRig(nil)
	if got := tools.Available(context.Background()); len(got) != 0 {
		t.Fatalf("offered with no source: %v", got)
	}
	tools, _ = toolRig(&fake{id: "pkg:radio", name: "Radio"})
	got := tools.Available(context.Background())
	if len(got) != 1 || got[0].Name != ToolName {
		t.Fatalf("available = %+v", got)
	}
	if out, ok := tools.Call(context.Background(), "other", nil, node.Request{}); ok || out != nil {
		t.Fatal("handled another tool")
	}
}

func TestToolWithNothingPlaying(t *testing.T) {
	tools, r := toolRig(&fake{id: "pkg:radio", name: "Radio"})
	r.step(0)
	out := call(t, tools, map[string]any{"action": "pause"})
	if out["success"] != false || out["message"] != "Nothing is playing here." {
		t.Fatalf("out = %v", out)
	}
}

func TestToolStatusAndControls(t *testing.T) {
	src := &ctlFake{fake: fake{id: "pkg:radio", name: "Spotify", st: &State{State: "playing", MediaTitle: "Song",
		MediaArtist: "Band", Actions: []string{"pause", "next", "volume"}}}}
	tools, r := toolRig(src)
	r.step(0)

	if out := call(t, tools, map[string]any{"action": "status"}); out["message"] != "Song by Band is playing on Spotify." {
		t.Fatalf("status = %v", out)
	}
	if out := call(t, tools, map[string]any{"action": "next"}); out["success"] != true || out["title"] != "Next song" {
		t.Fatalf("next = %v", out)
	}
	// Volume clamps and takes numeric strings; the reply is what the player read back.
	if out := call(t, tools, map[string]any{"action": "volume", "volume": "150%"}); out["volume"] != 100 || out["message"] != "Volume 100." {
		t.Fatalf("volume = %v", out)
	}
	if got := src.args[len(src.args)-1]["volume"]; got != 100 {
		t.Fatalf("volume sent = %v", got)
	}
	if out := call(t, tools, map[string]any{"action": "volume"}); out["success"] != false {
		t.Fatalf("volume with no level = %v", out)
	}
	if out := call(t, tools, map[string]any{"action": "pause"}); out["message"] != "Paused." || out["state"] != "paused" {
		t.Fatalf("pause = %v", out)
	}
}

func TestToolRefusesWhatTheSourceDoesNotSupport(t *testing.T) {
	src := &fake{id: "pkg:radio", name: "Radio", st: &State{State: "playing", Actions: []string{"pause"}}}
	tools, r := toolRig(src)
	r.step(0)
	out := call(t, tools, map[string]any{"action": "next"})
	if out["success"] != false || !strings.Contains(out["message"].(string), "Radio can't skip") || len(src.actions) != 0 {
		t.Fatalf("out = %v, actions = %v", out, src.actions)
	}
	if out := call(t, tools, map[string]any{"action": "rewind"}); out["success"] != false {
		t.Fatalf("unknown action = %v", out)
	}
}
