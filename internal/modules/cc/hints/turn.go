package hints

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
)

// Turn is build_turn_hint's input (turn_context.py). Nil pointers are Python None.
type Turn struct {
	// TurnSource is "wake", "follow_up" or "chat"; anything else degrades to inference.
	TurnSource           *string
	WakeConfidence       *float64
	FollowUpIteration    *int
	PreWakeSpeechSeconds *float64
	// WakeVerified false (bias mode, unverified clip) selects the mild misfire lean.
	WakeVerified     *bool
	Transcript       *string
	SelfPlayback     *bool
	SelfPlaybackKind *string
	// ConversationWakeVerdict is the WAKE turn's verdict propagated onto follow-ups; only
	// DoubtedWakeVerdict ("unverified") changes anything.
	ConversationWakeVerdict *string
	// DoubtRound is the conversation's answered-round count; DoubtMaxRounds is
	// voice.followup_doubt_max_rounds.
	DoubtRound     *int
	DoubtMaxRounds *int
	// MemberNames are the household members' display names (named-person lean).
	MemberNames []string
}

// TurnHint is build_turn_hint: a [turn context: …] line, or "" when there is no provenance
// signal at all.
func TurnHint(t Turn) string {
	media := IsMediaSelfPlayback(t.SelfPlayback, t.SelfPlaybackKind)
	src := ""
	if t.TurnSource != nil {
		src = *t.TurnSource
	}
	switch src {
	case "follow_up":
		return followUpHint(t, media)
	case "wake":
		return wakeHint(t.WakeConfidence, t.WakeVerified, t.Transcript, media)
	case "chat":
		return "[turn context: typed message — the user wrote this directly " +
			"to you in the app. It cannot be overheard speech; " +
			"<not_for_me/> does not apply. Answer it or call the right " +
			"tool.]"
	}
	if t.PreWakeSpeechSeconds != nil {
		return wakeHint(nil, nil, nil, media)
	}
	return ""
}

// ShouldDoubleCheckSentinel is should_double_check_sentinel: whether a first-look
// <not_for_me/> earns one /think re-check (sentinel_double_check for the engine).
func ShouldDoubleCheckSentinel(t Turn) bool {
	src := ""
	if t.TurnSource != nil {
		src = *t.TurnSource
	}
	if src == "follow_up" {
		it := 1
		if t.FollowUpIteration != nil && *t.FollowUpIteration != 0 {
			it = *t.FollowUpIteration
		}
		return it < FollowUpStrictIteration
	}
	if src == "chat" {
		return true
	}
	transcript := ""
	if t.Transcript != nil {
		transcript = *t.Transcript
	}
	if src == "wake" && t.WakeVerified != nil && !*t.WakeVerified &&
		!(textfilter.IsActionCommandShaped(transcript) || textfilter.IsReportShaped(transcript)) {
		return false
	}
	if src == "wake" && t.WakeConfidence != nil && *t.WakeConfidence >= WakeConfidentThreshold {
		return true
	}
	return t.PreWakeSpeechSeconds != nil && *t.PreWakeSpeechSeconds < QuietThresholdS
}

const mediaContextNote = " Context: music was playing from this node's own speaker when the wake " +
	"fired. Speech or lyrics in the media can false-wake the node, but " +
	"people also often talk to you over their music — especially to change " +
	"or stop it — so weigh the transcript content itself. A music-control " +
	"command (stop, pause, skip, next, play, volume) is directed at you: " +
	"act on it."

func withMediaNote(hint string, media bool) string {
	if !media {
		return hint
	}
	return hint[:len(hint)-1] + mediaContextNote + "]"
}

func commandShaped(transcript *string, media bool) bool {
	return transcript != nil && (textfilter.IsDeviceCommandShaped(*transcript) ||
		(media && textfilter.IsMusicControlShaped(*transcript)))
}

