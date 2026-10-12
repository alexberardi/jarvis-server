package voice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func constChunk(v int16) []int16 {
	c := make([]int16, ChunkSamples)
	for i := range c {
		c[i] = v
	}
	return c
}

func TestThresholdMatchesPython(t *testing.T) {
	e := DefaultEndpointing()
	cases := []struct {
		name string
		rms  []float64
		want float64
	}{
		{"no window: static", nil, 300},
		{"silent window: static", []float64{0, 0, 0}, 300},
		{"floor", []float64{10, 20, 30}, 200},
		{"ceiling", []float64{900, 950, 990}, 1000},
		// rms_sorted[n // 2] is the upper median for even n, rounded to 0.1, × 2, truncated.
		{"upper median, even n", []float64{100, 400, 160.04, 130}, 320},
		{"rounded then truncated", []float64{150.06, 150.06, 150.06}, 300},
		{"odd n", []float64{300, 100, 250.25}, 500},
	}
	for _, c := range cases {
		if got := e.Threshold(c.rms); got != c.want {
			t.Errorf("%s: Threshold(%v) = %v, want %v", c.name, c.rms, got, c.want)
		}
	}
	e.AutoMultiplier = 0
	if got := e.Threshold([]float64{400, 400}); got != 300 {
		t.Errorf("adaptive off: %v, want the static 300", got)
	}
}

func TestRecorderStopsLikeListen(t *testing.T) {
	e := DefaultEndpointing() // 0.8 s silence = 10 chunks, min 2 s = 25, max 7 s = 87
	loud, quiet := constChunk(2000), constChunk(10)

	// Speech then silence: stops at the first chunk where 10 silent chunks have gone by and
	// frame_count >= 25.
	r := e.newRecorder(300)
	for range 5 {
		r.add(loud)
	}
	for !r.add(quiet) {
	}
	if r.n != 26 || r.hitMax {
		t.Fatalf("speech then silence: %d chunks (hitMax %v), want 26 (frame_count 25 is the first allowed stop)", r.n, r.hitMax)
	}

	// Silence from the start still records the 2 s minimum.
	r = e.newRecorder(300)
	for !r.add(quiet) {
	}
	if r.n != 26 {
		t.Fatalf("all silence: %d chunks, want 26", r.n)
	}

	// Never silent: the 7 s cap (int(7 / 0.08) = 87 chunks).
	r = e.newRecorder(300)
	for !r.add(loud) {
	}
	if r.n != 87 || !r.hitMax || len(r.pcm) != 87*ChunkSamples {
		t.Fatalf("all speech: %d chunks hitMax=%v, want 87 and true", r.n, r.hitMax)
	}

	// A pause shorter than 0.8 s doesn't end it.
	r = e.newRecorder(300)
	seq := slices.Concat(rep(loud, 26), rep(quiet, 9), rep(loud, 1), rep(quiet, 10))
	stopped := -1
	for i, c := range seq {
		if r.add(c) {
			stopped = i
			break
		}
	}
	if stopped != len(seq)-1 {
		t.Fatalf("stopped at chunk %d, want %d (after the 10-chunk silence)", stopped, len(seq)-1)
	}
}

// TestRecorderOnsetWait: with an onset wait, silence before the first speech doesn't end
// the recording until the wait is over; once speech is heard the usual stop applies.
func TestRecorderOnsetWait(t *testing.T) {
	e := DefaultEndpointing()
	e.OnsetWaitSecs = 4 // int(4 / 0.08) = 50 chunks
	loud, quiet := constChunk(2000), constChunk(10)

	// A 3 s pause after the wake word, then speech, then silence: the pause doesn't stop it
	// (without the wait it would stop at 26 chunks); the silence after speech does.
	r := e.newRecorder(300)
	seq := slices.Concat(rep(quiet, 37), rep(loud, 10), rep(quiet, 10))
	stopped := -1
	for i, c := range seq {
		if r.add(c) {
			stopped = i
			break
		}
	}
	if stopped != len(seq)-1 || r.hitMax {
		t.Fatalf("pause then speech: stopped at %d (hitMax %v), want %d", stopped, r.hitMax, len(seq)-1)
	}

	// Nobody speaks: stops once the wait is over (frame_count 50), not at the 2 s minimum.
	r = e.newRecorder(300)
	for !r.add(quiet) {
	}
	if r.n != 51 || r.hitMax {
		t.Fatalf("all silence: %d chunks (hitMax %v), want 51", r.n, r.hitMax)
	}

	// A wait longer than the cap: the cap still applies.
	e.OnsetWaitSecs = 30
	r = e.newRecorder(300)
	for !r.add(quiet) {
	}
	if r.n != 87 || !r.hitMax {
		t.Fatalf("wait past the cap: %d chunks (hitMax %v), want 87 and true", r.n, r.hitMax)
	}

	// Speech right away: the wait doesn't delay the normal stop.
	e.OnsetWaitSecs = 4
	r = e.newRecorder(300)
	for range 5 {
		r.add(loud)
	}
	for !r.add(quiet) {
	}
	if r.n != 26 {
		t.Fatalf("speech first: %d chunks, want 26", r.n)
	}
}

