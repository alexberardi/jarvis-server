// Package voice is the agent's voice pipeline: microphone → 16 kHz decimation → openWakeWord →
// RMS-endpointed command recording → one voice turn against jarvisd (go-pi-node.md §1.1 rows
// 7-14) → reply playback → listening again.
//
// It knows nothing about desktops: a Go Pi node runs the same pipeline with HAT devices.
// Behaviour that the Python node defines (thresholds, endpointing, what is uploaded) is ported
// from it with the same defaults, cited per function.
package voice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// wakeChunk is openWakeWord's step: 1280 samples at 16 kHz (oww.Chunk; not imported here so
// the pipeline builds where onnxruntime isn't embedded).
const wakeChunk = 1280

// chunkQueue bounds how much captured audio may wait for the pipeline (1.28 s). While a turn
// is being answered nobody reads, so the oldest audio is dropped and the rest flushed
// afterwards: listening resumes on fresh audio, as the Python node's paused wake loop does.
const chunkQueue = 16

// Detector scores 16 kHz audio for the wake word, one wakeChunk at a time (*oww.Detector).
type Detector interface {
	Process(chunk []int16) (float32, error)
	Reset()
}

// PlaybackOpener opens the speaker at a reply's format (TTS is 24 kHz; chimes too).
type PlaybackOpener func(rate, channels int) (device.Playback, error)

// Config is the pipeline's tuning. DefaultConfig is the Python node's.
type Config struct {
	WakeThreshold float64       // fire when the score is above this (wake_word_threshold 0.4)
	Debounce      time.Duration // no second fire this soon after one (8 s)
	Endpointing   Endpointing
	JoinWarmup    time.Duration // wait this long for /conversation/start before the turn (10 s)
	StreamIdle    time.Duration // give up on a reply stream silent this long (8 s)
	Timezone      string        // node_context.timezone (IANA name)
	Chimes        bool          // wake and error chimes
}

// DefaultConfig is the Python node's behaviour (go-pi-node.md §1.1).
func DefaultConfig() Config {
	return Config{WakeThreshold: 0.4, Debounce: 8 * time.Second, Endpointing: DefaultEndpointing(),
		JoinWarmup: 10 * time.Second, StreamIdle: 8 * time.Second, Timezone: LocalTimezone(), Chimes: true}
}

// Outcome is how a turn ended.
type Outcome string

const (
	OutcomeSpoken      Outcome = "spoken"      // a reply was played
	OutcomeNotForMe    Outcome = "not_for_me"  // CC decided the speech wasn't addressed to Jarvis
	OutcomeNonSpeech   Outcome = "non_speech"  // STT heard nothing that looks like words
	OutcomeSilent      Outcome = "silent"      // "complete" with nothing to say
	OutcomeUnsupported Outcome = "unsupported" // tool_calls / validation: later phases
	OutcomeError       Outcome = "error"
)

// Turn is what happened after one wake (for logs, `jarvis-agent listen --once` and tests).
type Turn struct {
	ConversationID string
	WakeScore      float32
	Threshold      float64 // silence threshold used for endpointing
	RecordedSecs   float64
	HitMax         bool
	Transcript     string
	Outcome        Outcome
	Message        string  // what was said back
	ReplySecs      float64 // reply audio played
	Took           time.Duration
	Err            error
}

// Pipeline is one microphone's always-on loop. Run it once.
type Pipeline struct {
	Cfg      Config
	Capture  device.Capture // owned by Run's capture goroutine until Run returns
	Playback PlaybackOpener
	Detector Detector
	Server   Server
	Log      *slog.Logger
	OnTurn   func(Turn) // optional; called after every turn, on the pipeline goroutine

	Now   func() time.Time // defaults to time.Now
	NewID func() string    // conversation ids; defaults to a random UUID

	dropped atomic.Int64
	ends    sync.WaitGroup
}

// Dropped counts captured chunks discarded because the pipeline was busy.
func (p *Pipeline) Dropped() int64 { return p.dropped.Load() }

var errChunkTimeout = errors.New("voice: no audio from the microphone for 1 s")