func wakeHint(conf *float64, verified *bool, transcript *string, media bool) string {
	if verified != nil && !*verified && !commandShaped(transcript, media) {
		return withMediaNote("[turn context: the recorded wake clip did not clearly contain "+
			"the wake word when transcribed — weigh this as one signal that "+
			"the wake may have been a detector misfire, not as proof. A "+
			"coherent command or question addressed to you should still be "+
			"answered. Emit <not_for_me/> only when the transcript ALSO "+
			"reads like speech meant for someone else (a reply to another "+
			"person, a mid-story line, a dialogue fragment) — and note that "+
			"bare interjections and one- or two-word fragments (\"Okay.\", "+
			"\"Wow.\", \"Thank you.\") are the single most common shape of a "+
			"detector misfire on an unverified clip.]", media)
	}
	if conf != nil && *conf < WakeConfidentThreshold {
		return withMediaNote("[turn context: wake word fired at low confidence ("+f2(*conf)+
			") — possibly a false wake. Judge by the "+
			"transcript: a coherent command or question is still for you; "+
			"fragments, half-sentences, or noise are not.]", media)
	}
	scored := ""
	if conf != nil {
		scored = " (detection confidence " + f2(*conf) + ")"
	}
	return withMediaNote("[turn context: fresh wake — the user said your wake word"+scored+". "+
		"This turn is addressed to you: answer it or run the tool it calls "+
		"for. If it merely ASKS about the speaker's own life, answer DIRECTLY "+
		"from your User Profile when the fact is listed there (only call recall "+
		"when it's NOT — never go silent); but if the user REPORTS or REQUESTS "+
		"an action, call the tool that does it — never just say you did it. "+
		"Reserve <not_for_me/> for STT artifacts or speech explicitly aimed at "+
		"another person.]", media)
}

func followUpHint(t Turn, media bool) string {
	iteration := 1
	if t.FollowUpIteration != nil && *t.FollowUpIteration != 0 {
		iteration = max(1, *t.FollowUpIteration)
	}
	it := strconv.Itoa(iteration)
	shaped := commandShaped(t.Transcript, media)
	addressed := ""
	if !shaped && t.Transcript != nil {
		addressed = textfilter.AddressedHouseholdMember(*t.Transcript, t.MemberNames)
	}
	addressing := ""
	if addressed != "" {
		addressing = " This utterance directly addresses " + addressed + " by name — " +
			addressed + " is another member of this household, so it is " +
			"near-certainly meant for them, not you. Emit <not_for_me/> " +
			"unless it ALSO asks you something directly."
	}
	if t.ConversationWakeVerdict != nil && *t.ConversationWakeVerdict == DoubtedWakeVerdict && !shaped {
		wrapUp := ""
		if t.DoubtRound != nil && t.DoubtMaxRounds != nil && *t.DoubtRound >= *t.DoubtMaxRounds {
			wrapUp = " This suspected-misfire conversation has already run " +
				strconv.Itoa(*t.DoubtRound) + " answered rounds — wrap up now: if this " +
				"really is addressed to you, answer briefly and append " +
				"<exchange_complete/> to close the exchange; if it is " +
				"not, emit <not_for_me/>."
		}
		return "[turn context: follow-up window, iteration " + it + " — " +
			"there was no wake word, and this conversation BEGAN as a " +
			"suspected detector misfire (the original wake clip did not " +
			"contain the wake word when transcribed). The mic stayed open " +
			"after your last reply, so what it hears now is likely the " +
			"room's own conversation continuing without you. If this " +
			"utterance reads like people talking to each other (a reply " +
			"to someone else, a third-person reference, a mid-story line, " +
			"a plan being made between people), emit <not_for_me/>. If it " +
			"is unmistakably addressed to you, answer it — or run the " +
			"tool it calls for." + addressing + wrapUp + "]"
	}
	escalation := ""
	if iteration >= FollowUpStrictIteration {
		escalation = " This window has stayed open across several turns; the room has " +
			"likely moved on — respond only to explicit engagement (your name, " +
			"a direct question to you, an unmistakable continuation)."
	}
	return "[turn context: follow-up window, iteration " + it + " — there " +
		"was no wake word; the mic stayed open after your last reply to " +
		"catch a continuation. If this clearly continues your exchange, " +
		"answer it — or run the tool it calls for; don't reply with bare " +
		"prose when the request needs a tool. If the room's conversation has " +
		"moved on without you, emit <not_for_me/> — your exchange is over, " +
		"and going quiet is the designed ending, not a failure." +
		addressing + escalation + "]"
}

// pyStrip is str.strip().
func pyStrip(s string) string { return parse.PyStrip(s) }

// pyFloat is Python float(v) for a decoded JSON value (number, bool, numeric string).
func pyFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case json.Number:
		f, err := strconv.ParseFloat(x.String(), 64)
		return f, err == nil
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f, !math.IsInf(f, 0)
	case string:
		s := strings.ReplaceAll(pyStrip(x), "_", "")
		switch strings.ToLower(strings.TrimLeft(s, "+-")) {
		case "inf", "infinity", "nan":
		default:
			if strings.ContainsAny(s, "xXpP") {
				return 0, false
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
				return f, true
			}
			return 0, false
		}
		return f, true
	}
	return 0, false
}
