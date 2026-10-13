package screen

import "github.com/alexberardi/jarvis-server/internal/agent/winmatch"

// Naming a window is shared with dictation: internal/agent/winmatch. These names keep the
// screen code (and its tests) reading as before.

// Error codes for a named window that can't be captured.
const (
	CodeWindowNotFound   = winmatch.CodeNotFound
	CodeWindowNotVisible = winmatch.CodeNotVisible
)

// WindowError is a named window that can't be captured, with a message the model can relay.
type WindowError = winmatch.Error

type candidate = winmatch.Candidate

func pickWindow(cands []candidate, query string) (int, error) { return winmatch.Pick(cands, query) }

func normalizeQuery(q string) string { return winmatch.Normalize(q) }
