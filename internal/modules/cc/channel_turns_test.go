package cc

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// A channel's daemon, on a node of the household, posts what an account on its service sent.
// The whole path with the scripted model: a stranger gets nothing, the link command links, a
// message gets Jarvis's answer, a conversation is kept per place, and the provider is listed
// in the app once its daemon has called.
func TestChannelDaemonTurns(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	const base = "/api/v0/node/channels/testchat"
	app := "/api/v0/mobile/household/" + voiceHH + "/channels"
	tok, node := bearer("tok-7"), ce.node.h()
	post := func(route string, body map[string]any) map[string]any {
		t.Helper()
		return ce.do("POST", base+"/"+route, body, node).want(200).json()
	}
	msg := func(who, place, text string) map[string]any {
		t.Helper()
		return post("message", map[string]any{"external_user_id": who, "place": place, "text": text})
	}
	cmd := func(who, name string, args map[string]any) string {
		t.Helper()
		out := post("command", map[string]any{"external_user_id": who, "place": "dm:1", "name": name, "args": args})
		if out["private"] != true {
			t.Fatalf("%s: not private: %v", name, out)
		}
		return out["text"].(string)
	}

	// A stranger: no answer, and nothing reaches the model.
	if out := msg("alex#1", "dm:1", "hello"); out["linked"] != false || out["reply"] != nil {
		t.Fatalf("stranger got %v", out)
	}
	if got := cmd("alex#1", "status", nil); !strings.Contains(got, "don't know this account") {
		t.Fatalf("stranger's status: %q", got)
	}

	// The daemon's first call made the provider known, and running, in the app.
	list := ce.do("GET", app, nil, tok).want(200).json()["channels"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["provider"] != "testchat" || list[0].(map[string]any)["online"] != true {
		t.Fatalf("app list %v", list)
	}

	// Link with a code from the app; a wrong code says so.
	if got := cmd("alex#1", "link", map[string]any{"code": "AAAAAAAA"}); !strings.Contains(got, "wrong or has expired") {
		t.Fatalf("wrong code: %q", got)
	}
	code := ce.do("POST", app+"/testchat/link-code", map[string]any{}, tok).want(200).json()["code"].(string)
	if got := cmd("alex#1", "link", map[string]any{"code": code}); !strings.HasPrefix(got, "Linked.") {
		t.Fatalf("link: %q", got)
	}
	if got := cmd("alex#1", "status", nil); !strings.Contains(got, "Node: "+ce.node.id) || !strings.Contains(got, "Reminder messages: off") {
		t.Fatalf("status: %q", got)
	}

	// A message is a chat turn as that user; the next one in the same place carries on.
	ce.eng.say("Café opens at noon today.")
	if out := msg("alex#1", "dm:1", "when does the café open"); out["linked"] != true || out["reply"] != "Café opens at noon today." {
		t.Fatalf("turn: %v", out)
	}
	first := ce.m.channelConvs.get(voiceHH + "\x00testchat\x00alex#1\x00dm:1")
	if !strings.HasPrefix(first, "mobile-") {
		t.Fatalf("no conversation kept: %q", first)
	}
	ce.eng.say("Yes.")
	msg("alex#1", "dm:1", "today?")
	if got := ce.m.channelConvs.get(voiceHH + "\x00testchat\x00alex#1\x00dm:1"); got != first {
		t.Fatalf("the second message started another conversation: %q, was %q", got, first)
	}
	// Another place is another conversation; "new" forgets this one.
	ce.eng.say("Hello.")
	msg("alex#1", "ch:9", "hi")
	if other := ce.m.channelConvs.get(voiceHH + "\x00testchat\x00alex#1\x00ch:9"); other == "" || other == first {
		t.Fatalf("places share a conversation: %q", other)
	}
	if got := cmd("alex#1", "new", nil); got != "Started a fresh conversation." || ce.m.channelConvs.get(voiceHH+"\x00testchat\x00alex#1\x00dm:1") != "" {
		t.Fatalf("new: %q", got)
	}

	// Preferences by command.
	if got := cmd("alex#1", "notify", map[string]any{"state": "on"}); got != "I'll send your reminders here." {
		t.Fatalf("notify: %q", got)
	}
	if got := cmd("alex#1", "voice", map[string]any{"mode": "loud"}); !strings.Contains(got, "auto, always or never") {
		t.Fatalf("voice: %q", got)
	}
	if got := cmd("alex#1", "node", map[string]any{"name": "attic"}); !strings.Contains(got, "Yours are: "+ce.node.id) {
		t.Fatalf("node: %q", got)
	}
	if e := ce.do("GET", app, nil, tok).want(200).json()["channels"].([]any)[0].(map[string]any); e["linked"] != true || e["proactive"] != true {
		t.Fatalf("app entry after commands: %v", e)
	}

	// A photo that isn't one: the sender is told, and no turn runs.
	bad := post("message", map[string]any{"external_user_id": "alex#1", "place": "dm:1",
		"images": []any{map[string]any{"mime": "image/jpeg", "data": base64.StdEncoding.EncodeToString([]byte("not a photo"))}}})
	if !strings.Contains(bad["failed"].(string), "couldn't read that image") {
		t.Fatalf("bad image: %v", bad)
	}

	// Reminders: a node's pushed reminder for this user is queued for the channel because they
	// opted in ("notify on" above); the daemon takes it, and it goes once acknowledged. An alert
	// is not a reminder, and a reminder for someone who hasn't opted in is not queued.
	push := func(category string, user int) {
		t.Helper()
		ce.do("POST", "/api/v0/node/push-notification", map[string]any{"title": "Reminder", "body": "take the bins out",
			"category": category, "user_id": user}, node).want(200)
	}
	push("alert", 7)
	push("reminder", 8)
	if out := post("outbox", nil); len(out["messages"].([]any)) != 0 {
		t.Fatalf("outbox before any reminder for an opted-in user: %v", out)
	}
	push("reminder", 7)
	out := post("outbox", nil)["messages"].([]any)
	if len(out) != 1 {
		t.Fatalf("outbox: %v", out)
	}
	m := out[0].(map[string]any)
	if m["external_user_id"] != "alex#1" || m["text"] != "Reminder: take the bins out" {
		t.Fatalf("reminder: %v", m)
	}
	if left := post("outbox", map[string]any{"ack": []any{m["id"]}})["messages"].([]any); len(left) != 0 {
		t.Fatalf("after the ack: %v", left)
	}
	cmd("alex#1", "notify", map[string]any{"state": "off"})
	push("reminder", 7)
	if out := post("outbox", nil); len(out["messages"].([]any)) != 0 {
		t.Fatalf("a reminder after opting out: %v", out)
	}
}

// The daemon's routes are a node's: no key, a bad provider name and a body without a sender
// are refused, and another household's node speaks only for its own household's links.
func TestChannelDaemonRoutesAreTheHouseholdsNode(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	body := map[string]any{"external_user_id": "alex#1", "place": "dm:1", "text": "hi"}
	ce.do("POST", "/api/v0/node/channels/testchat/message", body, nil).want(400) // no key: the node routes' usual answer
	ce.do("POST", "/api/v0/node/channels/testchat/message", body, hdr{"X-API-Key": ce.node.id + ":wrong"}).want(401)
	ce.do("POST", "/api/v0/node/channels/Not%20One/message", body, ce.node.h()).want(404)
	ce.do("POST", "/api/v0/node/channels/testchat/message", map[string]any{"text": "hi"}, ce.node.h()).want(400)
	ce.do("POST", "/api/v0/node/channels/testchat/message", map[string]any{"external_user_id": "a", "place": "p"}, ce.node.h()).want(400)

	// alex#1 is linked in this household.
	code := ce.do("POST", "/api/v0/mobile/household/"+voiceHH+"/channels/testchat/link-code", map[string]any{}, bearer("tok-7")).want(200).json()["code"].(string)
	ce.do("POST", "/api/v0/node/channels/testchat/command", map[string]any{"external_user_id": "alex#1", "place": "dm:1",
		"name": "link", "args": map[string]any{"code": code}}, ce.node.h()).want(200)

	// The same account id, from another household's node, is a stranger there; and that node
	// cannot redeem a code made for this household.
	other := ce.createNode("far-node", "another-household")
	if out := ce.do("POST", "/api/v0/node/channels/testchat/message", body, other.h()).want(200).json(); out["linked"] != false {
		t.Fatalf("another household's node reached the link: %v", out)
	}
	code = ce.do("POST", "/api/v0/mobile/household/"+voiceHH+"/channels/testchat/link-code", map[string]any{}, bearer("tok-7")).want(200).json()["code"].(string)
	out := ce.do("POST", "/api/v0/node/channels/testchat/command", map[string]any{"external_user_id": "mallory", "place": "dm:1",
		"name": "link", "args": map[string]any{"code": code}}, other.h()).want(200).json()
	if !strings.Contains(out["text"].(string), "wrong or has expired") {
		t.Fatalf("another household's node redeemed the code: %v", out)
	}
}
