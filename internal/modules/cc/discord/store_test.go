package discord_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/discord"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

func newStore(t *testing.T) *discord.Store {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "cc", cc.Migrations()); err != nil {
		t.Fatal(err)
	}
	return &discord.Store{DB: d}
}

func TestStoreLinkLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, ok, err := s.ByDiscord(ctx, "hh", "d1"); ok || err != nil {
		t.Fatalf("empty store: %v %v", ok, err)
	}
	if err := s.Put(ctx, discord.Link{HouseholdID: "hh", DiscordUserID: "d1", UserID: 7, NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	l, ok, _ := s.ByDiscord(ctx, "hh", "d1")
	if !ok || l.UserID != 7 || l.VoiceReplies != discord.VoiceAuto || l.Proactive {
		t.Fatalf("link %+v", l)
	}

	// Relinking the same user to a new Discord account replaces the old one.
	_ = s.Put(ctx, discord.Link{HouseholdID: "hh", DiscordUserID: "d2", UserID: 7, NodeID: "n1"})
	if _, ok, _ := s.ByDiscord(ctx, "hh", "d1"); ok {
		t.Fatal("old Discord account still linked")
	}
	// Another user claiming the same Discord account takes it over; the first user loses it.
	_ = s.Put(ctx, discord.Link{HouseholdID: "hh", DiscordUserID: "d2", UserID: 8, NodeID: "n1"})
	if _, ok, _ := s.ByUser(ctx, "hh", 7); ok {
		t.Fatal("user 7 kept a link to a Discord account user 8 now holds")
	}

	// Preferences change only what is given.
	on, never := true, discord.VoiceNever
	if ok, err := s.SetPrefs(ctx, "hh", 8, discord.Prefs{Proactive: &on, VoiceReplies: &never}); !ok || err != nil {
		t.Fatalf("set prefs %v %v", ok, err)
	}
	l, _, _ = s.ByUser(ctx, "hh", 8)
	if !l.Proactive || l.VoiceReplies != discord.VoiceNever || l.NodeID != "n1" {
		t.Fatalf("prefs %+v", l)
	}
	if ok, _ := s.SetPrefs(ctx, "hh", 99, discord.Prefs{Proactive: &on}); ok {
		t.Fatal("prefs set for a user with no link")
	}

	pro, _ := s.Proactive(ctx, 8)
	if len(pro) != 1 {
		t.Fatalf("proactive %v", pro)
	}
	_ = s.Delete(ctx, "hh", 8)
	if pro, _ := s.Proactive(ctx, 8); len(pro) != 0 {
		t.Fatal("link survived delete")
	}
}

func TestCodes(t *testing.T) {
	now := time.Now()
	c := &discord.Codes{Now: func() time.Time { return now }}
	code := c.New("hh", 7, "n1")
	if len(code) != 8 {
		t.Fatalf("code %q", code)
	}
	// Typed from a phone: lower case and a dash are fine.
	typed := code[:4] + "-" + string([]byte{code[4] | 0x20}) + code[5:]
	hh, uid, node, ok, err := c.Redeem(typed, "d1")
	if err != nil || !ok || hh != "hh" || uid != 7 || node != "n1" {
		t.Fatalf("redeem %v %v %v %v %v", hh, uid, node, ok, err)
	}
	if _, _, _, ok, _ := c.Redeem(code, "d1"); ok {
		t.Fatal("a code works once")
	}

	// Expiry.
	code = c.New("hh", 7, "n1")
	now = now.Add(discord.CodeTTL + time.Second)
	if _, _, _, ok, _ := c.Redeem(code, "d1"); ok {
		t.Fatal("expired code accepted")
	}

	// A new code replaces the user's earlier one.
	first := c.New("hh", 9, "n1")
	second := c.New("hh", 9, "n1")
	if _, _, _, ok, _ := c.Redeem(first, "d3"); ok {
		t.Fatal("replaced code still works")
	}
	if _, _, _, ok, _ := c.Redeem(second, "d3"); !ok {
		t.Fatal("new code rejected")
	}

	// Guessing is limited per Discord account.
	for i := 0; i < 5; i++ {
		c.Redeem("AAAAAAAA", "guesser")
	}
	live := c.New("hh", 10, "n1")
	if _, _, _, ok, err := c.Redeem(live, "guesser"); ok || err != discord.ErrTooManyGuesses {
		t.Fatalf("guesser got through: %v %v", ok, err)
	}
}
