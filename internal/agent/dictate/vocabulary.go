package dictate

import (
	"context"
	"sort"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

// MaxVocabulary bounds the app names sent for speech-to-text (whisper keeps the last ~224
// tokens of its prompt, and the household's own sentence follows them).
const MaxVocabulary = 400

// Vocabulary is this computer's app names for speech-to-text (DESIGN.md §14 "Biasing
// speech-to-text"): the open windows' apps, most recently focused first, the app launched
// last, then the catalogue (apps.Vocabulary). Never sent to the LLM.
func (t *Tools) Vocabulary(ctx context.Context) string {
	var first []string
	if t.Win != nil {
		if ok, _ := t.Win.Available(ctx); ok {
			if ws, err := t.Win.List(ctx); err == nil {
				sort.SliceStable(ws, func(i, j int) bool { return ws[i].Recency < ws[j].Recency })
				for _, w := range ws {
					first = append(first, winmatch.DisplayName(winmatch.Candidate{Apps: w.Apps}))
				}
			}
		}
	}
	if l, ok := t.Launched.Recent(LaunchRecent); ok {
		first = append(first, l.Name)
	}
	var list []apps.App
	if t.Apps != nil {
		list, _ = t.Apps()
	}
	return apps.Vocabulary(first, list, MaxVocabulary)
}
