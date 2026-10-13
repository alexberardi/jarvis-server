package cc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Device-code pairing (docs/agent/DESIGN.md §13, docs/install/01-no-cli-plan.md item 1.1): a
// computer adds itself to a household without anyone typing a password into it.
//
//  1. The agent calls POST /nodes/pairings (no auth). It gets a short human code, a pairing id
//     and a poll secret. Only the agent ever sees the secret.
//  2. The agent shows the code; any member of a household sees the same code in the admin or
//     the app (GET /nodes/pairings) and approves it into one of their own households and a room
//     (POST /nodes/pairings/approve), or denies it.
//  3. The agent polls POST /nodes/pairings/{id}/poll with the secret. Once approved, the answer
//     is an ordinary provisioning token for that household, which the agent redeems at
//     POST /nodes/register: the node ends up exactly like one enrolled with a token today.
//
// Pairings live in memory: they last 10 minutes, and a restart only means starting again.
//
// Guessing: a code is 8 symbols from a 32-symbol alphabet (40 bits). A user gets
// pairingApproveFailLimit wrong codes per pairingTTL, and at most maxPendingPairings are live,
// so one window's chance of hitting any of them is about 10·100/2^40, under one in a billion.
// The approval list shows the codes to members anyway (the code is shown on both screens, plan
// §7 item 3): the limit matters for an app that approves a typed code.

const (
	pairingTTL = 10 * time.Minute
	// pairingPollInterval is the advisory seconds between polls.
	pairingPollInterval = 3
	// pairingStartLimit is how many pairings one address may start per pairingTTL.
	pairingStartLimit = 10
	// pairingApproveFailLimit is how many failed approvals or denials (an unknown code, a
	// household the user isn't in) one user may make per pairingTTL.
	pairingApproveFailLimit = 10
	// maxPendingPairings caps live pairings, and so the guessing odds and the memory.
	maxPendingPairings = 100
	// maxPairingAddrs caps the per-address start windows kept.
	maxPairingAddrs = 10_000
	// pairingRoom is the room a pairing lands in unless the approver picks another.
	pairingRoom = "desktop"

	pairingCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no I, O, 0, 1
	pairingCodeLen      = 8
)

type pairingState int

const (
	pairingPending pairingState = iota
	pairingApproved
	pairingDenied
)

type pairing struct {
	id         string
	secretHash [32]byte
	code       string // normalized: pairingCodeLen symbols, no dash
	name       string // the computer's name, as it asked
	platform   string
	from       string // the requesting address, shown to the approver
	created    time.Time
	expires    time.Time
	state      pairingState

	// Set on approval.
	household  string
	room       string
	nodeName   string
	approvedBy int64
	// nodeID is fixed at the first credential hand-out, so a repeated poll refreshes the same
	// node's token instead of minting another node.
	nodeID string
}

type pairingStore struct {
	mu     sync.Mutex
	byID   map[string]*pairing
	byCode map[string]*pairing
	starts map[string][]time.Time // address -> start times within pairingTTL
	fails  map[int64][]time.Time  // user -> failed approvals within pairingTTL
}

func newPairingStore() *pairingStore {
	return &pairingStore{
		byID: map[string]*pairing{}, byCode: map[string]*pairing{},
		starts: map[string][]time.Time{}, fails: map[int64][]time.Time{},
	}
}

// recent drops the times at or before now - span.
func recent(ts []time.Time, now time.Time, span time.Duration) []time.Time {
	cut := now.Add(-span)
	return slices.DeleteFunc(ts, func(t time.Time) bool { return !t.After(cut) })
}

// sweepLocked forgets expired pairings and empty rate windows.
func (s *pairingStore) sweepLocked(now time.Time) {
	for id, p := range s.byID {
		if !now.Before(p.expires) {
			s.removeLocked(id)
		}
	}
	for k, ts := range s.starts {
		if ts = recent(ts, now, pairingTTL); len(ts) == 0 {
			delete(s.starts, k)
		} else {
			s.starts[k] = ts
		}
	}
	for k, ts := range s.fails {
		if ts = recent(ts, now, pairingTTL); len(ts) == 0 {
			delete(s.fails, k)
		} else {
			s.fails[k] = ts
		}
	}
}

func (s *pairingStore) removeLocked(id string) {
	if p, ok := s.byID[id]; ok {
		delete(s.byCode, p.code)
		delete(s.byID, id)
	}
}

func (s *pairingStore) failLocked(uid int64, now time.Time) {
	s.fails[uid] = append(s.fails[uid], now)
}

func (s *pairingStore) lockedOutLocked(uid int64) bool {
	return len(s.fails[uid]) >= pairingApproveFailLimit
}

// newPairingCode returns a random code of pairingCodeLen symbols. 256 is a multiple of the
// alphabet's 32, so masking a random byte is unbiased.
func newPairingCode() string {
	b := make([]byte, pairingCodeLen)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = pairingCodeAlphabet[b[i]&31]
	}
	return string(b)
}

