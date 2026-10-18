package voice

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

// ErrNotListening: no pipeline is running (voice is off, or the microphone is being reopened).
var ErrNotListening = errors.New("voice: not listening")

// Live reaches the running pipeline from outside it: the node's settings updates
// (update_node_config) and the room's noise level (measure_ambient_noise). One Live outlives
// the pipelines Serve opens and reopens; set it as Options.Live.
type Live struct {
	mu       sync.Mutex
	tunes    []func(*Config) // every change so far: a new pipeline replays them all
	attached int
	measure  chan measureReq
}

type measureReq struct {
	d     time.Duration
	reply chan measureResult // buffered: the pipeline never waits for a caller that left
}

type measureResult struct {
	rms []float64
	err error
}

func NewLive() *Live { return &Live{measure: make(chan measureReq)} }

// Tune changes the pipeline's configuration: the running one applies it before it scores its
// next chunk (never in the middle of a turn), and every later one when it starts.
func (l *Live) Tune(f func(*Config)) {
	l.mu.Lock()
	l.tunes = append(l.tunes, f)
	l.mu.Unlock()
}

// Measure is the RMS of each microphone chunk over d, taken by the running pipeline with wake
// detection paused (the Python node's AudioBus subscriber under wake_paused). It waits for
// the pipeline to be idle, so a turn in progress finishes first; ctx bounds the whole wait.
// An empty result means the microphone gave nothing for a second.
func (l *Live) Measure(ctx context.Context, d time.Duration) ([]float64, error) {
	l.mu.Lock()
	n := l.attached
	l.mu.Unlock()
	if n == 0 {
		return nil, ErrNotListening
	}
	r := measureReq{d: d, reply: make(chan measureResult, 1)}
	select {
	case l.measure <- r:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case res := <-r.reply:
		return res.rms, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *Live) attach() {
	l.mu.Lock()
	l.attached++
	l.mu.Unlock()
}

func (l *Live) detach() {
	l.mu.Lock()
	l.attached--
	l.mu.Unlock()
}

// tune applies the changes made since the last call to p's configuration (p's goroutine).
func (p *Pipeline) tune() {
	if p.Live == nil {
		return
	}
	p.Live.mu.Lock()
	defer p.Live.mu.Unlock()
	for ; p.tuned < len(p.Live.tunes); p.tuned++ {
		p.Live.tunes[p.tuned](&p.Cfg)
	}
}

// measureRoom answers one Measure: round(d / 80 ms) chunks, or fewer if the mic stalls.
func (p *Pipeline) measureRoom(ctx context.Context, chunks <-chan []int16, r measureReq) error {
	n := max(1, int(math.Round(r.d.Seconds()/chunkSecs)))
	rms := make([]float64, 0, n)
	for range n {
		c, err := recv(ctx, chunks, time.Second)
		if errors.Is(err, errChunkTimeout) {
			break
		}
		if err != nil {
			r.reply <- measureResult{err: err}
			return err
		}
		rms = append(rms, RMS(c))
	}
	p.Log.Debug("voice: measured the room", "chunks", len(rms))
	r.reply <- measureResult{rms: rms}
	return nil
}
