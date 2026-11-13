package cc

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	dbridge "github.com/alexberardi/jarvis-server/internal/modules/cc/discord"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

type discordSrc struct{ cfgs []dbridge.Config }

func (s discordSrc) Configs(context.Context) ([]dbridge.Config, error) { return s.cfgs, nil }

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// bridgeOnline pretends the household's bot connected (no real Discord).
func bridgeOnline(ce *chatEnv) {
	ce.m.discord.SetSource(discordSrc{[]dbridge.Config{{HouseholdID: voiceHH, Token: "t", GuildID: "100000000000000001", ChannelID: "100000000000000002"}}})
	ce.m.discord.SetConnector(func(context.Context, dbridge.Config, *dbridge.Bridge, *slog.Logger) (io.Closer, error) {
		return nopCloser{}, nil
	})
	ce.m.discord.Reconcile(context.Background())
}

func TestDiscordLinkFlowOverTheApp(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	base := "/api/v0/mobile/household/" + voiceHH + "/discord"
	tok := bearer("tok-7")

	st := ce.do("GET", base, nil, tok).want(200).json()
	if st["enabled"] != false || st["online"] != false || st["linked"] != false {
		t.Fatalf("fresh status %v", st)
	}
	// The bot is not running: no code, and the message says what to do.
	r := ce.do("POST", base+"/link-code", map[string]any{"node_id": ce.node.id}, tok).want(409)
	if !strings.Contains(string(r.body), "not running") {
		t.Fatalf("offline body %s", r.body)
	}

	bridgeOnline(ce)
	if st := ce.do("GET", base, nil, tok).want(200).json(); st["online"] != true {
		t.Fatalf("online status %v", st)
	}
	ce.do("POST", base+"/link-code", map[string]any{"node_id": "no-such-node"}, tok).want(404)
	code := ce.do("POST", base+"/link-code", map[string]any{"node_id": ce.node.id}, tok).want(200).json()
	if len(code["code"].(string)) != 8 || code["expires_in"] != 600.0 {
		t.Fatalf("code %v", code)
	}
	// With one node the app need not name it. A new code replaces the earlier one.
	if old := code["code"].(string); old == "" {
		t.Fatal("no code")
	}
	code = ce.do("POST", base+"/link-code", map[string]any{}, tok).want(200).json()

	// Redeem the code as the bot would, then the app sees the link.
	hh, uid, node, ok, err := ce.m.discordCodes.Redeem(code["code"].(string), "alex#1")
	if err != nil || !ok || hh != voiceHH || uid != 7 || node != ce.node.id {
		t.Fatalf("redeem: %v %v %v %v %v", hh, uid, node, ok, err)
	}
	if err := ce.m.discordStore.Put(context.Background(), dbridge.Link{HouseholdID: hh, DiscordUserID: "alex#1", UserID: uid, NodeID: node}); err != nil {
		t.Fatal(err)
	}
	st = ce.do("GET", base, nil, tok).want(200).json()
	if st["linked"] != true || st["voice_replies"] != "auto" || st["proactive"] != false {
		t.Fatalf("linked status %v", st)
	}

	// Preferences: validated, and only for the caller's own link.
	ce.do("PUT", base+"/me", map[string]any{"voice_replies": "loud"}, tok).want(400)
	ce.do("PUT", base+"/me", map[string]any{"node_id": "elsewhere"}, tok).want(404)
	st = ce.do("PUT", base+"/me", map[string]any{"proactive": true, "voice_replies": "never"}, tok).want(200).json()
	if st["proactive"] != true || st["voice_replies"] != "never" {
		t.Fatalf("prefs %v", st)
	}
	ce.do("PUT", base+"/me", map[string]any{"proactive": true}, bearer("tok-8")).want(404) // no link of their own

	// A reminder reaches the DMs only because they opted in (the bot is a fake here, so the
	// store decides; the bridge test covers the sending).
	if pro, _ := ce.m.discordStore.Proactive(context.Background(), 7); len(pro) != 1 {
		t.Fatalf("opted-in links %v", pro)
	}
	ce.do("DELETE", base+"/me", nil, tok).want(200)
	if st := ce.do("GET", base, nil, tok).want(200).json(); st["linked"] != false {
		t.Fatalf("after unlink %v", st)
	}
}

func TestDiscordRoutesNeedMembership(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	for _, c := range []struct{ method, path string }{
		{"GET", "/discord"}, {"POST", "/discord/link-code"}, {"PUT", "/discord/me"}, {"DELETE", "/discord/me"},
	} {
		code := ce.do(c.method, "/api/v0/mobile/household/someone-elses/"+c.path[1:], map[string]any{}, bearer("tok-7")).status
		if code != 403 && code != 404 {
			t.Errorf("%s %s by a non member: %d", c.method, c.path, code)
		}
	}
}

