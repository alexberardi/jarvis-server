package voice

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"math"
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
// to 5 times, each window as long as the first (the Python node's shrank). It closes quietly when nobody speaks, on
// noise, on the node hearing itself, or when command-center ends the exchange.
//
// Time is counted in captured chunks (80 ms each), not wall-clock time: the same thing on a
// real microphone, and deterministic on a scripted one.

// FollowUpConfig is the follow-up window's tuning. DefaultFollowUp is the Python node's, except
// where DefaultFollowUp says otherwise.
type FollowUpConfig struct {
	ListenSecs        float64 // follow_up_listen_seconds: the first window (0 turns follow-up off)
	MaxIterations     int     // max_follow_up_iterations (5)
	DecaySecs         float64 // follow_up_timeout_decay_secs: each window this much shorter (0)
	MinTimeoutSecs    float64 // follow_up_min_timeout_secs: but never under this (0; never above ListenSecs)
	QuietWaitSecs     float64 // follow_up_quiet_wait_secs: at most this long draining the reply's tail (0.5 s)
	SilenceSecs       float64 // follow_up_silence_duration: silence that ends an answer
	MinAfterOnsetSecs float64 // follow_up_min_record_after_onset_secs (0.7 s)
	MinSpeechSecs     float64 // follow_up_min_speech_secs: shorter recordings are noise (0.3 s)
	MaxRecordSecs     float64 // an answer is cut here (0: Endpointing.MaxSecs)
	// MaxIgnored bounds the answers command-center may reject in one window (keep_listening)
	// before the node closes it on its own; command-center's own bound
	// (voice.follow_up_max_ignored, 3) normally closes it first.
	MaxIgnored int
	// IgnoredGraceSecs: an ignored answer doesn't restart the window, which keeps only what
	// was left of it, but at least this long, so the user can still answer once the room
	// is quiet again.
	IgnoredGraceSecs float64
	// ChoiceListenSecs: the window after the island listed choices to pick from (choice_listen_seconds,
	// default 8, at most MaxChoiceListenSecs): reading a list and answering takes longer than
	// the 3 s follow-up (DESKTOP-COMMANDS.md Q9).
	ChoiceListenSecs float64
	// ChainListenSecs: the window after a silent desktop command (the island showed it), so
	// the user can say the next step of a process ("switch to the Claude tab… now the budget
	// sheet… maximize it"): the 3 s after a spoken reply was too short to think of it (voice
	// test, 2026-10-25). Such steps don't count toward MaxIterations; maxChainSteps bounds them.
	ChainListenSecs float64
}

// maxChainSteps bounds one wake word's follow-up turns, silent desktop steps included.
const maxChainSteps = 30

// choiceHint biases whisper toward an answer to a numbered list in the choice window.
const choiceHint = "The first one. The second one. The third one. Number four. The last one."

// MaxChoiceListenSecs caps choice_listen_seconds.
const MaxChoiceListenSecs = 30

// ClampChoiceListen caps a choice window at MaxChoiceListenSecs.
func ClampChoiceListen(secs float64) float64 { return min(secs, MaxChoiceListenSecs) }

// MaxFollowUpListenSecs caps the follow-up window, wherever it is set (agent.json, --follow-up,
// the app's follow_up_listen_seconds): 8 s left every turn hanging long after the reply (user,
// 2026-10-19, #296).
const MaxFollowUpListenSecs = 3

// ClampFollowUpListen caps a follow-up window at MaxFollowUpListenSecs.
func ClampFollowUpListen(secs float64) float64 { return min(secs, MaxFollowUpListenSecs) }

// DefaultFollowUp is the follow-up window out of the box (go-pi-node.md §3.10 has the
// evidence). Changed from the Python node's: 3 s to start answering on every iteration, the
// cap (#296; it was 8 s, then 6, then 5, which kept every turn open long after the reply);
// 1.2 s of silence ends an answer instead of 0.5 (0.5 s cut 37% of real read utterances
// mid-sentence, 1.2 s 1.6%); answers may run 10 s instead of 7.
func DefaultFollowUp() FollowUpConfig {
	return FollowUpConfig{ListenSecs: MaxFollowUpListenSecs, MaxIterations: 5,
		QuietWaitSecs: 0.5, SilenceSecs: 1.2, MinAfterOnsetSecs: 0.7, MinSpeechSecs: 0.3,
		MaxRecordSecs: 10, MaxIgnored: 5, IgnoredGraceSecs: 2, ChoiceListenSecs: 8, ChainListenSecs: 8}
}

