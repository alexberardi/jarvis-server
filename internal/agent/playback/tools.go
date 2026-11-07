package playback

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// ToolName is the built-in transport command (P5): "pause the music", "next", "turn it down",
// "what's playing" go to whatever plays on this node, whichever package or player it is.
const ToolName = "playback_control"

// Actions the tool takes: the SDK's PLAYBACK_ACTIONS plus status.
var Actions = []string{"status", "play", "pause", "next", "previous", "volume", "seek", "shuffle", "repeat", "like"}

var spec = node.Spec{
	Name: ToolName,
	Description: "Control whatever is playing on this node, whichever app or service plays it: pause, resume (play), " +
		"skip (next), go back (previous), set the volume, seek, shuffle, repeat, like the track, or say what's playing " +
		"(status). To start something new by name, use the music service's own command instead.",
	Params: []node.Param{
		{Name: "action", Type: "string", Required: true, Enum: Actions},
		{Name: "volume", Type: "int", Description: "For volume: 0-100"},
		{Name: "position", Type: "float", Description: "For seek: seconds from the start of the track"},
	},
	Keywords: []string{"pause", "resume", "skip", "next song", "previous", "volume", "what's playing", "shuffle", "repeat"},
	Examples: []node.Example{
		{VoiceCommand: "pause the music", Params: map[string]any{"action": "pause"}, Primary: true},
		{VoiceCommand: "skip this song", Params: map[string]any{"action": "next"}},
		{VoiceCommand: "turn the music down to 30", Params: map[string]any{"action": "volume", "volume": 30}},
		{VoiceCommand: "what song is this", Params: map[string]any{"action": "status"}},
	},
	RunWithoutConfirmation: true,
}

// Tools offers playback_control while the node has any source (a music package installed,
// the display's player); with none it offers nothing.
type Tools struct {
	Hub *Hub
}

func (t *Tools) Available(context.Context) []node.ToolDef {
	if t.Hub == nil || t.Hub.Sources == nil || len(t.Hub.Sources()) == 0 {
		return nil
	}
	return []node.ToolDef{spec.Render()}
}

func (t *Tools) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != ToolName {
		return nil, false
	}
	action, _ := args["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	if !slices.Contains(Actions, action) {
		return fail(fmt.Sprintf("Unknown action %q.", action)), true
	}
	shown := t.Hub.Shown()
	if shown == nil {
		return fail("Nothing is playing here."), true
	}
	if action == "status" {
		return result(shown, describe(shown)), true
	}
	if !slices.Contains(shown.State.Actions, action) {
		return fail(fmt.Sprintf("%s can't %s.", shown.Name, verb(action))), true
	}
	ctrl := map[string]any{}
	switch action {
	case "volume":
		v, ok := number(args["volume"])
		if !ok {
			return fail("Say what volume, from 0 to 100."), true
		}
		ctrl["volume"] = max(0, min(100, int(v))) // clamped, never rejected
	case "seek":
		p, ok := number(args["position"])
		if !ok || p < 0 {
			return fail("Say where to seek to, in seconds."), true
		}
		ctrl["position"] = p
	}
	now, err := t.Hub.Control(ctx, action, ctrl)
	if errors.Is(err, ErrNothingPlaying) {
		return fail("Nothing is playing here."), true
	}
	if err != nil {
		return fail(fmt.Sprintf("%s didn't %s: %v", shown.Name, verb(action), err)), true
	}
	if now == nil {
		return map[string]any{"success": true, "message": "Stopped."}, true
	}
	return result(now, done(action, now)), true
}

func fail(msg string) map[string]any {
	return map[string]any{"success": false, "error": msg, "message": msg}
}

// result is the reply: the message, and what is playing as read back from the player.
func result(n *Now, msg string) map[string]any {
	out := map[string]any{"success": true, "message": msg, "source": n.Name, "state": n.State.State,
		"title": n.State.MediaTitle, "artist": n.State.MediaArtist, "album": n.State.MediaAlbum}
	if n.State.Volume != nil {
		out["volume"] = *n.State.Volume
	}
	return out
}

func describe(n *Now) string {
	what := n.State.MediaTitle
	if what == "" {
		what = "something"
	}
	if n.State.MediaArtist != "" {
		what += " by " + n.State.MediaArtist
	}
	if n.State.State == "paused" {
		return fmt.Sprintf("%s is paused on %s.", what, n.Name)
	}
	return fmt.Sprintf("%s is playing on %s.", what, n.Name)
}

func done(action string, n *Now) string {
	switch action {
	case "pause":
		return "Paused."
	case "play":
		return "Resumed."
	case "volume":
		if n.State.Volume != nil {
			return "Volume " + strconv.Itoa(*n.State.Volume) + "."
		}
		return "Volume set."
	case "next", "previous":
		return describe(n)
	}
	return "Done."
}

var verbs = map[string]string{"play": "resume", "next": "skip", "previous": "go back", "volume": "change the volume",
	"seek": "seek", "shuffle": "shuffle", "repeat": "repeat", "like": "like tracks", "pause": "pause"}

func verb(action string) string { return verbs[action] }

// number reads a model's number: a JSON number or a numeric string ("30", "45%").
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(n), "%"), 64)
		return f, err == nil
	}
	return 0, false
}
