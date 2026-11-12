package browser

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node/nodetest"
)

// everydayPage is a busy page whose controls share words with room talk (Search, Next, Close,
// Play, Save, Send, Window shades…), as a news or shopping site has. acts counts the clicks
// and fills sent to it.
func everydayPage(acts *atomic.Int32) func(Message) Message {
	return func(m Message) Message {
		switch m.Cmd {
		case "snapshot":
			el := func(ref, role, name, target string) any {
				e := map[string]any{"ref": ref, "name": name, "role": role, "position": "middle-center"}
				if target != "" {
					e["target"] = target
				}
				return e
			}
			var els []any
			for i, c := range [][3]string{
				{"link", "Home", "https://news.test/"}, {"link", "Sports", "https://news.test/sports"},
				{"link", "Weather", "https://news.test/weather"}, {"textbox", "Search", ""}, {"button", "Search", ""},
				{"button", "Sign in", ""}, {"button", "Close", ""}, {"button", "Play", ""}, {"button", "Pause", ""},
				{"button", "Mute", ""}, {"button", "Next", ""}, {"link", "Next page", "https://news.test/?p=2"},
				{"button", "Show more", ""}, {"button", "Save", ""}, {"button", "Send", ""}, {"button", "Delete", ""},
				{"button", "Print", ""}, {"button", "Copy link", ""}, {"button", "Zoom in", ""}, {"button", "Add to cart", ""},
				{"link", "Window shades, blackout, 2 pack", "https://shop.test/shades"}, {"link", "Turn signal bulbs", "https://shop.test/bulbs"},
				{"link", "Kids' bedtime stories", "https://news.test/bedtime"}, {"link", "Octopus facts for kids", "https://news.test/octopus"},
				{"button", "Open in new tab", ""}, {"link", "Back to top", "https://news.test/#top"}, {"checkbox", "Select all", ""},
				{"button", "Undo", ""}, {"button", "Find", ""}, {"link", "Seven day forecast", "https://news.test/weather/7"},
			} {
				els = append(els, el("e"+string(rune('a'+i%26))+string(rune('0'+i/26)), c[0], c[1], c[2]))
			}
			return ok(map[string]any{"tab": map[string]any{"tab_id": 4.0}, "gen": 9.0, "url": "https://news.test/", "title": "Everyday News", "elements": els})
		case "list_tabs", "read_page", "find": // reading only
		default: // act, fill, click, open, media, navigate, scroll, switch_tab, close_tab, download…
			acts.Add(1)
		}
		return ok(map[string]any{})
	}
}

// Room talk never clicks, types into, searches or scrolls the page (nodetest's corpus), with a
// busy page in front and the turn listening (numbers showing). "I couldn't find that control"
// is allowed for a "click …" that names nothing on the page (2026-11-10: the model ignoring
// "on the microwave" is a later step); acting on the page is not.
func TestBrowserLeavesOrdinarySpeechToTheModel(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	var acts atomic.Int32
	connectExt(t, h, path, "Firefox", true, everydayPage(&acts))
	ctx := context.Background()
	ts.TurnListening(ctx, true)
	defer ts.TurnListening(ctx, false)
	nodetest.NeverActs(t, ts)
	if n := acts.Load(); n != 0 {
		t.Fatalf("%d actions reached the page", n)
	}
	// The page is real enough to act on: a command does.
	if out, ok := ts.Direct(ctx, "click Save"); !ok || out["success"] != true || acts.Load() != 1 {
		t.Fatalf("click Save: %v %v, %d actions", out, ok, acts.Load())
	}
}