// Fixed parts of the Python node's follow-up listening.
const (
	quietConsecutiveSecs = 0.16 // listen_quiet_consecutive_secs: quiet this long = the tail is over
	quietFactor          = 1.5  // listen_quiet_rms_threshold = silence threshold × 1.5
	onsetChunks          = 3    // chunks above the threshold among the last onsetSpan that make speech onset
	onsetSpan            = 5    // a Bluetooth mic drops a chunk mid-word as silence: one gap is allowed
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
	EndIgnored       FollowUpEnd = "ignored"  // FollowUp.MaxIgnored answers rejected in one window
	EndDisabled      FollowUpEnd = "disabled" // FollowUp.ListenSecs <= 0
	EndBargeIn       FollowUpEnd = "barge_in" // the user talked over the reply: back to the wake word
	EndError         FollowUpEnd = "error"
)

// followUp runs the follow-up window after the woken exchange, recording each answer in
// t.FollowUps and why it closed in t.FollowUpEnd.
//
// An answer command-center rejects with keep_listening (the follow-up gate: another voice, a
// TV, speech not meant for Jarvis) is ignored: nothing is said, the iteration isn't used up,
// and the window listens again straight away, without the flush and quiet-wait that follow
// our own speech.
func (p *Pipeline) followUp(ctx context.Context, s *session, t *Turn) {
	cfg := p.Cfg.FollowUp
	if cfg.ListenSecs <= 0 && !s.choice { // a list on the island is answered even with follow-ups off
		t.FollowUpEnd = EndDisabled
		return
	}
	lastSaid := t.Message // for the self-echo check
	prev := ""
	noise := 0
	ignored := 0
	used := 0          // chunks of this iteration's window already heard (ignored answers included)
	afterReply := true // the last thing that happened was us speaking (or the wake exchange)
	steps := 0
	for it := 1; ; {
		if it > cfg.MaxIterations || steps >= maxChainSteps {
			t.FollowUpEnd = EndMaxIterations
			return
		}
		steps++
		window := max(min(cfg.MinTimeoutSecs, cfg.ListenSecs), cfg.ListenSecs-float64(it-1)*cfg.DecaySecs)
		// Speech that wasn't for us doesn't buy the room a new window: after an ignored
		// answer, only what is left of this one (but at least IgnoredGraceSecs).
		timeout := window
		if ignored > 0 {
			timeout = max(cfg.IgnoredGraceSecs, window-float64(used)*chunkSecs)
		}
		choosing := s.choice
		if s.choice {
			// The island lists choices: a longer window, listening at once (nothing was
			// spoken, so there is no reply tail to drain).
			s.choice = false
			timeout, afterReply = ClampChoiceListen(cmp.Or(cfg.ChoiceListenSecs, DefaultFollowUp().ChoiceListenSecs)), false
		}
		if s.quietTurn {
			s.quietTurn = false
			afterReply = false
			if !choosing {
				timeout = cmp.Or(cfg.ChainListenSecs, DefaultFollowUp().ChainListenSecs)
			}
		}
		if afterReply && AsksQuestion(lastSaid) {
			// A question waits for its answer (VOICE-FLOWS.md Q14): 3 s after "what are you
			// looking for?" closed before the user had started answering.
			timeout = max(timeout, cmp.Or(cfg.ChainListenSecs, DefaultFollowUp().ChainListenSecs))
		}
		s.log.Info("follow-up: listening", "iteration", it, "window_s", timeout)
		// Again before each listen: the last reply's command may have just started a player.
		p.duck(ctx)
		p.cue(CueFollowUp, time.Duration(timeout*float64(time.Second)))
		pcm, err := p.listenFollowUp(ctx, s, timeout, afterReply, &used)
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
		heardAfterReply := afterReply
		afterReply = false
		p.cue(CueThinking, 0)
		ex := Exchange{RecordedSecs: float64(len(pcm)) / CaptureRate}
		level := levelVsWake(s, pcm)
		cmd := Normalize(pcm)
		tctx := ctx
		if choosing {
			tctx = WithTranscribeHint(ctx, choiceHint)
		}
		tr, err := p.Server.Transcribe(tctx, wavBytes(cmd), wavBytes(slices.Concat(s.wakeClip, cmd)), s.id)
		if err != nil {
			// The Python node gives up on the window quietly here.
			ex.Outcome, ex.Err = OutcomeError, fmt.Errorf("transcribe: %w", err)
			t.FollowUps, t.FollowUpEnd = append(t.FollowUps, ex), EndError
			s.log.Warn("follow-up: transcription failed", "err", err)
			return
		}
		ex.Transcript = strings.TrimSpace(tr.Text)
		text := StripNonSpeech(tr.Text)
		if IsNonSpeech(text) {
			ex.Outcome = OutcomeNonSpeech
			t.FollowUps, t.FollowUpEnd = append(t.FollowUps, ex), EndNonSpeech
			return
		}
		if choosing {
			// An answer to the island's list is acted on here, without command-center: "the
			// second one" heard cold is the least reliable thing STT does, and a model
			// may only *say* it switched.
			if a, ok := p.Tools.(node.ChoiceAnswerer); ok {
				if out, ok := a.AnswerChoice(ctx, text); ok {
					ok, _ := out["success"].(bool)
					ex.Outcome = OutcomeSilent
					ex.Tools = append(ex.Tools, ToolRun{Name: "choice", Args: map[string]any{"answer": text}, Success: ok})
					t.FollowUps = append(t.FollowUps, ex)
					s.log.Info("follow-up: answered the island's list on the node", "transcript", text, "success", ok)
					if !ok {
						p.chime(ctx, errorChime)
					}
					if c, _ := out[node.ChoiceKey].(bool); c {
						s.choice = true // still ambiguous: the next list
					} else {
						s.quietTurn = true
					}
					noise, prev, used = 0, text, 0
					if !s.quietTurn {
						it++ // a silent step of a process doesn't use up the window count
					}
					continue
				}
			}
		}
		if IsFollowUpNoise(text, prev) && !(choosing && text != "") { // a repeated answer to a list is still an answer
			noise++
			ex.Outcome = OutcomeNoise
			t.FollowUps = append(t.FollowUps, ex)
			s.log.Info("follow-up: noise, not sent", "transcript", text, "in_a_row", noise)
			if noise >= maxConsecutiveNoise {
				t.FollowUpEnd = EndNoise
				return
			}
			prev = text
			used = 0
			it++
			continue
		}
		// Only an answer heard right after we spoke can be our reply's echo: after an ignored
		// answer nothing was said.
		if heardAfterReply && LooksLikeSelfEcho(text, lastSaid) {
			ex.Outcome = OutcomeEcho
			t.FollowUps, t.FollowUpEnd = append(t.FollowUps, ex), EndEcho
			s.log.Info("follow-up: heard our own reply, closing", "transcript", text)
			return
		}
		noise, prev = 0, text
		if tr.Speaker != nil && tr.Speaker.UserID != nil {
			s.userID = tr.Speaker.UserID
		}
		if end, ok := p.direct(ctx, s, &ex, text); ok { // funnel stage 1: done here, no command-center turn
			t.FollowUps = append(t.FollowUps, ex)
			if end != "" {
				t.FollowUpEnd = end
				return
			}
			if ex.Message != "" { // a spoken routine: its tail is drained before listening again
				lastSaid, afterReply = ex.Message, true
			}
			ignored, used = 0, 0
			if !s.quietTurn {
				it++
			}
			continue
		}

		// A follow_up turn of the same conversation, on the blocking route as the Python node
		// sends it (continue_conversation → POST /voice/command).
		iter := it
		activity := p.computerTurn(s, text, true) // "I'm done" stops a recording at once (#288)
		c, err := p.Server.VoiceCommand(ctx, StreamRequest{VoiceCommand: text, ConversationID: s.id,
			TurnSource: "follow_up", FollowUpIteration: &iter, LevelVsWake: level, Computer: activity})
		if err == nil && c.StopReason == "not_for_me" && c.KeepListening {
			ignored++
			ex.Outcome = OutcomeIgnored
			t.FollowUps = append(t.FollowUps, ex)
			s.log.Info("follow-up: not for me, still listening", "iteration", it, "transcript", text,
				"level_vs_wake_db", ptrVal(level), "ignored", ignored)
			if ignored >= cfg.MaxIgnored {
				t.FollowUpEnd = EndIgnored
				return
			}
			continue
		}
		end := EndError
		if err != nil {
			p.fail(ctx, &ex, fmt.Errorf("voice/command: %w", err))
		} else {
			end = p.resolve(ctx, s, &ex, c)
		}
		t.FollowUps = append(t.FollowUps, ex)
		s.log.Info("follow-up turn", "iteration", it, "transcript", text, "level_vs_wake_db", ptrVal(level),
			"outcome", ex.Outcome, "reply", ex.Message, "err", ex.Err)
		if ex.Message != "" {
			lastSaid = ex.Message
		}
		if end != "" {
			t.FollowUpEnd = end
			return
		}
		ignored, used = 0, 0
		afterReply = true
		if !s.quietTurn {
			it++ // a silent step of a process doesn't use up the window count
		}
	}
}

