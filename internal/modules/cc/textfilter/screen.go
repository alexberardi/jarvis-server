package textfilter

import "github.com/alexberardi/jarvis-server/internal/modules/cc/parse"

// Shape for the screen follow-through guard (VOICE-FLOWS.md Q32): "Scroll down a bit more" got
// "Already scrolled down a bit." and "Click it again" got "Already clicked on Wikipedia.", with
// no action this turn. One shape for every on-screen action, not one per verb.

var (
	// screenClaimNegation is deviceClaimNegation without "already": "already scrolled" is a
	// claim here (the user asked for it again), not a state report.
	screenClaimNegation = `(?:\b(?:not|never|nothing|no|will|going\s+to|let\s+me|try(?:ing)?|unable|failed|cannot|want|should|shall)\b|\w+n['’]t\b|['’]ll\b)`

	// screenClaimLead is what may come before the verb in a claim: nothing ("Already clicked on
	// Wikipedia."), the speaker ("I've opened…", "Done — I typed…") or filler ("Got it -"). A
	// clause with its own subject describes something, not an action on the computer: "when they
	// went up from the fifth tier", "the club moved to a new stadium" (2026-11-13: a page summary
	// read as a claim, nudged and written twice).
	screenClaimLead = `(?:\s*(?:\b(?:i|i['’]ve|ive|i\s+have|i['’]d|i\s+just|just|already|also|now|then|and|so|done|` +
		`got\s+it|ok|okay|sure|alright|all\s+right|all\s+set|yep|yes|there)\b|-)\s*)*`

	screenClaimVerbs = `\b(?:scrolled|clicked|tapped|pressed|typed|entered|filled(?:\s+in)?|opened|closed|launched|switched|` +
		`selected|navigated|searched|went|brought\s+up|pulled\s+up|maximi[sz]ed|minimi[sz]ed|focused|moved|reloaded|refreshed)\b`

	// screenClaimRE: a clause saying an on-screen action was done; one word may name what was
	// pressed or typed ("Got it — enter pressed").
	screenClaimRE = mustPyI(`(?:^|[.!?,;:—–]\s*)(?![^.!?,;:—–]*` + screenClaimNegation + `)` +
		`(?:` + screenClaimLead + screenClaimVerbs + `|\s*\w+\s+(?:pressed|typed|entered)\b)`)
)

// ClaimsScreenAction reports a reply saying something was done on the computer ("Got it -
// scrolled down a bit.", "Already clicked on Wikipedia.", "I've opened Firefox"). A question
// back ("want me to…"), a negated clause or a promise ("I'll click it") claims nothing.
func ClaimsScreenAction(reply string) bool {
	s := parse.PyStrip(offerTailRE.sub(parse.PyStrip(reply), "."))
	return s != "" && screenClaimRE.search(s)
}
