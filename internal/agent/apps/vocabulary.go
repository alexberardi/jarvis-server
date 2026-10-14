package apps

import (
	"sort"
	"strings"
)

// Vocabulary is the app names speech-to-text should expect on this computer, for whisper's
// initial prompt (DESIGN.md §14 "Biasing speech-to-text"): first the given names (the open
// windows' apps, most recent first, then the last launch), then the catalogue's one-word
// names (the ones a transcriber respells: "Omawrite", "Neovim"), then its two-word names;
// longer, descriptive names ("Avahi Zeroconf Browser") are plain words it already knows.
// Each name once, ", "-separated, cut before maxChars.
func Vocabulary(first []string, list []App, maxChars int) string {
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.Join(strings.Fields(n), " ")
		k := norm(n)
		if k == "" || seen[k] || len(n) > 40 {
			return
		}
		seen[k] = true
		names = append(names, n)
	}
	for _, n := range first {
		add(n)
	}
	byWords := func(n int) []string {
		var out []string
		for _, a := range list {
			if len(words(a.Name)) == n {
				out = append(out, a.Name)
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
		return out
	}
	for _, n := range byWords(1) {
		add(n)
	}
	for _, n := range byWords(2) {
		add(n)
	}
	var b strings.Builder
	for _, n := range names {
		if len(n) < 2 { // "X": a letter biases nothing
			continue
		}
		sep := ""
		if b.Len() > 0 {
			sep = ", "
		}
		if b.Len()+len(sep)+len(n) > maxChars {
			break
		}
		b.WriteString(sep + n)
	}
	return b.String()
}