func ptrVal(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// listenFollowUp is listen_for_follow_up: it returns the answer's audio, or nil when the
// window closed without one. timeoutSecs bounds the wait for onset (the quiet-wait included).
// afterReply: we just spoke, so what was captured meanwhile is dropped and the reply's tail
// drained first; after an ignored answer nothing was said, and nothing is dropped. used (may
// be nil) is advanced by every chunk heard, waiting and recording.
func (p *Pipeline) listenFollowUp(ctx context.Context, s *session, timeoutSecs float64, afterReply bool, used *int) ([]int16, error) {
	cfg := p.Cfg.FollowUp
	seen, recorded := 0, 0
	if used != nil {
		defer func() { *used += seen + recorded }()
	}
	if afterReply {
		// Echo: whatever was captured while the reply played is dropped, as the wake loop does.
		flush(s.chunks)
	}
	if p.listening != nil {
		p.listening("follow_up")
	}
	budget := max(1, frames(timeoutSecs))
	next := func() ([]int16, error) {
		c, err := recv(ctx, s.chunks, time.Second)
		seen++
		return c, err
	}
	if afterReply {
		if err := p.quietWaitWith(ctx, s, frames(cfg.QuietWaitSecs), &seen); err != nil {
			return nil, mapChunkTimeout(err)
		}
	}

	// Onset: enough loud chunks among the last onsetSpan. Everything heard is kept (a dip
	// mid-word must not lose the start), trimmed to 1.5 s before onset.
	required, at := onsetChunks, s.threshold
	var heard [][]int16
	var loud [onsetSpan]bool
	inSpan, most, peak := 0, 0, 0.0
	detected := false
	for i := 0; seen < budget; i++ {
		c, err := next()
		if err != nil {
			return nil, mapChunkTimeout(err)
		}
		heard = append(heard, c)
		r := RMS(c)
		peak = max(peak, r)
		if loud[i%onsetSpan] {
			inSpan--
		}
		loud[i%onsetSpan] = r >= at
		if loud[i%onsetSpan] {
			inSpan++
		}
		most = max(most, inSpan)
		if inSpan >= required {
			detected = true
			break
		}
	}
	if !detected {
		// What the window heard, so "it didn't hear me" can be told from a quiet mic.
		s.log.Info("follow-up: no speech onset", "peak_rms", math.Round(peak), "threshold", at,
			"most_loud", most, "span", onsetSpan, "required", required, "wake_level_db", ptrVal(s.wakeLevel))
		return nil, nil
	}
	if keep := max(1, frames(preOnsetKeepSecs)); len(heard) > keep {
		heard = heard[len(heard)-keep:]
	}
	p.cue(CueListening, 0)

	// Record until SilenceSecs of quiet, but at least MinAfterOnsetSecs after onset.
	silenceF := max(1, frames(cfg.SilenceSecs))
	minAfter := max(1, frames(cfg.MinAfterOnsetSecs))
	maxSecs := cfg.MaxRecordSecs
	if maxSecs <= 0 {
		maxSecs = p.Cfg.Endpointing.MaxSecs
	}
	maxF := max(1, frames(maxSecs))
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
		recorded++
		p.level(c)
		if RMS(c) < at {
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

var echoWord = regexp.MustCompile(`[a-z0-9']+`)

// echoTokens are s's words in order, lower-cased, apostrophes kept ("it's" is one word).
func echoTokens(s string) []string {
	return echoWord.FindAllString(strings.ToLower(s), -1)
}

// Self-echo thresholds (LooksLikeSelfEcho).
const (
	echoMinWords     = 2   // a one-word answer is never an echo ("Kitchen." to "Kitchen or oven?")
	echoOfTranscript = 0.9 // this much of the transcript is the reply's words, in order...
	echoOfReply      = 0.5 // ...and it covers at least this much of the reply
)

// AsksQuestion reports a reply that ends by asking the user something.
func AsksQuestion(reply string) bool {
	return strings.HasSuffix(strings.TrimRight(strings.TrimSpace(reply), `"'”’)`), "?")
}

// LooksLikeSelfEcho reports whether the transcript is the mic hearing what we last said rather
// than the user: nearly all of it (90 % of its words, stopwords included) must be the reply's
// own words in the reply's order, and those must cover at least half the reply. A user who
// repeats a few of the reply's words inside a request of their own is never an echo:
// "Can you type hello there?" after `Now, type "hello there" to get started.` matches 4 of its
// 5 words in order, but only 4 of the reply's 16.
//
// Stricter than the Python node's conversation_filters.looks_like_self_echo, which counted
// distinct non-stopwords in any order (85 %, or both of two) and so dropped that request
// (user report, 2026-10-14). A missed echo costs little: command-center's follow-up gate
// still rejects speech that isn't for Jarvis. A dropped request loses what the user asked.
func LooksLikeSelfEcho(text, lastSaid string) bool {
	user, said := echoTokens(text), echoTokens(lastSaid)
	if len(user) < echoMinWords || len(said) == 0 {
		return false
	}
	m := float64(inOrder(user, said))
	return m/float64(len(user)) >= echoOfTranscript && m/float64(len(said)) >= echoOfReply
}

// inOrder is the length of the longest common subsequence of a and b: how many of a's words
// appear in b in the same order.
func inOrder(a, b []string) int {
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	for i := range a {
		for j := range b {
			if a[i] == b[j] {
				cur[j+1] = prev[j] + 1
			} else {
				cur[j+1] = max(prev[j+1], cur[j])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
