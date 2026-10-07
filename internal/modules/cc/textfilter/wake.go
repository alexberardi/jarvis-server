package textfilter

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// Wake-clip verification, the pure parts of core/wake_verification.py. The verdict is computed
// in process by the caller (D40 01.Q6): transcribe the leading WakeSliceSeconds of the node's
// speaker_audio with the speaker pass off, then WakePhrasePresent / WakePhraseSimilarity.
const (
	// WakeSliceSeconds is how much of the leading speaker_audio is transcribed.
	WakeSliceSeconds = 2.2
	// VerdictWaitSeconds is the legacy bounded wait for a pending verdict on a wake turn.
	VerdictWaitSeconds = 1.2
	// ClipSimilarityFloor: a verify transcript scoring below this has no trace of the phrase.
	ClipSimilarityFloor = 0.5
	// DefaultWakePhrase is voice.wake_verification_phrase's default.
	DefaultWakePhrase = "jarvis"
	// FollowupDoubtMaxRoundsDefault is voice.followup_doubt_max_rounds' default (and the value
	// a non-positive or garbage setting falls back to).
	FollowupDoubtMaxRoundsDefault = 2
)

// WakeVerdict is the stored verdict dict ({verified, verdict, transcript, phrase, similarity,
// node_id, elapsed_ms}). Verdict is "verified" or "unverified" ("clip_unreliable" only on a
// verdict re-labelled by an older process).
type WakeVerdict struct {
	Verified   bool    `json:"verified"`
	Verdict    string  `json:"verdict"`
	Transcript string  `json:"transcript"`
	Phrase     string  `json:"phrase"`
	Similarity float64 `json:"similarity"` // round(similarity, 3)
	NodeID     string  `json:"node_id"`
	ElapsedMS  int64   `json:"elapsed_ms"`
}

// NormalizeWakeVerificationMode is _get_mode's value check: "bias" or "enforce" (after
// strip+lower), anything else is "off" (fail safe).
func NormalizeWakeVerificationMode(raw string) string {
	m := parse.PyLower(parse.PyStrip(raw))
	if m == "bias" || m == "enforce" {
		return m
	}
	return "off"
}

// NormalizeWakePhrase is _get_phrase's value handling: strip+lower, "" → "jarvis".
func NormalizeWakePhrase(raw string) string {
	if p := parse.PyLower(parse.PyStrip(raw)); p != "" {
		return p
	}
	return DefaultWakePhrase
}

var wakeTokenRE = regexp.MustCompile(`[a-z']+`)

// editDistance is _edit_distance: Damerau-Levenshtein (adjacent transposition = one edit),
// 3 for any length gap over 2.
func editDistance(a, b []rune) int {
	d := len(a) - len(b)
	if d > 2 || d < -2 {
		return 3
	}
	rows := [][]int{make([]int, len(b)+1)}
	for j := range rows[0] {
		rows[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := []int{i}
		for j := 1; j <= len(b); j++ {
			sub := 0
			if a[i-1] != b[j-1] {
				sub = 1
			}
			cost := min(rows[i-1][j]+1, cur[j-1]+1, rows[i-1][j-1]+sub)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cost = min(cost, rows[i-2][j-2]+1)
			}
			cur = append(cur, cost)
		}
		rows = append(rows, cur)
	}
	return rows[len(a)][len(b)]
}

// WakePhrasePresent is wake_phrase_present: the phrase is in the lowercased transcript, or a
// [a-z']+ token is within Damerau-Levenshtein 2 of it. An empty phrase always verifies.
func WakePhrasePresent(transcript, phrase string) bool {
	if transcript == "" {
		return false
	}
	target := parse.PyLower(parse.PyStrip(phrase))
	if target == "" {
		return true
	}
	text := parse.PyLower(transcript)
	if strings.Contains(text, target) {
		return true
	}
	tr := []rune(target)
	for _, tok := range wakeTokenRE.FindAllString(text, -1) {
		if editDistance([]rune(tok), tr) <= 2 {
			return true
		}
	}
	return false
}

