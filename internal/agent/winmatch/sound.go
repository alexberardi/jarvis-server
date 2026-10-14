package winmatch

import "strings"

// Sound-alike names: speech-to-text also splits and respells a name it doesn't know ("Oh my
// right" for omawrite), which edit distance on the letters can't reach. SoundKey reduces a
// name to roughly how it sounds (a small Metaphone-like key: silent letters dropped, voiced and
// unvoiced pairs merged, every vowel run one vowel), so names that sound the same get the same
// key. An equal key counts as a match only for long enough names (SoundsLike); a key one edit
// away is only ever a suggestion (SoundsNear), never acted on: "the oven" is one edit from
// neovim, and an oven is a real thing to ask for.

// soundMinKey and soundMinLen: shorter keys and names collide by accident.
const (
	soundMinKey = 4
	soundMinLen = 5
)

var soundRewrites = strings.NewReplacer(
	"ght", "t", "gh", "", "wr", "r", "kn", "n", "wh", "w", "ph", "f", "th", "t", "dh", "t",
	"ck", "k", "qu", "kw", "q", "k", "x", "ks", "ce", "se", "ci", "si", "cy", "sy", "c", "k",
	"z", "s", "v", "f", "d", "t", "b", "p", "g", "k", "m", "n",
)

func isVowel(r byte) bool { return strings.IndexByte("aeiouy", r) >= 0 }

// SoundKey is the sound-alike key of a name: "omawrite", "Omerite" and "oh my right" all
// become "anarat".
func SoundKey(s string) string {
	c := compact(s)
	if c == "" {
		return ""
	}
	c = soundRewrites.Replace(c)
	var b []byte
	for i := 0; i < len(c); i++ {
		ch := c[i]
		switch {
		case ch == 'h' && i > 0: // silent after the first letter ("oh", "john")
			continue
		case ch == 'w' && (i+1 >= len(c) || !isVowel(c[i+1])): // "aw", "ow": a vowel sound
			ch = 'a'
		case isVowel(ch):
			ch = 'a'
		}
		if n := len(b); n > 0 && b[n-1] == ch {
			continue // vowel runs and doubled consonants are one sound
		}
		b = append(b, ch)
	}
	if n := len(b); n > 2 && b[n-1] == 'a' {
		b = b[:n-1] // a final vowel is mostly silent ("write", "rite")
	}
	return string(b)
}

// SoundsLike reports whether two names sound the same: equal keys, both long enough.
func SoundsLike(a, b string) bool {
	if len(compact(a)) < soundMinLen && len(compact(b)) < soundMinLen {
		return false
	}
	ka, kb := SoundKey(a), SoundKey(b)
	return len(ka) >= soundMinKey && ka == kb
}

// SoundsNear reports whether two names sound alike within one edit of their keys. It is a
// suggestion, never a match.
func SoundsNear(a, b string) bool {
	ka, kb := SoundKey(a), SoundKey(b)
	if min(len(ka), len(kb)) < soundMinKey-1 || max(len(ka), len(kb)) < soundMinKey {
		return false
	}
	return distance([]rune(ka), []rune(kb)) <= 1
}

// Similarity is how alike a heard name and an app name are, 0..1: the better of their
// spelling (edit distance over the longer length) and their sound (the same over the keys,
// scaled to 0.9 so a sound-alike never outranks the same spelling).
func Similarity(heard, name string) float64 {
	a, b := []rune(compact(heard)), []rune(compact(name))
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	spell := 1 - float64(distance(a, b))/float64(max(len(a), len(b)))
	ka, kb := []rune(SoundKey(heard)), []rune(SoundKey(name))
	sound := 0.0
	if len(ka) > 0 && len(kb) > 0 {
		sound = 0.9 * (1 - float64(distance(ka, kb))/float64(max(len(ka), len(kb))))
	}
	return max(spell, sound)
}
