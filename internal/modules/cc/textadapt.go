package cc

import (
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/hints"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	tf "github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Thin bindings from the handler to the byte-exact text helpers (textfilter, hints; golden-
// tested against the legacy Python in fixtures/golden/voice).

var (
	cleanForTTS           = tf.CleanForTTS
	isSTTNoise            = tf.IsSTTNoise
	isKnowledgeDelegation = tf.IsKnowledgeDelegation
	canonicalArgsKey      = tf.CanonicalArgsKey
	acknowledgment        = tf.Acknowledgment
)

const (
	doubleCheckMessage     = tf.NotForMeDoubleCheckPrompt
	isoDateRetry           = tf.ISODateRetryNag
	maxIterationsFallback  = tf.MaxIterationsFallback
	fillerReplacement      = tf.FillerClarification
	continueStreamOverride = tf.PlainTextOverride
)

var (
	mustCallRetry     = tf.MustCallRetryNag
	toolDedupeNudge   = tf.ToolDedupeNag
	invalidParamRetry = tf.InvalidParamRetryNag
)

// splitSentences are the stripped, non-empty pieces of r"(?<=[.!?])\s+" (the fast paths speak
// each one).
func splitSentences(s string) []string {
	var out []string
	for _, p := range tf.SplitSentenceBoundary(s) {
		if p = parse.PyStrip(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isGenericFiller(msg string) bool {
	return tf.RewriteTerminalFiller("complete", msg) != msg
}

// forceGate is the shape-and-keyword half of the force-tools guard.
func forceGate(conv *conversation, utterance string, replies []string) bool {
	pool := tf.CollectToolKeywords(objectsAsAny(conv.commands), toolsAsAny(conv.tools))
	return tf.ForceToolsGate(utterance, replies, pool)
}

func objectsAsAny[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func toolsAsAny(ts []prompts.Tool) []any { return objectsAsAny(ts) }

// findInvalidParams validates client calls against the cached command parameter types.
func findInvalidParams(calls []parse.ToolCall, commands []*pyjson.Object) []string {
	args := make([]tf.ToolCallArgs, len(calls))
	for i, c := range calls {
		args[i] = tf.ToolCallArgs{Name: c.Function.Name, Arguments: c.Function.Arguments}
	}
	return tf.FindInvalidParams(args, tf.BuildParamMaps(objectsAsAny(commands)))
}

// --- hints ---

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func directionHint(in turnInput, speakerKnown bool) string {
	transcript := in.VoiceCommand
	return hints.DirectionHint(hints.Direction{
		PreWakeSpeechSeconds: in.PreWakeSeconds, WakeConfidence: in.WakeConfidence, TurnSource: strPtr(in.Source),
		Transcript: &transcript, SpeakerKnown: &speakerKnown, SelfPlayback: in.SelfPlayback,
		SelfPlaybackKind: strPtr(in.SelfPlaybackKind),
	})
}

func affectHint(affect map[string]any) string {
	if affect == nil {
		return ""
	}
	return hints.AffectHint(affect)
}

func hintTurn(in turnInput, st turnState, members []string) hints.Turn {
	transcript := in.VoiceCommand
	return hints.Turn{
		TurnSource: strPtr(in.Source), WakeConfidence: in.WakeConfidence, FollowUpIteration: in.FollowUpIteration,
		PreWakeSpeechSeconds: in.PreWakeSeconds, WakeVerified: st.wakeVerified, Transcript: &transcript,
		SelfPlayback: in.SelfPlayback, SelfPlaybackKind: strPtr(in.SelfPlaybackKind),
		ConversationWakeVerdict: strPtr(st.conversationWakeVerdict), DoubtRound: st.doubtRound,
		DoubtMaxRounds: st.doubtMaxRounds, MemberNames: members,
	}
}

func turnHint(in turnInput, st turnState, members []string) string {
	return hints.TurnHint(hintTurn(in, st, members))
}

func doubleCheckSentinel(in turnInput, st turnState) bool {
	return hints.ShouldDoubleCheckSentinel(hintTurn(in, st, nil))
}

func profileMatchHint(utterance, speakerBlock string) string {
	return hints.ProfileMatchHint(utterance, speakerBlock)
}

// wakeVerdictFor is run_wake_verification's verdict for a wake-clip transcript.
func wakeVerdictFor(transcript, phrase string) *wakeVerdict {
	phrase = tf.NormalizeWakePhrase(phrase)
	ok := tf.WakePhrasePresent(transcript, phrase)
	v := &wakeVerdict{Verified: ok, Verdict: "unverified", Transcript: strings.TrimSpace(transcript),
		Similarity: tf.WakePhraseSimilarity(transcript, phrase)}
	if ok {
		v.Verdict = "verified"
	}
	return v
}