// WakePhraseSimilarity is wake_phrase_similarity: the best difflib SequenceMatcher ratio
// between any [a-z']+ token and the phrase (1.0 when the phrase is contained or empty).
func WakePhraseSimilarity(transcript, phrase string) float64 {
	if transcript == "" {
		return 0
	}
	target := parse.PyLower(parse.PyStrip(phrase))
	if target == "" {
		return 1
	}
	text := parse.PyLower(transcript)
	if strings.Contains(text, target) {
		return 1
	}
	best := 0.0
	tr := []rune(target)
	for _, tok := range wakeTokenRE.FindAllString(text, -1) {
		best = max(best, sequenceRatio([]rune(tok), tr))
	}
	return best
}

// sequenceRatio is difflib.SequenceMatcher(None, a, b).ratio() (autojunk on, no junk).
func sequenceRatio(a, b []rune) float64 {
	b2j := map[rune][]int{}
	for j, r := range b {
		b2j[r] = append(b2j[r], j)
	}
	if n := len(b); n >= 200 {
		ntest := n/100 + 1
		for r, idx := range b2j {
			if len(idx) > ntest {
				delete(b2j, r)
			}
		}
	}
	var matches func(alo, ahi, blo, bhi int) int
	matches = func(alo, ahi, blo, bhi int) int {
		besti, bestj, bestsize := alo, blo, 0
		j2len := map[int]int{}
		for i := alo; i < ahi; i++ {
			newj2len := map[int]int{}
			for _, j := range b2j[a[i]] {
				if j < blo {
					continue
				}
				if j >= bhi {
					break
				}
				k := j2len[j-1] + 1
				newj2len[j] = k
				if k > bestsize {
					besti, bestj, bestsize = i-k+1, j-k+1, k
				}
			}
			j2len = newj2len
		}
		for besti > alo && bestj > blo && a[besti-1] == b[bestj-1] {
			besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
		}
		for besti+bestsize < ahi && bestj+bestsize < bhi && a[besti+bestsize] == b[bestj+bestsize] {
			bestsize++
		}
		if bestsize == 0 {
			return 0
		}
		n := bestsize
		if alo < besti && blo < bestj {
			n += matches(alo, besti, blo, bestj)
		}
		if besti+bestsize < ahi && bestj+bestsize < bhi {
			n += matches(besti+bestsize, ahi, bestj+bestsize, bhi)
		}
		return n
	}
	total := len(a) + len(b)
	if total == 0 {
		return 1
	}
	return 2 * float64(matches(0, len(a), 0, len(b))) / float64(total)
}

// SliceLeadingWAV is slice_leading_wav: a PCM WAV holding only the first seconds of wav
// (Python's wave module: 44-byte canonical header). ok=false on anything unparsable.
func SliceLeadingWAV(wav []byte, seconds float64) (out []byte, ok bool) {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, false
	}
	var channels, bits uint16
	var rate uint32
	var data []byte
	haveFmt := false
	for p := 12; p+8 <= len(wav); {
		id := string(wav[p : p+4])
		size := int(binary.LittleEndian.Uint32(wav[p+4 : p+8]))
		body := wav[p+8:]
		if size > len(body) {
			size = len(body)
		}
		switch id {
		case "fmt ":
			if size < 16 || binary.LittleEndian.Uint16(body[0:2]) != 1 {
				return nil, false
			}
			channels = binary.LittleEndian.Uint16(body[2:4])
			rate = binary.LittleEndian.Uint32(body[4:8])
			bits = binary.LittleEndian.Uint16(body[14:16])
			haveFmt = true
		case "data":
			data = body[:size]
		}
		if data != nil {
			break
		}
		p += 8 + size + size%2
	}
	if !haveFmt || data == nil || channels == 0 || bits == 0 {
		return nil, false
	}
	width := int(bits+7) / 8
	frame := width * int(channels)
	nframes := min(len(data)/frame, int(float64(rate)*seconds))
	frames := data[:nframes*frame]

	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(frames)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, channels)
	_ = binary.Write(&b, binary.LittleEndian, rate)
	_ = binary.Write(&b, binary.LittleEndian, uint32(int(rate)*frame))
	_ = binary.Write(&b, binary.LittleEndian, uint16(frame))
	_ = binary.Write(&b, binary.LittleEndian, uint16(width*8))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(frames)))
	b.Write(frames)
	return b.Bytes(), true
}
