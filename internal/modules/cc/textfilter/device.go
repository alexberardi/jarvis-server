package textfilter

import (
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// Shapes for the device follow-through guard (#512): "Turn off the office light bar" answered
// "Got it, Jordan. The office light bars are now off." with no successful control_device call
// — the model had muted the node instead (control_node) and nothing checked the claim.

var (
	// deviceTargetExcl: targets that are the node's own audio, music or a bare pronoun — those
	// requests are control_node's or a playback tool's, and nagging them would be wrong. The
	// articles are in it so "turn off the music" can't backtrack into article "" + target "the".
	deviceTargetExcl = `(?:it|that|this|them|everything|volume|sound|music|song|audio|mic(?:rophone)?|yourself|the|my|all)`

	// deviceChangeRequestRE: an utterance asking to change a named device's state: "turn off
	// the office light bar", "turn the office light bars off", "switch the fan on", "power off
	// the TV", "lock the front door", "dim the bedroom lights".
	deviceChangeRequestRE = mustPyI(
		`\b(?:turn|switch|power|shut)\s+(?:on|off)\s+(?:the\s+|my\s+|all\s+)?(?!` + deviceTargetExcl + `\b)[\w'’-]+` +
			`|\b(?:turn|switch|power|shut)\s+(?:the\s+|my\s+|all\s+)?(?!` + deviceTargetExcl + `\b)[\w'’-][\w'’ -]*?\s+(?:on|off)\b` +
			`|\b(?:lock|unlock)\s+(?:the\s+|my\s+)?[\w'’-]+` +
			`|\b(?:dim|brighten)\s+(?:the\s+|my\s+)?[\w'’-]+`)

	// deviceClaimNegation is negation plus intent and hedges: a negated clause, a promise
	// ("I'll turn it off"), or a state report ("already off") claims no change was made.
	deviceClaimNegation = `(?:\b(?:not|never|nothing|no|already|will|going\s+to|let\s+me|try(?:ing)?|unable|failed|cannot)\b|\w+n['’]t\b|['’]ll\b)`

	// deviceChangedClaimRE: a clause asserting a device is in its new state or was switched.
	deviceChangedClaimRE = mustPyI(`(?:^|[.!?,;:—–]\s*)(?![^.!?,;:—–]*` + deviceClaimNegation + `)[^.!?,;:—–]*?` +
		`(?:\b(?:is|are|['’]s|['’]re)\s+(?:now\s+)?(?:on|off|locked|unlocked)\b` +
		`|\b(?:turned|switched|powered|shut)\b[^.!?,;:—–]{0,40}?\b(?:on|off)\b` +
		`|\b(?:locked|unlocked|locking|unlocking)\b)`)
)

// IsDeviceChangeRequest reports an utterance asking to change a smart-home device's state —
// not the node's own audio ("turn down the volume", "mute"), which is control_node's job.
func IsDeviceChangeRequest(text string) bool {
	s := parse.PyStrip(text)
	return s != "" && !HasMultiSpeakerMarkers(s) && deviceChangeRequestRE.search(s)
}

// ClaimsDeviceChanged reports a reply asserting a device was changed or is in its new state
// ("The office light bars are now off.", "I've turned off the TV."). A question back, a
// negated clause, a promise ("I'll turn it off") or "already off" claims nothing.
func ClaimsDeviceChanged(reply string) bool {
	s := parse.PyStrip(offerTailRE.sub(parse.PyStrip(reply), "."))
	if s == "" || strings.HasSuffix(s, "?") {
		return false
	}
	return deviceChangedClaimRE.search(s)
}
