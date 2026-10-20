package voice

import (
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
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

// ComputerActivity is what a turn tells the server Jarvis is doing with this computer
// (computer_activity, #288): command-center steers a stop phrase to stop_recording, never drops
// it as not for it, and states in the prompt that a recording is running.
type ComputerActivity struct {
	Kind string `json:"kind"`           // "recording", "controlling" or "paused"
	Task string `json:"task,omitempty"` // its text ("Watching what you do", "Ordering the blue mug")
	// Stopped: this command was a stop phrase and the agent already stopped the task here.
	Stopped bool `json:"stopped,omitempty"`
}

// computerTurn is a command's computer_activity (nil: the computer is idle). A stop phrase ends
// the task locally, at once, whether the wake word held it or it started during this
// conversation (a recording "record a macro" started); the turn still goes to the server, so
// cc records it and stop_recording saves the recording.
func (p *Pipeline) computerTurn(s *session, transcript string) *ComputerActivity {
	if p.Computer == nil {
		return nil
	}
	k, text := p.Computer.Status()
	if h := s.held; h != nil && !h.stopped && k == computer.Paused && text == h.text {
		k = h.kind // held for this turn: report what it was
	}
	if k != computer.Controlling && k != computer.Recording && k != computer.Paused {
		return nil
	}
	a := &ComputerActivity{Kind: string(k), Task: text}
	if computeruse.IsStopPhrase(transcript) {
		if s.held != nil {
			s.held.stopped = true
		}
		p.Computer.Stop()
		a.Stopped = true
		s.log.Info("computer: stopped by voice", "kind", k, "task", text, "transcript", transcript)
	}
	return a
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
