package cc

import (
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

func playingReport(title string) map[string]any {
	return map[string]any{"now": map[string]any{"source": "pkg:radio", "name": "Radio", "updated_at": "2026-11-07T12:00:00Z",
		"state": map[string]any{"state": "playing", "media_title": title, "media_artist": "", "media_album": "", "context": "",
			"actions": []string{"pause", "volume"}, "volume": 40}}}
}

func TestPlaybackReportsAndReads(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	outsider := e.auth.addUser(2, "hh2", authn.RoleMember)
	kitchen := e.createNode("kitchen", "hh1")
	e.createNode("office", "hh1")
	other := e.createNode("other", "hh2")

	// Who may report and read.
	e.do("POST", "/api/v0/node/playback", playingReport("x"), nil).want(400) // node routes: a missing key is a validation error
	e.do("GET", "/api/v0/mobile/playback?household_id=hh1", nil, nil).want(401)
	e.do("GET", "/api/v0/mobile/playback", nil, bearer(member)).want(422)
	e.do("GET", "/api/v0/mobile/playback?household_id=hh1", nil, bearer(outsider)).detail(403, "User is not a member of this household")

	// Nothing plays yet.
	if r := e.do("GET", "/api/v0/mobile/playback?household_id=hh1", nil, bearer(member)).want(200).json(); len(r["playing"].([]any)) != 0 {
		t.Fatalf("got %v", r)
	}
	e.do("POST", "/api/v0/node/playback", playingReport("Jazz FM"), kitchen.h()).want(200)
	e.do("POST", "/api/v0/node/playback", playingReport("Other household"), other.h()).want(200)

	r := e.do("GET", "/api/v0/mobile/playback?household_id=hh1", nil, bearer(member)).want(200).json()
	playing := r["playing"].([]any)
	if len(playing) != 1 {
		t.Fatalf("playing = %v", playing)
	}
	p := playing[0].(map[string]any)
	st := p["state"].(map[string]any)
	if p["node_id"] != "kitchen" || p["name"] != "Radio" || st["media_title"] != "Jazz FM" || st["volume"] != 40.0 {
		t.Fatalf("entry = %v", p)
	}
	// The other household's node never shows here, and theirs shows only to them.
	if r := e.do("GET", "/api/v0/mobile/playback?household_id=hh2", nil, bearer(outsider)).want(200).json(); len(r["playing"].([]any)) != 1 {
		t.Fatalf("hh2 = %v", r)
	}

	// Stopped (null, or idle) clears it at once.
	e.do("POST", "/api/v0/node/playback", map[string]any{"now": nil}, kitchen.h()).want(200)
	if r := e.do("GET", "/api/v0/mobile/playback?household_id=hh1", nil, bearer(member)).want(200).json(); len(r["playing"].([]any)) != 0 {
		t.Fatalf("after stop = %v", r)
	}
	idle := playingReport("x")
	idle["now"].(map[string]any)["state"].(map[string]any)["state"] = "idle"
	e.do("POST", "/api/v0/node/playback", playingReport("Back"), kitchen.h()).want(200)
	e.do("POST", "/api/v0/node/playback", idle, kitchen.h()).want(200)
	if r := e.do("GET", "/api/v0/mobile/playback?household_id=hh1", nil, bearer(member)).want(200).json(); len(r["playing"].([]any)) != 0 {
		t.Fatalf("after idle = %v", r)
	}
}

func TestPlaybackExpiresWithoutRefresh(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	kitchen := e.createNode("kitchen", "hh1")
	e.do("POST", "/api/v0/node/playback", playingReport("Jazz FM"), kitchen.h()).want(200)
	e.advance(playbackTTL - time.Second)
	if r := e.do("GET", "/api/v0/mobile/playback?household_id=hh1", nil, bearer(member)).want(200).json(); len(r["playing"].([]any)) != 1 {
		t.Fatalf("expired early: %v", r)
	}
	e.advance(2 * time.Second) // the node went quiet: crashed or offline (P14)
	if r := e.do("GET", "/api/v0/mobile/playback?household_id=hh1", nil, bearer(member)).want(200).json(); len(r["playing"].([]any)) != 0 {
		t.Fatalf("stale report still shown: %v", r)
	}
}

func TestPlaybackReportIsChecked(t *testing.T) {
	e := newEnv(t)
	kitchen := e.createNode("kitchen", "hh1")
	for name, mutate := range map[string]func(map[string]any){
		"unknown state":  func(s map[string]any) { s["state"] = "buffering" },
		"unknown action": func(s map[string]any) { s["actions"] = []string{"rewind"} },
		"bad repeat":     func(s map[string]any) { s["repeat"] = "sometimes" },
		"long title":     func(s map[string]any) { s["media_title"] = string(make([]byte, 501)) },
	} {
		body := playingReport("x")
		mutate(body["now"].(map[string]any)["state"].(map[string]any))
		if code := e.do("POST", "/api/v0/node/playback", body, kitchen.h()).status; code != 422 {
			t.Errorf("%s: %d, want 422", name, code)
		}
	}
	noSource := playingReport("x")
	noSource["now"].(map[string]any)["source"] = ""
	e.do("POST", "/api/v0/node/playback", noSource, kitchen.h()).want(422)
}

func TestPlaybackControlRelaysAndReturnsTheReadBack(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	outsider := e.auth.addUser(2, "hh2", authn.RoleMember)
	kitchen := e.createNode("kitchen", "hh1")
	body := map[string]any{"node_id": "kitchen", "action": "volume", "volume": 30}

	e.do("POST", "/api/v0/mobile/playback/control", body, nil).want(401)
	e.do("POST", "/api/v0/mobile/playback/control", body, bearer(outsider)).want(403)
	e.do("POST", "/api/v0/mobile/playback/control", map[string]any{"node_id": "nope", "action": "pause"}, bearer(member)).want(404)
	e.do("POST", "/api/v0/mobile/playback/control", map[string]any{"node_id": "kitchen", "action": "rewind"}, bearer(member)).want(422)
	e.do("POST", "/api/v0/mobile/playback/control", map[string]any{"node_id": "kitchen", "action": "pause"}, bearer(member)).detail(409, "Node is offline")

	e.do("POST", "/api/v0/admin/nodes/heartbeat", nil, kitchen.h()).want(200)
	c := e.dialNode(kitchen)
	c.subscribe("jarvis/nodes/kitchen/commands")
	done := make(chan map[string]any, 1)
	go func() { done <- e.do("POST", "/api/v0/mobile/playback/control", body, bearer(member)).want(200).json() }()

	// The node gets the command, with an expiry, and answers with a report.
	verb, details := command(t, c.next())
	if verb != "playback_control" || details["action"] != "volume" || details["volume"] != 30.0 || details["expires_at"] == nil {
		t.Fatalf("command %s %v", verb, details)
	}
	e.do("POST", "/api/v0/node/playback", playingReport("Jazz FM"), kitchen.h()).want(200)
	select {
	case r := <-done:
		p := r["playing"].(map[string]any)
		if p["node_id"] != "kitchen" || p["state"].(map[string]any)["volume"] != 40.0 {
			t.Fatalf("control = %v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control didn't return after the node's report")
	}
}

func TestPlaybackCover(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	outsider := e.auth.addUser(2, "hh2", authn.RoleMember)
	kitchen := e.createNode("kitchen", "hh1")
	jpeg := hdr{"Content-Type": "image/jpeg"}
	for k, v := range kitchen.h() {
		jpeg[k] = v
	}
	get := func(tok string) *resp {
		return e.do("GET", "/api/v0/mobile/playback/cover?node_id=kitchen", nil, bearer(tok))
	}
	withCover := func(title, cover string) map[string]any {
		r := playingReport(title)
		r["cover"] = cover
		return r
	}

	// Uploads: a node key, a valid key and image type, at most 2 MiB.
	e.do("PUT", "/api/v0/node/playback/cover?key=abc", "x", hdr{"Content-Type": "image/jpeg"}).want(400)
	e.do("PUT", "/api/v0/node/playback/cover?key=a/b", "x", jpeg).want(422)
	e.do("PUT", "/api/v0/node/playback/cover?key=abc", "x", kitchen.h()).want(415)
	e.do("PUT", "/api/v0/node/playback/cover?key=abc", "", jpeg).want(422)
	e.do("PUT", "/api/v0/node/playback/cover?key=abc", strings.Repeat("x", 2<<20+1), jpeg).want(413)
	e.do("POST", "/api/v0/node/playback", withCover("x", "a b"), kitchen.h()).want(422)

	// Nothing until the report names the uploaded cover.
	e.do("PUT", "/api/v0/node/playback/cover?key=abc", "cover-one", jpeg).want(204)
	get(member).want(404)
	e.do("POST", "/api/v0/node/playback", withCover("One", "abc"), kitchen.h()).want(200)
	r := get(member).want(200)
	if string(r.body) != "cover-one" || r.header.Get("Content-Type") != "image/jpeg" || r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("cover = %q %v", r.body, r.header)
	}
	if p := e.do("GET", "/api/v0/mobile/playback?household_id=hh1", nil, bearer(member)).want(200).json()["playing"].([]any); p[0].(map[string]any)["cover"] != "abc" {
		t.Fatalf("entry = %v", p)
	}
	get(outsider).detail(403, "User is not a member of this household")
	e.do("GET", "/api/v0/mobile/playback/cover?node_id=nope", nil, bearer(member)).want(404)
	e.do("GET", "/api/v0/mobile/playback/cover?node_id=kitchen", nil, nil).want(401)

	// A new track without a cover: the old one is not shown for it.
	e.do("POST", "/api/v0/node/playback", playingReport("Two"), kitchen.h()).want(200)
	get(member).want(404)
	// Reported before its upload: shown once the upload lands.
	e.do("POST", "/api/v0/node/playback", withCover("Three", "def"), kitchen.h()).want(200)
	get(member).want(404)
	e.do("PUT", "/api/v0/node/playback/cover?key=def", "cover-three", jpeg).want(204)
	if string(get(member).want(200).body) != "cover-three" {
		t.Fatal("cover three not served")
	}
	// Stopped, or stale: gone with the record.
	e.do("POST", "/api/v0/node/playback", map[string]any{"now": nil}, kitchen.h()).want(200)
	get(member).want(404)
	e.do("PUT", "/api/v0/node/playback/cover?key=def", "cover-three", jpeg).want(204)
	e.do("POST", "/api/v0/node/playback", withCover("Three", "def"), kitchen.h()).want(200)
	e.advance(playbackTTL + time.Second)
	get(member).want(404)
}
