package discord_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/discord"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

type fakeSource struct {
	mu   sync.Mutex
	cfgs []discord.Config
}

func (f *fakeSource) Configs(context.Context) ([]discord.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]discord.Config(nil), f.cfgs...), nil
}
func (f *fakeSource) set(c ...discord.Config) { f.mu.Lock(); f.cfgs = c; f.mu.Unlock() }

type closer struct{ closed *int }

func (c closer) Close() error { *c.closed++; return nil }

func TestManagerReconcile(t *testing.T) {
	var opened []string
	closed := 0
	src := &fakeSource{}
	m := &discord.Manager{
		Source: src, Store: newStore(t), Codes: &discord.Codes{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Connect: func(_ context.Context, c discord.Config, b *discord.Bridge, _ *slog.Logger) (io.Closer, error) {
			opened = append(opened, c.HouseholdID+":"+c.ChannelID)
			if b.ChannelID != c.ChannelID || b.HouseholdID != c.HouseholdID {
				t.Errorf("bridge not set up from the config: %+v", b)
			}
			return closer{&closed}, nil
		},
	}
	a := discord.Config{HouseholdID: "a", Token: "t", GuildID: "1", ChannelID: "10"}
	m.Reconcile(bg)
	if len(opened) != 0 {
		t.Fatal("started a bot with nothing configured")
	}
	src.set(a)
	m.Reconcile(bg)
	m.Reconcile(bg) // idempotent
	if len(opened) != 1 || !m.IsOnline("a") {
		t.Fatalf("opened %v", opened)
	}
	// A changed channel restarts it, a removed household stops it.
	a.ChannelID = "11"
	src.set(a)
	m.Reconcile(bg)
	if len(opened) != 2 || opened[1] != "a:11" || closed != 1 {
		t.Fatalf("restart: opened %v closed %d", opened, closed)
	}
	src.set()
	m.Reconcile(bg)
	if closed != 2 || m.IsOnline("a") {
		t.Fatalf("stop: closed %d online %v", closed, m.Online())
	}
}

func TestManagerRetriesAfterAFailedStart(t *testing.T) {
	tries := 0
	src := &fakeSource{}
	src.set(discord.Config{HouseholdID: "a", Token: "bad", GuildID: "1", ChannelID: "10"})
	m := &discord.Manager{Source: src, Store: newStore(t), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Connect: func(context.Context, discord.Config, *discord.Bridge, *slog.Logger) (io.Closer, error) {
			tries++
			if tries == 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return closer{new(int)}, nil
		}}
	m.Reconcile(bg)
	if m.IsOnline("a") {
		t.Fatal("online after a failed start")
	}
	m.Reconcile(bg)
	if !m.IsOnline("a") || tries != 2 {
		t.Fatalf("no retry: tries %d", tries)
	}
}

func TestSettingsSource(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	svc, err := settings.New(d, "t", discord.Definitions(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	src := discord.SettingsSource{Settings: svc}
	set := func(hh, key string, v any) {
		t.Helper()
		if err := svc.Set(ctx, key, v, settings.Scope{HouseholdID: hh}); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	set("a", discord.SettingEnabled, true)
	set("a", discord.SettingBotToken, "secret-token")
	set("a", discord.SettingGuildID, "100000000000000001")
	if cfgs, _ := src.Configs(ctx); len(cfgs) != 0 {
		t.Fatalf("a household with no channel started: %+v", cfgs)
	}
	set("a", discord.SettingChannelID, "100000000000000002")
	set("b", discord.SettingEnabled, false)
	set("b", discord.SettingBotToken, "t")
	set("b", discord.SettingGuildID, "100000000000000001")
	set("b", discord.SettingChannelID, "100000000000000002")
	cfgs, err := src.Configs(ctx)
	if err != nil || len(cfgs) != 1 || cfgs[0].HouseholdID != "a" || cfgs[0].Token != "secret-token" || cfgs[0].VoiceChannelID != "" {
		t.Fatalf("configs %+v %v", cfgs, err)
	}
	// An ID that is not an ID is refused at the door.
	if err := svc.Set(ctx, discord.SettingChannelID, "#jarvis", settings.Scope{HouseholdID: "a"}); err == nil {
		if err := svc.Validate(discord.SettingChannelID, "#jarvis"); err == nil {
			t.Fatal("a channel name was accepted as an ID")
		}
	}
}
