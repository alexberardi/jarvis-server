package phone

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Rows of cc_phone_call_sessions and cc_phone_contacts (baseline migration, 11 phone).

// States (phone_call_service.py).
const (
	StateDraft     = "draft"
	StateConfirmed = "confirmed"
	StateDialing   = "dialing"
	StateInCall    = "in_call"
	StateWrapup    = "wrapup"
	StateDone      = "done"
	StateFailed    = "failed"
	StateDeclined  = "declined"
	StateExpired   = "expired"
)

func isTerminal(s string) bool {
	return s == StateDone || s == StateFailed || s == StateDeclined || s == StateExpired
}

func isActive(s string) bool { return s == StateDialing || s == StateInCall || s == StateWrapup }

// transitions is the legal transition table. D40 11.Q3 adds confirmed → declined, so a
// cancelled confirmed call loses the claim CAS instead of dialing.
var transitions = map[string]map[string]bool{
	StateDraft:     {StateConfirmed: true, StateDeclined: true, StateExpired: true, StateFailed: true},
	StateConfirmed: {StateDialing: true, StateFailed: true, StateExpired: true, StateDeclined: true},
	StateDialing:   {StateInCall: true, StateWrapup: true, StateFailed: true},
	StateInCall:    {StateWrapup: true, StateFailed: true},
	StateWrapup:    {StateDone: true, StateFailed: true},
}

// CanTransition reports whether from → to is legal.
func CanTransition(from, to string) bool { return transitions[from][to] }

// Session is one call session row.
type Session struct {
	ID             string
	HouseholdID    string
	UserID         *int64
	ConfirmedBy    *int64
	ContactID      string
	ContactName    string
	ContactAddress string
	Goal           string
	Details        string
	ResolvedNumber string
	DialedNumber   string
	NumberEdited   bool
	LineType       string
	State          string
	ErrorMessage   string
	TranscriptJSON string
	OutcomeJSON    string
	AudioKey       string
	HeartbeatAt    time.Time
	CallSID        string
	Duration       *int64
	ErrandID       string
	ErrandStep     *int64
	CreatedAt      time.Time
	ConfirmedAt    time.Time
	InCallAt       time.Time
	ExpiresAt      time.Time
	EndedAt        time.Time
}

