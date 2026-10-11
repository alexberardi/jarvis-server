// Package voice is the agent's voice pipeline: microphone → 16 kHz decimation → openWakeWord →
// RMS-endpointed command recording → one voice turn against jarvisd (go-pi-node.md §1.1 rows
// 7-14) → reply playback → a follow-up window without the wake word (rows 16, 19) → listening
// again.
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

	"github.com/alexberardi/jarvis-server/internal/agent/node"
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
	ToolTimeout   time.Duration // bound on one client tool call (25 s, the MQTT dispatcher's)
	FollowUp      FollowUpConfig
}

// DefaultConfig is the Python node's behaviour (go-pi-node.md §1.1).
func DefaultConfig() Config {
	return Config{WakeThreshold: 0.4, Debounce: 8 * time.Second, Endpointing: DefaultEndpointing(),
		JoinWarmup: 10 * time.Second, StreamIdle: 8 * time.Second, Timezone: LocalTimezone(), Chimes: true,
		ToolTimeout: 25 * time.Second, FollowUp: DefaultFollowUp()}
}

// Outcome is how one exchange (an utterance and its reply) ended.
type Outcome string

const (
	OutcomeSpoken    Outcome = "spoken"     // a reply was played
	OutcomeNotForMe  Outcome = "not_for_me" // CC decided the speech wasn't addressed to Jarvis
	OutcomeNonSpeech Outcome = "non_speech" // STT heard nothing that looks like words
	OutcomeSilent    Outcome = "silent"     // "complete" with nothing to say
	OutcomeNoise     Outcome = "noise"      // follow-up only: a lone word or a repeat, not sent
	OutcomeEcho      Outcome = "self_echo"  // follow-up only: the mic heard our own reply
	OutcomeError     Outcome = "error"
)

// Exchange is one utterance and what came of it: the command after the wake word, or one
// answer in the follow-up window.
type Exchange struct {
	RecordedSecs float64
	Transcript   string
	Outcome      Outcome
	Message      string       // the final thing said back
	ReplySecs    float64      // reply audio played, validation questions included
	Tools        []ToolRun    // client tools run, in order
	Validations  []Validation // questions command-center asked, and the answers heard
	Err          error
}

// Validation is one validation_required question and the spoken answer sent back.
type Validation struct {
	Question, Answer string
}

// Turn is what happened after one wake (for logs, `jarvis-agent listen --once` and tests):
// the woken exchange, then any in the follow-up window. It is reported once the conversation
// is over.
type Turn struct {
	ConversationID string
	WakeScore      float32
	Threshold      float64 // silence threshold used for endpointing
	HitMax         bool
	Exchange                  // the command after the wake word
	FollowUps      []Exchange // answers in the follow-up window, in order
	FollowUpEnd    FollowUpEnd
	Took           time.Duration
}

