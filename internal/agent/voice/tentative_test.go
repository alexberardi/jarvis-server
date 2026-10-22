package voice

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// N:core/wake_loop.py's pre_wake_speech_seconds: frames of the 5 s pre-wake window above the
// speech threshold, × 80 ms. The threshold is clamp(median × 3, 300, 2500) off the window's
// own floor, or the static 2500 during self-playback (the floor is the music) or without one.
func TestPreWakeSpeechMatchesPython(t *testing.T) {
	window := func(floor float64, loud float64, n int) []float64 {
		w := slices.Repeat([]float64{floor}, preWakeChunks-n)
		return append(w, slices.Repeat([]float64{loud}, n)...)
	}
	v := DefaultPreWakeVAD()
	cases := []struct {
		name   string
		rms    []float64
		self   bool
		secs   float64
		thr    float64
		source string
	}{
		{"quiet room: the 300 floor", window(50, 1200, 10), false, 0.8, 300, "auto"},
		{"floor × 3", window(400, 1300, 7), false, 0.56, 1200, "auto"},
		{"frames at the threshold don't count", window(400, 1200, 7), false, 0, 1200, "auto"},
		{"loud floor: the 2500 ceiling", window(1000, 2600, 3), false, 0.24, 2500, "auto"},
		{"self-playback: static", window(1000, 2400, 20), true, 0, 2500, "static_self_playback"},
		{"self-playback, speech over it", window(1000, 3000, 20), true, 1.6, 2500, "static_self_playback"},
		{"no window", nil, false, 0, 2500, "static"},
		{"silent floor", window(0, 2600, 1), false, 0.08, 2500, "static"},
	}
	for _, c := range cases {
		secs, thr, src := v.SpeechSecs(c.rms, c.self)
		if secs != c.secs || thr != c.thr || src != c.source {
			t.Errorf("%s: %v s, threshold %v (%s); want %v s, %v (%s)", c.name, secs, thr, src, c.secs, c.thr, c.source)
		}
	}
}