// The bot token is entered through the household settings, write only.
func TestDiscordSettingsFromTheApp(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	base := "/api/v0/mobile/household/" + voiceHH + "/settings/"
	adm := bearer("tok-7")
	ce.auth.mu.Lock()
	ce.auth.roles[voiceHH][7] = authn.RoleAdmin
	ce.auth.mu.Unlock()
	r := ce.do("PUT", base+"discord.bot_token", map[string]any{"value": "MTx.secret.token"}, adm).want(200).json()
	if r["value"] != "********" {
		t.Fatalf("the token was echoed: %v", r)
	}
	ce.do("PUT", base+"discord.guild_id", map[string]any{"value": "100000000000000001"}, adm).want(200)
	ce.do("PUT", base+"discord.guild_id", map[string]any{"value": "#jarvis"}, adm).want(400) // not an ID
	ce.do("PUT", base+"discord.channel_id", map[string]any{"value": "100000000000000002"}, adm).want(200)
	ce.do("PUT", base+"discord.enabled", map[string]any{"value": true}, adm).want(200)

	got := ce.do("GET", "/api/v0/mobile/household/"+voiceHH+"/settings", nil, adm).want(200).json()["settings"].(map[string]any)
	if got["discord.bot_token"] != "********" || got["discord.enabled"] != true || got["discord.channel_id"] != "100000000000000002" {
		t.Fatalf("settings read back: %v", got)
	}
	// The real source now sees a complete config, with the real token.
	cfgs, err := (dbridge.SettingsSource{Settings: ce.m.settings}).Configs(context.Background())
	if err != nil || len(cfgs) != 1 || cfgs[0].Token != "MTx.secret.token" || cfgs[0].HouseholdID != voiceHH {
		t.Fatalf("configs %+v %v", cfgs, err)
	}
}

type dmOut struct {
	mu  sync.Mutex
	dms []string
}

func (o *dmOut) React(context.Context, string, string, string) error { return nil }
func (o *dmOut) Typing(context.Context, string) error                { return nil }
func (o *dmOut) Send(context.Context, string, string) error          { return nil }
func (o *dmOut) SendVoice(context.Context, string, []byte, float64) error {
	return nil
}
func (o *dmOut) StartThread(context.Context, string, string, string) (string, error) { return "", nil }
func (o *dmOut) SendDM(_ context.Context, u, t string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dms = append(o.dms, u+": "+t)
	return nil
}

// A reminder a node pushes reaches Discord only for a user who opted in, only as a DM, and only
// for reminder-like categories.
func TestDiscordReminderIsOptIn(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	out := &dmOut{}
	ce.m.discord.SetSource(discordSrc{[]dbridge.Config{{HouseholdID: voiceHH, Token: "t", GuildID: "100000000000000001", ChannelID: "100000000000000002"}}})
	ce.m.discord.SetConnector(func(_ context.Context, _ dbridge.Config, b *dbridge.Bridge, _ *slog.Logger) (io.Closer, error) {
		b.Out = out
		return nopCloser{}, nil
	})
	ce.m.discord.Reconcile(context.Background())
	ctx := context.Background()
	if err := ce.m.discordStore.Put(ctx, dbridge.Link{HouseholdID: voiceHH, DiscordUserID: "alex#1", UserID: 7, NodeID: ce.node.id}); err != nil {
		t.Fatal(err)
	}
	push := func(category string, user any) {
		t.Helper()
		body := map[string]any{"title": "Reminder", "body": "take the bins out", "category": category}
		if user != nil {
			body["user_id"] = user
		}
		ce.do("POST", "/api/v0/node/push-notification", body, ce.node.h()).want(200)
	}
	dms := func() []string { out.mu.Lock(); defer out.mu.Unlock(); return append([]string(nil), out.dms...) }

	push("reminder", 7) // linked but never opted in
	time.Sleep(100 * time.Millisecond)
	if len(dms()) != 0 {
		t.Fatalf("a user who did not opt in got %v", dms())
	}
	on := true
	if _, err := ce.m.discordStore.SetPrefs(ctx, voiceHH, 7, dbridge.Prefs{Proactive: &on}); err != nil {
		t.Fatal(err)
	}
	push("alert", 7) // not a reminder
	push("reminder", nil)
	push("reminder", 8) // someone else
	time.Sleep(100 * time.Millisecond)
	if len(dms()) != 0 {
		t.Fatalf("sent for the wrong category or person: %v", dms())
	}
	push("reminder", 7)
	for i := 0; i < 100 && len(dms()) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := dms(); len(got) != 1 || got[0] != "alex#1: Reminder: take the bins out" {
		t.Fatalf("opted in user: %v", got)
	}
}

// A big noisy phone-sized photo, shrunk the way the bridge does, is accepted by the chat's own
// image check (at most 2 MiB, JPEG, PNG or WebP).
func TestDiscordPhotoFitsTheChatLimit(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(r.IntN(256)), byte(r.IntN(256)), byte(r.IntN(256)), 255
	}
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	if raw.Len() <= chatMaxImageBytes {
		t.Fatalf("test photo is only %d bytes, not bigger than the limit", raw.Len())
	}
	small, ok := recipes.PrepareVisionPhoto(raw.Bytes())
	if !ok || len(small) > chatMaxImageBytes {
		t.Fatalf("shrunk photo: ok=%v, %d bytes (limit %d)", ok, len(small), chatMaxImageBytes)
	}
	parsed, err := parseChatImages([]any{map[string]any{"mime": "image/jpeg", "data": base64.StdEncoding.EncodeToString(small)}})
	if err != nil || len(parsed) != 1 {
		t.Fatalf("the chat refused the shrunk photo: %v", err)
	}
	if _, ok := recipes.PrepareVisionPhoto([]byte("not an image")); ok {
		t.Fatal("garbage accepted as a photo")
	}
}
