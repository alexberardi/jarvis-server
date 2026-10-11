package cc

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The follow-up gate: "is this speech for me?" for turns in a node's follow-up window, where
// there is no wake word (docs/agent/go-pi-node.md §3.10). Three cheap signals, combined
// conservatively, because a follow-up from the person Jarvis is talking to must never be
// dropped:
//
//   - voice: the follow-up's voice against the voice that woke the node, from the same
//     speaker model as identification (stt.VoiceOf), but identifying nobody;
//   - level: the node's measure of the follow-up's loudness against the wake request
//     (level_vs_wake_db; uploads are normalised, so only the node can tell);
//   - semantics: the model's own <not_for_me/> verdict, which the follow-up posture already
//     asks for; the acoustic evidence reaches it as one hint line.
//
// Only a different voice that is ALSO much quieter (someone across the room, a TV) is dropped
// before the model. A rejected follow-up is silent and leaves no trace in the conversation's
// history; the response says keep_listening so the node keeps its window open, up to
// voice.follow_up_max_ignored rejections in a row.

const (
	settingFollowUpGate          = "voice.follow_up_gate"
	settingFollowUpVoiceMismatch = "voice.follow_up_voice_mismatch"
	settingFollowUpVoiceMatch    = "voice.follow_up_voice_match"
	settingFollowUpQuietDB       = "voice.follow_up_quiet_db"
	settingFollowUpMaxIgnored    = "voice.follow_up_max_ignored"
)

// followUpGateDefinitions are the gate's settings. The defaults come from the evaluation in
// go-pi-node.md §3.10 (ERes2Net, 48 kHz node uploads).
func followUpGateDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingFollowUpGate, Category: "voice", Type: settings.Bool, Default: true,
			Description: "Follow-up gate: in a node's follow-up window, compare the voice and loudness of what was " +
				"heard with the wake request, and let speech that isn't for Jarvis (another person, a TV) pass " +
				"silently without ending the window or entering the conversation's history. Off: every follow-up " +
				"goes to the model as before, and a not-for-me follow-up ends the window."},
		{Key: settingFollowUpVoiceMismatch, Category: "voice", Type: settings.Float, Default: 0.25,
			Description: "Follow-up gate: a voice similarity to the wake request below this counts as a different " +
				"voice (ERes2Net cosine; same-speaker follow-ups measured 0.26-0.88, other voices -0.14-0.47)."},
		{Key: settingFollowUpVoiceMatch, Category: "voice", Type: settings.Float, Default: 0.40,
			Description: "Follow-up gate: a voice similarity at or above this counts as the voice that woke the node."},
		{Key: settingFollowUpQuietDB, Category: "voice", Type: settings.Float, Default: -10.0,
			Description: "Follow-up gate: a follow-up whose speech level is this many dB below the wake request's " +
				"(or more) counts as far away. Only a different voice that is also this quiet is dropped before " +
				"the model."},
		{Key: settingFollowUpMaxIgnored, Category: "voice", Type: settings.Int, Default: int64(3),
			Description: "Follow-up gate: rejected follow-ups in a row before the node's follow-up window closes."},
	}
}

// voiceVerdict is what the voice comparison says.
type voiceVerdict string

const (
	voiceUnknown   voiceVerdict = "unknown" // no comparison: too little speech, no model, no wake clip
	voiceSame      voiceVerdict = "same"
	voiceUnsure    voiceVerdict = "unsure"
	voiceDifferent voiceVerdict = "different"
)

// gateEvidence is one follow-up's acoustic evidence and what it adds up to.
type gateEvidence struct {
	VoiceSim *float64
	LevelDB  *float64
	Voice    voiceVerdict
	Quiet    bool
}

// reject: drop the follow-up before the model. Both acoustic signals must agree: a
// different voice from far away. Same-speaker follow-ups never measured below 0.26 in the
// evaluation, and the far rooms of the same speaker stayed above -10.4 dB.
func (e gateEvidence) reject() bool { return e.Voice == voiceDifferent && e.Quiet }

// doubted: the acoustics lean "not for me"; the model is told, and its <not_for_me/> is not
// double-checked (the re-check exists to protect genuine follow-ups, and costs a thinking pass).
func (e gateEvidence) doubted() bool {
	return e.Voice == voiceDifferent || (e.Quiet && e.Voice != voiceSame)
}

// hint is the line the model gets, or "" when the acoustics say nothing.
func (e gateEvidence) hint() string {
	switch {
	case e.Voice == voiceDifferent:
		s := "[voice check: this follow-up's voice does not match the person who woke you — it may be " +
			"someone else in the room, or a TV or radio, rather than a reply to you."
		if e.Quiet {
			s += " It was also much quieter than the wake request, as if from across the room."
		}
		return s + " Answer it only if it is clearly addressed to you (your name, an answer to a " +
			"question you just asked, or an unmistakable continuation of what you just said); " +
			"otherwise emit <not_for_me/>.]"
	case e.Quiet && e.Voice != voiceSame:
		return "[voice check: this follow-up was much quieter than the wake request, as if from across " +
			"the room or a TV. Answer a clear continuation of your exchange; emit <not_for_me/> for speech " +
			"that reads as meant for someone else.]"
	}
	return ""
}

