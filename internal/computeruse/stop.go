package computeruse

import (
	"slices"
	"strings"
	"unicode"
)

// stopPhrases are whole commands that end a computer task (Jarvis controlling the computer, or
// recording a macro). The whole utterance must be one (after the fillers): "don't stop", "stop
// the music", "I'm done with dinner" or "wait" are not. The agent stops the task locally on one
// (internal/agent/voice/computer.go), and command-center makes sure the turn stops the
// recording too (#288).
var stopPhrases = []string{
	"stop", "stop it", "stop that", "stop stop", "stop everything", "stop the task",
	"stop what you're doing", "cancel", "cancel it", "cancel that", "cancel the task",
	"that's enough", "thats enough", "enough", "abort", "quit", "never mind", "nevermind",
	// A recording's (#288): what start_recording tells the user to say, and its neighbours.
	"stop recording", "stop the recording", "stop watching", "stop watching me",
	"i'm done", "im done", "i am done", "i'm all done", "all done", "done", "we're done",
	"i'm finished", "im finished", "i am finished", "finished",
	"that's it", "thats it", "that is it", "that's all", "thats all",
}

// stopFillers are dropped from either end of the command before matching.
var stopFillers = []string{"hey", "ok", "okay", "alright", "jarvis", "please", "now", "just", "thanks"}

// IsStopPhrase reports whether a voice command means "stop what you're doing on the computer"
// ("Stop.", "Jarvis, stop it", "That's enough", "Never mind", "Stop recording.", "I'm done."),
// with dictate.ClassifyAnswer's normalisation (lowercase, letters, digits and apostrophes).
func IsStopPhrase(s string) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.ReplaceAll(s, "’", "'")) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	for len(words) > 0 && slices.Contains(stopFillers, words[0]) {
		words = words[1:]
	}
	for len(words) > 0 && slices.Contains(stopFillers, words[len(words)-1]) {
		words = words[:len(words)-1]
	}
	return slices.Contains(stopPhrases, strings.Join(words, " "))
}
