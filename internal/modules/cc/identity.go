package cc

import (
	"context"
	"sync"
	"time"
)

// Per-conversation signals produced by the media proxy's transcription (docs/cc/06 §11
// "identity service"): who jarvisd identified in the turn's audio, and the wake-clip verdict.
// They are keyed by conversation id, not by cache entry, because the node transcribes while
// its /conversation/start is still in flight. Nothing is keyed per node (D3), and a record
// dies with its conversation (end, or the idle TTL).

const signalTTL = 15 * time.Minute

// turnIdentity is the speaker pass of one transcription.
type turnIdentity struct {
	UserID     int64 // 0: unknown, ambiguous (D21) or recognition off
	Outcome    string
	Confidence float64
	// RecognitionOff: the household has speaker recognition disabled (D35/M14).
	RecognitionOff bool
}

type signalEntry struct {
	identity *turnIdentity // the newest identity not yet consumed by a turn
	wake     *wakeVerdict
	wakeDone chan struct{} // non-nil while a verification is pending or done
	at       time.Time
}

type convSignals struct {
	mu  sync.Mutex
	m   map[string]*signalEntry
	now func() time.Time
}

func newConvSignals(now func() time.Time) *convSignals {
	if now == nil {
		now = time.Now
	}
	return &convSignals{m: map[string]*signalEntry{}, now: now}
}

func (s *convSignals) entry(id string) *signalEntry {
	e := s.m[id]
	if e == nil {
		e = &signalEntry{}
		s.m[id] = e
	}
	e.at = s.now()
	s.gcLocked()
	return e
}

func (s *convSignals) gcLocked() {
	now := s.now()
	for k, e := range s.m {
		if now.Sub(e.at) > signalTTL {
			delete(s.m, k)
		}
	}
}

// recordIdentity stores a transcription's speaker pass for the conversation.
func (s *convSignals) recordIdentity(convID string, id turnIdentity) {
	if convID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(convID).identity = &id
}

// takeIdentity consumes the identity recorded since the conversation's last turn (nil: this
// turn had no fresh transcription, e.g. a text-only turn).
func (s *convSignals) takeIdentity(convID string) *turnIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.m[convID]
	if e == nil {
		return nil
	}
	id := e.identity
	e.identity = nil
	return id
}

// beginWake marks a wake verification as running; finish delivers the verdict (nil = no
// verdict, fail open).
func (s *convSignals) beginWake(convID string) (finish func(*wakeVerdict)) {
	s.mu.Lock()
	e := s.entry(convID)
	done := make(chan struct{})
	e.wakeDone, e.wake = done, nil
	s.mu.Unlock()
	var once sync.Once
	return func(v *wakeVerdict) {
		once.Do(func() {
			s.mu.Lock()
			if e.wakeDone == done {
				e.wake = v
			}
			s.mu.Unlock()
			close(done)
		})
	}
}

// wake returns the conversation's wake verdict, waiting up to wait for a running
// verification (legacy polled ≤1.2 s; every miss fails open).
func (s *convSignals) wakeVerdict(ctx context.Context, convID string, wait time.Duration) *wakeVerdict {
	s.mu.Lock()
	e := s.m[convID]
	var done chan struct{}
	if e != nil {
		done = e.wakeDone
	}
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		return nil
	case <-ctx.Done():
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return e.wake
}

// storedWake returns the verdict without waiting (follow-up doubt propagation).
func (s *convSignals) storedWake(convID string) *wakeVerdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.m[convID]; e != nil {
		return e.wake
	}
	return nil
}

func (s *convSignals) drop(convID string) {
	s.mu.Lock()
	delete(s.m, convID)
	s.mu.Unlock()
}
