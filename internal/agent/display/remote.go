package display

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/devicetypes"
)

// A remote destination is one of the household's imported devices whose catalogue type is media
// (a TV, a speaker, a cast target): the display drives it through the household device API, and
// the device's manager does the backend work. Nothing here names a backend.

// FindDestination is the media device named by id, entity id or name (case-insensitive).
func FindDestination(ctx context.Context, cc *node.CC, name string) (map[string]any, error) {
	var devices []map[string]any
	if err := cc.Do(ctx, "GET", "/api/v0/node/devices", nil, &devices); err != nil {
		return nil, fmt.Errorf("destination unavailable; choose local playback: %w", err)
	}
	var found map[string]any
	for _, d := range devices {
		domain, _ := d["domain"].(string)
		if t, _ := devicetypes.Lookup(domain); t.ControlType != "media" {
			continue
		}
		label, _ := d["name"].(string)
		if d["id"] == name || d["entity_id"] == name || strings.EqualFold(label, name) {
			if found != nil {
				return nil, errors.New("destination name is ambiguous; choose it from the list")
			}
			found = d
		}
	}
	if found == nil {
		return nil, errors.New("destination unavailable; choose local playback")
	}
	return found, nil
}

// RemoteAction is a media request as a device action and its data.
func RemoteAction(r MediaRequest) (string, map[string]any, error) {
	switch r.Action {
	case "play":
		if r.Query != "" {
			return "", nil, errors.New("searches open on this display; choose local playback")
		}
		if r.URL == "" {
			return "play", nil, nil
		}
		if u, err := url.Parse(r.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "", nil, errors.New("external destinations need a direct HTTP(S) media link")
		}
		return "play_media", map[string]any{"url": r.URL}, nil
	case "resume":
		return "play", nil, nil
	case "pause", "stop":
		return r.Action, nil, nil
	case "seek":
		if r.Seconds < 0 || r.Seconds > 864000 {
			return "", nil, errors.New("invalid seek position")
		}
		return "seek", map[string]any{"seconds": r.Seconds}, nil
	}
	return "", nil, errors.New("unsupported media action")
}

// ControlRemote runs r on the remote destination; status is that device's playback.
func ControlRemote(ctx context.Context, cc *node.CC, r MediaRequest) (map[string]any, error) {
	d, err := FindDestination(ctx, cc, r.Destination)
	if err != nil {
		return nil, err
	}
	path := "/api/v0/node/devices/" + url.PathEscape(fmt.Sprint(d["id"]))
	if r.Action == "status" {
		var s map[string]any
		if err := cc.Do(ctx, "GET", path+"/state", nil, &s); err != nil {
			return nil, fmt.Errorf("destination unavailable: %w", err)
		}
		return RemoteStatus(s), nil
	}
	action, data, err := RemoteAction(r)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := cc.Do(ctx, "POST", path+"/control", map[string]any{"action": action, "data": data}, &out); err != nil {
		return nil, fmt.Errorf("destination unavailable; choose local playback: %w", err)
	}
	if out["success"] != true {
		msg, _ := out["error"].(string)
		if msg == "" {
			msg = "destination rejected playback"
		}
		return nil, errors.New(msg)
	}
	return map[string]any{"success": true, "destination": d["id"]}, nil
}

// RemoteStatus is a media device's state answer in the shape of local playback status: which
// controls it offers comes from the device's own features, never from local playback.
func RemoteStatus(s map[string]any) map[string]any {
	state, _ := s["state"].(map[string]any)
	hints, _ := s["ui_hints"].(map[string]any)
	value, _ := state["state"].(string)
	var features []string
	if f, ok := hints["features"].([]any); ok {
		for _, x := range f {
			if v, ok := x.(string); ok {
				features = append(features, v)
			}
		}
	}
	out := map[string]any{"active": value == "playing" || value == "paused" || value == "buffering",
		"kind": "remote", "paused": value == "paused", "state": value, "features": features,
		"controllable": slices.Contains(features, "pause") || slices.Contains(features, "play")}
	if e, ok := s["error"].(string); ok && e != "" {
		out["error"] = e
	}
	for _, k := range []string{"position", "duration", "media_title"} {
		if v, ok := state[k]; ok {
			out[k] = v
		}
	}
	return out
}
