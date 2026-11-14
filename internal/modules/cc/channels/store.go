// Package channels is what jarvisd keeps for chat-service channels (docs/CHANNELS.md): which
// account on a service speaks as which Jarvis user, the one-time codes that link them, and
// which services' daemons have called in. A provider is a name a package picks ("discord");
// nothing here names one. The link store and codes are from the Discord bridge of #649.
package channels

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"regexp"
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

// OnlineFor is how recently a provider's daemon must have called in to count as online.
const OnlineFor = time.Minute

var providerRE = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// ValidProvider reports whether s can be a provider's name.
func ValidProvider(s string) bool { return providerRE.MatchString(s) }

// Link ties an account on a provider to a Jarvis user in one household.
type Link struct {
	HouseholdID  string
	Provider     string
	ExternalID   string
	UserID       int64
	NodeID       string // the node the user's turns run through
	VoiceReplies string
	Proactive    bool // reminders may be sent unasked
	CreatedAt    time.Time
}

// Provider is a service whose daemon has called in.
type Provider struct {
	Name     string
	NodeID   string
	LastSeen time.Time
}

// Store is the cc_channel_links and cc_channel_providers tables.
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

const linkCols = `household_id, provider, external_user_id, user_id, node_id, voice_replies, proactive, created_at`

func scanLink(row interface{ Scan(...any) error }) (Link, error) {
	var l Link
	var created string
	var pro int
	if err := row.Scan(&l.HouseholdID, &l.Provider, &l.ExternalID, &l.UserID, &l.NodeID, &l.VoiceReplies, &pro, &created); err != nil {
		return Link{}, err
	}
	l.Proactive = pro != 0
	l.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return l, nil
}

