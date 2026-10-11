package voice

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The follow-up window (go-pi-node.md §1.1 row 16), ported from the Python node's
// follow_up_loop (N:core/follow_up_loop.py) and listen_for_follow_up
// (N:scripts/speech_to_text.py:455-691) with their defaults.
//
// After every reply the model didn't mark terminal, the node keeps listening without the wake
// word: wait for the room to go quiet (the reply's tail), then for speech onset, record until
// a short silence, transcribe, and send it as a follow_up turn of the same conversation. Up
// to 5 times, each window shorter than the last. It closes quietly when nobody speaks, on
// noise, on the node hearing itself, or when command-center ends the exchange.
//
// Time is counted in captured chunks (80 ms each), not wall-clock time: the same thing on a
// real microphone, and deterministic on a scripted one.

// FollowUpConfig is the follow-up window's tuning. DefaultFollowUp is the Python node's.
type FollowUpConfig struct {
	ListenSecs        float64 // follow_up_listen_seconds: the first window (4 s; 0 turns follow-up off)
	MaxIterations     int     // max_follow_up_iterations (5)
	DecaySecs         float64 // follow_up_timeout_decay_secs: each window this much shorter (2 s)
	MinTimeoutSecs    float64 // follow_up_min_timeout_secs: but never under this (3 s)
	QuietWaitSecs     float64 // follow_up_quiet_wait_secs: at most this long draining the reply's tail (0.5 s)
	SilenceSecs       float64 // follow_up_silence_duration: silence that ends an answer (0.5 s)
	MinAfterOnsetSecs float64 // follow_up_min_record_after_onset_secs (0.7 s)
	MinSpeechSecs     float64 // follow_up_min_speech_secs: shorter recordings are noise (0.3 s)
}

// DefaultFollowUp is the Python node's configuration out of the box.
func DefaultFollowUp() FollowUpConfig {
	return FollowUpConfig{ListenSecs: 4, MaxIterations: 5, DecaySecs: 2, MinTimeoutSecs: 3,
		QuietWaitSecs: 0.5, SilenceSecs: 0.5, MinAfterOnsetSecs: 0.7, MinSpeechSecs: 0.3}
}

// Fixed parts of the Python node's follow-up listening.
const (
	quietConsecutiveSecs = 0.16 // listen_quiet_consecutive_secs: quiet this long = the tail is over
	quietFactor          = 1.5  // listen_quiet_rms_threshold = silence threshold × 1.5
	onsetBase            = 3    // chunks above the threshold that make speech onset...
	onsetMax             = 8    // ...one more per iteration, up to this
	preOnsetKeepSecs     = 1.5  // audio kept from before onset
	// validationQuietWaitSecs is listen()'s listen_quiet_max_wait_secs, which the Python
	// validation handler uses before recording an answer.
	validationQuietWaitSecs = 0.8
	// maxConsecutiveNoise noise transcriptions in a row close the window.
	maxConsecutiveNoise = 2
)

// FollowUpEnd is why the follow-up window closed (or never opened).
type FollowUpEnd string

const (
	EndTimeout       FollowUpEnd = "timeout"         // nobody spoke
	EndNonSpeech     FollowUpEnd = "non_speech"      // STT heard no words
	EndNoise         FollowUpEnd = "noise"           // two noise transcriptions in a row
	EndEcho          FollowUpEnd = "self_echo"       // the mic heard our own reply
	EndOfExchange    FollowUpEnd = "end_of_exchange" // the model marked its reply terminal
	EndClearHistory  FollowUpEnd = "clear_history"   // a command finished the exchange
	EndNotForMe      FollowUpEnd = "not_for_me"      // command-center: not addressed to Jarvis
	EndMaxIterations FollowUpEnd = "max_iterations"
	EndDisabled      FollowUpEnd = "disabled" // FollowUp.ListenSecs <= 0
	EndError         FollowUpEnd = "error"
)

