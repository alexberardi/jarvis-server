package channels_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/channels"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

func newStore(t *testing.T) *channels.Store {
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
	return &channels.Store{DB: d}
}

func TestStoreLinkLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, ok, err := s.ByExternal(ctx, "hh", "discord", "d1"); ok || err != nil {
		t.Fatalf("empty store: %v %v", ok, err)
	}
	if err := s.Put(ctx, channels.Link{HouseholdID: "hh", Provider: "discord", ExternalID: "d1", UserID: 7, NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	l, ok, _ := s.ByExternal(ctx, "hh", "discord", "d1")
	if !ok || l.UserID != 7 || l.VoiceReplies != channels.VoiceAuto || l.Proactive || l.Provider != "discord" {
		t.Fatalf("link %+v", l)
	}

	// A provider's links are its own: the same account id on another service is nobody, and the
	// user's link on one service leaves the other alone.
	if _, ok, _ := s.ByExternal(ctx, "hh", "slack", "d1"); ok {
		t.Fatal("a discord link answered for slack")
	}
	_ = s.Put(ctx, channels.Link{HouseholdID: "hh", Provider: "slack", ExternalID: "U1", UserID: 7, NodeID: "n1"})
	if _, ok, _ := s.ByExternal(ctx, "hh", "discord", "d1"); !ok {
		t.Fatal("linking slack dropped the discord link")
	}

	// Relinking the same user to a new account on a provider replaces the old one.
	_ = s.Put(ctx, channels.Link{HouseholdID: "hh", Provider: "discord", ExternalID: "d2", UserID: 7, NodeID: "n1"})
	if _, ok, _ := s.ByExternal(ctx, "hh", "discord", "d1"); ok {
		t.Fatal("old account still linked")
	}
	// Another user claiming the same account takes it over; the first user loses it.
	_ = s.Put(ctx, channels.Link{HouseholdID: "hh", Provider: "discord", ExternalID: "d2", UserID: 8, NodeID: "n1"})
	if _, ok, _ := s.ByUser(ctx, "hh", "discord", 7); ok {
		t.Fatal("user 7 kept a link to an account user 8 now holds")
	}

	// Preferences change only what is given.
	on, never := true, channels.VoiceNever
	if ok, err := s.SetPrefs(ctx, "hh", "discord", 8, channels.Prefs{Proactive: &on, VoiceReplies: &never}); !ok || err != nil {
		t.Fatalf("set prefs %v %v", ok, err)
	}
	l, _, _ = s.ByUser(ctx, "hh", "discord", 8)
	if !l.Proactive || l.VoiceReplies != channels.VoiceNever || l.NodeID != "n1" {
		t.Fatalf("prefs %+v", l)
	}
	if ok, _ := s.SetPrefs(ctx, "hh", "discord", 99, channels.Prefs{Proactive: &on}); ok {
		t.Fatal("prefs set for a user with no link")
	}

	pro, _ := s.Proactive(ctx, 8)
	if len(pro) != 1 {
		t.Fatalf("proactive %v", pro)
	}
	_ = s.Delete(ctx, "hh", "discord", 8)
	if pro, _ := s.Proactive(ctx, 8); len(pro) != 0 {
		t.Fatal("link survived delete")
	}
}

// A provider is known once its daemon has called in, and online while it keeps doing so.
func TestProvidersAreSeen(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	now := time.Date(2026, 11, 14, 4, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	if ps, err := s.Providers(ctx, "hh"); err != nil || len(ps) != 0 {
		t.Fatalf("fresh: %v %v", ps, err)
	}
	_ = s.Seen(ctx, "hh", "slack", "n2")
	_ = s.Seen(ctx, "hh", "discord", "n1")
	_ = s.Seen(ctx, "other", "discord", "n9")
	now = now.Add(channels.OnlineFor - time.Second)
	_ = s.Seen(ctx, "hh", "discord", "n3") // it moved to another node
	ps, _ := s.Providers(ctx, "hh")
	if len(ps) != 2 || ps[0].Name != "discord" || ps[0].NodeID != "n3" || ps[1].Name != "slack" {
		t.Fatalf("providers %+v", ps)
	}
	if !s.Online(ps[0]) || !s.Online(ps[1]) {
		t.Fatalf("both called in within %v", channels.OnlineFor)
	}
	now = now.Add(2 * time.Second)
	if !s.Online(ps[0]) || s.Online(ps[1]) {
		t.Fatal("slack last called in over a minute ago; discord did not")
	}
	for name, want := range map[string]bool{"discord": true, "my_bridge2": true, "": false, "Discord": false, "a/b": false, "a b": false} {
		if channels.ValidProvider(name) != want {
			t.Errorf("ValidProvider(%q) != %v", name, want)
		}
	}
}

func TestCodes(t *testing.T) {
	now := time.Now()
	c := &channels.Codes{Now: func() time.Time { return now }}
	code := c.New("hh", "discord", 7, "n1")
	if len(code) != 8 {
		t.Fatalf("code %q", code)
	}
	// A code is for the provider and household it was made for.
	if _, ok, _ := c.Redeem(code, "hh", "slack", "U1"); ok {
		t.Fatal("a discord code linked a slack account")
	}
	if _, ok, _ := c.Redeem(code, "other", "discord", "d1"); ok {
		t.Fatal("a code linked an account in another household")
	}
	// Typed from a phone: lower case and a dash are fine.
	typed := code[:4] + "-" + string([]byte{code[4] | 0x20}) + code[5:]
	p, ok, err := c.Redeem(typed, "hh", "discord", "d1")
	if err != nil || !ok || p.HouseholdID != "hh" || p.UserID != 7 || p.NodeID != "n1" || p.Provider != "discord" {
		t.Fatalf("redeem %+v %v %v", p, ok, err)
	}
	if _, ok, _ := c.Redeem(code, "hh", "discord", "d1"); ok {
		t.Fatal("a code works once")
	}

	// Expiry.
	code = c.New("hh", "discord", 7, "n1")
	now = now.Add(channels.CodeTTL + time.Second)
	if _, ok, _ := c.Redeem(code, "hh", "discord", "d1"); ok {
		t.Fatal("expired code accepted")
	}

	// A new code replaces the user's earlier one for that provider, not for another.
	first := c.New("hh", "discord", 9, "n1")
	slack := c.New("hh", "slack", 9, "n1")
	second := c.New("hh", "discord", 9, "n1")
	if _, ok, _ := c.Redeem(first, "hh", "discord", "d3"); ok {
		t.Fatal("replaced code still works")
	}
	if _, ok, _ := c.Redeem(second, "hh", "discord", "d3"); !ok {
		t.Fatal("new code rejected")
	}
	if _, ok, _ := c.Redeem(slack, "hh", "slack", "U3"); !ok {
		t.Fatal("the slack code went with the discord one")
	}

	// Guessing is limited per account.
	for range 5 {
		c.Redeem("AAAAAAAA", "hh", "discord", "guesser")
	}
	live := c.New("hh", "discord", 10, "n1")
	if _, ok, err := c.Redeem(live, "hh", "discord", "guesser"); ok || err != channels.ErrTooManyGuesses {
		t.Fatalf("guesser got through: %v %v", ok, err)
	}
}