func TestStreamRequestCarriesPreWakeSpeechAndSelfPlayback(t *testing.T) {
	secs, on := 0.56, true
	b, err := json.Marshal(StreamRequest{VoiceCommand: "x", ConversationID: "c", PreWakeSpeechSecs: &secs,
		SelfPlayback: &on, SelfPlaybackKind: "music"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["pre_wake_speech_seconds"] != 0.56 || got["self_playback"] != true || got["self_playback_kind"] != "music" {
		t.Fatalf("request %s", b)
	}
	b, _ = json.Marshal(StreamRequest{VoiceCommand: "x", ConversationID: "c"})
	got = nil
	_ = json.Unmarshal(b, &got)
	for _, k := range []string{"pre_wake_speech_seconds", "self_playback", "self_playback_kind"} {
		if _, has := got[k]; has {
			t.Fatalf("%s sent unset: %s", k, b)
		}
	}
}

// scoredDetector returns scripted scores, one per scored chunk (0.01 past the script), and is
// the pipeline's clock: each chunk it scores is 80 ms later.
type scoredDetector struct {
	mu     sync.Mutex
	scores map[int]float32
	n      int
	now    time.Time
}

func (d *scoredDetector) Process(c []int16) (float32, error) {
	if len(c) != wakeChunk {
		return 0, errors.New("bad chunk size")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.scores[d.n]
	d.n++
	d.now = d.now.Add(80 * time.Millisecond)
	if !ok {
		s = 0.01
	}
	return s, nil
}
func (d *scoredDetector) Reset() {}
func (d *scoredDetector) Now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.now
}

// tentativeRig is a node playing music (or not) whose detector scores the given chunks.
func tentativeRig(t *testing.T, mic *fakeCapture, selfPlaying bool, scores map[int]float32) (*rig, *fakeMusic) {
	r := newRig(t, mic, newServer())
	det := &scoredDetector{scores: scores, now: time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)}
	m := &fakeMusic{done: make(chan struct{}), self: selfPlaying}
	r.p.Detector, r.p.Now, r.p.Music = det, det.Now, m
	r.p.Cfg.FollowUp.ListenSecs = 0
	return r, m
}

// N:core/wake_loop.py:403-466: during self-playback a score in the tentative band ducks the
// music without a turn; a window that lapses with no fire puts it back.
func TestTentativeWakeDucksThenExpires(t *testing.T) {
	mic := script(rep(noise, 60))
	r, m := tentativeRig(t, mic, true, map[int]float32{10: 0.3})
	stop := r.run(t)
	r.noTurnBy(t, mic, 50) // the window (20 chunks) has lapsed by chunk 30
	_ = stop()
	if got, want := m.list(), []string{"duck", "restore"}; !slices.Equal(got, want) {
		t.Fatalf("music steps %v, want %v", got, want)
	}
}

// The fire completes the tentative: one turn, the music ducked once more (Ducker.Duck keeps the
// first saved volume) and restored after it; the clip reaches back past the tentative duck.
func TestTentativeWakeCompletedByAFire(t *testing.T) {
	mic := script(rep(noise, 36), rep(speech, 12))
	r, m := tentativeRig(t, mic, true, map[int]float32{30: 0.3, 35: 0.95})
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if !tr.Tentative || tr.WakeScore != 0.95 {
		t.Fatalf("turn %+v: want a fire that completed a tentative wake", tr)
	}
	if got, want := m.list(), []string{"duck", "duck", "restore"}; !slices.Equal(got, want) {
		t.Fatalf("music steps %v, want %v", got, want)
	}
	in := r.srv.streamIn
	if in.SelfPlayback == nil || !*in.SelfPlayback || in.SelfPlaybackKind != "music" || in.PreWakeSpeechSecs == nil {
		t.Fatalf("stream request %+v: want self_playback music and pre_wake_speech_seconds", in)
	}
	cmd, _ := decodeWAV(t, r.srv.cmdWAV)
	spk, _ := decodeWAV(t, r.srv.spkWAV)
	if clip := len(spk) - len(cmd); clip != 36*ChunkSamples {
		t.Fatalf("wake clip %d chunks, want all 36 heard (2 s + the window)", clip/ChunkSamples)
	}
}

// A fire with no tentative open, on a node playing music: the normal 2 s clip.
func TestFireDuringMusicWithoutTentativeKeepsTheNormalClip(t *testing.T) {
	mic := script(rep(noise, 40), rep(speech, 12))
	r, _ := tentativeRig(t, mic, true, map[int]float32{39: 0.95})
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	cmd, _ := decodeWAV(t, r.srv.cmdWAV)
	spk, _ := decodeWAV(t, r.srv.spkWAV)
	if tr.Tentative || len(spk)-len(cmd) != wakeClipChunks*ChunkSamples {
		t.Fatalf("tentative %v, clip %d chunks; want none and %d", tr.Tentative, (len(spk)-len(cmd))/ChunkSamples, wakeClipChunks)
	}
}

// One tentative per 10 s: a lyric dip must not strobe the music.
func TestTentativeWakeCoolDown(t *testing.T) {
	mic := script(rep(noise, 200))
	// 10 opens; 50 is 3.2 s later (cooling down); 140 is 10.4 s later.
	r, m := tentativeRig(t, mic, true, map[int]float32{10: 0.3, 50: 0.35, 140: 0.25})
	stop := r.run(t)
	r.noTurnBy(t, mic, 190)
	_ = stop()
	if got, want := m.list(), []string{"duck", "restore", "duck", "restore"}; !slices.Equal(got, want) {
		t.Fatalf("music steps %v, want %v", got, want)
	}
}

// Without the node's own music there is nothing to duck: a band score does nothing; the band
// stops below 0.2.
func TestTentativeWakeNeedsSelfPlayback(t *testing.T) {
	mic := script(rep(noise, 40))
	r, m := tentativeRig(t, mic, false, map[int]float32{10: 0.3})
	stop := r.run(t)
	r.noTurnBy(t, mic, 30)
	_ = stop()
	mic = script(rep(noise, 40))
	r2, m2 := tentativeRig(t, mic, true, map[int]float32{10: 0.19})
	stop = r2.run(t)
	r2.noTurnBy(t, mic, 30)
	_ = stop()
	if len(m.list()) != 0 || len(m2.list()) != 0 {
		t.Fatalf("music steps %v / %v, want none", m.list(), m2.list())
	}
}

// Stopping mid-tentative still puts the music back.
func TestTentativeWakeRestoredWhenThePipelineStops(t *testing.T) {
	mic := script(rep(noise, 40))
	r, m := tentativeRig(t, mic, true, map[int]float32{10: 0.3})
	stop := r.run(t)
	r.noTurnBy(t, mic, 14)
	_ = stop()
	if got, want := m.list(), []string{"duck", "restore"}; !slices.Equal(got, want) {
		t.Fatalf("music steps %v, want %v", got, want)
	}
}
