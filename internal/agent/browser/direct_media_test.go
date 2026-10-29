package browser

import (
	"context"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestMediaRequest(t *testing.T) {
	for said, want := range map[string]mediaReq{
		"Click the video.":            {action: "toggle"},
		"click on the video player":   {action: "toggle"},
		"Play the video":              {action: "play"},
		"resume":                      {action: "play"},
		"Pause.":                      {action: "pause"},
		"stop the video":              {action: "pause"},
		"Jarvis, pause the music":     {action: "pause"},
		"mute the video":              {action: "mute"},
		"unmute":                      {action: "unmute"},
		"skip ahead 30 seconds":       {action: "skip_forward", seconds: 30},
		"fast forward":                {action: "skip_forward"},
		"go back ten seconds":         {action: "skip_back", seconds: 10},
		"rewind":                      {action: "skip_back"},
		"restart the video":           {action: "restart"},
		"go back":                     {}, // the page's back, not the video's
		"stop":                        {action: "pause"},
		"play Adele Hello on YouTube": {}, // finding something to play is the model's
		"click videos":                {}, // a page control, not media
	} {
		if got := mediaRequest(said); got != want {
			t.Errorf("%q: %+v, want %+v", said, got, want)
		}
	}
}

// mediaPage is a tab with a paused video (or none, with media false).
func mediaPage(media bool) (func(Message) Message, func() []map[string]any) {
	var mu sync.Mutex
	var sent []map[string]any
	return func(m Message) Message {
			if m.Cmd != "media" {
				return ok(map[string]any{})
			}
			mu.Lock()
			sent = append(sent, m.Args)
			mu.Unlock()
			if !media {
				return Message{OK: false, Error: "no video or audio found", Code: "no_media"}
			}
			return ok(map[string]any{"tab": map[string]any{"tab_id": 4.0}, "action": m.Args["action"], "media": map[string]any{"paused": true, "kind": "video"}})
		}, func() []map[string]any {
			mu.Lock()
			defer mu.Unlock()
			return append([]map[string]any(nil), sent...)
		}
}

func TestClickTheVideoPlaysIt(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	page, sent := mediaPage(true)
	connectExt(t, h, path, "Firefox", true, page)
	ctx := context.Background()
	out, ok := ts.Direct(ctx, "Click the video.")
	if !ok || out["success"] != true || out[node.IslandKey] != "Playing" {
		t.Fatalf("click the video: %v %v", out, ok)
	}
	if s := sent(); len(s) != 2 || s[0]["action"] != "status" || s[1]["action"] != "play" {
		t.Fatalf("media calls %v; want status, then play (it was paused)", s)
	}
	if out, _ := ts.Direct(ctx, "skip ahead 30 seconds"); out["success"] != true || sent()[2]["seconds"] != 30.0 {
		t.Fatalf("skip: %v %v", out, sent())
	}
}

// With no video, "stop" is left to the rest of the funnel, and "pause" to the model.
func TestNoVideoPassesOn(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	page, _ := mediaPage(false)
	connectExt(t, h, path, "Firefox", true, page)
	if out, ok := ts.directMedia(context.Background(), "stop"); ok {
		t.Fatalf("no video, still taken: %v", out)
	}
}