// followUp runs the follow-up window after the woken exchange, recording each answer in
// t.FollowUps and why it closed in t.FollowUpEnd.
func (p *Pipeline) followUp(ctx context.Context, s *session, t *Turn) {
	cfg := p.Cfg.FollowUp
	if cfg.ListenSecs <= 0 {
		t.FollowUpEnd = EndDisabled
		return
	}
	lastSaid := t.Message // for the self-echo check
	prev := ""
	noise := 0
	for it := 1; ; it++ {
		if it > cfg.MaxIterations {
			t.FollowUpEnd = EndMaxIterations
			return
		}
		timeout := max(cfg.MinTimeoutSecs, cfg.ListenSecs-float64(it-1)*cfg.DecaySecs)
		s.log.Info("follow-up: listening", "iteration", it, "window_s", timeout)
		pcm, err := p.listenFollowUp(ctx, s, timeout, it)
		if err != nil {
			t.FollowUpEnd = EndError
			if ctx.Err() == nil {
				s.log.Warn("follow-up: listening failed", "err", err)
			}
			return
		}
		if pcm == nil {
			s.log.Info("follow-up: window closed, nobody spoke", "iteration", it)
			t.FollowUpEnd = EndTimeout
			return
		}
		ex := Exchange{RecordedSecs: float64(len(pcm)) / CaptureRate}
		cmd := Normalize(pcm)
		tr, err := p.Server.Transcribe(ctx, wavBytes(cmd), wavBytes(slices.Concat(s.wakeClip, cmd)), s.id)
		if err != nil {
			// The Python node gives up on the window quietly here.
			ex.Outcome, ex.Err = OutcomeError, fmt.Errorf("transcribe: %w", err)
			t.FollowUps, t.FollowUpEnd = append(t.FollowUps, ex), EndError
			s.log.Warn("follow-up: transcription failed", "err", err)
			return
		}
		text := strings.TrimSpace(tr.Text)
		ex.Transcript = text
		if IsNonSpeech(text) {
			ex.Outcome = OutcomeNonSpeech
			t.FollowUps, t.FollowUpEnd = append(t.FollowUps, ex), EndNonSpeech
			return
		}
		if IsFollowUpNoise(text, prev) {
			noise++
			ex.Outcome = OutcomeNoise
			t.FollowUps = append(t.FollowUps, ex)
			s.log.Info("follow-up: noise, not sent", "transcript", text, "in_a_row", noise)
			if noise >= maxConsecutiveNoise {
				t.FollowUpEnd = EndNoise
				return
			}
			prev = text
			continue
		}
		if LooksLikeSelfEcho(text, lastSaid) {
			ex.Outcome = OutcomeEcho
			t.FollowUps, t.FollowUpEnd = append(t.FollowUps, ex), EndEcho
			s.log.Info("follow-up: heard our own reply, closing", "transcript", text)
			return
		}
		noise, prev = 0, text
		if tr.Speaker != nil && tr.Speaker.UserID != nil {
			s.userID = tr.Speaker.UserID
		}

		// A follow_up turn of the same conversation, on the blocking route as the Python node
		// sends it (continue_conversation → POST /voice/command).
		iter := it
		c, err := p.Server.VoiceCommand(ctx, StreamRequest{VoiceCommand: tr.Text, ConversationID: s.id,
			TurnSource: "follow_up", FollowUpIteration: &iter})
		end := EndError
		if err != nil {
			p.fail(ctx, &ex, fmt.Errorf("voice/command: %w", err))
		} else {
			end = p.resolve(ctx, s, &ex, c)
		}
		t.FollowUps = append(t.FollowUps, ex)
		s.log.Info("follow-up turn", "iteration", it, "transcript", text, "outcome", ex.Outcome, "reply", ex.Message, "err", ex.Err)
		if ex.Message != "" {
			lastSaid = ex.Message
		}
		if end != "" {
			t.FollowUpEnd = end
			return
		}
	}
}

// listenFollowUp is listen_for_follow_up: it returns the answer's audio, or nil when the
// window closed without one. timeoutSecs bounds the wait for onset (the quiet-wait included).
func (p *Pipeline) listenFollowUp(ctx context.Context, s *session, timeoutSecs float64, iteration int) ([]int16, error) {
	cfg := p.Cfg.FollowUp
	// Echo: whatever was captured while the reply played is dropped, as the wake loop does.
	flush(s.chunks)
	if p.listening != nil {
		p.listening("follow_up")
	}
	budget := max(1, frames(timeoutSecs))
	seen := 0
	next := func() ([]int16, error) {
		c, err := recv(ctx, s.chunks, time.Second)
		seen++
		return c, err
	}
	if err := p.quietWaitWith(ctx, s, frames(cfg.QuietWaitSecs), &seen); err != nil {
		return nil, mapChunkTimeout(err)
	}

	// Onset: enough loud chunks in a row. Everything heard is kept (a dip mid-word must not
	// lose the start), trimmed to 1.5 s before onset.
	required := min(onsetMax, onsetBase+(iteration-1))
	var heard [][]int16
	run := 0
	detected := false
	for seen < budget {
		c, err := next()
		if err != nil {
			return nil, mapChunkTimeout(err)
		}
		heard = append(heard, c)
		if RMS(c) >= s.threshold {
			run++
			if run >= required {
				detected = true
				break
			}
		} else {
			run = 0
		}
	}
	if !detected {
		return nil, nil
	}
	if keep := max(1, frames(preOnsetKeepSecs)); len(heard) > keep {
		heard = heard[len(heard)-keep:]
	}

	// Record until SilenceSecs of quiet, but at least MinAfterOnsetSecs after onset.
	silenceF := max(1, frames(cfg.SilenceSecs))
	minAfter := max(1, frames(cfg.MinAfterOnsetSecs))
	maxF := max(1, frames(p.Cfg.Endpointing.MaxSecs))
	silent := 0
	for post := 1; post <= maxF; post++ {
		c, err := recv(ctx, s.chunks, time.Second)
		if errors.Is(err, errChunkTimeout) {
			s.log.Warn("follow-up recording stopped: no audio for 1 s")
			break
		}
		if err != nil {
			return nil, err
		}
		heard = append(heard, c)
		if RMS(c) < s.threshold {
			silent++
		} else {
			silent = 0
		}
		if post >= minAfter && silent >= silenceF {
			break
		}
	}
	if secs := float64(len(heard)) * chunkSecs; secs < cfg.MinSpeechSecs {
		s.log.Info("follow-up recording too short; noise", "secs", secs)
		return nil, nil
	}
	return slices.Concat(heard...), nil
}

