package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// freePBXEndpoint and freePBXAOR are the FreePBX 17 extension defaults the lane copies and
// jarvisd has to cope with (docs/cc/phone-sip.md §3), as written in pjsip.conf.
var freePBXEndpoint = map[string]string{
	"allow":             "ulaw,alaw,gsm,g726,g722",
	"context":           "from-internal",
	"dtmf_mode":         "rfc4733",
	"direct_media":      "yes",
	"timers":            "yes",
	"rtp_timeout":       "30",
	"send_pai":          "yes",
	"send_rpid":         "no",
	"rtp_symmetric":     "yes",
	"force_rport":       "yes",
	"rewrite_contact":   "yes",
	"max_audio_streams": "1",
	// What the L2 lane's first run (FreePBX 17.0.33.2, 2026-10-22) found L1 had wrong.
	"trust_id_outbound":                  "no",
	"aggregate_mwi":                      "no",
	"mwi_subscribe_replaces_unsolicited": "yes",
	"one_touch_recording":                "yes",
	"record_on_feature":                  "apprecord",
	"record_off_feature":                 "apprecord",
	"tos_video":                          "af41",
	"cos_video":                          "4",
}

var freePBXAOR = map[string]string{
	"max_contacts":      "1",
	"remove_existing":   "yes",
	"qualify_frequency": "60",
}

// contactTakenExt is the lane's extension set to Remove Existing = no, as an older FreePBX
// install or an admin has it: a second contact is refused while the first is bound.
const contactTakenExt = "102"

// parsePJSIPConf reads pjsip.conf into "<section>/<type>" → settings. FreePBX reuses a name
// for an extension's endpoint and aor, so the type tells them apart. Comments and #include
// lines are skipped.
func parsePJSIPConf(path string) (map[string]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]map[string]string{}
	var name string
	var cur map[string]string
	flush := func() {
		if cur != nil {
			out[name+"/"+cur["type"]] = cur
		}
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, ";"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "["):
			flush()
			name = strings.TrimSuffix(strings.TrimPrefix(strings.Fields(line)[0], "["), "]")
			cur = map[string]string{}
		case cur != nil:
			if k, v, ok := strings.Cut(line, "="); ok {
				cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	flush()
	return out, sc.Err()
}

var shownLine = regexp.MustCompile(`^\s+([a-z0-9_]+)\s+:\s*(.*?)\s*$`)

// shownSettings reads `pjsip show endpoint|aor X` into setting → value.
func shownSettings(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if s := shownLine.FindStringSubmatch(line); s != nil {
			m[s[1]] = s[2]
		}
	}
	return m
}

// shownDefaults is freePBXEndpoint/AOR as the running Asterisk prints them.
var shownDefaults = map[string]string{
	"direct_media":      "true",
	"timers":            "yes",
	"rtp_timeout":       "30",
	"send_pai":          "true",
	"max_contacts":      "1",
	"remove_existing":   "true",
	"qualify_frequency": "60",
}

// missingDefaults lists the defaults the running PBX does not have, "key=got (want)".
func missingDefaults(shown map[string]string) []string {
	var bad []string
	for k, want := range shownDefaults {
		if shown[k] != want {
			bad = append(bad, fmt.Sprintf("%s=%q (want %q)", k, shown[k], want))
		}
	}
	sort.Strings(bad)
	return bad
}

// words splits text for WER as tools/pipeline does: lower case, punctuation dropped.
func words(s string) []string {
	ws := strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(s, "’", "'")), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	for i := range ws {
		ws[i] = strings.Trim(ws[i], "'")
	}
	return ws
}

// wordErrorRateWithin is the word error rate of said against the best-matching stretch of
// heard: the edit distance may start and end anywhere in heard for free. A far end hears the
// PBX's own prompts around what it was sent (the echo test's instructions), and those words
// are not errors; words missing from said's stretch or wrong in it are.
func wordErrorRateWithin(said, heard string) float64 {
	a, b := words(said), words(heard)
	if len(a) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1) // row 0: free start anywhere in heard
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			sub := prev[j-1]
			if a[i-1] != b[j-1] {
				sub++
			}
			cur[j] = min(sub, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	best := math.MaxInt
	for _, d := range prev { // free end anywhere in heard
		best = min(best, d)
	}
	return float64(best) / float64(len(a))
}

// bestWER is the lowest wordErrorRateWithin over the ways of saying it (whisper writes "101"
// for "one zero one") and which one that was.
func bestWER(alternatives []string, heard string) (float64, int) {
	best, at := math.Inf(1), -1
	for i, s := range alternatives {
		if w := wordErrorRateWithin(s, heard); w < best {
			best, at = w, i
		}
	}
	return best, at
}