// normalizePairingCode accepts what a person types: any case, with or without the dash or
// spaces.
func normalizePairingCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r == '-' || unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// displayPairingCode is the code as shown: ABCD-EFGH.
func displayPairingCode(c string) string {
	if len(c) != pairingCodeLen {
		return c
	}
	return c[:4] + "-" + c[4:]
}

// cleanLabel trims a client-supplied name to max runes, without control characters.
func cleanLabel(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

// peerAddr is the socket peer's address. X-Forwarded-For is not trusted: pairing runs on the
// LAN listener, and a spoofable key would defeat the start limit.
func peerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func tooMany(w http.ResponseWriter, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(int(pairingTTL.Seconds())))
	detail(w, http.StatusTooManyRequests, msg)
}

// --- POST /nodes/pairings (no auth): start ---

func (m *Module) handleStartPairing(w http.ResponseWriter, r *http.Request) {
	b, _, ok := readBody(w, r, true)
	if !ok {
		return
	}
	name, _ := b.str("name", false)
	platform, _ := b.str("platform", false)
	if !b.done(w) {
		return
	}
	name = cleanLabel(name, 64)
	if name == "" {
		name = "Computer"
	}
	platform = cleanLabel(platform, 32)
	now := m.now()
	from := peerAddr(r)

	s := m.pairings
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if len(s.starts[from]) >= pairingStartLimit {
		tooMany(w, "Too many pairing requests from this address. Try again later.")
		return
	}
	if _, known := s.starts[from]; !known && len(s.starts) >= maxPairingAddrs {
		tooMany(w, "Too many pairing requests. Try again later.")
		return
	}
	if len(s.byID) >= maxPendingPairings {
		tooMany(w, "Too many pairings waiting for approval. Try again later.")
		return
	}
	s.starts[from] = append(s.starts[from], now)
	code := newPairingCode()
	for s.byCode[code] != nil {
		code = newPairingCode()
	}
	secret := tokenURLSafe(32)
	p := &pairing{
		id: uuid4(), secretHash: sha256.Sum256([]byte(secret)), code: code,
		name: name, platform: platform, from: from, created: now, expires: now.Add(pairingTTL),
	}
	s.byID[p.id] = p
	s.byCode[code] = p
	m.deps.Log.Info("cc: pairing started", "pairing", p.id, "name", name, "from", from)
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"pairing_id": p.id, "poll_secret": secret, "code": displayPairingCode(code),
		"expires_at": p.expires.UTC().Format(time.RFC3339), "expires_in": int(pairingTTL.Seconds()),
		"interval": pairingPollInterval,
	})
}

// --- POST /nodes/pairings/{pairing_id}/poll (the poll secret): the requester's view ---

const pairingNotFound = "Pairing not found or expired"

func (m *Module) handlePollPairing(w http.ResponseWriter, r *http.Request) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	secret, _ := b.str("poll_secret", true)
	if !b.done(w) {
		return
	}
	now := m.now()
	s := m.pairings
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	p := s.byID[r.PathValue("pairing_id")]
	// An unknown id and a wrong secret answer alike, so the id alone reveals nothing.
	sum := sha256.Sum256([]byte(secret))
	if p == nil || subtle.ConstantTimeCompare(sum[:], p.secretHash[:]) != 1 {
		detail(w, http.StatusNotFound, pairingNotFound)
		return
	}
	left := int(p.expires.Sub(now).Seconds())
	switch p.state {
	case pairingPending:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "pending", "expires_in": left, "interval": pairingPollInterval})
		return
	case pairingDenied:
		s.removeLocked(p.id)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "denied"})
		return
	}
	// Approved: hand out a provisioning token for the approver's household. A repeated poll
	// (a lost answer) replaces the token, so at most one is ever valid. Once the node has
	// registered, the pairing is done.
	room, name := p.room, p.nodeName
	raw, nodeID, expires, err := m.mintProvisioningToken(r.Context(), p.household, &room, &name, p.nodeID, &p.approvedBy)
	var se *statusErr
	if errors.As(err, &se) && se.status == http.StatusBadRequest {
		s.removeLocked(p.id)
		detail(w, http.StatusGone, "Pairing already used")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	p.nodeID = nodeID
	out := map[string]any{
		"status": "approved", "node_id": nodeID, "provisioning_token": raw,
		"household_id": p.household, "room": room, "name": name,
		"expires_at": pyNaive(expires), "expires_in": int(provisioningTTL.Seconds()),
	}
	m.addNodeURLs(r, out)
	httpx.WriteJSON(w, http.StatusOK, out)
}

// --- GET /nodes/pairings (user JWT, a household member): what is waiting ---

// memberships lists the user's households (the auth module implements authn.MembershipLister).
func (m *Module) memberships(ctx context.Context, uid int64) ([]authn.Membership, error) {
	ml, ok := m.Auth.(authn.MembershipLister)
	if !ok {
		return nil, fail(http.StatusServiceUnavailable, "Household listing unavailable")
	}
	return ml.Memberships(ctx, uid)
}

