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
	// Greeting plays command-center's wake greeting instead of the wake chime when one is
	// cached (greeting.go; the Pi's). Off with the chimes.
	Greeting    bool
	ToolTimeout time.Duration // bound on one client tool call (25 s, the MQTT dispatcher's)
	FollowUp    FollowUpConfig
	BargeIn     BargeInConfig
	Tentative   TentativeConfig
	PreWakeVAD  PreWakeVAD
	// WakeThresholdAuto calibrates the wake threshold from Pipeline.WakeScores
	// (wake_word_threshold_auto; off: WakeThreshold as set).
	WakeThresholdAuto bool
	NotForMe          NotForMeConfig // the wake gate's cool-down after not_for_me
}

// DefaultConfig is the Python node's behaviour (go-pi-node.md §1.1).
func DefaultConfig() Config {
	return Config{WakeThreshold: 0.4, Debounce: 8 * time.Second, Endpointing: DefaultEndpointing(),
		JoinWarmup: 10 * time.Second, StreamIdle: 8 * time.Second, Timezone: LocalTimezone(), Chimes: true,
		ToolTimeout: 25 * time.Second, FollowUp: DefaultFollowUp(), BargeIn: DefaultBargeIn(),
		Tentative: DefaultTentative(), PreWakeVAD: DefaultPreWakeVAD(), NotForMe: DefaultNotForMe()}
}

// Outcome is how one exchange (an utterance and its reply) ended.
type Outcome string

