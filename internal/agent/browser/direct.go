package browser

import (
	"context"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Funnel stage 1 for the page (VOICE-FLOWS.md Q27): "scroll down", "scroll down a bit more",
// "scroll to the top" scroll the current tab with no model. The model answered "scroll down a
// bit more" with "already scrolled" and did nothing.
var (
	reScroll = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?scroll\s+(?:the\s+page\s+)?(up|down)((?:\s+(?:a|bit|little|some|more|again|please|the\s+page))*)[\s.!?]*$`)
	reEdge   = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?(?:scroll|go|jump)\s+(?:back\s+)?(?:up\s+|down\s+)?to\s+the\s+(top|bottom)(?:\s+of\s+the\s+page)?[\s.!?]*$`)
)

// Direct resolves page controls and simple browser actions locally (funnel stage 1); ok false
// passes the request on.
func (t *Tools) Direct(ctx context.Context, said string) (map[string]any, bool) {
	if !node.Enabled(ctx, ToolClick) || len(t.Hub.Browsers()) == 0 {
		return t.directScroll(ctx, said)
	}
	if out, ok := t.directMedia(ctx, said); ok { // before control matching: "click the video" is media
		return out, true
	}
	if out, ok := t.directHints(ctx, said); ok {
		return out, true
	}
	if out, ok := t.directWeb(ctx, said); ok {
		return out, true
	}
	return t.directScroll(ctx, said)
}

func (t *Tools) directScroll(ctx context.Context, said string) (map[string]any, bool) {
	var dir string
	if m := reScroll.FindStringSubmatch(said); m != nil {
		dir = strings.ToLower(m[1])
	} else if m := reEdge.FindStringSubmatch(said); m != nil {
		dir = strings.ToLower(m[1])
	} else {
		return nil, false
	}
	if !node.Enabled(ctx, ToolScroll) || len(t.Hub.Browsers()) == 0 {
		return nil, false
	}
	out, ok := t.Call(ctx, ToolScroll, map[string]any{"direction": dir}, node.Request{VoiceCommand: said})
	if ok && out["error_code"] == CodeAtEnd {
		out[node.IslandKey], out[node.IslandOnlyKey] = "Already at "+map[bool]string{true: "the top", false: "the bottom"}[dir == "up" || dir == "top"], true
		return out, true // done as far as it goes: the island says so, no model
	}
	if !ok || out["success"] != true {
		return nil, false // the model can say why, or scroll some other way
	}
	out[node.IslandOnlyKey] = true
	t.refreshHints(ctx)
	return out, true
}

var _ node.Director = (*Tools)(nil)
