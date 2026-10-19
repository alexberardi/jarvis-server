package voice

import (
	"log/slog"
	"slices"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
)

// Computer is what Jarvis is doing with this computer (*computer.Activity): while it controls
// the computer or records, the wake word pauses it at once, and a turn that says "stop" ends
// it locally, without waiting for the server (docs/agent/COMPUTER_CONTROL.md, piece 23).
type Computer interface {
	Status() (computer.Kind, string)
	Set(k computer.Kind, text string)
	Pause()
	Stop() bool
}

// held is the computer task the wake word paused for a turn.
type held struct {
	kind    computer.Kind // Controlling or Recording, to resume
	text    string
	stopped bool // the turn said stop
}

// holdComputer pauses a task in progress the moment the wake word fires, before the chime and
// before anything goes to the server. Nothing in progress (or already paused): nil.
func (p *Pipeline) holdComputer(log *slog.Logger) *held {
	if p.Computer == nil {
		return nil
	}
	k, text := p.Computer.Status()
	if k != computer.Controlling && k != computer.Recording {
		return nil
	}
	p.Computer.Pause()
	log.Info("computer: paused for the wake word", "was", k, "task", text)
	return &held{kind: k, text: text}
}

// stopIfAsked ends the held task when the woken command is a stop phrase. The turn still goes
// to the server, so cc records it.
func (p *Pipeline) stopIfAsked(s *session, transcript string) {
	if s.held == nil || !IsStopPhrase(transcript) {
		return
	}
	s.held.stopped = true
	p.Computer.Stop()
	s.log.Info("computer: stopped by voice", "task", s.held.text, "transcript", transcript)
}

// releaseComputer resumes the held task after a turn that did not stop it, unless something
// else changed it meanwhile (the tray's stop, the server, a new task).
func (p *Pipeline) releaseComputer(s *session) {
	h := s.held
	if h == nil || h.stopped {
		return
	}
	if k, text := p.Computer.Status(); k != computer.Paused || text != h.text {
		return
	}
	p.Computer.Set(h.kind, h.text)
	s.log.Info("computer: resumed after the turn", "kind", h.kind, "task", h.text)
}

// stopPhrases are whole commands that end a computer task. The whole utterance must be one
// (after the fillers): "don't stop", "stop the music" or "wait" are not.
var stopPhrases = []string{
	"stop", "stop it", "stop that", "stop stop", "stop everything", "stop the task",
	"stop what you're doing", "cancel", "cancel it", "cancel that", "cancel the task",
	"that's enough", "thats enough", "enough", "abort", "quit", "never mind", "nevermind",
}

// stopFillers are dropped from either end of the command before matching.
var stopFillers = []string{"hey", "ok", "okay", "jarvis", "please", "now", "just"}

// IsStopPhrase reports whether a voice command means "stop what you're doing on the computer"
// ("Stop.", "Jarvis, stop it", "That's enough", "Never mind"), with dictate.ClassifyAnswer's
// normalisation (lowercase, letters, digits and apostrophes).
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
