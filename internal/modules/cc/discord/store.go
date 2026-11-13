// Package discord is the Discord bridge: people message Jarvis from Discord (DM, one channel,
// slash commands, voice clips, a voice channel) and get the reply where they asked. It replies
// only to what it was sent, plus reminders for users who opted in. See docs/discord-bridge.md.
package discord

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// Voice reply modes (Link.VoiceReplies).
const (
	VoiceAuto   = "auto"   // a voice clip answers a voice clip
	VoiceAlways = "always" // every answer also comes as a clip
	VoiceNever  = "never"
)

// Link ties a Discord account to a Jarvis user in one household.
type Link struct {
	HouseholdID   string
	DiscordUserID string
	UserID        int64
	NodeID        string
	VoiceReplies  string
	Proactive     bool
	CreatedAt     time.Time
}

// Store is the cc_discord_links table.
type Store struct {
	DB  *db.DB
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const linkCols = `household_id, discord_user_id, user_id, node_id, voice_replies, proactive, created_at`

func scanLink(row interface{ Scan(...any) error }) (Link, error) {
	var l Link
	var created string
	var pro int
	if err := row.Scan(&l.HouseholdID, &l.DiscordUserID, &l.UserID, &l.NodeID, &l.VoiceReplies, &pro, &created); err != nil {
		return Link{}, err
	}
	l.Proactive = pro != 0
	l.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return l, nil
}

// Put links a Discord account to a user, replacing the user's earlier link in the household
// and any other user's claim on the same Discord account.
func (s *Store) Put(ctx context.Context, l Link) error {
	if l.VoiceReplies == "" {
		l.VoiceReplies = VoiceAuto
	}
	tx, err := s.DB.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM cc_discord_links WHERE household_id = ? AND (user_id = ? OR discord_user_id = ?)`,
		l.HouseholdID, l.UserID, l.DiscordUserID); err != nil {
		return err
	}
	pro := 0
	if l.Proactive {
		pro = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cc_discord_links (`+linkCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		l.HouseholdID, l.DiscordUserID, l.UserID, l.NodeID, l.VoiceReplies, pro, s.now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// ByDiscord finds the link for a Discord account in a household.
func (s *Store) ByDiscord(ctx context.Context, hh, discordUserID string) (Link, bool, error) {
	l, err := scanLink(s.DB.Read.QueryRowContext(ctx, `SELECT `+linkCols+` FROM cc_discord_links WHERE household_id = ? AND discord_user_id = ?`, hh, discordUserID))
	return l, err == nil, notNoRows(err)
}

// ByUser finds a Jarvis user's link in a household.
func (s *Store) ByUser(ctx context.Context, hh string, userID int64) (Link, bool, error) {
	l, err := scanLink(s.DB.Read.QueryRowContext(ctx, `SELECT `+linkCols+` FROM cc_discord_links WHERE household_id = ? AND user_id = ?`, hh, userID))
	return l, err == nil, notNoRows(err)
}

// Prefs changes a link's settings; nil fields stay as they are.
type Prefs struct {
	NodeID       *string
	VoiceReplies *string
	Proactive    *bool
}

// SetPrefs updates the user's link, reporting whether they have one.
func (s *Store) SetPrefs(ctx context.Context, hh string, userID int64, p Prefs) (bool, error) {
	l, ok, err := s.ByUser(ctx, hh, userID)
	if err != nil || !ok {
		return false, err
	}
	if p.NodeID != nil {
		l.NodeID = *p.NodeID
	}
	if p.VoiceReplies != nil {
		l.VoiceReplies = *p.VoiceReplies
	}
	if p.Proactive != nil {
		l.Proactive = *p.Proactive
	}
	pro := 0
	if l.Proactive {
		pro = 1
	}
	_, err = s.DB.Write.ExecContext(ctx, `UPDATE cc_discord_links SET node_id = ?, voice_replies = ?, proactive = ? WHERE household_id = ? AND user_id = ?`,
		l.NodeID, l.VoiceReplies, pro, hh, userID)
	return true, err
}

// Delete removes the user's link in a household.
func (s *Store) Delete(ctx context.Context, hh string, userID int64) error {
	_, err := s.DB.Write.ExecContext(ctx, `DELETE FROM cc_discord_links WHERE household_id = ? AND user_id = ?`, hh, userID)
	return err
}

// Proactive lists the links that opted in to messages Jarvis starts (reminders and alarms) for a user.
func (s *Store) Proactive(ctx context.Context, userID int64) ([]Link, error) {
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT `+linkCols+` FROM cc_discord_links WHERE user_id = ? AND proactive = 1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func notNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// --- one-time link codes ---

// CodeTTL is how long a link code lasts.
const CodeTTL = 10 * time.Minute

// codeAlphabet leaves out 0, O, 1, I and L so a code can be read aloud or typed from a phone screen.
const codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

type pending struct {
	hh      string
	userID  int64
	nodeID  string
	expires time.Time
}

// Codes issues and redeems link codes. A code is made in the app by a signed in user and sent to
// the bot by the Discord account to link. In memory: a restart just makes the user ask again.
type Codes struct {
	Now func() time.Time

	mu       sync.Mutex
	m        map[string]pending
	failures map[string][]time.Time // by Discord user, recent bad guesses
}

func (c *Codes) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// New makes a code for a user, replacing any earlier code of theirs.
func (c *Codes) New(hh string, userID int64, nodeID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]pending{}
	}
	for k, p := range c.m {
		if p.userID == userID && p.hh == hh || !p.expires.After(c.now()) {
			delete(c.m, k)
		}
	}
	for {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			panic(err) // no entropy: nothing sensible to do
		}
		for i := range b {
			b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
		}
		code := string(b)
		if _, taken := c.m[code]; !taken {
			c.m[code] = pending{hh: hh, userID: userID, nodeID: nodeID, expires: c.now().Add(CodeTTL)}
			return code
		}
	}
}

// maxBadGuesses per Discord account per CodeTTL.
const maxBadGuesses = 5

var ErrTooManyGuesses = errors.New("too many wrong codes, wait a few minutes")

// Redeem consumes a code for a Discord account. ok is false for an unknown or expired code.
func (c *Codes) Redeem(code, discordUserID string) (hh string, userID int64, nodeID string, ok bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.failures == nil {
		c.failures = map[string][]time.Time{}
	}
	recent := c.failures[discordUserID][:0]
	for _, t := range c.failures[discordUserID] {
		if now.Sub(t) < CodeTTL {
			recent = append(recent, t)
		}
	}
	c.failures[discordUserID] = recent
	if len(recent) >= maxBadGuesses {
		return "", 0, "", false, ErrTooManyGuesses
	}
	p, found := c.m[normaliseCode(code)]
	if !found || !p.expires.After(now) {
		c.failures[discordUserID] = append(recent, now)
		return "", 0, "", false, nil
	}
	delete(c.m, normaliseCode(code))
	return p.hh, p.userID, p.nodeID, true, nil
}

func normaliseCode(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z':
			out = append(out, ch-32)
		case ch == ' ' || ch == '-':
		default:
			out = append(out, ch)
		}
	}
	return string(out)
}
