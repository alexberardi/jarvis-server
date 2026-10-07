// Package hints builds the per-turn bracketed hints command-center appends to the user
// message (never to messages[0], so the cached prefix stays byte-stable), ported byte-exact
// from the legacy Python (jarvis-command-center/app/core):
//
//   - direction_hint.py: [direction hint: …] from the pre-wake VAD reading, the wake score
//     and transcript shape;
//   - affect_hint.py: [voice: …] from whisper's affect read (D38: the producer is cut, so
//     affect is always nil and this is a no-op kept for contract stability);
//   - turn_context.py: [turn context: …] for wake / follow-up / chat turns, the doubted-
//     conversation caution and the named-person lean, plus should_double_check_sentinel;
//   - profile_match.py: [profile match: …] restating speaker-profile lines the utterance names.
//
// The user message is the utterance, then each non-empty hint after "\n\n" in the order
// direction, affect, turn, profile-match, agent context (prompts.UserMessage). Every string
// is prompt bytes; fixtures/golden/voice is the gate.
package hints

import (
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
)

// Thresholds (direction_hint.py / turn_context.py).
const (
	QuietThresholdS         = 1.5
	ActiveThresholdS        = 4.5
	WindowSeconds           = 5.0
	BorderlineConfidence    = 0.75
	WakeConfidentThreshold  = BorderlineConfidence
	FollowUpStrictIteration = 3
	DoubtedWakeVerdict      = "unverified"
	AffectMinConfidence     = 0.5
)

func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
func f2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// windowS is f"{WINDOW_SECONDS:.0f}".
var windowS = strconv.FormatFloat(WindowSeconds, 'f', 0, 64)

const speakerKnownNote = " Note: the speaker's voice matched a known household member, which " +
	"leans toward this being directed at you."

// IsMediaSelfPlayback is is_media_self_playback: the node reported its own speaker playing
// music at wake (a missing kind counts as music; any other kind does not).
func IsMediaSelfPlayback(selfPlayback *bool, selfPlaybackKind *string) bool {
	if selfPlayback == nil || !*selfPlayback {
		return false
	}
	return selfPlaybackKind == nil || *selfPlaybackKind == "music"
}

// Direction is build_direction_hint's input. Nil pointers are Python None.
type Direction struct {
	PreWakeSpeechSeconds *float64
	WakeConfidence       *float64
	TurnSource           *string
	// Transcript nil means not available (the device-command guard is skipped).
	Transcript       *string
	SpeakerKnown     *bool
	SelfPlayback     *bool
	SelfPlaybackKind *string
}

// DirectionHint is build_direction_hint: a [direction hint: …] line, or "" when the signal
// isn't actionable.
func DirectionHint(d Direction) string {
	media := IsMediaSelfPlayback(d.SelfPlayback, d.SelfPlaybackKind)
	quiet := ""
	if !media && d.PreWakeSpeechSeconds != nil && *d.PreWakeSpeechSeconds < QuietThresholdS {
		quiet = "[direction hint: room was quiet (" + f1(*d.PreWakeSpeechSeconds) +
			"s of speech in the " + windowS + "s before wake) — strong signal this is directed at you]"
	}
	if d.Transcript != nil && (textfilter.IsDeviceCommandShaped(*d.Transcript) ||
		(media && textfilter.IsMusicControlShaped(*d.Transcript))) {
		return quiet
	}
	ambientNote := ""
	if d.SpeakerKnown != nil && *d.SpeakerKnown {
		ambientNote = speakerKnownNote
	}
	wake := d.TurnSource != nil && *d.TurnSource == "wake"
	if wake && d.Transcript != nil && *d.Transcript != "" {
		t := *d.Transcript
		if textfilter.HasMultiSpeakerMarkers(t) {
			return "[direction hint: the transcript contains multi-speaker " +
				"dialogue markers (dash-prefixed turns) — this usually " +
				"means the mic caught people (or a TV) talking to each " +
				"other, which leans toward <not_for_me/>. Still answer if " +
				"the content is unmistakably addressed to you." +
				ambientNote + "]"
		}
		if !(d.SpeakerKnown != nil && *d.SpeakerKnown) && textfilter.IsShortNonCommandFragment(t) {
			return "[direction hint: the transcript is a very short fragment " +
				"from an unrecognized speaker — on a wake turn this is " +
				"usually an STT fragment or overheard cross-talk, which " +
				"leans toward <not_for_me/>. Still answer if it is a real " +
				"request to you.]"
		}
	}
	if media || d.PreWakeSpeechSeconds == nil {
		return ""
	}
	if quiet != "" {
		return quiet
	}
	pre := *d.PreWakeSpeechSeconds
	if pre > ActiveThresholdS {
		return "[direction hint: continuous speech detected (" + f1(pre) + "s in the " + windowS +
			"s before wake) — wake may have fired during a conversation between people; " +
			"emit <not_for_me/> unless the transcript is clearly addressed to you." + ambientNote + "]"
	}
	if wake && d.WakeConfidence != nil && *d.WakeConfidence < BorderlineConfidence {
		return "[direction hint: intermittent speech before wake (" + f1(pre) + "s in the " + windowS +
			"s window) AND the wake score was marginal (" + f2(*d.WakeConfidence) + ") — this " +
			"pattern usually means the wake fired inside a conversation " +
			"between people. A coherent sentence is NOT evidence it is for " +
			"you: if the transcript reads like people talking to each other " +
			"(replies to something you didn't say, third-person references, " +
			"mid-story fragments, 'we/let's' plans), emit <not_for_me/>. " +
			"Answer only if it plausibly addresses you." + ambientNote + "]"
	}
	return ""
}

// AffectHint is build_affect_hint over whisper's affect dict ({read, arousal, confidence});
// "" for nil, malformed, low-confidence or neutral reads. Confidence is float(value) in Python:
// a number, a bool, or a numeric string.
func AffectHint(affect map[string]any) string {
	if affect == nil {
		return ""
	}
	conf := 0.0
	if v, ok := affect["confidence"]; ok {
		c, ok := pyFloat(v)
		if !ok {
			return ""
		}
		conf = c
	}
	if conf < AffectMinConfidence {
		return ""
	}
	read := ""
	if s, ok := affect["read"].(string); ok {
		read = pyStrip(s)
	}
	if read == "" {
		return ""
	}
	switch affect["arousal"] {
	case "low":
		return "[voice: they sound " + read + ". Meet them there — a little warmer, " +
			"gentler, and less hurried. Do NOT mention their mood or how they " +
			"sound; just let it shape your tone.]"
	case "high":
		return "[voice: they sound " + read + ". Match their energy — a bit more lively " +
			"and engaged. Do NOT mention their mood or how they sound; just let " +
			"it shape your tone.]"
	}
	return ""
}
