package voice

import "time"

// Indicator shows what the pipeline is doing on screen (the desktop agent's "island"). The
// pipeline calls it on its own goroutine, so implementations must return quickly. A nil
// Indicator (the default: tests, headless runs, the Pi) shows nothing.
type Indicator interface {
	// SetState is called on every change. window is the time left to start answering, for
	// CueFollowUp only (after an ignored answer it is what is left of the window, not a fresh
	// one, the same rule the window itself follows).
	SetState(c Cue, window time.Duration)
	// Level is the loudness of what is being heard (CueListening) or said (CueSpeaking):
	// RMS amplitude, 0 (silence) to 1 (full scale).
	Level(v float32)
}

// Cue is a state the Indicator shows.
type Cue int

const (
	CueIdle      Cue = iota // nothing going on: hidden
	CueListening            // recording what the user says (after the wake word, an answer, a validation reply)
	CueThinking             // transcribing and waiting for command-center
	CueSpeaking             // playing a reply
	CueFollowUp             // the follow-up window is open, nobody is speaking yet
	CueError                // the turn failed (a brief flash, then hidden)
)

func (c Cue) String() string {
	switch c {
	case CueListening:
		return "listening"
	case CueThinking:
		return "thinking"
	case CueSpeaking:
		return "speaking"
	case CueFollowUp:
		return "follow_up"
	case CueError:
		return "error"
	}
	return "idle"
}

// cue tells the Indicator, if any.
func (p *Pipeline) cue(c Cue, window time.Duration) {
	if p.Indicator != nil {
		p.Indicator.SetState(c, window)
	}
}

// level reports the loudness of a chunk heard or played.
func (p *Pipeline) level(pcm []int16) {
	if p.Indicator != nil {
		p.Indicator.Level(float32(RMS(pcm) / 32768))
	}
}
