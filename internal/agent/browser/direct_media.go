package browser

import (
	"context"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/targets"
)

// Funnel stage 1 for what's playing in the browser: "click the video", "pause", "skip ahead 30
// seconds" go to browser_media at once. "Click the video" used to be mistaken for an element
// name and fail (2026-10-27).

var (
	reMediaToggle = regexp.MustCompile(`(?i)^(?:click|tap|toggle)\s+(?:on\s+)?(?:the\s+)?(?:video(?:\s+player)?|player)$`)
	reMediaPlay   = regexp.MustCompile(`(?i)^(?:play|resume|unpause|start)(?:\s+(?:the\s+)?(?:video|music|song|it|playback))?(?:\s+again)?$`)
	reMediaPause  = regexp.MustCompile(`(?i)^(?:pause|stop)(?:\s+(?:the\s+)?(?:video|music|song|it|playback))?$`)
	reMediaMute   = regexp.MustCompile(`(?i)^(un)?mute(?:\s+(?:the\s+)?(?:video|music|it|tab|sound))?$`)
	reMediaFwd    = regexp.MustCompile(`(?i)^(?:(?:skip|jump|go)\s+(?:ahead|forward)|fast\s+forward)(?:\s+(?:by\s+)?(.+?)\s+seconds?)?$`)
	reMediaBack   = regexp.MustCompile(`(?i)^(?:(?:skip|jump|go)\s+back|rewind)(?:\s+(?:by\s+)?(.+?)\s+seconds?)?$`)
	reMediaRest   = regexp.MustCompile(`(?i)^(?:restart|start\s+over)(?:\s+(?:the\s+)?(?:video|song|it))?$`)
)

// mediaReq is a browser_media call said; action "toggle" plays a paused player and pauses a
// playing one.
type mediaReq struct {
	action  string
	seconds int
}

func mediaRequest(said string) mediaReq {
	s := rePolite.ReplaceAllString(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(said), ".!?")), "")
	secs := func(m []string) int {
		if n, ok := targets.SpokenNumber(m[1]); ok {
			return n
		}
		return 0
	}
	switch {
	case reMediaToggle.MatchString(s):
		return mediaReq{action: "toggle"}
	case reMediaPlay.MatchString(s):
		return mediaReq{action: "play"}
	case reMediaPause.MatchString(s):
		return mediaReq{action: "pause"}
	case reMediaRest.MatchString(s):
		return mediaReq{action: "restart"}
	}
	if m := reMediaMute.FindStringSubmatch(s); m != nil {
		return mediaReq{action: strings.ToLower(m[1]) + "mute"}
	}
	if m := reMediaFwd.FindStringSubmatch(s); m != nil {
		return mediaReq{action: "skip_forward", seconds: secs(m)}
	}
	// A bare "go back" is the page's; the video's names seconds or says skip/rewind.
	if m := reMediaBack.FindStringSubmatch(s); m != nil && (m[1] != "" || !strings.HasPrefix(strings.ToLower(s), "go")) {
		return mediaReq{action: "skip_back", seconds: secs(m)}
	}
	return mediaReq{}
}

var mediaShown = map[string]string{"play": "Playing", "pause": "Paused", "mute": "Muted", "unmute": "Unmuted",
	"skip_forward": "Skipped ahead", "skip_back": "Skipped back", "restart": "From the start"}

// directMedia carries out a mediaRequest; ok false passes it on (nothing said, or no video or
// audio to control: "stop" then passes through to the next handler or model).
func (t *Tools) directMedia(ctx context.Context, said string) (map[string]any, bool) {
	r := mediaRequest(said)
	if r.action == "" || !node.Enabled(ctx, ToolMedia) {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	action := r.action
	if action == "toggle" {
		st, err := t.media(ctx, map[string]any{"action": "status"})
		if err != nil {
			return t.mediaFail(err)
		}
		m, _ := st["media"].(map[string]any)
		action = "pause"
		if m["paused"] == true {
			action = "play"
		}
	}
	args := map[string]any{"action": action}
	if r.seconds > 0 {
		args["seconds"] = r.seconds
	}
	out, err := t.media(ctx, args)
	if err != nil {
		return t.mediaFail(err)
	}
	out["success"], out[node.IslandKey], out[node.IslandOnlyKey] = true, mediaShown[action], true
	return out, true
}

func (t *Tools) mediaFail(err error) (map[string]any, bool) {
	if t.Log != nil {
		t.Log.Info("browser: media control failed", "code", code(err), "err", message(err))
	}
	if c := code(err); c == "no_media" || c == "tab_not_found" {
		return nil, false
	}
	return t.hintFail(err), true
}
