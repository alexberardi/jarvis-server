package textfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Tool-execution-engine helpers (core/tool_execution_engine.py, core/tool_routing.py). The
// nag strings are prompt bytes (D22: byte-exact).

// Nag tags. A nag is a standalone role=system message whose content STARTS with its tag
// (_is_retry_nag); [ISO_DATE_RETRY] is counted by substring, as legacy did.
const (
	TagMustCallRetry       = "[MUST_CALL_RETRY]"
	TagToolDedupe          = "[TOOL_DEDUPE]"
	TagInvalidParamRetry   = "[INVALID_PARAM_RETRY]"
	TagISODateRetry        = "[ISO_DATE_RETRY]"
	TagNotForMeDoubleCheck = "[NOT_FOR_ME_DOUBLE_CHECK]"
)

// ISODateRetryNag is the [ISO_DATE_RETRY] system message.
const ISODateRetryNag = "[ISO_DATE_RETRY] The dates you provided were incorrect. " +
	"Use date KEY STRINGS only: today, tomorrow, yesterday, this_weekend, etc. " +
	`Example: {"resolved_datetimes": ["today"]}. Try again.`

// NotForMeDoubleCheckPrompt is the USER message of the sentinel double-check pass (sent with
// max_tokens 1536).
const NotForMeDoubleCheckPrompt = "[NOT_FOR_ME_DOUBLE_CHECK] You dismissed that utterance with " +
	"<not_for_me/>, but the wake evidence says it was " +
	"directed at you (wake word fired; the room was " +
	"quiet). Re-read it and reason it through. " +
	"Remember: speech ABOUT a person or pet by name — " +
	"\"who is Leo?\", \"where's mom?\" — is a question " +
	"FOR you; answer it from the User Profile or the " +
	"recall tool. Only if you still conclude the " +
	"speech was not aimed at you, reply with " +
	"<not_for_me/> alone again. Otherwise answer " +
	"normally or call the right tool. /think"

// MaxIterationsFallback is spoken when the tool loop exhausts its iterations (D40 02.Q9:
// a natural fallback replaces "Maximum tool execution iterations reached."; the trace keeps
// error "max_iterations_exceeded").
const MaxIterationsFallback = "Sorry, I got stuck on that."

// MaxIterationsError is the error code kept for traces.
const MaxIterationsError = "max_iterations_exceeded"

// MustCallRetryNag is the force-tools guard's system message; attempt is 1 or 2.
func MustCallRetryNag(attempt int) string {
	return "[MUST_CALL_RETRY] You MUST call a tool. Do NOT answer directly. " +
		"Pick the best matching function from the available tools. " +
		"(attempt " + strconv.Itoa(attempt) + "/2)"
}

// ToolDedupeNag is the [TOOL_DEDUPE] system message for a repeated client call.
func ToolDedupeNag(toolName string) string {
	return TagToolDedupe + " The results of " + toolName + " " +
		"with those arguments are already in this " +
		"conversation above — answer from them " +
		"concisely; do not re-run the tool or repeat " +
		"the full results aloud."
}

// InvalidParamRetryNag is the [INVALID_PARAM_RETRY n/max] system message.
func InvalidParamRetryNag(attempt, maxRetries int, invalid []string) string {
	return "[INVALID_PARAM_RETRY " + strconv.Itoa(attempt) + "/" + strconv.Itoa(maxRetries) + "] " +
		"Some parameters have invalid types or formats. " +
		"Fix the parameters to match expected types and return absolute " +
		"date/time values when required. Invalid: " + strings.Join(invalid, ", ")
}

// IsNag is _is_retry_nag: a role=system message whose content, left-stripped, starts with tag.
func IsNag(role, content, tag string) bool {
	return role == "system" && strings.HasPrefix(strings.TrimLeftFunc(content, parse.IsPySpace), tag)
}

// CanonicalArgsKey is _canonical_args_key: SHA-256 hex of the arguments re-serialised with
// sorted keys and compact separators (ensure_ascii), "null" counting as {}. ok=false when the
// JSON doesn't parse (dedupe then fails open for that call).
func CanonicalArgsKey(arguments string) (string, bool) {
	v, err := pyjson.Loads(arguments)
	if err != nil {
		return "", false
	}
	if v == nil {
		v = pyjson.NewObject()
	}
	var b strings.Builder
	sortedCompact(&b, v)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), true
}

