package computer

import "regexp"

// injectionAsk is the question a run stops on when a page tries to steer the model.
const injectionAsk = "This page is trying to give me instructions, so I stopped and did nothing it said. Should I carry on with the task?"

// injectionPatterns are phrases page text has no business containing unless it is talking to the
// model. They are deliberately few and plain: a hit stops the run and asks, it never acts.
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bignore\s+(all|your|previous|any|the)\s+(\w+\s+)?instructions\b`),
	regexp.MustCompile(`(?i)\byou\s+are\s+(an\s+ai|chatgpt|jarvis|a\s+language\s+model)\b`),
	regexp.MustCompile(`(?i)\bsystem\s+prompt\b`),
	regexp.MustCompile(`(?i)(^|\s)assistant\s*:`),
}

// injectionLike reports whether page text reads like instructions to the model.
func injectionLike(text string) bool {
	for _, re := range injectionPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}
