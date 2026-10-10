package textfilter

import (
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// Shape detectors (core/transcript_filter.py). They classify the transcript's FORM, never its
// meaning, and feed the direction/turn hints and the force-tools guard.

const bracketedToken = `[\*\[\(\<][^\*\]\)\>]+[\*\]\)\>]`

var (
	bracketedOnlyRE = mustPy(`^\s*(?:` + bracketedToken + `\s*[\.,!?\-]?\s*)+\.?\s*$`)
	pureFillerRE    = mustPy(`^\s*(?:\.{2,}|-{2,}|\?{2,}|!{2,}|…+)\s*$`)
	speakerDashRE   = mustPy(`(?:^|\s)-\s+\S`)
	wordRE          = mustPy(`[\w']+`)
	wordFindRE      = regexp2Words{}
)

var deviceCommandVerbs = []string{
	"turn", "switch", "flip", "toggle", "dim", "brighten",
	"lock", "unlock", "set", "start", "stop", "pause", "resume",
	"open", "close", "shut",
}

const verbPrefix = `^\s*(?:hey\s+)?(?:\w+[,.!]\s+)?(?:please,?\s+)?`

var deviceCommandRE = mustPyI(verbPrefix + `(?:` + strings.Join(deviceCommandVerbs, "|") + `)\b\s+\S+`)

var musicControlRE = mustPyI(verbPrefix +
	`(?:` +
	`(?:stop|pause|resume|skip|mute|unmute)\b` +
	`|next\b` +
	`|play\b` +
	`|(?:turn|crank)\s+(?:it|that|this|the\s+\w+)\s+(?:up|down)\b` +
	`|turn\s+(?:up|down|off)\s+the\s+\w+` +
	`|volume\s+(?:up|down)\b` +
	`|(?:louder|quieter|softer)\b` +
	`|(?:go\s+back|previous)\b` +
	`)`)

var actionCommandVerbs = append(append([]string(nil), deviceCommandVerbs...),
	"play", "log", "add", "remind", "cancel", "mark", "record", "create",
	"delete", "remove", "schedule", "send", "text", "call", "snooze",
	"skip", "mute", "unmute", "check", "save", "note", "clear",
)

var actionCommandRE = mustPyI(verbPrefix + `(?:` + strings.Join(actionCommandVerbs, "|") + `)\b\s+\S+`)

var questionLeadWords = []string{
	"what", "how", "why", "when", "where", "who", "whose", "which",
	"should", "could", "can", "would", "will", "shall", "may", "might",
	"do", "does", "did", "is", "are", "am", "was", "were", "have", "has",
	"isn't", "aren't", "don't", "doesn't", "didn't", "won't", "wouldn't",
	"couldn't", "shouldn't",
}

var questionLeadRE = mustPyI(verbPrefix + `(?:` + strings.Join(questionLeadWords, "|") + `)\b`)

var reportShapeRE = mustPyI(`^\s*(?:hey\s+)?(?:\w+[,.!]\s+)?` +
	`(?:the\s+)?[\w']+\s+` +
	`(?:just\s+|already\s+|finally\s+)?` +
	`(?:took|takes|taken|gave|given|administered|got)\s+` +
	`(?:(?:the\s+)?[\w']+\s+)?` +
	`(?:his|her|their|my|our|its|the|a|an|some|him|them)\b`)

var (
	claimBaseVerbs = []string{
		"check", "look", "mark", "log", "record", "add", "set", "remind",
		"schedule", "cancel", "delete", "remove", "turn", "play", "pause",
		"start", "send", "update", "save", "note", "create", "put", "track",
		"book", "order", "text", "message",
	}
	claimPastVerbs = []string{
		"checked", "looked", "marked", "logged", "recorded", "added", "set",
		"reminded", "scheduled", "cancelled", "canceled", "deleted", "removed",
		"turned", "played", "paused", "started", "sent", "updated", "saved",
		"noted", "created", "put", "tracked", "booked", "ordered", "texted",
		"messaged",
	}
	claimIngVerbs = []string{
		"checking", "looking", "marking", "logging", "recording", "adding",
		"setting", "reminding", "scheduling", "cancelling", "canceling",
		"deleting", "removing", "turning", "playing", "pausing", "starting",
		"sending", "updating", "saving", "noting", "creating", "putting",
		"tracking", "booking", "ordering", "texting", "messaging",
	}
	claimIntentRE = mustPyI(`\b(?:i'?ll|i\s+will|i'?m\s+going\s+to|i\s+am\s+going\s+to|let\s+me|i\s+can)\s+` +
		`(?:go\s+ahead\s+and\s+)?(?:just\s+|now\s+)?` +
		`(?:` + strings.Join(claimBaseVerbs, "|") + `)\b`)
	claimDoneRE = mustPyI(`\b(?:i'?ve|i\s+have|i)\s+` +
		`(?:just\s+|already\s+)?` +
		`(?:` + strings.Join(claimPastVerbs, "|") + `)\b`)
	claimPassiveRE = mustPyI(`\b(?:is|are|was|were|'s|'re|has|have|had)\s+(?:been\s+)?` +
		`(?:now\s+|already\s+)?` +
		`(?:` + strings.Join(claimPastVerbs, "|") + `)\b`)
	claimProgressRE = mustPyI(`(?:\bi'?m\s+(?:just\s+|now\s+)?|^\s*)` +
		`(?:` + strings.Join(claimIngVerbs, "|") + `)\b`)
)

// HasMultiSpeakerMarkers is has_multi_speaker_markers: two or more whisper dash-prefixed
// speaker turns ("- Uh-huh. - Eat it.").
func HasMultiSpeakerMarkers(text string) bool {
	if text == "" {
		return false
	}
	return speakerDashRE.count(text) >= 2
}

// IsDeviceCommandShaped is is_device_command_shaped: an imperative household-device verb plus
// an object, single speaker, no "?".
func IsDeviceCommandShaped(text string) bool {
	if text == "" {
		return false
	}
	s := parse.PyStrip(text)
	if s == "" || strings.Contains(s, "?") || HasMultiSpeakerMarkers(s) {
		return false
	}
	return deviceCommandRE.search(s)
}

// IsQuestionShaped is is_question_shaped: a "?" anywhere, or a leading interrogative or
// fronted auxiliary after an optional politeness/wake prefix.
func IsQuestionShaped(text string) bool {
	if text == "" {
		return false
	}
	s := parse.PyStrip(text)
	if s == "" {
		return false
	}
	if strings.Contains(s, "?") {
		return true
	}
	return questionLeadRE.search(s)
}

// IsActionCommandShaped is is_action_command_shaped: an imperative action verb plus an object,
// single speaker, not a question. The only imperative shape that arms the force-tools guard.
func IsActionCommandShaped(text string) bool {
	if text == "" {
		return false
	}
	s := parse.PyStrip(text)
	if s == "" || IsQuestionShaped(s) || HasMultiSpeakerMarkers(s) {
		return false
	}
	return actionCommandRE.search(s)
}

// IsReportShaped is is_report_shaped: "<subject> took/gave/administered <object> ...".
func IsReportShaped(text string) bool {
	if text == "" {
		return false
	}
	s := parse.PyStrip(text)
	if s == "" || IsQuestionShaped(s) || HasMultiSpeakerMarkers(s) {
		return false
	}
	return reportShapeRE.search(s)
}

// ResponseClaimsAction is response_claims_action: the MODEL's reply promises or claims a tool
// action ("I'll check…", "I've marked…", "…is marked as taken", "Setting a reminder…").
func ResponseClaimsAction(text string) bool {
	if text == "" {
		return false
	}
	s := parse.PyStrip(text)
	if s == "" {
		return false
	}
	return claimIntentRE.search(s) || claimDoneRE.search(s) || claimPassiveRE.search(s) || claimProgressRE.search(s)
}

// IsMusicControlShaped is is_music_control_shaped: a music-control verb opening the utterance
// (bare verbs count), single speaker, no "?". Only meaningful during self-playback.
func IsMusicControlShaped(text string) bool {
	if text == "" {
		return false
	}
	s := parse.PyStrip(text)
	if s == "" || strings.Contains(s, "?") || HasMultiSpeakerMarkers(s) {
		return false
	}
	return musicControlRE.search(s)
}

// --- named-person addressing ---

var addressingExcluded = map[string]bool{"jarvis": true, "hey": true}

var sentenceSplitRE = regexp.MustCompile(`[.!?;]+`)

const humanImperatives = "come|go|stop|wait|get|put|grab|bring|take|look|listen|sit|stand|stay|hold|leave|hurry|let|help|eat|finish|clean|pick"

type nameToken struct{ lowered, display string }

// addressableNameTokens is _addressable_name_tokens: first-name tokens, lowercased, ≥2 chars,
// not jarvis/hey, first occurrence wins (dict insertion order).
func addressableNameTokens(memberNames []string) []nameToken {
	var out []nameToken
	seen := map[string]bool{}
	for _, name := range memberNames {
		words := wordFindRE.findAll(name)
		if len(words) == 0 {
			continue
		}
		tok := words[0]
		lowered := parse.PyLower(tok)
		if len([]rune(lowered)) < 2 || addressingExcluded[lowered] || seen[lowered] {
			continue
		}
		seen[lowered] = true
		out = append(out, nameToken{lowered, tok})
	}
	return out
}

// AddressedHouseholdMember is addressed_household_member: the first-name token of the member a
// sentence of text directly addresses ("Miles, come here", "Miles come here", "come here,
// Miles", "come here Miles"), or "" when none.
func AddressedHouseholdMember(text string, memberNames []string) string {
	if text == "" {
		return ""
	}
	tokens := addressableNameTokens(memberNames)
	if len(tokens) == 0 {
		return ""
	}
	for _, sentence := range sentenceSplitRE.Split(text, -1) {
		s := parse.PyStrip(sentence)
		if s == "" {
			continue
		}
		for _, t := range tokens {
			esc := regexpEscape(t.lowered)
			if mustPyI(`^`+esc+`\s*[,!:]`).search(s) ||
				mustPyI(`^`+esc+`\s+(?:`+humanImperatives+`)\b`).search(s) ||
				mustPyI(`,\s*`+esc+`\s*$`).search(s) ||
				mustPyI(`^(?:`+humanImperatives+`)\b.*\s`+esc+`\s*$`).search(s) {
				return t.display
			}
		}
	}
	return ""
}

// IsAddressedToOtherPerson is is_addressed_to_other_person.
func IsAddressedToOtherPerson(text string, memberNames []string) bool {
	return AddressedHouseholdMember(text, memberNames) != ""
}

// IsShortNonCommandFragment is is_short_non_command_fragment: one or two words that aren't a
// device command.
func IsShortNonCommandFragment(text string) bool {
	if text == "" {
		return false
	}
	n := wordRE.count(text)
	if n == 0 || n > 2 {
		return false
	}
	return !IsDeviceCommandShaped(text)
}

// IsSTTNoise is is_stt_noise: empty, bracket-only annotations (*sniff*, [laughter]),
// punctuation fillers, or nothing word-like at all. Noise never reaches the LLM.
func IsSTTNoise(text string) bool {
	if text == "" {
		return true
	}
	s := parse.PyStrip(text)
	if s == "" {
		return true
	}
	if pureFillerRE.search(s) || bracketedOnlyRE.search(s) {
		return true
	}
	return !wordRE.search(s)
}

// regexpEscape is re.escape for the characters a name token can hold (\w and ').
func regexpEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// regexp2Words is re.findall(r"[\w']+", s).
type regexp2Words struct{}

func (regexp2Words) findAll(s string) []string {
	var out []string
	m, _ := wordRE.re.FindStringMatch(s)
	for m != nil {
		out = append(out, m.String())
		m, _ = wordRE.re.FindNextMatch(m)
	}
	return out
}

// Acknowledgement shape (Go-only, not in the legacy filter): a closing remark that answers the
// assistant rather than asking for anything ("thanks", "ok, got it", "no thanks", "never mind").
// Every word is from ackWords and at least one is from ackClosers, so a bare "ok", "yes" or
// "sure" (which can accept an offer) is not one.
var (
	ackWords = wordSet("ok okay k alright all right thanks thank thx ty you so much very " +
		"cool nice great perfect awesome good fine got it gotcha sounds no nope nah never mind " +
		"nevermind that's thats all bye goodbye night cheers appreciate wow lol haha oh hey jarvis")
	ackClosers = wordSet("thanks thank thx ty cool nice got gotcha no nope nah never nevermind " +
		"bye goodbye cheers appreciate wow lol haha")
)

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		out[w] = true
	}
	return out
}

// IsAcknowledgementShaped reports a closing remark: short, every word an acknowledgement word,
// at least one a closer. The force-tools guard never fires on one.
func IsAcknowledgementShaped(text string) bool {
	s := strings.ReplaceAll(parse.PyLower(parse.PyStrip(text)), "’", "'")
	if s == "" || strings.Contains(s, "?") {
		return false
	}
	words := strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r == '\'' || r >= '0' && r <= '9')
	})
	if len(words) == 0 || len(words) > 6 {
		return false
	}
	closer := false
	for _, w := range words {
		if !ackWords[w] {
			return false
		}
		closer = closer || ackClosers[w]
	}
	return closer
}