// fields is the evidence for logs and traces.
func (e gateEvidence) fields() []any {
	f := []any{"voice", string(e.Voice), "quiet", e.Quiet}
	if e.VoiceSim != nil {
		f = append(f, "voice_similarity", round3(*e.VoiceSim))
	}
	if e.LevelDB != nil {
		f = append(f, "level_vs_wake_db", *e.LevelDB)
	}
	return f
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// followUpGateOn reports voice.follow_up_gate for the node.
func (m *Module) followUpGateOn(ctx context.Context, hh, node string) bool {
	return m.settings.Bool(ctx, settingFollowUpGate, settings.Scope{HouseholdID: hh, NodeID: node})
}

// followUpEvidence gathers a follow-up turn's evidence: the voice comparison the transcription
// recorded for this conversation (consumed), and the level the node sent.
func (m *Module) followUpEvidence(ctx context.Context, conv *conversation, in turnInput) gateEvidence {
	sc := settings.Scope{HouseholdID: conv.householdID, NodeID: conv.nodeID}
	e := gateEvidence{Voice: voiceUnknown, LevelDB: in.LevelVsWake}
	if sim := m.signals.takeVoiceMatch(conv.id); sim != nil {
		e.VoiceSim = sim
		switch {
		case *sim < m.settings.Float(ctx, settingFollowUpVoiceMismatch, sc):
			e.Voice = voiceDifferent
		case *sim >= m.settings.Float(ctx, settingFollowUpVoiceMatch, sc):
			e.Voice = voiceSame
		default:
			e.Voice = voiceUnsure
		}
	}
	if in.LevelVsWake != nil && *in.LevelVsWake <= m.settings.Float(ctx, settingFollowUpQuietDB, sc) {
		e.Quiet = true
	}
	return e
}

// ignoreFollowUp records a rejected follow-up and says whether the node should keep listening.
func (m *Module) ignoreFollowUp(ctx context.Context, conv *conversation) bool {
	conv.ignoredFollowUps++
	limit := m.settings.Int(ctx, settingFollowUpMaxIgnored, settings.Scope{HouseholdID: conv.householdID, NodeID: conv.nodeID})
	return int64(conv.ignoredFollowUps) <= limit
}

// --- the voice comparison, fed by the media proxy's transcription ---

// noteVoice is called for every transcription of a conversation: the first one's voice (the
// wake clip + command) becomes the reference, every later one is compared with it.
// claimReference reports which role the next clip plays.
func (s *convSignals) claimReference(convID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(convID)
	if e.voiceRefTaken {
		return false
	}
	e.voiceRefTaken = true
	return true
}

// setVoiceReference stores the conversation's reference voice (unusable: no comparisons).
func (s *convSignals) setVoiceReference(convID string, v stt.Voice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.Usable() {
		s.entry(convID).voiceRef = &v
	}
}

// compareVoice scores a later clip against the reference and keeps the result for the next
// turn. It returns the similarity (nil: no comparison).
func (s *convSignals) compareVoice(convID string, v stt.Voice) *float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(convID)
	e.voiceMatch = nil
	if e.voiceRef == nil {
		return nil
	}
	sim, ok := e.voiceRef.Similarity(v)
	if !ok {
		return nil
	}
	e.voiceMatch = &sim
	return &sim
}

// takeVoiceMatch consumes the newest comparison (nil: none since the last turn).
func (s *convSignals) takeVoiceMatch(convID string) *float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.m[convID]
	if e == nil {
		return nil
	}
	v := e.voiceMatch
	e.voiceMatch = nil
	return v
}

// voiceCheck runs beside a transcription when the gate is on: it embeds the clip (the
// speaker audio for the reference, the transcribed answer for a comparison) and records it.
// It never fails the transcription; done is closed when it has finished.
func (m *Module) voiceCheck(ctx context.Context, convID string, file, speakerAudio []byte) (done <-chan struct{}) {
	ch := make(chan struct{})
	ref := m.signals.claimReference(convID)
	clip := file
	if ref && len(speakerAudio) > 0 {
		clip = speakerAudio
	}
	go func() {
		defer close(ch)
		start := m.now()
		v, err := m.STT.VoiceOf(ctx, clip)
		ms := m.now().Sub(start).Milliseconds()
		if err != nil {
			// No speaker model installed is a configuration, not a fault: the gate then runs
			// on the level and the model alone.
			lvl := slog.LevelWarn
			if errors.Is(err, stt.ErrSpeakerUnavailable) {
				lvl = slog.LevelDebug
			}
			m.deps.Log.Log(ctx, lvl, "cc: follow-up voice check skipped", "conversation_id", convID, "err", err)
			return
		}
		if ref {
			m.setVoiceRef(convID, v, ms)
			return
		}
		sim := m.signals.compareVoice(convID, v)
		f := []any{"conversation_id", convID, "speech_s", round3(v.SpeechSecs), "ms", ms}
		if sim != nil {
			f = append(f, "similarity", round3(*sim))
		}
		m.deps.Log.Info("cc: follow-up voice compared", f...)
	}()
	return ch
}

func (m *Module) setVoiceRef(convID string, v stt.Voice, ms int64) {
	m.signals.setVoiceReference(convID, v)
	m.deps.Log.Info("cc: follow-up voice reference", "conversation_id", convID, "usable", v.Usable(),
		"speech_s", round3(v.SpeechSecs), "ms", ms)
}

// askedQuestion reports whether our last spoken reply in msgs ended in a question: the
// follow-up is then an expected answer ("Which room?" → "Kitchen."), not overheard speech.
func askedQuestion(msgs []chatMsg) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != "assistant" || len(m.ToolCalls) > 0 {
			continue
		}
		text := strings.TrimSpace(parse.StripExchangeComplete(parse.OutsideThink(m.Content)))
		if text == "" {
			continue
		}
		return strings.HasSuffix(strings.TrimRight(text, " \"'”’)"), "?")
	}
	return false
}