// mapChunkTimeout: a microphone that stalls during the window closes it, like a timeout.
func mapChunkTimeout(err error) error {
	if errors.Is(err, errChunkTimeout) {
		return nil
	}
	return err
}

// quietWait drains chunks until the room has been quiet for quietConsecutiveSecs (below the
// silence threshold × 1.5), or maxChunks went by (_drain_until_quiet). The drained audio is
// the tail of what we just said.
func (p *Pipeline) quietWait(ctx context.Context, s *session, maxChunks int) error {
	var n int
	return mapChunkTimeout(p.quietWaitWith(ctx, s, maxChunks, &n))
}

func (p *Pipeline) quietWaitWith(ctx context.Context, s *session, maxChunks int, seen *int) error {
	if maxChunks <= 0 {
		return nil
	}
	need := max(1, frames(quietConsecutiveSecs))
	quiet := s.threshold * quietFactor
	below := 0
	for range max(1, maxChunks) {
		c, err := recv(ctx, s.chunks, time.Second)
		*seen++
		if err != nil {
			return err
		}
		if RMS(c) < quiet {
			below++
			if below >= need {
				return nil
			}
		} else {
			below = 0
		}
	}
	return nil
}

// IsFollowUpNoise is conversation_filters.is_follow_up_noise minus its lone-word rule: in the
// follow-up window, only an exact repeat of the previous transcription (Whisper hallucinating on
// similar noise) is noise. One-word answers ("Kitchen.") are real answers and are always sent
// (user decision 2026-10-10; Python dropped words outside a small allowlist).
func IsFollowUpNoise(text, prev string) bool {
	if text == "" {
		return true
	}
	lowered := strings.TrimRight(strings.ToLower(strings.TrimSpace(text)), ".!,?")
	return prev != "" && lowered == strings.TrimRight(strings.ToLower(strings.TrimSpace(prev)), ".!,?")
}

var echoWord = regexp.MustCompile(`[a-z']+`)

// echoStopwords are ignored on the user's side (conversation_filters._ECHO_STOPWORDS).
var echoStopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`the a an is are was were be been being to of and or for in on at by with
		i you it we they he she me us them this that these those here there do does did have has had
		will would can could should may might must what when where why how who`) {
		m[w] = true
	}
	return m
}()

func echoWords(s string, dropStop bool) map[string]bool {
	m := map[string]bool{}
	for _, w := range echoWord.FindAllString(strings.ToLower(s), -1) {
		if len(w) >= 2 && !(dropStop && echoStopwords[w]) {
			m[w] = true
		}
	}
	return m
}

// LooksLikeSelfEcho is conversation_filters.looks_like_self_echo: the transcript is mostly
// the words of what we last said, so the mic heard the speaker, not the user. Two significant
// words must both match; three or more, 85% of them.
func LooksLikeSelfEcho(text, lastSaid string) bool {
	if text == "" || lastSaid == "" {
		return false
	}
	user := echoWords(text, true)
	if len(user) < 2 {
		return false
	}
	said := echoWords(lastSaid, false)
	overlap := 0
	for w := range user {
		if said[w] {
			overlap++
		}
	}
	if len(user) == 2 {
		return overlap == 2
	}
	return float64(overlap)/float64(len(user)) >= 0.85
}
