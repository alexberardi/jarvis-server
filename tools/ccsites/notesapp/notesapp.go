package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Note is one note in the notes fixture's state file (notesapp/notes.py --state).
type Note struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// NotesState reads the notes fixture's state file, {"notes": [{"title", "body"}]}.
// The app writes it on every save, so a missing file (an error matching
// fs.ErrNotExist) means nothing was saved yet.
func NotesState(path string) ([]Note, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state struct {
		Notes *[]Note `json:"notes"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("notes state %s: %w", path, err)
	}
	if state.Notes == nil {
		return nil, fmt.Errorf("notes state %s: no \"notes\" list", path)
	}
	return *state.Notes, nil
}
