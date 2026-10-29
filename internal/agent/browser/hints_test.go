package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The fake page includes the duplicate thumbnail/title links common on search result pages.
func videoPage(m Message) Message {
	switch m.Cmd {
	case "snapshot":
		link := func(ref, name, target string) any {
			return map[string]any{"ref": ref, "name": name, "role": "link", "target": target, "position": "middle-center"}
		}
		return ok(map[string]any{"tab": map[string]any{"tab_id": 4.0}, "gen": 9.0,
			"url": "https://www.youtube.com/results?search_query=hello", "elements": []any{
				link("e1", "Adele - Hello (Official Music Video)", "https://www.youtube.com/watch?v=adele"),
				link("e2", "Adele - Hello (Official Music Video)", "https://www.youtube.com/watch?v=adele"),
				link("e3", "Lionel Richie - Hello (Official Music Video)", "https://www.youtube.com/watch?v=lionel"),
				link("e4", "Lionel Richie - Hello (Official Music Video)", "https://www.youtube.com/watch?v=lionel"),
				link("e5", "Videos", "https://www.youtube.com/results?search_query=hello&sp=EgIQAQ%253D%253D"),
			}})
	case "act":
		return ok(map[string]any{"ok": true})
	}
	return ok(map[string]any{})
}

// Part names and misheard names still work without numbering. Identical result links are safe
// to collapse because either ref navigates to the same destination.
func TestClickByPartOrSoundOfAName(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	f := connectExt(t, h, path, "Firefox", true, videoPage)
	ctx := context.Background()
	for said, ref := range map[string]string{
		"Click Adele Hello.":                    "e1",
		"click adil hello official music video": "e1",
		"Click Lionel Richie Hello.":            "e3",
		"Click the first video.":                "e1",
		"Click the first link.":                 "e1",
		"The first one. Click the first video.": "e1",
		"Go to videos.":                         "e5",
		"Quick videos.":                         "e5",
		"Good videos.":                          "e5",
		"The second one. Quick videos.":         "e5",
	} {
		ts.readTargets(ctx, false)
		out, ok := ts.Direct(ctx, said)
		if !ok || out["success"] != true || lastArgs(t, f, "act")["ref"] != ref {
			t.Errorf("%q: %v %v, act %v; want %s", said, out, ok, lastArgs(t, f, "act")["ref"], ref)
		}
	}
	ts.readTargets(ctx, false)
	if _, ok := ts.Direct(ctx, "Good morning."); ok {
		t.Error("a misheard click with nothing like it on the page was taken")
	}
	ts.readTargets(ctx, false)
	if _, ok := ts.directHints(ctx, "go to youtube.com"); ok {
		t.Error("a site not on the page was taken for a control")
	}
	ts.readTargets(ctx, false)
	if _, ok := ts.Direct(ctx, "hello"); ok {
		t.Error("a bare word clicked something")
	}
	ts.readTargets(ctx, false)
	if _, ok := ts.Direct(ctx, "Press enter."); ok {
		t.Error("press enter was taken for a control; it's a key")
	}
	ts.readTargets(ctx, false)
	out, ok := ts.Direct(ctx, "Click line over and say hello.")
	if !ok || out["success"] != false || out[node.ChoiceKey] != true {
		t.Errorf("click on nothing: %v %v", out, ok)
	}
}

// The active voice snapshot is invisible. Names still resolve directly, and a typing command
// aimed at another application remains available to the dictation handler.
func TestNameResolutionWithoutOverlay(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	var received []Message
	f := connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		received = append(received, m)
		switch m.Cmd {
		case "snapshot":
			return ok(map[string]any{"tab": map[string]any{"tab_id": 4.0}, "gen": 9.0,
				"url": "https://www.google.com/", "elements": []any{
					map[string]any{"ref": "e1", "name": "Search", "role": "combobox"},
					map[string]any{"ref": "e2", "name": "Images", "role": "link", "target": "https://www.google.com/imghp"},
					map[string]any{"ref": "e3", "name": "", "role": "button"},
				}})
		case "act":
			return ok(map[string]any{"ok": true})
		}
		return ok(map[string]any{})
	})
	ctx := context.Background()
	ts.readTargets(ctx, false)
	if out, ok := ts.Direct(ctx, "click images"); !ok || out["success"] != true || lastArgs(t, f, "act")["ref"] != "e2" {
		t.Fatalf("a name with no overlay: %v %v", out, ok)
	}
	ts.readTargets(ctx, false)
	if out, ok := ts.Direct(ctx, "Type dogs into the search box."); !ok || out["success"] != true || lastArgs(t, f, "act")["ref"] != "e1" {
		t.Fatalf("type into Search: %v %v", out, ok)
	}
	if _, ok := ts.Direct(ctx, "type hello into Slack"); ok {
		t.Fatal("a name not on the page was kept; it should pass to dictation")
	}
	ts.readTargets(ctx, false)
	if out, ok := ts.AnswerChoice(ctx, "The first one. Click the first video."); !ok || out["success"] != false || out[node.ChoiceKey] != true {
		t.Fatalf("a click in the middle of a sentence escaped the page handler: %v %v", out, ok)
	}
	for _, msg := range received {
		if msg.Cmd != "snapshot" && msg.Cmd != "act" {
			t.Errorf("unexpected page command %q; selection must not draw an overlay", msg.Cmd)
		}
	}
	if res, _ := ts.Call(ctx, ToolClick, map[string]any{"text": "images"}, node.Request{}); res["success"] != true || lastArgs(t, f, "act")["ref"] != "e2" {
		t.Fatalf("browser_click by name: %v", res)
	}
}

