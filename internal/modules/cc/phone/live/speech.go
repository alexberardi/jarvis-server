package live

import (
	"regexp"
	"strings"
	"unicode"
)

// Speech formatting for TTS input only (services/speech_format.py): identifiers (member IDs,
// phone numbers, spelled names) are dictated character by character; ordinary numbers (years,
// times, quantities, prices, short addresses) are left alone. The transcript keeps the
// readable form.

// speechEdge are characters that wrap a token but are not part of an identifier.
const speechEdge = ".,;:!?\"')(“”‘’"

var phonetic = map[rune]string{
	'A': "Apple", 'B': "Boy", 'C': "Cat", 'D': "Dog", 'E': "Echo", 'F': "Frank",
	'G': "George", 'H': "Henry", 'I': "Igloo", 'J': "July", 'K': "King", 'L': "Lion",
	'M': "Mary", 'N': "Nancy", 'O': "Ocean", 'P': "Paul", 'Q': "Queen", 'R': "Robert",
	'S': "Sam", 'T': "Tom", 'U': "Umbrella", 'V': "Victor", 'W': "William",
	'X': "X-ray", 'Y': "Yellow", 'Z': "Zebra",
}

var (
	spelledLettersRE = regexp.MustCompile(`^[A-Za-z](?:[-.][A-Za-z]){2,}$`)
	identifierRE     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.\-]*[A-Za-z0-9])?$`)
	speechSplitRE    = regexp.MustCompile(`\s+`)
)

func isASCIILetter(c rune) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isASCIIDigit(c rune) bool  { return c >= '0' && c <= '9' }

// spell voices an identifier one character at a time: letters phonetically, digits singly.
func spell(token string) string {
	var parts []string
	for _, c := range token {
		switch {
		case isASCIILetter(c):
			u := unicode.ToUpper(c)
			parts = append(parts, string(u)+" as in "+phonetic[u])
		case isASCIIDigit(c):
			parts = append(parts, string(c))
		}
	}
	return strings.Join(parts, ", ")
}

func isIdentifier(token string) bool {
	if spelledLettersRE.MatchString(token) {
		return true
	}
	if !identifierRE.MatchString(token) {
		return false
	}
	var digits int
	var alpha, sep bool
	for _, c := range token {
		switch {
		case isASCIIDigit(c):
			digits++
		case isASCIILetter(c):
			alpha = true
		case c == '.' || c == '-':
			sep = true
		}
	}
	if alpha {
		return digits >= 2
	}
	if sep {
		return digits >= 7 && digits <= 15
	}
	return digits >= 5
}

// FormatForSpeech rewrites identifiers in text for digit-by-digit / letter-by-letter TTS.
func FormatForSpeech(text string) string {
	if text == "" {
		return text
	}
	// re.split(r"(\s+)") keeps the separators; rebuild token / whitespace alternation.
	var out strings.Builder
	last := 0
	for _, loc := range append(speechSplitRE.FindAllStringIndex(text, -1), []int{len(text), len(text)}) {
		token := text[last:loc[0]]
		if token != "" {
			out.WriteString(formatToken(token))
		}
		out.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	return out.String()
}

func formatToken(token string) string {
	if strings.HasPrefix(strings.TrimLeft(token, speechEdge), "$") {
		return token
	}
	left := strings.TrimLeft(token, speechEdge)
	lead := token[:len(token)-len(left)]
	right := strings.TrimRight(token, speechEdge)
	trail := token[len(right):]
	if len(lead)+len(trail) >= len(token) {
		return token // all edge characters
	}
	core := token[len(lead) : len(token)-len(trail)]
	if isIdentifier(core) {
		return lead + spell(core) + trail
	}
	return token
}
