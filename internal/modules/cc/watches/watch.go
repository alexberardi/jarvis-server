package watches

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Source is what a watch observes (cc_watches.source_spec): a web page or a search, or a signal
// kind on the household's signal bus (A3).
type Source struct {
	URL     string            `json:"url,omitempty"`     // web: a page
	Query   string            `json:"query,omitempty"`   // web: a search
	Extract string            `json:"extract,omitempty"` // web: what to read ("the price")
	Kind    string            `json:"kind,omitempty"`    // signal: the signal kind
	UserID  *int64            `json:"user_id,omitempty"` // signal: only this member's signals
	Facts   map[string]string `json:"facts,omitempty"`   // signal: facts that must equal these
}

// Watch is one cc_watches row. UserID is the owner (A8); nil means the household.
type Watch struct {
	ID, HouseholdID   string
	UserID            *int64
	Title, SourceKind string
	Source            Source
	Condition         Condition
	CadenceSeconds    int
	State, FirePolicy string
	CooldownSeconds   int

	ExpiresAt, LastCheckedAt, LastFiredAt *time.Time
	LastValue, LastError                  string
	CreatedAt, UpdatedAt                  time.Time
}

const (
	SourceWeb    = "web"
	SourceSignal = "signal"

	StateActive = "active"
	StatePaused = "paused"
	StateDone   = "done"

	FireOnce        = "once"
	FireEveryChange = "every_change"
	FireCooldown    = "cooldown"

	// DefaultTTL is how long a watch lives when it is created without an expiry.
	DefaultTTL = 30 * 24 * time.Hour
)

// Validate checks the watch's own fields, then its condition against its source kind. It does
// not apply the household's limits (watches.max_active, watches.min_cadence_minutes).
func (w Watch) Validate() error {
	if strings.TrimSpace(w.Title) == "" {
		return errors.New("watch: a title is required")
	}
	switch w.SourceKind {
	case SourceWeb:
		if (w.Source.URL == "") == (w.Source.Query == "") {
			return errors.New("watch: a web watch needs a URL or a query, not both")
		}
		if w.Source.URL != "" {
			u, err := url.Parse(w.Source.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return errors.New("watch: the URL must be http or https")
			}
		}
		if strings.TrimSpace(w.Source.Extract) == "" {
			return errors.New("watch: a web watch needs what to read (extract)")
		}
	case SourceSignal:
		if w.Source.Kind == "" {
			return errors.New("watch: a signal watch needs a signal kind")
		}
	default:
		return fmt.Errorf("watch: unknown source kind %q", w.SourceKind)
	}
	switch w.FirePolicy {
	case FireOnce, FireEveryChange:
	case FireCooldown:
		if w.CooldownSeconds <= 0 {
			return errors.New("watch: a cooldown needs cooldown_seconds > 0")
		}
	default:
		return fmt.Errorf("watch: unknown fire policy %q", w.FirePolicy)
	}
	if err := w.Condition.Validate(w.SourceKind); err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	return nil
}

// Describe says what the watch waits for in plain words, for the card and the voice tools:
// "the price to drop below 500 on example.com".
func Describe(w Watch) string {
	c := w.Condition
	subject := "the signal"
	switch {
	case w.SourceKind == SourceWeb:
		subject = w.Source.Extract
	case c.Field != "":
		subject = "the " + c.Field
	}
	var s string
	switch c.Op {
	case OpLT:
		s = subject + " to drop below " + c.Value
	case OpGT:
		s = subject + " to go above " + c.Value
	case OpChanged:
		s = subject + " to change"
	case OpEquals:
		s = subject + " to be " + c.Value
	case OpContains:
		s = subject + " to mention " + c.Value
	case OpAbsentFor:
		s = subject + " not to be " + c.Value + " for " + c.For
	default:
		s = subject
	}
	switch {
	case w.SourceKind != SourceWeb:
	case w.Source.URL != "":
		if l := SourceLabel(w.Source.URL); l != "" {
			s += " on " + l
		}
	case w.Source.Query != "":
		s += " in searches for “" + w.Source.Query + "”"
	}
	return s
}

// SourceLabel is a URL's host without "www." (and without the port), "" when unparsable.
func SourceLabel(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}