// Run captures and listens until ctx ends (nil) or the microphone fails (its error). It
// returns only after the capture goroutine has stopped reading, so the caller may then close
// Capture, and after pending /conversation/end calls have been sent.
func (p *Pipeline) Run(ctx context.Context) error {
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.NewID == nil {
		p.NewID = newUUID
	}
	if p.Log == nil {
		p.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(ctx)
	chunks := make(chan []int16, chunkQueue)
	var capErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(chunks)
		capErr = p.capture(ctx, chunks)
	}()
	err := p.listen(ctx, chunks)
	cancel()
	wg.Wait()
	p.ends.Wait()
	if capErr != nil {
		err = capErr // the queue closed because the microphone failed
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (p *Pipeline) capture(ctx context.Context, chunks chan []int16) error {
	for ctx.Err() == nil {
		buf := make([]int16, ChunkSamples)
		if err := p.Capture.Read(buf); err != nil {
			return fmt.Errorf("microphone: %w", err)
		}
		select {
		case chunks <- buf:
		default:
			// Full: drop the oldest chunk so what waits is the most recent audio.
			select {
			case <-chunks:
			default:
			}
			select {
			case chunks <- buf:
			default:
			}
			p.dropped.Add(1)
		}
	}
	return nil
}

// recv waits for the next chunk; timeout 0 waits indefinitely. A closed queue means capture
// stopped (its error is reported by Run).
func recv(ctx context.Context, chunks <-chan []int16, timeout time.Duration) ([]int16, error) {
	var tc <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		tc = t.C
	}
	select {
	case c, ok := <-chunks:
		if !ok {
			return nil, context.Canceled
		}
		return c, nil
	case <-ctx.Done():
		return nil, context.Canceled
	case <-tc:
		return nil, errChunkTimeout
	}
}

func flush(chunks <-chan []int16) {
	for {
		select {
		case _, ok := <-chunks:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// ring keeps the last n items.
type ring[T any] struct {
	n     int
	items []T
}

func (r *ring[T]) push(v T) {
	r.items = append(r.items, v)
	if len(r.items) > r.n {
		r.items = append(r.items[:0], r.items[len(r.items)-r.n:]...)
	}
}

// listen is the wake loop (N:core/wake_loop.py): per 80 ms chunk, the raw RMS into the
// pre-wake window and the chunk into the wake clip, then decimate and score.
func (p *Pipeline) listen(ctx context.Context, chunks <-chan []int16) error {
	dec := audio.NewDecimator(decimation)
	var pend []int16
	pre := &ring[float64]{n: preWakeChunks}
	clip := &ring[[]int16]{n: wakeClipChunks}
	var lastFire time.Time
	for {
		chunk, err := recv(ctx, chunks, 0)
		if err != nil {
			return err
		}
		pre.push(RMS(chunk))
		clip.push(chunk)
		pend = dec.Process(chunk, pend)
		for len(pend) >= wakeChunk {
			score, err := p.Detector.Process(pend[:wakeChunk])
			if err != nil {
				return fmt.Errorf("wake word: %w", err)
			}
			pend = append(pend[:0], pend[wakeChunk:]...)
			now := p.Now()
			if float64(score) <= p.Cfg.WakeThreshold || (!lastFire.IsZero() && now.Sub(lastFire) < p.Cfg.Debounce) {
				continue
			}
			lastFire = now
			p.Detector.Reset() // oww is reset after a fire
			p.turn(ctx, chunks, score, slices.Concat(clip.items...), slices.Clone(pre.items))
			if ctx.Err() != nil {
				return nil
			}
			// Back to listening on fresh audio: what was captured during the turn (our own
			// reply, mostly) is neither scored nor counted as the room's noise floor.
			flush(chunks)
			pre.items, clip.items, pend = pre.items[:0], clip.items[:0], pend[:0]
			dec = audio.NewDecimator(decimation)
			p.Detector.Reset()
			break
		}
	}
}

// pendingStart is /conversation/start running in the background from the moment of wake.
type pendingStart struct {
	done chan struct{}
	err  error
}

// turn runs one voice turn (go-pi-node.md §1.1 rows 6-15) and reports it.
func (p *Pipeline) turn(ctx context.Context, chunks <-chan []int16, score float32, wakeClip []int16, preRMS []float64) {
	start := p.Now()
	t := Turn{ConversationID: p.NewID(), WakeScore: score}
	log := p.Log.With("conversation_id", t.ConversationID)
	log.Info("wake", "score", fmt.Sprintf("%.3f", score))

	// Row 7: warm the conversation up while the user is still talking.
	st := &pendingStart{done: make(chan struct{})}
	go func() {
		defer close(st.done)
		st.err = p.Server.StartConversation(ctx, t.ConversationID, map[string]any{"timezone": p.Cfg.Timezone})
	}()
	// Row 14: always end it, once both the turn and start are over, without holding up
	// listening.
	turnDone := make(chan struct{})
	defer close(turnDone)
	p.ends.Add(1)
	go func() {
		defer p.ends.Done()
		<-turnDone
		<-st.done
		if err := p.Server.EndConversation(context.WithoutCancel(ctx), t.ConversationID); err != nil {
			log.Warn("conversation/end failed", "err", err)
		}
	}()

	defer func() {
		t.Took = p.Now().Sub(start)
		lvl := slog.LevelInfo
		if t.Err != nil {
			lvl = slog.LevelWarn
		}
		log.Log(ctx, lvl, "voice turn", "outcome", t.Outcome, "transcript", t.Transcript, "reply", t.Message,
			"recorded_s", fmt.Sprintf("%.2f", t.RecordedSecs), "reply_s", fmt.Sprintf("%.2f", t.ReplySecs),
			"took", t.Took.Round(time.Millisecond), "err", t.Err)
		if p.OnTurn != nil {
			p.OnTurn(t)
		}
	}()
	fail := func(err error) {
		t.Outcome, t.Err = OutcomeError, err
		if ctx.Err() == nil {
			p.chime(ctx, errorChime)
		}
	}

	// Row 6: acknowledge, then record from after the chime (history_secs=0).
	p.chime(ctx, wakeChime)
	flush(chunks)

	// Row 8: record with RMS endpointing.
	t.Threshold = p.Cfg.Endpointing.Threshold(preRMS)
	rec := p.Cfg.Endpointing.newRecorder(t.Threshold)
	for {
		c, err := recv(ctx, chunks, time.Second)
		if errors.Is(err, errChunkTimeout) {
			log.Warn("recording stopped: no audio for 1 s")
			break
		}
		if err != nil {
			t.Outcome, t.Err = OutcomeError, err
			return
		}
		if rec.add(c) {
			break
		}
	}
	t.RecordedSecs, t.HitMax = float64(len(rec.pcm))/CaptureRate, rec.hitMax
	if len(rec.pcm) == 0 {
		fail(errors.New("nothing was recorded"))
		return
	}

	// Row 9: STT. The command is normalised; speaker_audio is the wake clip + command.
	cmd := Normalize(rec.pcm)
	tr, err := p.Server.Transcribe(ctx, wavBytes(cmd), wavBytes(slices.Concat(wakeClip, cmd)), t.ConversationID)
	if err != nil {
		fail(fmt.Errorf("transcribe: %w", err))
		return
	}
	t.Transcript = strings.TrimSpace(tr.Text)
	// Row 10.
	if IsNonSpeech(tr.Text) {
		t.Outcome = OutcomeNonSpeech
		return
	}

	// Row 11: join the warmup (bounded), then the streaming turn.
	select {
	case <-st.done:
		if st.err != nil {
			fail(fmt.Errorf("conversation/start: %w", st.err))
			return
		}
	case <-time.After(p.Cfg.JoinWarmup):
		log.Warn("conversation/start still running; sending the command anyway", "waited", p.Cfg.JoinWarmup)
	case <-ctx.Done():
		t.Outcome, t.Err = OutcomeError, ctx.Err()
		return
	}
	conf := float64(score)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reply, err := p.Server.VoiceStream(sctx, StreamRequest{VoiceCommand: tr.Text, ConversationID: t.ConversationID,
		TurnSource: "wake", WakeConfidence: &conf, Affect: tr.Affect})
	if err != nil {
		fail(fmt.Errorf("voice/command/stream: %w", err))
		return
	}

	// Row 12: streamed speech.
	if reply.Audio != nil {
		t.Message = reply.Audio.Message
		t.ReplySecs, err = p.playAudio(sctx, cancel, reply.Audio)
		if err != nil {
			fail(err)
			return
		}
		t.Outcome = OutcomeSpoken
		return
	}

	// Row 13: a control response.
	c := reply.Control
	msg := ""
	if c.AssistantMessage != nil {
		msg = strings.TrimSpace(*c.AssistantMessage)
	}
	switch c.StopReason {
	case "not_for_me":
		t.Outcome = OutcomeNotForMe
	case "complete", "":
		if msg == "" {
			// The Python node says "Task completed." here (go-pi-node.md §1.4); say nothing.
			t.Outcome = OutcomeSilent
			return
		}
		t.Message = msg
		if t.ReplySecs, err = p.speak(ctx, msg); err != nil {
			fail(err)
			return
		}
		t.Outcome = OutcomeSpoken
	case "tool_calls", "validation_required":
		log.Warn("reply needs client tools or a follow-up question, which this node doesn't do yet",
			"stop_reason", c.StopReason, "tool_calls", len(c.ToolCalls))
		t.Outcome, t.Message = OutcomeUnsupported, msg
		p.chime(ctx, errorChime)
	default: // "error"
		t.Outcome, t.Err = OutcomeError, fmt.Errorf("command-center stopped with %q", c.StopReason)
		if msg != "" {
			t.Message = msg
			t.ReplySecs, _ = p.speak(ctx, msg)
		} else {
			p.chime(ctx, errorChime)
		}
	}
}

func (p *Pipeline) speak(ctx context.Context, text string) (float64, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a, err := p.Server.Speak(sctx, text)
	if err != nil {
		return 0, fmt.Errorf("tts: %w", err)
	}
	return p.playAudio(sctx, cancel, a)
}

// playAudio streams PCM to the speaker as it arrives. A body silent for StreamIdle is
// abandoned (cancel aborts the request), as the Python node's 8 s inter-chunk timeout does.
func (p *Pipeline) playAudio(ctx context.Context, cancel context.CancelFunc, a *Audio) (float64, error) {
	defer a.Close()
	pb, err := p.Playback(a.Rate, a.Channels)
	if err != nil {
		return 0, fmt.Errorf("speaker: %w", err)
	}
	defer pb.Close()
	var idle atomic.Bool
	timer := time.AfterFunc(p.Cfg.StreamIdle, func() { idle.Store(true); cancel() })
	defer timer.Stop()
	frameBytes := 2 * a.Channels
	var pending []byte
	buf := make([]byte, 8192)
	frames := 0
	for {
		n, rerr := a.Body.Read(buf)
		if n > 0 {
			timer.Reset(p.Cfg.StreamIdle)
			pending = append(pending, buf[:n]...)
			if usable := len(pending) - len(pending)%frameBytes; usable > 0 {
				s := make([]int16, usable/2)
				for i := range s {
					s[i] = int16(binary.LittleEndian.Uint16(pending[2*i:]))
				}
				if err := pb.Write(s); err != nil {
					return float64(frames) / float64(a.Rate), fmt.Errorf("speaker: %w", err)
				}
				frames += usable / frameBytes
				pending = append(pending[:0], pending[usable:]...)
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			secs := float64(frames) / float64(a.Rate)
			if idle.Load() {
				return secs, fmt.Errorf("reply stream idle for %s", p.Cfg.StreamIdle)
			}
			return secs, fmt.Errorf("reply stream: %w", rerr)
		}
	}
	secs := float64(frames) / float64(a.Rate)
	if err := pb.Drain(); err != nil {
		return secs, fmt.Errorf("speaker: %w", err)
	}
	return secs, nil
}

// chime plays a synthesised cue; a speaker failure is logged, never fatal to the turn.
func (p *Pipeline) chime(ctx context.Context, pcm []int16) {
	if !p.Cfg.Chimes || ctx.Err() != nil {
		return
	}
	pb, err := p.Playback(chimeRate, 1)
	if err != nil {
		p.Log.Warn("chime: speaker unavailable", "err", err)
		return
	}
	defer pb.Close()
	err = pb.Write(pcm)
	if err == nil {
		err = pb.Drain()
	}
	if err != nil {
		p.Log.Warn("chime failed", "err", err)
	}
}

// wavBytes is a 48 kHz mono 16-bit WAV, what the Python node uploads.
func wavBytes(pcm []int16) []byte {
	b := make([]byte, 2*len(pcm))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	var out bytes.Buffer
	_ = audio.WritePCM16WAV(&out, b, 1, CaptureRate)
	return out.Bytes()
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
