package cc

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/channels"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// The link flow over the app's routes, for a provider core has never heard of until its daemon
// calls in (ported from #649's Discord test).
func TestChannelLinkFlowOverTheApp(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	ctx := context.Background()
	base := "/api/v0/mobile/household/" + voiceHH + "/channels"
	tok := bearer("tok-7")
	list := func() []any {
		t.Helper()
		return ce.do("GET", base, nil, tok).want(200).json()["channels"].([]any)
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("fresh list %v", got)
	}
	// Nothing has called in: no code, and the message says what to do.
	r := ce.do("POST", base+"/discord/link-code", map[string]any{"node_id": ce.node.id}, tok).want(409)
	if !strings.Contains(string(r.body), "not running") {
		t.Fatalf("offline body %s", r.body)
	}
	ce.do("POST", base+"/Not A Provider/link-code", map[string]any{}, tok).want(404)

	// The provider's daemon calls in (K3's routes do this).
	if err := ce.m.channelStore.Seen(ctx, voiceHH, "discord", ce.node.id); err != nil {
		t.Fatal(err)
	}
	got := list()
	if len(got) != 1 {
		t.Fatalf("list %v", got)
	}
	if e := got[0].(map[string]any); e["provider"] != "discord" || e["online"] != true || e["linked"] != false {
		t.Fatalf("entry %v", e)
	}
	ce.do("POST", base+"/discord/link-code", map[string]any{"node_id": "no-such-node"}, tok).want(404)
	code := ce.do("POST", base+"/discord/link-code", map[string]any{"node_id": ce.node.id}, tok).want(200).json()
	if len(code["code"].(string)) != 8 || code["expires_in"] != 600.0 {
		t.Fatalf("code %v", code)
	}
	// With one node the app need not name it. A new code replaces the earlier one.
	code = ce.do("POST", base+"/discord/link-code", map[string]any{}, tok).want(200).json()

	// Redeem the code as the daemon's link command would, then the app sees the link.
	p, ok, err := ce.m.channelCodes.Redeem(code["code"].(string), voiceHH, "discord", "alex#1")
	if err != nil || !ok || p.UserID != 7 || p.NodeID != ce.node.id {
		t.Fatalf("redeem: %+v %v %v", p, ok, err)
	}
	if err := ce.m.channelStore.Put(ctx, channels.Link{HouseholdID: voiceHH, Provider: "discord", ExternalID: "alex#1",
		UserID: p.UserID, NodeID: p.NodeID}); err != nil {
		t.Fatal(err)
	}
	if e := list()[0].(map[string]any); e["linked"] != true || e["voice_replies"] != "auto" || e["proactive"] != false || e["node_id"] != ce.node.id {
		t.Fatalf("linked entry %v", e)
	}

	// Preferences: validated, and only for the caller's own link on that provider.
	ce.do("PUT", base+"/discord/me", map[string]any{"voice_replies": "loud"}, tok).want(400)
	ce.do("PUT", base+"/discord/me", map[string]any{"node_id": "elsewhere"}, tok).want(404)
	st := ce.do("PUT", base+"/discord/me", map[string]any{"proactive": true, "voice_replies": "never"}, tok).want(200).json()
	if st["proactive"] != true || st["voice_replies"] != "never" || st["provider"] != "discord" {
		t.Fatalf("prefs %v", st)
	}
	ce.do("PUT", base+"/discord/me", map[string]any{"proactive": true}, bearer("tok-8")).want(404) // no link of their own
	ce.do("PUT", base+"/slack/me", map[string]any{"proactive": true}, tok).want(404)               // nor theirs on another service

	if pro, _ := ce.m.channelStore.Proactive(ctx, voiceHH, 7); len(pro) != 1 {
		t.Fatalf("opted-in links %v", pro)
	}
	ce.do("DELETE", base+"/discord/me", nil, tok).want(200)
	if e := list()[0].(map[string]any); e["linked"] != false {
		t.Fatalf("after unlink %v", e)
	}
}

// Another household's channels are not this member's to see or link.
func TestChannelRoutesNeedMembership(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	for _, c := range []struct{ method, path string }{
		{"GET", ""}, {"POST", "/discord/link-code"}, {"PUT", "/discord/me"}, {"DELETE", "/discord/me"},
	} {
		code := ce.do(c.method, "/api/v0/mobile/household/someone-elses/channels"+c.path, map[string]any{}, bearer("tok-7")).status
		if code != 403 && code != 404 {
			t.Errorf("%s %s by a non member: %d", c.method, c.path, code)
		}
	}
}
