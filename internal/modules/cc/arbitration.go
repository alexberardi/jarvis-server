package cc

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Wake arbitration (loop #287, docs/decisions/2026-10-19-wake-arbitration.md): one "Hey Jarvis"
// heard by several nodes of a household (a phone, a desktop and a Pi in one room) opens a wake
// turn on each. Each turn bids here; the turns of one wake (same household, other nodes, close
// in time, the same request) form a group, one of them answers and the rest stand down silently
// (not_for_me). A wake turn waits only when another node of its household woke within the
// window and has not sent its command yet, so a lone node never waits.
//
// Pick order: a node mid-activity for the request (Busy: recording on it, or the follow-up of
// its own conversation) answers at once; else the best wake score, then the loudest speech
// level, among nodes that report them; else the first to arrive.

// wakeArbitrationKeep is how long starts and groups are kept for late bids (well over any window).
const wakeArbitrationKeep = 30 * time.Second

// wakeArbitrationMax caps the window setting: a wake turn never waits longer.
const wakeArbitrationMax = 10 * time.Second

// recordingBusyFor is how long a recording counts as under way without a stop: the agent
// stops by itself after 30 minutes.
const recordingBusyFor = 30 * time.Minute

// arbitrateWake bids conv's wake (or follow-up) turn and reports whether this node stands
// down. Other turns (typed chat, a node that doesn't say) never arbitrate.
func (m *Module) arbitrateWake(ctx context.Context, conv *conversation, in turnInput) (bool, error) {
	if in.Source != "wake" && in.Source != "follow_up" {
		return false, nil
	}
	secs := m.settings.Float(ctx, settingWakeArbitration, settings.Scope{HouseholdID: conv.householdID})
	window := min(time.Duration(secs*float64(time.Second)), wakeArbitrationMax)
	b := m.wakeBid(conv, in)
	if wait, ok := ctx.Value(turnWaitKey{}).(context.Context); ok {
		ctx = wait
	}
	v, err := m.wakes.bid(ctx, b, window)
	if err != nil {
		return false, err
	}
	if v.StandDown {
		m.deps.Log.Info("cc: wake_arbitration stood_down", "conversation_id", conv.id, "node_id", conv.nodeID,
			"winner_node_id", v.Winner.Node, "winner_conversation_id", v.Winner.ConversationID, "nodes", v.Members,
			"waited_ms", v.Waited.Milliseconds(), "wake_confidence", fptr(in.WakeConfidence), "speech_level_db", fptr(in.SpeechLevel),
			"transcript", in.VoiceCommand)
	} else if v.Members > 1 || v.Waited > 0 {
		m.deps.Log.Info("cc: wake_arbitration answers", "conversation_id", conv.id, "node_id", conv.nodeID,
			"nodes", v.Members, "waited_ms", v.Waited.Milliseconds(), "busy", b.Busy)
	}
	return v.StandDown, nil
}

// wakeBid is conv's bid for in. Busy: a follow-up, or a recording on the node (a voice
// start_recording, or the agent's computer_activity, #288).
func (m *Module) wakeBid(conv *conversation, in turnInput) wakeBid {
	return wakeBid{Household: conv.householdID, Node: conv.nodeID, ConversationID: conv.id, Transcript: in.VoiceCommand,
		Busy:  in.Source == "follow_up" || in.Computer.recording() || m.nodeRecording(conv.nodeID),
		Score: in.WakeConfidence, Level: in.SpeechLevel}
}

// nodeRecording: a voice start_recording is under way on the node.
func (m *Module) nodeRecording(nodeID string) bool {
	at, ok := m.recordingOn.Load(nodeID)
	return ok && m.now().Sub(at.(time.Time)) < recordingBusyFor
}

func fptr(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// wakeBid is one turn asking to answer.
type wakeBid struct {
	Household, Node, ConversationID, Transcript string
	// Busy: the node is mid-activity for this request; it answers without waiting.
	Busy bool
	// Score is the node's wake score (wake_confidence), Level its speech level in dBFS
	// (speech_level_db); nil when the node didn't report it.
	Score, Level *float64

	arrived  time.Time
	woke     time.Time // its conversation's start; zero when none was seen
	withdrew bool      // the caller left before the group was decided
}

// arbVerdict is a bid's outcome.
type arbVerdict struct {
	StandDown bool
	Winner    wakeBid
	Members   int           // bids in the group when it was decided (or when this one joined late)
	Waited    time.Duration // how long this bid was held
}

type wakeStart struct {
	household, node string
	at              time.Time
	turned          bool // its conversation has bid
}

type wakeGroup struct {
	household string
	first     time.Time // first bid's arrival
	window    time.Duration
	members   []*wakeBid
	winner    *wakeBid
	done      chan struct{} // closed when decided
}

type wakeArbiter struct {
	mu     sync.Mutex
	now    func() time.Time
	after  func(time.Duration) <-chan time.Time
	starts map[string]*wakeStart // by conversation id
	groups []*wakeGroup
}

func newWakeArbiter(now func() time.Time, after func(time.Duration) <-chan time.Time) *wakeArbiter {
	if after == nil {
		after = time.After
	}
	return &wakeArbiter{now: now, after: after, starts: map[string]*wakeStart{}}
}

// started records a node's conversation start: a node fires it at wake time, before it has a
// transcript, so it is the earliest sign that a node woke.
func (a *wakeArbiter) started(household, node, convID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.prune(now)
	a.starts[convID] = &wakeStart{household: household, node: node, at: now}
}

// bid joins b's wake group and returns whether it answers. It waits (up to window after the
// group's first bid) only while another node of the household that woke with it has yet to
// bid. window <= 0 turns arbitration off. A cancelled ctx withdraws the bid.
func (a *wakeArbiter) bid(ctx context.Context, b wakeBid, window time.Duration) (arbVerdict, error) {
	if window <= 0 {
		return arbVerdict{Winner: b, Members: 1}, nil
	}
	a.mu.Lock()
	now := a.now()
	a.prune(now)
	me := b
	me.arrived = now
	if s := a.starts[b.ConversationID]; s != nil {
		me.woke = s.at
		s.turned = true
	}
	g := a.groupFor(&me, window)
	if g == nil {
		g = &wakeGroup{household: b.Household, first: now, window: window, done: make(chan struct{})}
		a.groups = append(a.groups, g)
	}
	g.members = append(g.members, &me)
	if g.winner != nil {
		v := a.verdict(g, &me, 0)
		if me.Busy { // the conversation's own node is never silenced; it joined too late to win
			v.StandDown = false
		}
		a.mu.Unlock()
		return v, nil
	}
	if me.Busy {
		a.decide(g)
	}
	a.release(b.Household) // its node is no longer pending
	if g.winner != nil {
		v := a.verdict(g, &me, 0)
		a.mu.Unlock()
		return v, nil
	}
	wait := g.first.Add(window).Sub(now)
	a.mu.Unlock()

	select {
	case <-g.done:
	case <-a.after(wait):
	case <-ctx.Done():
		a.mu.Lock()
		defer a.mu.Unlock()
		if g.winner == nil {
			me.withdrew = true
			return arbVerdict{}, ctx.Err()
		}
		return a.verdict(g, &me, a.now().Sub(now)), nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if g.winner == nil {
		a.decide(g)
	}
	return a.verdict(g, &me, a.now().Sub(now)), nil
}

// dropped: convID's turn was dropped by its own node's gates (noise, an unverified wake, the
// follow-up gate) and never bids, so no group waits for it.
func (a *wakeArbiter) dropped(convID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.starts[convID]; s != nil && !s.turned {
		s.turned = true
		a.release(s.household)
	}
}

// release decides each waiting group of the household that no longer waits for a node.
func (a *wakeArbiter) release(household string) {
	for _, o := range a.groups {
		if o.winner == nil && o.household == household && !a.pending(o, o.window) {
			a.decide(o)
		}
	}
}

func (a *wakeArbiter) verdict(g *wakeGroup, me *wakeBid, waited time.Duration) arbVerdict {
	return arbVerdict{StandDown: g.winner != me, Winner: *g.winner, Members: len(g.members), Waited: waited}
}

// groupFor is the group b belongs to: same household, no bid from b's node yet, the same
// request, and close in time (arrivals within window, or conversation starts within window).
func (a *wakeArbiter) groupFor(b *wakeBid, window time.Duration) *wakeGroup {
	for _, g := range a.groups {
		if g.household != b.Household || !sameRequest(g.members[0].Transcript, b.Transcript) {
			continue
		}
		near, sameNode := absDur(b.arrived.Sub(g.first)) <= window, false
		for _, m := range g.members {
			sameNode = sameNode || m.Node == b.Node
			near = near || (!m.woke.IsZero() && !b.woke.IsZero() && absDur(m.woke.Sub(b.woke)) <= window)
		}
		if near && !sameNode {
			return g
		}
	}
	return nil
}

// pending: another node of g's household woke within window of one of g's bids and has not
// bid yet, so g waits for it.
func (a *wakeArbiter) pending(g *wakeGroup, window time.Duration) bool {
	for _, s := range a.starts {
		if s.turned || s.household != g.household {
			continue
		}
		mine := false
		for _, m := range g.members {
			mine = mine || m.Node == s.node
		}
		if mine {
			continue
		}
		for _, m := range g.members {
			ref := m.woke
			if ref.IsZero() {
				ref = m.arrived
			}
			if absDur(s.at.Sub(ref)) <= window {
				return true
			}
		}
	}
	return false
}

// decide picks g's winner among the bids still present and closes g.done.
func (a *wakeArbiter) decide(g *wakeGroup) {
	var best *wakeBid
	for _, m := range g.members {
		if !m.withdrew && (best == nil || betterBid(m, best)) {
			best = m
		}
	}
	if best == nil { // everyone left: the next bid decides
		return
	}
	g.winner = best
	close(g.done)
}

// betterBid reports whether x beats y (y arrived first): busy, then wake score, then speech
// level (each only when both report it and they differ), else y keeps it.
func betterBid(x, y *wakeBid) bool {
	if x.Busy != y.Busy {
		return x.Busy
	}
	if x.Score != nil && y.Score != nil && *x.Score != *y.Score {
		return *x.Score > *y.Score
	}
	if x.Level != nil && y.Level != nil && *x.Level != *y.Level {
		return *x.Level > *y.Level
	}
	return false
}

func (a *wakeArbiter) prune(now time.Time) {
	for id, s := range a.starts {
		if now.Sub(s.at) > wakeArbitrationKeep {
			delete(a.starts, id)
		}
	}
	kept := a.groups[:0]
	for _, g := range a.groups {
		if now.Sub(g.first) <= wakeArbitrationKeep {
			kept = append(kept, g)
		}
	}
	clear(a.groups[len(kept):])
	a.groups = kept
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// --- the same request ---

// sameRequest reports whether two nodes' transcripts are one utterance heard twice: equal once
// normalised (case, punctuation, a leading wake phrase, number words, plural s), allowing one
// near-miss word per five beyond the first five (STT differs a little between microphones;
// "turn on" and "turn off" still differ).
func sameRequest(a, b string) bool {
	ta, tb := requestTokens(a), requestTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	allowed := (max(len(ta), len(tb)) - 1) / 5
	return tokenDistance(ta, tb) <= allowed
}

var wakeWords = map[string]bool{"hey": true, "hi": true, "ok": true, "okay": true, "jarvis": true}

var numberWords = map[string]string{"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5",
	"six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10", "eleven": "11", "twelve": "12",
	"fifteen": "15", "twenty": "20", "thirty": "30", "forty": "40", "fifty": "50", "sixty": "60"}

func requestTokens(s string) []string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	var out []string
	for _, w := range words {
		w = strings.ReplaceAll(w, "'", "")
		if len(out) == 0 && wakeWords[w] {
			continue
		}
		if n, ok := numberWords[w]; ok {
			w = n
		}
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			w = strings.TrimSuffix(w, "s")
		}
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// tokenDistance is the word-level edit distance, where swapping two near-identical words
// ("time"/"timer") costs nothing.
func tokenDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			sub := 1
			if nearWord(a[i-1], b[j-1]) {
				sub = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+sub)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// nearWord: equal, or one edit apart and at least four letters long.
func nearWord(a, b string) bool {
	if a == b {
		return true
	}
	if min(len(a), len(b)) < 4 {
		return false
	}
	return charDistance(a, b) <= 1
}

func charDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			sub := 1
			if ra[i-1] == rb[j-1] {
				sub = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+sub)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