// Put links an account to a user, replacing the user's earlier link on that provider in the
// household and any other user's claim on the same account.
func (s *Store) Put(ctx context.Context, l Link) error {
	if l.VoiceReplies == "" {
		l.VoiceReplies = VoiceAuto
	}
	tx, err := s.DB.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM cc_channel_links WHERE household_id = ? AND provider = ?
		AND (user_id = ? OR external_user_id = ?)`, l.HouseholdID, l.Provider, l.UserID, l.ExternalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cc_channel_links (`+linkCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		l.HouseholdID, l.Provider, l.ExternalID, l.UserID, l.NodeID, l.VoiceReplies, l.Proactive,
		s.now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// ByExternal finds the link for an account on a provider in a household.
func (s *Store) ByExternal(ctx context.Context, hh, provider, externalID string) (Link, bool, error) {
	l, err := scanLink(s.DB.Read.QueryRowContext(ctx, `SELECT `+linkCols+` FROM cc_channel_links
		WHERE household_id = ? AND provider = ? AND external_user_id = ?`, hh, provider, externalID))
	return l, err == nil, notNoRows(err)
}

// ByUser finds a Jarvis user's link on a provider in a household.
func (s *Store) ByUser(ctx context.Context, hh, provider string, userID int64) (Link, bool, error) {
	l, err := scanLink(s.DB.Read.QueryRowContext(ctx, `SELECT `+linkCols+` FROM cc_channel_links
		WHERE household_id = ? AND provider = ? AND user_id = ?`, hh, provider, userID))
	return l, err == nil, notNoRows(err)
}

// Prefs changes a link's settings; nil fields stay as they are.
type Prefs struct {
	NodeID       *string
	VoiceReplies *string
	Proactive    *bool
}

// SetPrefs updates the user's link, reporting whether they have one.
func (s *Store) SetPrefs(ctx context.Context, hh, provider string, userID int64, p Prefs) (bool, error) {
	l, ok, err := s.ByUser(ctx, hh, provider, userID)
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
	_, err = s.DB.Write.ExecContext(ctx, `UPDATE cc_channel_links SET node_id = ?, voice_replies = ?, proactive = ?
		WHERE household_id = ? AND provider = ? AND user_id = ?`, l.NodeID, l.VoiceReplies, l.Proactive, hh, provider, userID)
	return true, err
}

// Delete removes the user's link on a provider in a household.
func (s *Store) Delete(ctx context.Context, hh, provider string, userID int64) error {
	_, err := s.DB.Write.ExecContext(ctx, `DELETE FROM cc_channel_links WHERE household_id = ? AND provider = ? AND user_id = ?`,
		hh, provider, userID)
	return err
}

// Proactive lists a user's links in a household that opted in to messages Jarvis starts
// (reminders).
func (s *Store) Proactive(ctx context.Context, hh string, userID int64) ([]Link, error) {
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT `+linkCols+` FROM cc_channel_links
		WHERE household_id = ? AND user_id = ? AND proactive = 1 ORDER BY provider`, hh, userID)
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

// Seen notes that provider's daemon called in from a node of the household.
func (s *Store) Seen(ctx context.Context, hh, provider, nodeID string) error {
	_, err := s.DB.Write.ExecContext(ctx, `INSERT INTO cc_channel_providers (household_id, provider, node_id, last_seen_at)
		VALUES (?, ?, ?, ?) ON CONFLICT (household_id, provider) DO UPDATE SET node_id = excluded.node_id,
		last_seen_at = excluded.last_seen_at`, hh, provider, nodeID, s.now().UTC().Format(time.RFC3339))
	return err
}

// Providers are the services whose daemons have called in for a household, by name.
func (s *Store) Providers(ctx context.Context, hh string) ([]Provider, error) {
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT provider, node_id, last_seen_at FROM cc_channel_providers
		WHERE household_id = ? ORDER BY provider`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		var p Provider
		var seen string
		if err := rows.Scan(&p.Name, &p.NodeID, &seen); err != nil {
			return nil, err
		}
		p.LastSeen, _ = time.Parse(time.RFC3339, seen)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Online reports whether p's daemon called in within OnlineFor.
func (s *Store) Online(p Provider) bool { return s.now().Sub(p.LastSeen) < OnlineFor }

// OutboxKeep is how long a message waits for its provider's daemon before it is dropped.
const OutboxKeep = 24 * time.Hour

// outboxBatch is the most messages one Outbox call hands over.
const outboxBatch = 20

// Outgoing is a message jarvisd wants said unasked: a reminder, to an account on a provider.
type Outgoing struct {
	ID         string
	ExternalID string
	Text       string
}

// Enqueue queues text for an account on a provider; id is the caller's (a uuid).
func (s *Store) Enqueue(ctx context.Context, id, hh, provider, externalID, text string) error {
	_, err := s.DB.Write.ExecContext(ctx, `INSERT INTO cc_channel_outbox (id, household_id, provider, external_user_id, text, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, hh, provider, externalID, text, s.now().UTC().Format(time.RFC3339))
	return err
}

// Outbox drops the messages a provider's daemon says it sent (ack) and the ones older than
// OutboxKeep, and returns the oldest still waiting. A message stays until acknowledged, so one
// the daemon took and failed to send is handed over again.
func (s *Store) Outbox(ctx context.Context, hh, provider string, ack []string) ([]Outgoing, error) {
	for _, id := range ack {
		if _, err := s.DB.Write.ExecContext(ctx, `DELETE FROM cc_channel_outbox WHERE id = ? AND household_id = ? AND provider = ?`,
			id, hh, provider); err != nil {
			return nil, err
		}
	}
	if _, err := s.DB.Write.ExecContext(ctx, `DELETE FROM cc_channel_outbox WHERE created_at < ?`,
		s.now().Add(-OutboxKeep).UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	// Only for an account still linked and still opted in: unlinking or opting out stops what
	// was already queued.
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT o.id, o.external_user_id, o.text FROM cc_channel_outbox o
		WHERE o.household_id = ? AND o.provider = ? AND EXISTS (SELECT 1 FROM cc_channel_links l
			WHERE l.household_id = o.household_id AND l.provider = o.provider
			AND l.external_user_id = o.external_user_id AND l.proactive = 1)
		ORDER BY o.created_at, o.id LIMIT ?`, hh, provider, outboxBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Outgoing{}
	for rows.Next() {
		var o Outgoing
		if err := rows.Scan(&o.ID, &o.ExternalID, &o.Text); err != nil {
			return nil, err
		}
		out = append(out, o)
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

// Pending is what a code links once redeemed.
type Pending struct {
	HouseholdID string
	Provider    string
	UserID      int64
	NodeID      string
	expires     time.Time
}

// Codes issues and redeems link codes. A code is made in the app by a signed in user, for one
// provider, and sent to that provider's bot by the account to link. In memory: a restart just
// makes the user ask again.
type Codes struct {
	Now func() time.Time

	mu       sync.Mutex
	m        map[string]Pending
	failures map[string][]time.Time // by provider and account, recent bad guesses
}

func (c *Codes) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// New makes a code for a user on a provider, replacing any earlier code of theirs for it.
func (c *Codes) New(hh, provider string, userID int64, nodeID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]Pending{}
	}
	for k, p := range c.m {
		if p.UserID == userID && p.HouseholdID == hh && p.Provider == provider || !p.expires.After(c.now()) {
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
			c.m[code] = Pending{HouseholdID: hh, Provider: provider, UserID: userID, NodeID: nodeID, expires: c.now().Add(CodeTTL)}
			return code
		}
	}
}

// maxBadGuesses per account per CodeTTL.
const maxBadGuesses = 5

var ErrTooManyGuesses = errors.New("too many wrong codes, wait a few minutes")

// Redeem consumes a code for an account on a provider in a household. ok is false for a code
// that is unknown, expired, or made for another provider or household: each counts as a wrong
// guess, and after maxBadGuesses the account waits out CodeTTL.
func (c *Codes) Redeem(code, hh, provider, externalID string) (p Pending, ok bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.failures == nil {
		c.failures = map[string][]time.Time{}
	}
	who := provider + "\x00" + externalID
	recent := c.failures[who][:0]
	for _, t := range c.failures[who] {
		if now.Sub(t) < CodeTTL {
			recent = append(recent, t)
		}
	}
	c.failures[who] = recent
	if len(recent) >= maxBadGuesses {
		return Pending{}, false, ErrTooManyGuesses
	}
	key := normaliseCode(code)
	p, found := c.m[key]
	if !found || !p.expires.After(now) || p.Provider != provider || p.HouseholdID != hh {
		c.failures[who] = append(recent, now)
		return Pending{}, false, nil
	}
	delete(c.m, key)
	return p, true, nil
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