// fakeModel is jarvisd's /node/llm/chat answering reply, recording the prompts.
func fakeModel(t *testing.T, reply string) (func() *node.CC, func() []string) {
	var mu sync.Mutex
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		for _, m := range b.Messages {
			prompts = append(prompts, m.Content)
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"content": reply})
	}))
	t.Cleanup(srv.Close)
	cc := node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k1"})
	return func() *node.CC { return cc }, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(prompts) }
}

// A name nothing matches: the model picks only a current ref, even when its JSON is fenced or
// surrounded by prose. "none" passes through for typing regardless of the model's confidence.
func TestModelPicksWhenNoNameMatches(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	f := connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		if m.Cmd == "snapshot" {
			return ok(map[string]any{"tab": map[string]any{"tab_id": 4.0}, "gen": 9.0, "url": "https://www.google.com/",
				"elements": []any{
					map[string]any{"ref": "e1", "name": "Search", "role": "combobox", "position": "top-center", "context": "Search the web"},
					map[string]any{"ref": "e2", "name": "Images", "role": "link", "position": "top-center", "context": "Search the web"},
				}})
		}
		if m.Cmd == "act" {
			return ok(map[string]any{"ok": true})
		}
		return ok(map[string]any{})
	})
	ctx := context.Background()
	var prompts func() []string
	ts.CC, prompts = fakeModel(t, "Here is the result:\n```json\n{\"ref\":\"e2\",\"confidence\":0.91}\n```")
	ts.readTargets(ctx, false)
	out, ok := ts.Direct(ctx, "click the pictures tab")
	if !ok || out["success"] != true || lastArgs(t, f, "act")["ref"] != "e2" {
		t.Fatalf("model pick: %v %v", out, ok)
	}
	p := strings.Join(prompts(), "\n")
	for _, want := range []string{"click the pictures tab", "e2", "Images", "Search the web", "top-center", PageBegin, PageEnd} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	// A low-confidence concrete choice asks for clarification.
	ts.readTargets(ctx, false)
	ts.CC, _ = fakeModel(t, `{"ref":"e1","confidence":0.4}`)
	if out, ok := ts.Direct(ctx, "click the search thing"); !ok || out["success"] != false || out[node.ChoiceKey] != true {
		t.Fatalf("uncertain target: %v %v", out, ok)
	}
	// An explicit miss, even with low confidence, doesn't swallow dictation.
	ts.readTargets(ctx, false)
	ts.CC, _ = fakeModel(t, `{"ref":"none","confidence":0.2}`)
	if _, ok := ts.Direct(ctx, "type hello into Slack"); ok {
		t.Fatal("a low-confidence none blocked typing into Slack")
	}
	// A fabricated id is never acted on.
	ts.readTargets(ctx, false)
	ts.CC, _ = fakeModel(t, `{"ref":"e99","confidence":0.99}`)
	acts := len(f.received())
	if out, _ := ts.Direct(ctx, "click the purple elephant"); out["success"] != false || len(f.received()) != acts {
		t.Fatalf("a made-up id: %v", out)
	}
}

// A click starts a navigation that the browser hasn't begun when the click returns: the page
// must be read again when the next command comes, not at once. Re-reading at once kept the
// Images page while the Videos page loaded, and every later command missed (2026-10-28).
func TestNextCommandSeesThePageTheClickOpened(t *testing.T) {
	var mu sync.Mutex
	page, snaps := "images", 0
	var waits []any
	h, path := startHub(t)
	ts := NewTools(h)
	f := connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		mu.Lock()
		defer mu.Unlock()
		switch m.Cmd {
		case "snapshot":
			snaps++
			waits = append(waits, m.Args["wait"])
			els := []any{map[string]any{"ref": "e1", "name": "Videos", "role": "link", "target": "https://g.co/v"}}
			if page == "videos" {
				els = []any{map[string]any{"ref": "e1", "name": "Top 5 Dogs of the Week", "role": "link", "target": "https://yt/1"}}
			}
			return ok(map[string]any{"tab": map[string]any{"tab_id": 4.0}, "gen": float64(snaps), "url": "https://g.co/" + page, "elements": els})
		case "act":
			return ok(map[string]any{"ok": true})
		}
		return ok(map[string]any{})
	})
	ctx := context.Background()
	ts.TurnListening(ctx, true)
	if out, ok := ts.Direct(ctx, "go to videos"); !ok || out["success"] != true {
		t.Fatalf("go to videos: %v %v", out, ok)
	}
	mu.Lock()
	if snaps != 1 {
		t.Errorf("%d snapshots right after the click; the page hasn't changed yet", snaps)
	}
	page = "videos" // the browser loads the new page while the user speaks
	mu.Unlock()
	out, ok := ts.Direct(ctx, "Click Top 5 Dogs of the Week.")
	if !ok || out["success"] != true || lastArgs(t, f, "act")["gen"] != 2.0 {
		t.Fatalf("the new page's link: %v %v (act %v)", out, ok, lastArgs(t, f, "act"))
	}
	mu.Lock()
	defer mu.Unlock()
	if waits[len(waits)-1] != true {
		t.Errorf("the re-read didn't wait for the page to load: %v", waits)
	}
	ts.TurnListening(ctx, false)
}