func sortedCompact(b *strings.Builder, v any) {
	switch x := v.(type) {
	case *pyjson.Object:
		keys := append([]string(nil), x.Keys()...)
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(pyjson.Dumps(k, true))
			b.WriteByte(':')
			e, _ := x.Get(k)
			sortedCompact(b, e)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			sortedCompact(b, e)
		}
		b.WriteByte(']')
	default:
		b.WriteString(pyjson.Dumps(v, true))
	}
}

// keywordMatchMinLen is _KEYWORD_MATCH_MIN_LEN.
const keywordMatchMinLen = 3

var nonAlnumRE = regexp.MustCompile(`[^a-z0-9]+`)

// normalizeForKeywordMatch is _normalize_for_keyword_match: lowercase, non [a-z0-9] runs to a
// space, whitespace collapsed, padded with one space each side.
func normalizeForKeywordMatch(text string) string {
	collapsed := strings.Join(strings.FieldsFunc(nonAlnumRE.ReplaceAllString(parse.PyLower(text), " "), parse.IsPySpace), " ")
	return " " + collapsed + " "
}

// UtteranceMatchesKeywords is utterance_matches_keywords: any keyword (≥3 chars after
// normalisation) appears as whole words in the utterance.
func UtteranceMatchesKeywords(utterance string, keywords []string) bool {
	if utterance == "" {
		return false
	}
	nu := normalizeForKeywordMatch(utterance)
	if parse.PyStrip(nu) == "" {
		return false
	}
	for _, kw := range keywords {
		nk := parse.PyStrip(normalizeForKeywordMatch(kw))
		if len([]rune(nk)) < keywordMatchMinLen {
			continue
		}
		if strings.Contains(nu, " "+nk+" ") {
			return true
		}
	}
	return false
}

// CollectToolKeywords is collect_tool_keywords over any number of pyjson-decoded lists of
// command definitions (top-level "keywords") or OpenAI tool dicts (keywords under
// "function"): deduplicated, first-seen order, original casing, blank entries skipped.
func CollectToolKeywords(sources ...[]any) []string {
	seen := map[string]bool{}
	var out []string
	for _, src := range sources {
		for _, it := range src {
			o, _ := it.(*pyjson.Object)
			if o == nil {
				continue
			}
			raw, has := o.Get("keywords")
			if !has || raw == nil {
				if fn, ok := objGet(o, "function").(*pyjson.Object); ok {
					raw = objGet(fn, "keywords")
				}
			}
			list, ok := raw.([]any)
			if !ok {
				continue
			}
			for _, k := range list {
				s, ok := k.(string)
				if ok && parse.PyStrip(s) != "" && !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// ForceToolsGate is the shape-and-keyword half of the force-tools guard (engine step i): with
// the guard otherwise armed (force_tool_calls, fewer than 2 retries, no double-check, no
// terminal sentinel), it reports whether a prose reply should be popped and retried with
// MustCallRetryNag. utterance is the turn's user utterance ("" = none: legacy always-retry);
// replies are the turn's terminal texts outside <think> (raw content, parsed message);
// keywordPool is CollectToolKeywords(available commands, cached tools, turn tools).
func ForceToolsGate(utterance string, replies []string, keywordPool []string) bool {
	if utterance == "" {
		return true
	}
	if IsQuestionShaped(utterance) {
		return false
	}
	actionable := IsActionCommandShaped(utterance) || IsReportShaped(utterance)
	claims := false
	for _, r := range replies {
		if ResponseClaimsAction(r) {
			claims = true
			break
		}
	}
	if !actionable && !claims {
		return false
	}
	text := utterance
	if claims && !actionable {
		parts := []string{utterance}
		for _, r := range replies {
			if r != "" {
				parts = append(parts, r)
			}
		}
		text = strings.Join(parts, " ")
	}
	if len(keywordPool) > 0 && !UtteranceMatchesKeywords(text, keywordPool) {
		return false
	}
	return true
}
