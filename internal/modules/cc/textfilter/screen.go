package textfilter

import "github.com/alexberardi/jarvis-server/internal/modules/cc/parse"

// Shape for the screen follow-through guard (VOICE-FLOWS.md Q32): "Scroll down a bit more" got
// "Already scrolled down a bit." and "Click it again" got "Already clicked on Wikipedia.", with
// no action this turn. One shape for every on-screen action, not one per verb.

var (
	// screenClaimNegation is deviceClaimNegation without "already": "already scrolled" is a
	// claim here (the user asked for it again), not a state report.
	screenClaimNegation = `(?:\b(?:not|never|nothing|no|will|going\s+to|let\s+me|try(?:ing)?|unable|failed|cannot|want|should|shall)\b|\w+n['’]t\b|['’]ll\b)`

	// screenClaimRE: a clause saying an on-screen action was done.
	screenClaimRE = mustPyI(`(?:^|[.!?,;:—–]\s*)(?![^.!?,;:—–]*` + screenClaimNegation + `)[^.!?,;:—–]*?` +
		`\b(?:scrolled|clicked|tapped|pressed|typed|entered|filled(?:\s+in)?|opened|closed|launched|switched|` +
		`selected|navigated|searched|went|brought\s+up|pulled\s+up|maximi[sz]ed|minimi[sz]ed|focused|moved|reloaded|refreshed)\b`)
)

// ClaimsScreenAction reports a reply saying something was done on the computer ("Got it -
// scrolled down a bit.", "Already clicked on Wikipedia.", "I've opened Firefox"). A question
// back ("want me to…"), a negated clause or a promise ("I'll click it") claims nothing.
func ClaimsScreenAction(reply string) bool {
	s := parse.PyStrip(offerTailRE.sub(parse.PyStrip(reply), "."))
	return s != "" && screenClaimRE.search(s)
}