// requireAnyHousehold returns the user's households, or a 403 when they belong to none.
func (m *Module) requireAnyHousehold(ctx context.Context, uid int64) ([]authn.Membership, error) {
	hs, err := m.memberships(ctx, uid)
	if err != nil {
		return nil, err
	}
	if len(hs) == 0 {
		return nil, fail(http.StatusForbidden, "User is not a member of any household")
	}
	return hs, nil
}

func (m *Module) handleListPairings(w http.ResponseWriter, r *http.Request, u authn.User) {
	hs, err := m.requireAnyHousehold(r.Context(), u.ID)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	now := m.now()
	s := m.pairings
	s.mu.Lock()
	var pending []*pairing
	s.sweepLocked(now)
	for _, p := range s.byID {
		if p.state == pairingPending {
			pending = append(pending, p)
		}
	}
	s.mu.Unlock()
	slices.SortFunc(pending, func(a, b *pairing) int { return a.created.Compare(b.created) })
	list := make([]map[string]any, 0, len(pending))
	for _, p := range pending {
		list = append(list, map[string]any{
			"code": displayPairingCode(p.code), "name": p.name, "platform": p.platform,
			"requested_from": p.from, "created_at": p.created.UTC().Format(time.RFC3339),
			"expires_in": int(p.expires.Sub(now).Seconds()),
		})
	}
	households := make([]map[string]any, 0, len(hs))
	for _, h := range hs {
		households = append(households, map[string]any{"id": h.HouseholdID, "name": h.Name, "role": string(h.Role)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"pairings": list, "households": households})
}

// pendingByCode finds a live, pending pairing by its code, counting a miss against the user.
// It returns a 429 when the user is locked out.
func (m *Module) pendingByCode(uid int64, code string) (*pairing, error) {
	now := m.now()
	s := m.pairings
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if s.lockedOutLocked(uid) {
		return nil, fail(http.StatusTooManyRequests, "Too many failed pairing approvals. Try again later.")
	}
	p := s.byCode[normalizePairingCode(code)]
	if p == nil || p.state != pairingPending {
		s.failLocked(uid, now)
		return nil, fail(http.StatusNotFound, "Pairing code not found or expired")
	}
	return p, nil
}

func (m *Module) writePairingErr(w http.ResponseWriter, err error) {
	var se *statusErr
	if errors.As(err, &se) && se.status == http.StatusTooManyRequests {
		tooMany(w, se.Error())
		return
	}
	m.writeErr(w, err)
}

// --- POST /nodes/pairings/approve (user JWT): into one of the approver's own households ---

func (m *Module) handleApprovePairing(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	code, _ := b.str("code", true)
	hh, _ := b.str("household_id", true)
	room, _ := b.str("room", false)
	name, _ := b.str("name", false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	p, err := m.pendingByCode(u.ID, code)
	if err != nil {
		m.writePairingErr(w, err)
		return
	}
	// The approver must be in the TARGET household (D5), superuser or not: a pairing joins
	// one of the approver's own households (DESIGN §13 Q2).
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		var se *statusErr
		if errors.As(err, &se) {
			s := m.pairings
			s.mu.Lock()
			s.failLocked(u.ID, m.now())
			s.mu.Unlock()
		}
		m.writeErr(w, err)
		return
	}
	if room = cleanLabel(room, 64); room == "" {
		room = pairingRoom
	}
	if name = cleanLabel(name, 64); name == "" {
		name = p.name
	}
	s := m.pairings
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under the lock: another approver, a denial or the expiry may have won.
	if s.byID[p.id] != p || p.state != pairingPending || !m.now().Before(p.expires) {
		detail(w, http.StatusNotFound, "Pairing code not found or expired")
		return
	}
	p.state, p.household, p.room, p.nodeName, p.approvedBy = pairingApproved, hh, room, name, u.ID
	m.deps.Log.Info("cc: pairing approved", "pairing", p.id, "household", hh, "room", room, "by", u.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "approved", "code": displayPairingCode(p.code), "household_id": hh, "room": room, "name": name,
	})
}

// --- POST /nodes/pairings/deny (user JWT, a household member) ---

func (m *Module) handleDenyPairing(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	code, _ := b.str("code", true)
	if !b.done(w) {
		return
	}
	if _, err := m.requireAnyHousehold(r.Context(), u.ID); err != nil {
		m.writeErr(w, err)
		return
	}
	p, err := m.pendingByCode(u.ID, code)
	if err != nil {
		m.writePairingErr(w, err)
		return
	}
	s := m.pairings
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID[p.id] != p || p.state != pairingPending {
		detail(w, http.StatusNotFound, "Pairing code not found or expired")
		return
	}
	p.state = pairingDenied
	m.deps.Log.Info("cc: pairing denied", "pairing", p.id, "by", u.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "denied", "code": displayPairingCode(p.code)})
}