func rep(c []int16, n int) [][]int16 {
	out := make([][]int16, n)
	for i := range out {
		out[i] = c
	}
	return out
}

// Golden values from jarvis-node-setup scripts/speech_to_text.py
// _normalize_frames_to_target_rms(target_rms_db=-18, max_gain_db=18) on the same signals.
func TestNormalizeMatchesPython(t *testing.T) {
	cases := []struct {
		k, dc       int
		head        []int16
		sum, sumAbs int64
	}{
		{k: 1, dc: 50, head: []int16{-794, -500, -206, 87, 381, 675}, sum: -45, sumAbs: 1913727},          // +18 dB (capped)
		{k: 30, dc: -200, head: []int16{-7106, -4476, -1845, 784, 3414, 6045}, sum: -2, sumAbs: 17145906}, // +7.49 dB
		{k: 300, dc: 10, head: []int16{-29989, -18889, -7789, 3310, 14410, 25510}, sum: -10, sumAbs: 72364410},
		{k: 0, dc: 0, head: []int16{0, 0, 0, 0, 0, 0}, sum: 0, sumAbs: 0},
		{k: 5, dc: 0, head: []int16{-3970, -2500, -1031, 438, 1907, 3377}, sum: 14, sumAbs: 9578132},
	}
	for _, c := range cases {
		x := make([]int16, 4800)
		for i := range x {
			x[i] = int16(c.dc + ((i*37)%201-100)*c.k)
		}
		y := Normalize(x)
		var sum, sumAbs int64
		for _, v := range y {
			sum += int64(v)
			sumAbs += max(int64(v), -int64(v))
		}
		if !slices.Equal(y[:6], c.head) || sum != c.sum || sumAbs != c.sumAbs {
			t.Errorf("k=%d dc=%d: head %v sum %d sumAbs %d, want %v %d %d", c.k, c.dc, y[:6], sum, sumAbs, c.head, c.sum, c.sumAbs)
		}
	}
}

// Golden values from jarvis-node-setup core/voice_filters.py is_non_speech.
func TestIsNonSpeechMatchesPython(t *testing.T) {
	want := map[string]bool{
		"": true, "   ": true, "[BLANK_AUDIO]": true, "*sniff*": true, "*sniff*.": true,
		"(wind blowing)": true, "<inaudible>": true, "*sad noises* *cough*": true, "...": true,
		". . .": false, "okay": false, "bye.": false, "What time is it?": false,
		"[Music] play the song": false, "(laughs) hello": false, "hey *sniff*": false,
		"[BLANK_AUDIO] [BLANK_AUDIO]": true,
	}
	for in, w := range want {
		if got := IsNonSpeech(in); got != w {
			t.Errorf("IsNonSpeech(%q) = %v, want %v", in, got, w)
		}
	}
}

func TestEnsureModelsFetchesVerifiesAndCaches(t *testing.T) {
	body := map[string]string{"a.onnx": "model a", "b.onnx": "model b"}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	models := map[string]string{"a.onnx": sum("model a"), "b.onnx": sum("model b")}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(body[strings.TrimPrefix(r.URL.Path, "/")]))
	}))
	defer srv.Close()
	dir := t.TempDir()
	got, err := ensureModels(context.Background(), dir, srv.URL, nil, models)
	if err != nil || len(got) != 2 {
		t.Fatalf("first fetch: %v %v", got, err)
	}
	if got, err := ensureModels(context.Background(), dir, srv.URL, nil, models); err != nil || len(got) != 0 || hits != 2 {
		t.Fatalf("second call should fetch nothing: %v %v (hits %d)", got, err, hits)
	}
	// A corrupted file is replaced; a server serving the wrong bytes is refused.
	_ = os.WriteFile(filepath.Join(dir, "a.onnx"), []byte("junk"), 0o644)
	body["b.onnx"] = "tampered"
	_ = os.Remove(filepath.Join(dir, "b.onnx"))
	if _, err := ensureModels(context.Background(), dir, srv.URL, nil, models); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tampered download: err = %v, want a sha256 mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.onnx")); !os.IsNotExist(err) {
		t.Fatalf("a model failing its hash must not be left in place (stat: %v)", err)
	}
}

func TestLocalTimezoneHonoursTZ(t *testing.T) {
	t.Setenv("TZ", "Europe/Paris")
	if got := LocalTimezone(); got != "Europe/Paris" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("TZ", "")
	if got := LocalTimezone(); got == "" || got == "Local" {
		t.Fatalf("got %q, want an IANA name or UTC", got)
	}
}
