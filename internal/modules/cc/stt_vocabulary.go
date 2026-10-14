package cc

import (
	"strings"
	"sync"
	"unicode"
)

// STT vocabulary (docs/agent/DESIGN.md §14 "Biasing speech-to-text"): a desktop node sends the
// names of its apps (open windows first, then its catalogue) as node_context.stt_vocabulary
// at /conversation/start, and its voice commands are transcribed with them in whisper's
// initial prompt, so "omawrite" is heard as Omawrite rather than "Omerite". It goes to whisper
// only, never into the LLM prompt. The node's latest list is kept in memory (a restart forgets
// it until the node's next conversation start), keyed by node: a node's first transcription
// can arrive while its start is still running.

// maxSTTVocabulary bounds a node's list (whisper keeps only the last ~224 tokens of a prompt).
const maxSTTVocabulary = 400

// sttVocab is each node's latest vocabulary.
type sttVocab struct {
	mu sync.Mutex
	m  map[string]string
}

func (v *sttVocab) set(node, words string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m == nil {
		v.m = map[string]string{}
	}
	if words == "" {
		delete(v.m, node)
		return
	}
	v.m[node] = words
}

func (v *sttVocab) get(node string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.m[node]
}

// cleanVocabulary keeps a node's list printable and bounded: control characters become
// spaces, runs of spaces one, and it is cut at a comma before maxSTTVocabulary bytes.
func cleanVocabulary(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxSTTVocabulary {
		return s
	}
	cut := s[:maxSTTVocabulary]
	if i := strings.LastIndex(cut, ","); i > 0 {
		return cut[:i]
	}
	return strings.ToValidUTF8(cut, "")
}