// Contact is one phonebook row.
type Contact struct {
	ID             string
	HouseholdID    string
	Name           string
	NormalizedName string
	Number         string
	Address        *string
	Source         string
	LineType       *string
	DoNotCall      bool
	Notes          *string
	VerifiedAt     time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const sessionCols = `id, household_id, user_id, confirmed_by, contact_id, contact_name, contact_address, goal,
	details, resolved_number, dialed_number, number_edited, line_type, state, error_message, transcript_json,
	outcome_json, audio_object_key, heartbeat_at, twilio_call_sid, duration_seconds, errand_id, errand_step,
	created_at, confirmed_at, in_call_at, expires_at, ended_at`

type scanner interface{ Scan(dest ...any) error }

func scanSession(r scanner) (*Session, error) {
	var s Session
	var uid, cby, dur, step sql.NullInt64
	var cid, cname, caddr, det, res, dial, lt, em, tj, oj, ak, hb, sid, eid, ca, cf, ic, ex, en sql.NullString
	var edited int
	if err := r.Scan(&s.ID, &s.HouseholdID, &uid, &cby, &cid, &cname, &caddr, &s.Goal, &det, &res, &dial, &edited,
		&lt, &s.State, &em, &tj, &oj, &ak, &hb, &sid, &dur, &eid, &step, &ca, &cf, &ic, &ex, &en); err != nil {
		return nil, err
	}
	if uid.Valid {
		s.UserID = &uid.Int64
	}
	if cby.Valid {
		s.ConfirmedBy = &cby.Int64
	}
	if dur.Valid {
		s.Duration = &dur.Int64
	}
	if step.Valid {
		s.ErrandStep = &step.Int64
	}
	s.ContactID, s.ContactName, s.ContactAddress = cid.String, cname.String, caddr.String
	s.Details, s.ResolvedNumber, s.DialedNumber, s.LineType = det.String, res.String, dial.String, lt.String
	s.ErrorMessage, s.TranscriptJSON, s.OutcomeJSON, s.AudioKey = em.String, tj.String, oj.String, ak.String
	s.CallSID, s.ErrandID = sid.String, eid.String
	s.NumberEdited = edited != 0
	s.HeartbeatAt, s.CreatedAt, s.ConfirmedAt = parseTS(hb.String), parseTS(ca.String), parseTS(cf.String)
	s.InCallAt, s.ExpiresAt, s.EndedAt = parseTS(ic.String), parseTS(ex.String), parseTS(en.String)
	return &s, nil
}

// errNotFound is a missing row.
var errNotFound = sql.ErrNoRows

func (s *Service) session(ctx context.Context, q querier, id string) (*Session, error) {
	return scanSession(q.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM cc_phone_call_sessions WHERE id = ?`, id))
}

// sessionIn loads a session scoped to its household (callbacks never cross households).
func (s *Service) sessionIn(ctx context.Context, q querier, id, hh string) (*Session, error) {
	return scanSession(q.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM cc_phone_call_sessions WHERE id = ? AND household_id = ?`, id, hh))
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// transitionTx applies a legal transition inside tx, guarded by the current state (a CAS, so
// a concurrent writer can't be overwritten). It stamps confirmed_at, in_call_at and, on a
// terminal state, ended_at plus the call duration measured from in_call_at (D40 11.Q6).
// ok=false: illegal, or the row moved on.
func (s *Service) transitionTx(ctx context.Context, tx querier, sess *Session, to string, extra map[string]any) (bool, error) {
	if !CanTransition(sess.State, to) {
		return false, nil
	}
	now := s.now()
	set := []string{"state = ?"}
	args := []any{to}
	add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }
	switch {
	case to == StateConfirmed:
		add("confirmed_at", dbTime(now))
	case to == StateInCall:
		add("in_call_at", dbTime(now))
	case isTerminal(to):
		add("ended_at", dbTime(now))
		if !sess.InCallAt.IsZero() {
			add("duration_seconds", int64(now.Sub(sess.InCallAt).Seconds()))
		}
	}
	for _, col := range sortedKeys(extra) {
		add(col, extra[col])
	}
	args = append(args, sess.ID, sess.State)
	res, err := tx.ExecContext(ctx, `UPDATE cc_phone_call_sessions SET `+strings.Join(set, ", ")+
		` WHERE id = ? AND state = ?`, args...)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	sess.State = to
	switch {
	case to == StateConfirmed:
		sess.ConfirmedAt = now
	case to == StateInCall:
		sess.InCallAt = now
	case isTerminal(to):
		sess.EndedAt = now
	}
	return true, nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// transition runs transitionTx in its own write.
func (s *Service) transition(ctx context.Context, sess *Session, to string, extra map[string]any) (bool, error) {
	return s.transitionTx(ctx, s.DB.Write, sess, to, extra)
}

const contactCols = `id, household_id, name, normalized_name, number, address, source, line_type, do_not_call,
	notes, verified_at, created_at, updated_at`

func scanContact(r scanner) (*Contact, error) {
	var c Contact
	var addr, lt, notes, ver sql.NullString
	var created, updated string
	var dnc int
	if err := r.Scan(&c.ID, &c.HouseholdID, &c.Name, &c.NormalizedName, &c.Number, &addr, &c.Source, &lt, &dnc,
		&notes, &ver, &created, &updated); err != nil {
		return nil, err
	}
	if addr.Valid {
		c.Address = &addr.String
	}
	if lt.Valid {
		c.LineType = &lt.String
	}
	if notes.Valid {
		c.Notes = &notes.String
	}
	c.DoNotCall = dnc != 0
	c.VerifiedAt, c.CreatedAt, c.UpdatedAt = parseTS(ver.String), parseTS(created), parseTS(updated)
	return &c, nil
}

func (s *Service) contacts(ctx context.Context, hh string) ([]*Contact, error) {
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT `+contactCols+` FROM cc_phone_contacts
		WHERE household_id = ? ORDER BY name ASC, id ASC`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) contact(ctx context.Context, q querier, hh, id string) (*Contact, error) {
	return scanContact(q.QueryRowContext(ctx, `SELECT `+contactCols+` FROM cc_phone_contacts
		WHERE id = ? AND household_id = ?`, id, hh))
}

// dncNumbers is the household's do-not-call numbers (D40 11.Q4).
func (s *Service) dncNumbers(ctx context.Context, hh string) (map[string]bool, error) {
	rows, err := s.DB.Read.QueryContext(ctx,
		`SELECT number FROM cc_phone_contacts WHERE household_id = ? AND do_not_call = 1`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if norm, err := NormalizeUS(n); err == nil {
			out[norm] = true
		}
		out[n] = true
	}
	return out, rows.Err()
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// --- time and ids ---

func dbTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, f := range []string{"2006-01-02T15:04:05.000Z", time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// pyNaive renders a UTC time like Python's naive datetime.isoformat() (what mobile parses).
func pyNaive(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05")
	}
	return t.Format("2006-01-02T15:04:05.000000")
}

func uuid4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
