package discord

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Live voice: the bot sits in one Discord voice channel and talks with the linked people in it,
// like a node at home. Discord only sends audio while someone is speaking, so a pause in the
// packets is the end of what they said. Each utterance goes through the same speech to text,
// chat and text to speech as everything else, and the reply plays into the channel. If the
// person speaks over it, it stops. Nothing is recorded to disk, and audio from anyone who is not
// linked is dropped before it is decoded.

// Voice tuning. The gap is how long a speaker is silent before their utterance is complete.
const (
	defaultSilenceGap = 800 * time.Millisecond
	minSpeech         = 350 * time.Millisecond // shorter than this is a cough, not a request
	maxUtterance      = 30 * time.Second       // longer is cut off and sent
	linkCacheTTL      = 15 * time.Second
	frameDuration     = 20 * time.Millisecond
)

// PCMDecoder turns an Opus packet into 48 kHz stereo samples (libopus, or a fake in tests).
type PCMDecoder interface {
	Decode(packet []byte) ([]int16, error)
	Close()
}

// Player plays Opus packets into the voice channel. Stop silences it at once.
type Player interface {
	Play(packets [][]byte)
	Stop()
	Playing() bool
}

// Voice runs the conversation in a voice channel. Frame feeds it audio; Tick (called about ten
// times a second by Run) completes utterances.
type Voice struct {
	HouseholdID string
	Store       *Store
	Chat        ChatFunc
	Hear        func(ctx context.Context, wav []byte) (string, error)
	WAV         func(pcm []int16) ([]byte, error)
	Synth       func(ctx context.Context, text string) ([][]byte, error)
	NewDecoder  func() (PCMDecoder, error)
	Player      Player
	Log         *slog.Logger
	Now         func() time.Time
	SilenceGap  time.Duration
	// OnHeard, when set, is told what was heard and said (the Discord side posts it in the text
	// channel so the conversation is visible).
	OnHeard func(userID, heard, reply string)

	mu       sync.Mutex
	speakers map[string]*speaker
	links    map[string]linkCache
	convs    map[string]string
	closed   bool
}

type speaker struct {
	dec     PCMDecoder
	pcm     []int16
	last    time.Time
	started time.Time
	cancel  context.CancelFunc // the turn in flight for this speaker, if any
	busy    bool
}

type linkCache struct {
	link    Link
	ok      bool
	expires time.Time
}

func (v *Voice) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Voice) log() *slog.Logger {
	if v.Log != nil {
		return v.Log
	}
	return slog.Default()
}

func (v *Voice) gap() time.Duration {
	if v.SilenceGap > 0 {
		return v.SilenceGap
	}
	return defaultSilenceGap
}

// allowed reports whether a Discord user is linked, caching the answer so 50 packets a second do
// not each query the database.
func (v *Voice) allowed(userID string) (Link, bool) {
	now := v.now()
	v.mu.Lock()
	c, ok := v.links[userID]
	v.mu.Unlock()
	if ok && now.Before(c.expires) {
		return c.link, c.ok
	}
	l, found, err := v.Store.ByDiscord(context.Background(), v.HouseholdID, userID)
	if err != nil {
		v.log().Warn("discord voice: link lookup failed", "err", err)
		return Link{}, false
	}
	v.mu.Lock()
	if v.links == nil {
		v.links = map[string]linkCache{}
	}
	v.links[userID] = linkCache{link: l, ok: found, expires: now.Add(linkCacheTTL)}
	v.mu.Unlock()
	return l, found
}

// Frame takes one Opus packet from a speaker.
func (v *Voice) Frame(userID string, opus []byte) {
	if _, ok := v.allowed(userID); !ok {
		return // a stranger's audio is never decoded, kept or heard
	}
	now := v.now()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	if v.speakers == nil {
		v.speakers = map[string]*speaker{}
	}
	sp := v.speakers[userID]
	if sp == nil {
		dec, err := v.NewDecoder()
		if err != nil {
			v.log().Warn("discord voice: no decoder", "err", err)
			return
		}
		sp = &speaker{dec: dec}
		v.speakers[userID] = sp
	}
	if len(sp.pcm) == 0 {
		sp.started = now
		// Speaking over Jarvis stops it, and drops the answer still being made for them.
		if v.Player != nil && v.Player.Playing() {
			v.Player.Stop()
		}
		if sp.cancel != nil {
			sp.cancel()
		}
	}
	out, err := sp.dec.Decode(opus)
	if err != nil {
		return
	}
	sp.pcm = append(sp.pcm, out...)
	sp.last = now
}

