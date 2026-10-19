package textfilter

import (
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// Beyond legacy: shapes for the follow-through guards (cc/engine.go), found on a desktop agent's
// real test pass (2026-10-15): "Remember that my parking spot is B12." got "Got it, Jordan.
// Parking spot B12 noted." with no tool call; "Do you remember where my parking spot is?" got
// "Not in my memory" without a search; "Open Omerite and type hello there." opened the app and
// then told the user to type.

// negation is a clause that says something did not happen.
const negation = `\b(?:not|never|nothing|no|\w+n['’]t)\b`

var (
	// claimBareRE: a short clause ending in a bare participle — "Parking spot B12 noted.",
	// "Noted!", "All saved." — unless the clause is negated ("That wasn't saved.").
	claimBareRE = mustPyI(`(?:^|[.!?,;:—–]\s*)(?![^.!?,;:—–]*` + negation + `)` +
		`(?:[\w'’-]+\s+){0,4}(?:noted|saved|stored|logged|remembered|recorded)(?=\s*(?:[.!]|$))`)
	// claimMindRE: "I'll keep that in mind", "I've made a note of it". Only an answer to a
	// memory request (ClaimsRemembered): in small talk "I'll keep that in mind" claims nothing
	// (legacy golden).
	claimMindRE = mustPyI(`\b(?:keep(?:ing)?|kept)\s+(?:that|it|this)\s+in\s+mind\b|\b(?:make|made|making)\s+a\s+note\b`)
	// ackOpenerRE: a reply that opens by accepting ("Got it, Jordan. Your parking spot is B12."):
	// to a memory request, that says it was stored.
	ackOpenerRE = mustPyI(`^\W*(?:got\s+it|okay|ok|sure|alright|all\s+right|noted|will\s+do|done|you\s+got\s+it|understood)\b`)

	// rememberRequestRE: an utterance asking to store something, plain or polite.
	rememberRequestRE = mustPyI(verbPrefix + `(?:(?:can|could|would|will)\s+you\s+(?:please\s+)?)?` +
		`(?:remember|note|don'?t\s+forget|keep\s+in\s+mind|make\s+a\s+note)\b\s+\S+`)

	// deniesStoredFactRE: a reply saying the fact isn't stored or known.
	deniesStoredFactRE = mustPyI(`\b(?:not|nothing)\s+(?:in|on)\s+(?:my\s+)?(?:memory|memories|records?|notes?|file)\b` +
		`|\bi\s+(?:don'?t|do\s+not|didn'?t|haven'?t|have\s+not)\s+(?:got\s+)?(?:have\s+)?(?:any\s+)?(?:record|memory|memories|note)s?\b` +
		`|\bi\s+(?:don'?t|do\s+not)\s+(?:have|know|see|remember|recall)\b[^.!?]*` +
		`\b(?:stored|saved|noted|on\s+file|in\s+(?:my\s+)?memory|on\s+record|recorded|your|that)\b` +
		`|\bi\s+(?:don'?t|do\s+not)\s+(?:remember|recall)\b`)
	// asksOwnFactRE: a question about the speaker's own facts ("where's my…", "do you remember…").
	asksOwnFactRE = mustPyI(`\b(?:my|mine|i|me)\b|\b(?:remember|recall)\b`)

	// stopWatchingRequestRE: an utterance asking to stop a watch (#221): "stop watching the
	// example.com one", "cancel the kettle watch", "I don't need the kettle watch anymore".
	stopWatchingRequestRE = mustPyI(`\b(?:stop|quit|cancel|end|remove|delete|drop)\b(?:\s+\S+){0,4}?\s+` +
		`(?:watch(?:es|ing)?|track(?:ing)?|monitor(?:ing)?|keeping\s+an\s+eye)\b` +
		`|\b(?:don'?t|do\s+not|no\s+longer)\s+(?:(?:need|want)\b[^.!?]*?\s)?(?:watch|track|monitor)`)
	// stoppedClaimRE: a reply saying a watch was stopped, unless its clause is negated ("I
	// haven't stopped it", "Nothing was cancelled"), that it will be ("I'll stop watching it"),
	// or that it no longer is watched ("I won't watch it anymore").
	stoppedClaimRE = mustPyI(`(?:^|[.!?,;:—–]\s*)(?![^.!?,;:—–]*` + negation + `)[^.!?,;:—–]*?` +
		`\b(?:stopped|cancel+ed|removed|deleted|turned\s+off)\b` +
		`|\bi(?:'ll|\s+will)\s+(?:stop|cancel)\b|\bno\s+longer\s+(?:watching|tracking|monitoring)\b|\bwon'?t\s+(?:watch|track|monitor)\w*\b[^.!?]*\banymore\b`)

	// typeAfterOpenRE: "open X and type Y", "launch X then write Y": a request to open an app and
	// type words into it, with words to type. Speech can run "type" into the next word ("Open
	// Neovim and Typeello World" for "type hello world"), so "type" glued to more letters counts,
	// unless the word is one of type's own forms or compounds.
	typeAfterOpenRE = mustPyI(`\b(?:open|launch|start|pull\s+up|bring\s+up)\b.+?(?:\band\b|\bthen\b|&)\s*(?:then\s+)?` +
		`(?:please\s+)?(?:(?:type|write|jot(?:\s+down)?|dictate|enter|put)\b` +
		`|type(?!(?:s|d|r|rs|script|writer|writers|set|face|faces|cast)\b)(?=\w))\s*(?<words>\S[^\n]*)`)
)

// claimsBeyondLegacy reports the follow-through claim shapes ResponseClaimsAction adds.
func claimsBeyondLegacy(s string) bool {
	return claimBareRE.search(s)
}

// ClaimsRemembered reports a reply to a memory request that says it was stored: any action
// claim, "I'll keep that in mind", or one that opens by accepting ("Got it, …"). A question back
// claims nothing.
func ClaimsRemembered(reply string) bool {
	s := parse.PyStrip(reply)
	if s == "" || strings.HasSuffix(s, "?") {
		return false
	}
	return ResponseClaimsAction(s) || claimMindRE.search(s) || ackOpenerRE.search(s)
}

// IsRememberRequest reports an utterance asking Jarvis to store a fact ("remember that…",
// "can you remember…", "note that…", "don't forget…").
func IsRememberRequest(text string) bool {
	s := parse.PyStrip(text)
	if s == "" || HasMultiSpeakerMarkers(s) {
		return false
	}
	return rememberRequestRE.search(s)
}

// IsStopWatchingRequest reports an utterance asking Jarvis to stop watching something.
func IsStopWatchingRequest(text string) bool {
	s := parse.PyStrip(text)
	return s != "" && !HasMultiSpeakerMarkers(s) && stopWatchingRequestRE.search(s)
}

// ClaimsStoppedWatching reports a reply to a stop-watching request that says it was stopped:
// "stopped", "no longer watching", or one that opens by accepting ("Okay, …"). A question back
// claims nothing. Not ResponseClaimsAction: it reads "Nothing was cancelled." as a claim.
func ClaimsStoppedWatching(reply string) bool {
	s := parse.PyStrip(reply)
	if s == "" || strings.HasSuffix(s, "?") {
		return false
	}
	return stoppedClaimRE.search(s) || ackOpenerRE.search(s)
}

// DeniesStoredFact reports a reply saying a fact about the user isn't stored ("Not in my
// memory", "I don't have your gate code stored").
func DeniesStoredFact(reply string) bool {
	s := parse.PyStrip(reply)
	return s != "" && deniesStoredFactRE.search(s)
}

// AsksOwnFact reports a question about the speaker's own life: question-shaped, and about "my"
// things or what Jarvis remembers.
func AsksOwnFact(utterance string) bool {
	s := parse.PyStrip(utterance)
	return s != "" && IsQuestionShaped(s) && !HasMultiSpeakerMarkers(s) && asksOwnFactRE.search(s)
}

// IsTypeAfterOpen reports "open X and type Y": an app to open, then words to type into it.
func IsTypeAfterOpen(utterance string) bool {
	return TypeAfterOpenWords(utterance) != ""
}

// TypeAfterOpenWords is Y in "open X and type Y", as said (with "type" left out when speech ran
// it into the first word: "Typeello World" → "ello World"), or "" when the utterance isn't one.
func TypeAfterOpenWords(utterance string) string {
	s := parse.PyStrip(utterance)
	if s == "" || HasMultiSpeakerMarkers(s) {
		return ""
	}
	m, err := typeAfterOpenRE.re.FindStringMatch(s)
	if err != nil || m == nil {
		return ""
	}
	g := m.GroupByName("words")
	if g == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(g.String()), ".!?,;: ")
}
