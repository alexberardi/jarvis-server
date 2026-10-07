package parse

import (
	"regexp"
)

// Sentinel literals the model is told to emit (core_rules NOT_FOR_ME / EXCHANGE_COMPLETE).
const (
	NotForMeLiteral         = "<not_for_me/>"
	ExchangeCompleteLiteral = "<exchange_complete/>"
)

var (
	// not_for_me.py _SENTINEL_RE: case, separators, optional self-close and stray
	// whitespace tolerated; anchored to angle brackets so prose "not for me" never matches.
	notForMeRE = regexp.MustCompile(`(?i)<[` + pySpaceClass + `]*not[` + pySpaceClass + `_\-]+for[` + pySpaceClass + `_\-]+me[` + pySpaceClass + `]*/?[` + pySpaceClass + `]*>`)
	// exchange_complete.py _MARKER_RE (the separator is optional here: "*", not "+").
	exchangeCompleteRE = regexp.MustCompile(`(?i)<[` + pySpaceClass + `]*exchange[` + pySpaceClass + `_\-]*complete[` + pySpaceClass + `]*/?[` + pySpaceClass + `]*>`)
	// tool_execution_engine._THINK_BLOCK_RE: reasoning never counts as a sentinel; an
	// unclosed block (length truncation) runs to end of text.
	thinkAnyRE = regexp.MustCompile(`(?is)<think>.*?(?:</think>|\z)`)
)

// OutsideThink is the engine's _outside_think: text with every <think>…</think> region (any
// case) removed, an unclosed trailing one to end of text.
func OutsideThink(text string) string {
	if text == "" {
		return text
	}
	return thinkAnyRE.ReplaceAllLiteralString(text, "")
}

// ContainsNotForMe reports whether text carries the <not_for_me/> sentinel anywhere
// (not_for_me.contains_sentinel). Callers that must ignore reasoning pass OutsideThink(text);
// SentinelNotForMe does both.
func ContainsNotForMe(text string) bool { return text != "" && notForMeRE.MatchString(text) }

// StripNotForMe removes every sentinel and trims (not_for_me.strip_sentinel).
func StripNotForMe(text string) string {
	if text == "" {
		return ""
	}
	return PyStrip(notForMeRE.ReplaceAllLiteralString(text, ""))
}

// ContainsExchangeComplete reports whether text carries <exchange_complete/>
// (exchange_complete.contains_marker).
func ContainsExchangeComplete(text string) bool {
	return text != "" && exchangeCompleteRE.MatchString(text)
}

// StripExchangeComplete removes every marker and trims (exchange_complete.strip_marker).
func StripExchangeComplete(text string) string {
	if text == "" {
		return ""
	}
	return PyStrip(exchangeCompleteRE.ReplaceAllLiteralString(text, ""))
}

// SentinelNotForMe is the engine's step h check: the sentinel outside <think> in any of the
// given texts (raw content, parsed assistant message, …).
func SentinelNotForMe(texts ...string) bool {
	for _, t := range texts {
		if ContainsNotForMe(OutsideThink(t)) {
			return true
		}
	}
	return false
}

// SentinelExchangeComplete is the engine's terminal-marker check for <exchange_complete/>
// outside <think> in any of the given texts.
func SentinelExchangeComplete(texts ...string) bool {
	for _, t := range texts {
		if ContainsExchangeComplete(OutsideThink(t)) {
			return true
		}
	}
	return false
}