// Pipeline is one microphone's always-on loop. Run it once.
type Pipeline struct {
	Cfg      Config
	Capture  device.Capture // owned by Run's capture goroutine until Run returns
	Playback PlaybackOpener
	Detector Detector
	Server   Server
	// Tools are the client tools offered at /conversation/start and run on tool_calls: the
	// same Toolset the node serves over MQTT. Nil offers none.
	Tools  node.Toolset
	Log    *slog.Logger
	OnTurn func(Turn) // optional; called after every turn, on the pipeline goroutine

	Now   func() time.Time // defaults to time.Now
	NewID func() string    // conversation ids; defaults to a random UUID

	// listening, when set, is told each time the pipeline starts listening without the wake
	// word ("validation", "follow_up"), after it has dropped what was captured while it spoke.
	// Tests use it to script the user's answer.
	listening func(stage string)

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

// turn runs one voice turn (go-pi-node.md §1.1 rows 6-19) and reports it.
func (p *Pipeline) turn(ctx context.Context, chunks <-chan []int16, score float32, wakeClip []int16, preRMS []float64) {
	start := p.Now()
	t := Turn{ConversationID: p.NewID(), WakeScore: score}
	log := p.Log.With("conversation_id", t.ConversationID)
	log.Info("wake", "score", fmt.Sprintf("%.3f", score))

	// Row 7: warm the conversation up while the user is still talking.
	st := &pendingStart{done: make(chan struct{})}
	go func() {
		defer close(st.done)
		st.err = p.Server.StartConversation(ctx, t.ConversationID, map[string]any{"timezone": p.Cfg.Timezone}, p.toolSchemas(ctx))
	}()
	// Row 14: always end it, once the turn (follow-ups included) and start are both over,
	// without holding up listening.
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

	s := &session{id: t.ConversationID, log: log, chunks: chunks, wakeClip: wakeClip}
	defer func() {
		t.Took = p.Now().Sub(start)
		lvl := slog.LevelInfo
		if t.Err != nil {
			lvl = slog.LevelWarn
		}
		log.Log(ctx, lvl, "voice turn", "outcome", t.Outcome, "transcript", t.Transcript, "reply", t.Message,
			"recorded_s", fmt.Sprintf("%.2f", t.RecordedSecs), "reply_s", fmt.Sprintf("%.2f", t.ReplySecs),
			"follow_ups", len(t.FollowUps), "follow_up_end", t.FollowUpEnd,
			"took", t.Took.Round(time.Millisecond), "err", t.Err)
		// A command's deferred step (a media command starting playback) runs once everything
		// has been said, as the Python node runs on_response_complete after its follow-up
		// loop. Off the pipeline goroutine: listening must not wait for it.
		for _, f := range s.onComplete {
			go func() {
				if err := f(); err != nil {
					log.Warn("on_response_complete failed", "err", err)
				}
			}()
		}
		if p.OnTurn != nil {
			p.OnTurn(t)
		}
	}()

	// Row 6: acknowledge, then record from after the chime (history_secs=0).
	p.chime(ctx, wakeChime)
	flush(chunks)

	// Row 8: record with RMS endpointing.
	t.Threshold = p.Cfg.Endpointing.Threshold(preRMS)
	s.threshold = t.Threshold
	pcm, hitMax, err := p.record(ctx, s)
	t.RecordedSecs, t.HitMax = float64(len(pcm))/CaptureRate, hitMax
	if err != nil {
		t.Outcome, t.Err = OutcomeError, err
		return
	}
	if len(pcm) == 0 {
		p.fail(ctx, &t.Exchange, errors.New("nothing was recorded"))
		return
	}

	end, ok := p.wakeExchange(ctx, s, &t.Exchange, st, score, pcm)
	if !ok {
		return
	}
	// Rows 16 and 19: keep listening without the wake word, as the Python node does after
	// every reply the model didn't mark terminal.
	t.FollowUpEnd = end
	if end == "" {
		p.followUp(ctx, s, &t)
	}
}

// wakeExchange is the woken command: STT, then the streamed turn and whatever it asks for.
// It returns why the follow-up window must stay shut ("" opens it), and false when the turn
// went wrong (no follow-up then either).
func (p *Pipeline) wakeExchange(ctx context.Context, s *session, ex *Exchange, st *pendingStart, score float32, pcm []int16) (FollowUpEnd, bool) {
	log := s.log
	// Row 9: STT. The command is normalised; speaker_audio is the wake clip + command.
	tr, ok := p.transcribe(ctx, s, ex, pcm)
	if !ok {
		return "", false
	}
	// Row 10. The Python node still opens the follow-up window after a wake that heard
	// nothing (the user may just have paused after "hey Jarvis").
	if IsNonSpeech(tr.Text) {
		ex.Outcome = OutcomeNonSpeech
		return "", true
	}

	// Row 11: join the warmup (bounded), then the streaming turn.
	select {
	case <-st.done:
		if st.err != nil {
			p.fail(ctx, ex, fmt.Errorf("conversation/start: %w", st.err))
			return "", false
		}
	case <-time.After(p.Cfg.JoinWarmup):
		log.Warn("conversation/start still running; sending the command anyway", "waited", p.Cfg.JoinWarmup)
	case <-ctx.Done():
		ex.Outcome, ex.Err = OutcomeError, ctx.Err()
		return "", false
	}
	conf := float64(score)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reply, err := p.Server.VoiceStream(sctx, StreamRequest{VoiceCommand: tr.Text, ConversationID: s.id,
		TurnSource: "wake", WakeConfidence: &conf, Affect: tr.Affect})
	if err != nil {
		p.fail(ctx, ex, fmt.Errorf("voice/command/stream: %w", err))
		return "", false
	}

	// Row 12: streamed speech. (A 200 carries no end_of_exchange, for Python either: the
	// window opens.)
	if reply.Audio != nil {
		ex.Message = reply.Audio.Message
		ex.ReplySecs, err = p.playAudio(sctx, cancel, reply.Audio)
		if err != nil {
			p.fail(ctx, ex, err)
			return "", false
		}
		ex.Outcome = OutcomeSpoken
		return "", true
	}
	// Rows 13, 18, 19: a control response.
	end := p.resolve(ctx, s, ex, reply.Control)
	return end, ex.Outcome != OutcomeError
}

// transcribe sends one recording to STT: the normalised command, and speaker_audio = the wake
// clip + the command (the same wake clip for every follow-up: they have no wake word of their
// own). Failures are reported on ex with the error chime.
func (p *Pipeline) transcribe(ctx context.Context, s *session, ex *Exchange, pcm []int16) (Transcript, bool) {
	cmd := Normalize(pcm)
	tr, err := p.Server.Transcribe(ctx, wavBytes(cmd), wavBytes(slices.Concat(s.wakeClip, cmd)), s.id)
	if err != nil {
		p.fail(ctx, ex, fmt.Errorf("transcribe: %w", err))
		return Transcript{}, false
	}
	ex.Transcript = strings.TrimSpace(tr.Text)
	if tr.Speaker != nil && tr.Speaker.UserID != nil {
		s.userID = tr.Speaker.UserID
	}
	return tr, true
}

// record is listen(): RMS endpointing from the next chunk on. A microphone that goes quiet
// for 1 s ends the recording early.
func (p *Pipeline) record(ctx context.Context, s *session) (pcm []int16, hitMax bool, err error) {
	rec := p.Cfg.Endpointing.newRecorder(s.threshold)
	for {
		c, err := recv(ctx, s.chunks, time.Second)
		if errors.Is(err, errChunkTimeout) {
			s.log.Warn("recording stopped: no audio for 1 s")
			break
		}
		if err != nil {
			return rec.pcm, rec.hitMax, err
		}
		if rec.add(c) {
			break
		}
	}
	return rec.pcm, rec.hitMax, nil
}

// fail ends an exchange in error, with the error chime unless the pipeline is stopping.
func (p *Pipeline) fail(ctx context.Context, ex *Exchange, err error) {
	ex.Outcome, ex.Err = OutcomeError, err
	if ctx.Err() == nil {
		p.chime(ctx, errorChime)
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