const (
	OutcomeSpoken      Outcome = "spoken"      // a reply was played
	OutcomeNotForMe    Outcome = "not_for_me"  // CC decided the speech wasn't addressed to Jarvis
	OutcomeNonSpeech   Outcome = "non_speech"  // STT heard nothing that looks like words
	OutcomeSilent      Outcome = "silent"      // "complete" with nothing to say
	OutcomeNoise       Outcome = "noise"       // follow-up only: an exact repeat, not sent
	OutcomeEcho        Outcome = "self_echo"   // follow-up only: the mic heard our own reply
	OutcomeIgnored     Outcome = "ignored"     // follow-up only: CC's gate said not for me, keep listening
	OutcomeInterrupted Outcome = "interrupted" // the user talked over the reply (barge-in): it was cut off
	OutcomeError       Outcome = "error"
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
	PreRollSecs    float64 // of RecordedSecs, the audio kept from under the wake chime
	// PreWakeSpeechSecs is the speech heard in the 5 s before the wake (PreWakeVAD).
	PreWakeSpeechSecs float64
	// Tentative: the fire completed a tentative wake (the music was ducked before it).
	Tentative   bool
	HitMax      bool
	Exchange               // the command after the wake word
	FollowUps   []Exchange // answers in the follow-up window, in order
	FollowUpEnd FollowUpEnd
	Took        time.Duration
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
	// Indicator, when set, is shown the pipeline's state (indicator.go). Nil shows nothing.
	Indicator Indicator
	// Vocabulary, when set, is the names speech-to-text should expect (a desktop's apps), sent
	// as node_context.stt_vocabulary at each conversation start for whisper's prompt. Never
	// the LLM's (DESIGN.md §14).
	Vocabulary func(ctx context.Context) string
	// Live, when set, lets the node retune and measure this pipeline while it runs (live.go).
	Live *Live
	// Mic, when set, is how others record through this pipeline's microphone (take.go).
	Mic *Mic
	// Music, when set, is ducked for each turn and restored after it (music.go).
	Music Music
	// Computer, when set, is paused by the wake word while Jarvis controls the computer or
	// records, stopped by a "stop" turn and resumed after any other (computer.go).
	Computer Computer
	// WakeScores, when set, records the score of each legitimate wake, for the calibrated
	// threshold (Config.WakeThresholdAuto).
	WakeScores *WakeScores

	Now   func() time.Time // defaults to time.Now
	NewID func() string    // conversation ids; defaults to a random UUID

	// listening, when set, is told each time the pipeline starts listening without the wake
	// word ("validation", "follow_up"), after it has dropped what was captured while it spoke.
	// Tests use it to script the user's answer.
	listening func(stage string)
	// lockstep makes the capture goroutine hand each chunk over before it reads the next (an
	// unbuffered queue, a blocking send): a scripted microphone is then heard in full and in
	// order however the scheduler runs the two goroutines. A real microphone does not wait,
	// so production keeps the bounded, dropping queue. Tests only.
	lockstep bool

	dropped atomic.Int64
	ends    sync.WaitGroup // pending /conversation/end calls and background music steps
	bg      serial         // the music steps, in order (music.go)
	tuned   int            // Live.tunes applied so far
	gate    wakeGate       // the debounce and the not_for_me cool-down (wakegate.go)
	// tentativeAt is the last tentative wake (tentative.go): its cool-down outlives a turn, so
	// a lyric dip can't strobe the music across wakes either.
	tentativeAt time.Time
	greet       greeter // the next wake greeting (greeting.go)
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
	if p.Live != nil {
		p.tune()
		p.Live.attach()
		defer p.Live.detach()
	}
	if p.Mic != nil {
		p.Mic.attached.Add(1)
		defer p.Mic.attached.Add(-1)
	}
	ctx, cancel := context.WithCancel(ctx)
	p.prefetchGreeting(ctx)
	queue := chunkQueue
	if p.lockstep {
		queue = 0
	}
	chunks := make(chan []int16, queue)
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

// holder is a Capture that can move to another device (device.Switch, the Pi's echo canceller):
// Hold keeps it where it is until Release.
type holder interface {
	Hold()
	Release()
}

// holdCapture keeps the microphone on its device while a recording runs: the echo canceller
// engages and disengages off the pipeline goroutine (afterTurn), and a command recorded half
// on the plain mic and half on the cancelled one would lose its words at the switch. The
// returned func releases it.
func (p *Pipeline) holdCapture() func() {
	h, ok := p.Capture.(holder)
	if !ok {
		return func() {}
	}
	h.Hold()
	return h.Release
}

func (p *Pipeline) capture(ctx context.Context, chunks chan []int16) error {
	for ctx.Err() == nil {
		buf := make([]int16, ChunkSamples)
		if err := p.Capture.Read(buf); err != nil {
			return fmt.Errorf("microphone: %w", err)
		}
		if p.lockstep {
			select {
			case chunks <- buf:
			case <-ctx.Done():
			}
			continue
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

// chimeTailChunks is how far into the recording the wake chime can still reach: the
// microphone's own buffering delivers its last frames after the speaker has drained.
const chimeTailChunks = 2

// takePreRoll empties the queue, keeping its last n chunks (the most recent audio) in order.
func takePreRoll(chunks <-chan []int16, n int) []int16 {
	var kept [][]int16
	for {
		select {
		case c, ok := <-chunks:
			if ok {
				kept = append(kept, c)
				if len(kept) > n {
					kept = kept[1:]
				}
				continue
			}
		default:
		}
		return slices.Concat(kept...)
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
	var tw tentative
	defer func() { p.tentativeEnd(ctx, &tw, "stopped") }() // the music back if we stop mid-window
	var measure chan measureReq
	if p.Live != nil {
		measure = p.Live.measure
	}
	// fresh starts listening again on new audio after a turn, a take or a room measurement:
	// what was captured meanwhile (our own reply, mostly) is neither scored nor counted as the
	// room's noise floor.
	fresh := func() {
		flush(chunks)
		pre.items, clip.items, pend = pre.items[:0], clip.items[:0], pend[:0]
		dec = audio.NewDecimator(decimation)
		p.Detector.Reset()
		tw = tentative{}
	}
	for {
		var chunk []int16
		select {
		case req := <-p.takes():
			p.tentativeEnd(ctx, &tw, "paused") // listening pauses: the music must not stay ducked
			pcm, err := p.take(ctx, chunks, req.take)
			req.done <- takeResult{pcm, err}
			if ctx.Err() != nil {
				return nil
			}
			fresh()
			continue
		case r := <-measure:
			// Wake detection is paused while the room is measured, then starts afresh.
			p.tentativeEnd(ctx, &tw, "paused")
			if err := p.measureRoom(ctx, chunks, r); err != nil {
				return err
			}
			fresh()
			continue
		case c, ok := <-chunks:
			if !ok {
				return context.Canceled // capture stopped (its error is reported by Run)
			}
			chunk = c
		case <-ctx.Done():
			return context.Canceled
		}
		p.tune()
		self := p.selfPlaying()
		clip.n = p.wakeClipLen(self)
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
			p.tentativeExpire(ctx, &tw, now, score)
			if float64(score) <= p.fireThreshold(&tw) || !p.fire(score) {
				p.tentativeTrigger(ctx, &tw, now, score, self)
				continue
			}
			p.Detector.Reset() // oww is reset after a fire
			// A fire inside a tentative's window completes it: the music is already ducked (the
			// turn's own duck keeps the first saved volume), and the clip reaches back past the
			// duck. Any other fire gets the usual 2 s.
			w := wakeFire{score: score, preRMS: slices.Clone(pre.items), selfPlaying: self,
				tentativeTriggered: tw.triggered, tentativeCompleted: tw.open}
			tw.open = false
			items := clip.items
			if !w.tentativeCompleted {
				items = items[max(0, len(items)-wakeClipChunks):]
			}
			w.clip = slices.Concat(items...)
			p.turn(ctx, chunks, w)
			if ctx.Err() != nil {
				return nil
			}
			fresh()
			break
		}
	}
}

// nodeContext is /conversation/start's node_context: the timezone, and the vocabulary.
func (p *Pipeline) nodeContext(ctx context.Context) map[string]any {
	nc := map[string]any{"timezone": p.Cfg.Timezone}
	if p.Vocabulary != nil {
		if v := p.Vocabulary(ctx); v != "" {
			nc["stt_vocabulary"] = v
		}
	}
	return nc
}

// pendingStart is /conversation/start running in the background from the moment of wake.
type pendingStart struct {
	done chan struct{}
	err  error
}

// wakeFire is what the wake loop knew when the wake word fired.
type wakeFire struct {
	score  float32
	clip   []int16   // the wake clip, ending in the chunk that fired
	preRMS []float64 // the pre-wake window's RMS, one per chunk
	// selfPlaying is the node's own music at the fire: one snapshot for everything this
	// wake decides and sends (N:core/wake_loop.py).
	selfPlaying                            bool
	tentativeTriggered, tentativeCompleted bool // tentative.go
}

// turn runs one voice turn (go-pi-node.md §1.1 rows 6-19) and reports it.
func (p *Pipeline) turn(ctx context.Context, chunks <-chan []int16, w wakeFire) {
	start := p.Now()
	score, preRMS := w.score, w.preRMS
	t := Turn{ConversationID: p.NewID(), WakeScore: score, Tentative: w.tentativeCompleted}
	log := p.Log.With("conversation_id", t.ConversationID)
	preSecs, vadThreshold, vadSource := p.Cfg.PreWakeVAD.SpeechSecs(preRMS, w.selfPlaying)
	t.PreWakeSpeechSecs = preSecs
	log.Info("wake", "score", fmt.Sprintf("%.3f", score), "pre_wake_speech_seconds", preSecs,
		"vad_threshold", fmt.Sprintf("%.1f", vadThreshold), "vad_threshold_source", vadSource,
		"buffered_frames", len(preRMS), "self_playback", w.selfPlaying,
		"tentative_triggered", w.tentativeTriggered, "tentative_completed", w.tentativeCompleted)
	h := p.holdComputer(log)   // first: the user is about to speak to Jarvis
	release := p.holdCapture() // the command and its follow-ups on one device (holdCapture)
	p.duck(ctx)                // the music down while the user speaks (music.go)

	// Row 7: warm the conversation up while the user is still talking.
	st := &pendingStart{done: make(chan struct{})}
	go func() {
		defer close(st.done)
		st.err = p.Server.StartConversation(ctx, t.ConversationID, p.nodeContext(ctx), p.toolSchemas(ctx))
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

	s := &session{id: t.ConversationID, log: log, chunks: chunks, wakeClip: w.clip, held: h,
		preWakeSpeech: preSecs, selfPlaying: w.selfPlaying}
	p.cue(CueListening, 0)
	defer func() {
		p.releaseComputer(s)
		if t.Err != nil || t.FollowUpEnd == EndError {
			p.cue(CueError, 0)
		} else {
			p.cue(CueIdle, 0)
		}
		t.Took = p.Now().Sub(start)
		lvl := slog.LevelInfo
		if t.Err != nil {
			lvl = slog.LevelWarn
		}
		log.Log(ctx, lvl, "voice turn", "outcome", t.Outcome, "transcript", t.Transcript, "reply", t.Message,
			"recorded_s", fmt.Sprintf("%.2f", t.RecordedSecs), "pre_roll_s", fmt.Sprintf("%.2f", t.PreRollSecs), "reply_s", fmt.Sprintf("%.2f", t.ReplySecs),
			"follow_ups", len(t.FollowUps), "follow_up_end", t.FollowUpEnd,
			"took", t.Took.Round(time.Millisecond), "err", t.Err)
		p.afterWake(log, &t, w.selfPlaying)
		release() // before the restore: what it switches applies from the next listen on
		// The music back, then a command's deferred step (music.go). Off the pipeline
		// goroutine: listening must not wait for it.
		p.afterTurn(ctx, s)
		p.prefetchGreeting(ctx) // the next wake's, now the model is idle
		if p.OnTurn != nil {
			p.OnTurn(t)
		}
	}()

	// Row 6: acknowledge, then record from after the chime, keeping up to PreRollSecs of what
	// was heard while it played (0: history_secs=0, all of it dropped). Under a spoken
	// greeting the mic heard a voice, which whisper would write down: none of it is kept.
	greeted := p.acknowledge(ctx)
	preRoll := frames(p.Cfg.Endpointing.PreRollSecs)
	if greeted {
		preRoll = 0
	}
	pre := takePreRoll(chunks, preRoll)

	// Row 8: record with RMS endpointing.
	t.Threshold = p.Cfg.Endpointing.Threshold(preRMS)
	s.threshold = t.Threshold
	pcm, hitMax, err := p.record(ctx, s)
	// The level reference leaves the pre-roll out: it may hold the chime's own echo.
	if db, ok := SpeechLevel(pcm, s.threshold); ok {
		s.wakeLevel = &db // the follow-up gate's level reference
	}
	if len(pcm) > 0 {
		pcm = slices.Concat(pre, pcm)
		t.PreRollSecs = float64(len(pre)) / CaptureRate
		if tail := pcm[:min(len(pcm), len(pre)+chimeTailChunks*ChunkSamples)]; greeted {
			clear(tail) // the greeting's last words, still in the mic's buffers
		} else if p.Cfg.Chimes { // the chime the mic heard, in the pre-roll or the first chunks after it (#358)
			muteChime(tail)
		}
	}
	t.RecordedSecs, t.HitMax = float64(len(pcm))/CaptureRate, hitMax
	if err != nil {
		t.Outcome, t.Err = OutcomeError, err
		return
	}
	if len(pcm) == 0 {
		p.fail(ctx, &t.Exchange, errors.New("nothing was recorded"))
		return
	}
	p.cue(CueThinking, 0)

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
	tr.Text = StripNonSpeech(tr.Text)
	if IsNonSpeech(tr.Text) {
		ex.Outcome = OutcomeNonSpeech
		return "", true
	}
	activity := p.computerTurn(s, tr.Text, false) // a stop phrase stops it locally, at once; the server still hears it
	if end, ok := p.direct(ctx, s, ex, tr.Text); ok {
		return end, true
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
	req := StreamRequest{VoiceCommand: tr.Text, ConversationID: s.id, TurnSource: "wake", WakeConfidence: &conf,
		SpeechLevel: s.wakeLevel, Affect: tr.Affect, Computer: activity,
		PreWakeSpeechSecs: &s.preWakeSpeech, SelfPlayback: &s.selfPlaying}
	if s.selfPlaying {
		req.SelfPlaybackKind = "music"
	}
	reply, err := p.Server.VoiceStream(sctx, req)
	if err != nil {
		p.fail(ctx, ex, fmt.Errorf("voice/command/stream: %w", err))
		return "", false
	}

	// Row 12: streamed speech. (A 200 carries no end_of_exchange, for Python either: the
	// window opens.)
	if reply.Audio != nil {
		ex.Message = reply.Audio.Message
		ex.ReplySecs, err = p.playAudio(sctx, cancel, reply.Audio, s)
		if errors.Is(err, errBargeIn) {
			ex.Outcome = OutcomeInterrupted
			return EndBargeIn, true
		}
		if err != nil {
			p.fail(ctx, ex, err)
			return "", false
		}
		ex.Outcome = OutcomeSpoken
		return "", true
	}
	// Rows 13, 18, 19: a control response.
	if reply.Control != nil && reply.Control.StoodDown {
		log.Info("standing down: another node answers this wake")
	}
	end := p.resolve(ctx, s, ex, reply.Control)
	return end, ex.Outcome != OutcomeError
}

// direct is funnel stage 1 (VOICE-FLOWS.md Q27): a clear one-step command the node's own tools
// carry out ("switch to the Claude tab"), with no model and no command-center turn. It reports
// like a silent desktop command: the island shows it, the next window waits for the next step,
// a list on the island opens the choice window. A result that asks to be spoken (Signals.Speak)
// is a spoken reply instead; end is then why the follow-up window stays shut ("" opens it).
func (p *Pipeline) direct(ctx context.Context, s *session, ex *Exchange, text string) (end FollowUpEnd, handled bool) {
	d, ok := p.Tools.(node.Director)
	if !ok {
		return "", false
	}
	out, ok := d.Direct(node.WithSpeaker(ctx, s.userID), text)
	if !ok {
		return "", false
	}
	sig := node.TakeSignals(out)
	success, _ := out["success"].(bool)
	choice, _ := out[node.ChoiceKey].(bool)
	ex.Outcome = OutcomeSilent
	ex.Tools = append(ex.Tools, ToolRun{Name: "direct", Args: map[string]any{"said": text}, Success: success})
	s.log.Info("direct: done on the node", "transcript", text, "success", success, "choice", choice)
	if msg, _ := out["message"].(string); sig.Speak && msg != "" {
		return p.say(ctx, s, ex, msg, ""), true
	}
	if choice {
		s.choice = true
		return "", true
	}
	if !success {
		p.chime(ctx, errorChime)
	}
	s.quietTurn = true
	return "", true
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
		p.level(c)
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

// speak says text. With a session, the user may talk over it (barge-in: errBargeIn).
func (p *Pipeline) speak(ctx context.Context, text string, s *session) (float64, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a, err := p.Server.Speak(sctx, text)
	if err != nil {
		return 0, fmt.Errorf("tts: %w", err)
	}
	return p.playAudio(sctx, cancel, a, s)
}

// playAudio streams PCM to the speaker as it arrives. A body silent for StreamIdle is
// abandoned (cancel aborts the request), as the Python node's 8 s inter-chunk timeout does.
//
// With a session (a reply, not a validation question or an error message: the Python node
// watches only replies) and barge-in on, the microphone is watched while it plays: a wake word
// over the reply aborts the request, stops the speaker without draining what it holds (Close
// drops it) and returns errBargeIn.
func (p *Pipeline) playAudio(ctx context.Context, cancel context.CancelFunc, a *Audio, s *session) (float64, error) {
	defer a.Close()
	pb, err := p.Playback(a.Rate, a.Channels)
	if err != nil {
		return 0, fmt.Errorf("speaker: %w", err)
	}
	defer pb.Close()
	p.cue(CueSpeaking, 0)
	var barged atomic.Bool
	if s != nil && p.Cfg.BargeIn.Enabled {
		stop := p.watchBargeIn(s.log, s.chunks, func() { barged.Store(true); cancel() })
		defer stop() // before pb.Close: the speaker stops once the watch is over
	}
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
				pcm := make([]int16, usable/2)
				for i := range pcm {
					pcm[i] = int16(binary.LittleEndian.Uint16(pending[2*i:]))
				}
				p.level(pcm)
				if err := pb.Write(pcm); err != nil {
					return float64(frames) / float64(a.Rate), fmt.Errorf("speaker: %w", err)
				}
				frames += usable / frameBytes
				pending = append(pending[:0], pending[usable:]...)
			}
		}
		if barged.Load() {
			return float64(frames) / float64(a.Rate), errBargeIn
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
	if err := p.play(chimeRate, 1, pcm); err != nil {
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
