package voice

import (
	"context"
	"sync"
)

// Music is the node's own music during a turn (internal/agent/music on a Pi; N:core/wake_loop.py
// and follow_up_loop.py): ducked when the wake word fires and before each follow-up listen,
// restored once the turn is over, before a command's deferred play starts.
type Music interface {
	Duck(ctx context.Context)
	Restore(ctx context.Context)
	// SetSelfPlaying records that the node's own music started (a deferred play ran).
	SetSelfPlaying(ctx context.Context, on bool, trigger string)
	// SelfPlaying is the cached "the node's own music is playing" (no pactl): it arms the
	// tentative wake (tentative.go) and goes to command-center with the wake.
	SelfPlaying() bool
}

// serial runs tasks off the pipeline goroutine, one at a time and in the order they were
// submitted (N:'s single background executor): a restore never overtakes the duck before it,
// and listening never waits for pactl.
type serial struct {
	mu   sync.Mutex
	last chan struct{}
}

func (q *serial) do(f func()) (done <-chan struct{}) {
	q.mu.Lock()
	prev, next := q.last, make(chan struct{})
	q.last = next
	q.mu.Unlock()
	go func() {
		defer close(next)
		if prev != nil {
			<-prev
		}
		f()
	}()
	return next
}

// background runs f on the pipeline's serial queue; Run waits for it before returning, so a
// stopping agent still restores the music. f's context outlives the turn's.
func (p *Pipeline) background(ctx context.Context, f func(ctx context.Context)) {
	ctx = context.WithoutCancel(ctx)
	p.ends.Add(1)
	p.bg.do(func() {
		defer p.ends.Done()
		f(ctx)
	})
}

// duck ducks the music, in the background (the wake fire, each follow-up listen).
func (p *Pipeline) duck(ctx context.Context) {
	if p.Music == nil {
		return
	}
	p.background(ctx, p.Music.Duck)
}

// restore puts the music back, in the background (a tentative wake that lapsed).
func (p *Pipeline) restore(ctx context.Context) {
	if p.Music == nil {
		return
	}
	p.background(ctx, p.Music.Restore)
}

// afterTurn restores the music, then runs the turn's deferred steps (a media command starting
// playback) once everything has been said, as N: runs on_response_complete after its follow-up
// loop: after the restore, so the new player's stream starts on the real sink.
func (p *Pipeline) afterTurn(ctx context.Context, s *session) {
	if p.Music == nil && len(s.onComplete) == 0 {
		return
	}
	p.background(ctx, func(ctx context.Context) {
		if p.Music != nil {
			p.Music.Restore(ctx)
		}
		started := false
		for _, f := range s.onComplete {
			if err := f(); err != nil {
				s.log.Warn("on_response_complete failed", "err", err)
				continue
			}
			started = true
		}
		if started && p.Music != nil {
			// Right for the next wake before its duck lists the streams; engages the mic gain
			// profile (we're between recordings here).
			p.Music.SetSelfPlaying(ctx, true, "deferred_play")
		}
	})
}