// Tick completes the utterances whose speaker has gone quiet (or talked for too long).
func (v *Voice) Tick() {
	now := v.now()
	type ready struct {
		id  string
		pcm []int16
	}
	var done []ready
	v.mu.Lock()
	for id, sp := range v.speakers {
		if len(sp.pcm) == 0 {
			continue
		}
		quiet := now.Sub(sp.last) >= v.gap()
		long := now.Sub(sp.started) >= maxUtterance
		if !quiet && !long {
			continue
		}
		pcm := sp.pcm
		sp.pcm = nil
		if time.Duration(len(pcm)/2)*time.Second/48000 < minSpeech {
			continue
		}
		done = append(done, ready{id, pcm})
	}
	v.mu.Unlock()
	for _, r := range done {
		go v.turn(r.id, r.pcm)
	}
}

// turn is one utterance: hear it, answer it, say the answer.
func (v *Voice) turn(userID string, pcm []int16) {
	link, ok := v.allowed(userID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnTimeout)
	defer cancel()
	v.mu.Lock()
	sp := v.speakers[userID]
	if sp == nil || v.closed {
		v.mu.Unlock()
		return
	}
	sp.cancel = cancel
	conv := v.convs[userID]
	v.mu.Unlock()

	wav, err := v.WAV(pcm)
	if err != nil {
		return
	}
	heard, err := v.Hear(ctx, wav)
	heard = strings.TrimSpace(heard)
	if err != nil || heard == "" || ctx.Err() != nil {
		return
	}
	reply, cid, err := v.Chat(ctx, link.UserID, v.HouseholdID, link.NodeID, conv, heard, nil)
	if ctx.Err() != nil {
		return // they spoke again: this answer is stale
	}
	if err != nil {
		v.log().Warn("discord voice: chat failed", "user", link.UserID, "err", err)
		reply = "Sorry, I couldn't do that."
	}
	if cid != "" {
		v.mu.Lock()
		if v.convs == nil {
			v.convs = map[string]string{}
		}
		v.convs[userID] = cid
		v.mu.Unlock()
	}
	if v.OnHeard != nil {
		v.OnHeard(userID, heard, reply)
	}
	packets, err := v.Synth(ctx, reply)
	if err != nil || ctx.Err() != nil || len(packets) == 0 {
		return
	}
	v.Player.Play(packets)
}

// Run ticks until ctx ends.
func (v *Voice) Run(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			v.Tick()
		}
	}
}

// Close stops everything: playback, turns in flight, decoders. The buffered audio is dropped.
func (v *Voice) Close() {
	v.mu.Lock()
	v.closed = true
	for _, sp := range v.speakers {
		if sp.cancel != nil {
			sp.cancel()
		}
		sp.dec.Close()
		sp.pcm = nil
	}
	v.speakers = nil
	v.mu.Unlock()
	if v.Player != nil {
		v.Player.Stop()
	}
}

// QueuePlayer is a Player backed by a queue of 20 ms packets that the Discord sender drains one
// at a time (ProvideOpusFrame). An empty queue yields nothing, which the sender treats as silence.
type QueuePlayer struct {
	mu sync.Mutex
	q  [][]byte
}

func (p *QueuePlayer) Play(packets [][]byte) {
	p.mu.Lock()
	p.q = append(p.q, packets...)
	p.mu.Unlock()
}

func (p *QueuePlayer) Stop() {
	p.mu.Lock()
	p.q = nil
	p.mu.Unlock()
}

func (p *QueuePlayer) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.q) > 0
}

// ProvideOpusFrame is voice.OpusFrameProvider: the next packet, or none when idle.
func (p *QueuePlayer) ProvideOpusFrame() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.q) == 0 {
		return nil, nil
	}
	f := p.q[0]
	p.q = p.q[1:]
	return f, nil
}

func (p *QueuePlayer) Close() { p.Stop() }
