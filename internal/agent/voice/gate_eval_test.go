package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/voice/sherpa"
)

// The acoustic half of the follow-up gate's evaluation (go-pi-node.md §3.10). It renders
// conversations with Kokoro voices and real LibriSpeech/node-mic recordings, puts them through
// simulated rooms (near talker, someone across the room, a TV), runs each answer through the
// pipeline's own follow-up listening and endpointing, and measures what the gate sees: the
// speech level relative to the wake (this package's SpeechLevel) and the same-voice
// similarity (stt.EmbedVoice, what command-center computes). It writes the cases to
// $JARVIS_GATE_EVAL/cases.json for the semantic half (internal/modules/cc
// TestFollowUpGateEvalLive) and prints the tables.
//
//	JARVIS_SHERPA_MODELS=…/spikes/voice-onnx/models JARVIS_GATE_EVAL=/tmp/gate \
//	  [JARVIS_GATE_LIBRI=dir of <speaker>/*.wav] [JARVIS_GATE_REAL=dir with <who>/{enroll,near,far}/*.wav] \
//	  go test -run TestGateEval -v -timeout 30m ./internal/agent/voice/
//
// Synthetic rooms are a stand-in for real ones: the numbers rank the signals and set the
// defaults; the real-room check is a person and a TV (STATUS.md lists it).

type gateCase struct {
	ID       string `json:"id"`
	Category string `json:"category"` // genuine, genuine_far, other_person, tv, same_unrelated, long_pause
	Context  string `json:"context"`  // key into gateContexts
	Text     string `json:"text"`
	Voice    string `json:"voice"`
	Room     string `json:"room"` // near, turned, mid, far, tv
	// Measured.
	LevelDelta   *float64 `json:"level_delta_db"`
	VoiceSim     *float64 `json:"voice_similarity"`
	ProbeSpeech  float64  `json:"probe_speech_s"`
	RecordedSecs float64  `json:"recorded_s"`
	Truncated    bool     `json:"truncated"`
	EmbedMs      float64  `json:"embed_ms"`
}

// gateContext is the woken exchange a follow-up continues.
type gateContext struct {
	Command, Reply string
}

var gateContexts = map[string]gateContext{
	"weather":  {"What's the weather like today?", "It's 66 and overcast, with a chance of rain this afternoon."},
	"timer":    {"Set a timer for ten minutes.", "Timer set for ten minutes."},
	"time":     {"What time is it?", "It's 8:15 PM."},
	"shopping": {"Add milk to my shopping list.", "Added milk to your shopping list."},
	"austen":   {"Who wrote Pride and Prejudice?", "Jane Austen wrote Pride and Prejudice, published in 1813."},
	"calendar": {"What's on my calendar tomorrow?", "You have a dentist appointment at 3 PM tomorrow."},
}

type gateLine struct {
	cat, ctx, text string
}

// The follow-ups. Each context's user voice says the genuine and same_unrelated lines; other
// voices say other_person; tv lines go through the TV room.
var gateLines = []gateLine{
	{"genuine", "weather", "Do I need an umbrella then?"},
	{"genuine", "weather", "What about tomorrow?"},
	{"genuine", "timer", "Actually, make it fifteen minutes."},
	{"genuine", "time", "And what's the date today?"},
	{"genuine", "shopping", "Also add eggs and bread."},
	{"genuine", "shopping", "Can you read me the whole list?"},
	{"genuine", "austen", "When was she born?"},
	{"genuine", "austen", "What else did she write?"},
	{"genuine", "calendar", "Can you remind me an hour before?"},
	{"genuine", "calendar", "Is there anything else that day?"},
	{"genuine_far", "weather", "What about this weekend?"},
	{"genuine_far", "shopping", "And some coffee too."},
	{"genuine_far", "austen", "Which one is the most famous?"},
	{"other_person", "weather", "Hey, did you remember to pick up the dry cleaning?"},
	{"other_person", "timer", "Mom, where did you put my phone charger?"},
	{"other_person", "time", "I told him we'd be there by seven but he never listens."},
	{"other_person", "shopping", "Can you pass me the remote?"},
	{"other_person", "austen", "Yeah, no, the other one, the blue one."},
	{"other_person", "calendar", "So anyway, she said the meeting got moved again."},
	{"tv", "weather", "What's the weather going to look like for the weekend, Jim?"},
	{"tv", "timer", "Police say the suspect was last seen heading north on Interstate 95."},
	{"tv", "time", "And the score is now three to one with ten minutes left in the second half."},
	{"tv", "shopping", "Ask your doctor if this medication is right for you."},
	{"tv", "austen", "Coming up after the break, the five best beaches in Florida."},
	{"tv", "calendar", "Turn off the lights and lock the doors, she whispered."},
	{"same_unrelated", "weather", "Honey, can you grab my keys from the counter?"},
	{"same_unrelated", "timer", "Sorry, I'm on the phone, give me a second."},
	{"same_unrelated", "shopping", "No, I said the red folder, not the green one."},
	{"same_unrelated", "calendar", "Okay kids, time for dinner, go wash your hands."},
	{"same_unrelated", "austen", "Ugh, the dog got into the trash again."},
}

// long_pause: genuine follow-ups said in pieces, with the gap after each piece (seconds).
type pausedLine struct {
	ctx    string
	pieces []string
	gaps   []float64
}

var pausedLines = []pausedLine{
	{"shopping", []string{"Can you also add eggs,", "and some bread,", "and maybe a bag of coffee?"}, []float64{0.6, 0.9}},
	{"weather", []string{"And what about this weekend,", "because I was thinking about", "going hiking on Saturday?"}, []float64{0.8, 1.0}},
	{"calendar", []string{"Can you move it to,", "um,", "four o'clock instead?"}, []float64{0.7, 1.1}},
	{"austen", []string{"What else did she write,", "and which one", "should I read first?"}, []float64{0.5, 0.75}},
	{"timer", []string{"Actually,", "make it fifteen minutes,", "and call it the pasta timer."}, []float64{0.9, 0.6}},
}

// Kokoro voices (sid from internal/modules/tts): the user per context, others for the room.
var (
	userVoices = map[string]int{"weather": 3, "timer": 11, "time": 21, "shopping": 16, "austen": 9, "calendar": 24}
	// other_person/tv voices: never the context's user.
	otherVoices = []int{0, 2, 4, 12, 13, 15, 17, 20, 22, 25, 27}
	voiceNames  = map[int]string{0: "af_alloy", 2: "af_bella", 3: "af_heart", 4: "af_jessica", 9: "af_sarah",
		11: "am_adam", 12: "am_echo", 13: "am_eric", 15: "am_liam", 16: "am_michael", 17: "am_onyx",
		20: "bf_alice", 21: "bf_emma", 22: "bf_isabella", 24: "bm_daniel", 25: "bm_fable", 27: "bm_lewis"}
)

// --- the simulated room ---

// roomSpec is a talker's path to the mic: gain (dB, relative to the near talker), reverb
// (wet mix and decay), and the TV's extras.
type roomSpec struct {
	gainDB      float64
	wet, decay  float64
	tv          bool
	jitterDB    float64 // random per-utterance level variation, ±
	description string
}

var rooms = map[string]roomSpec{
	"near":   {0, 0.12, 0.25, false, 3, "same talker, ~0.5-1 m"},
	"turned": {-6, 0.30, 0.35, false, 2, "same talker, turned away / ~2 m"},
	"mid":    {-4, 0.25, 0.35, false, 3, "another person ~1.5-2 m"},
	"far":    {-12, 0.45, 0.5, false, 3, "another person across the room ~4 m"},
	"tv":     {-10, 0.40, 0.5, true, 3, "TV across the room (band-limited, compressed, music bed)"},
}

const (
	nearSpeechDBFS = -26.0 // a near talker's speech level on a desktop mic
	noiseFloorRMS  = 25.0  // room tone (≈ -62 dBFS)
	capRate        = CaptureRate
)

type biquad struct{ b0, b1, b2, a1, a2, z1, z2 float64 }

func (f *biquad) run(x float64) float64 {
	y := f.b0*x + f.z1
	f.z1 = f.b1*x - f.a1*y + f.z2
	f.z2 = f.b2*x - f.a2*y
	return y
}

func lowpass(fc float64) *biquad {
	w := 2 * math.Pi * fc / capRate
	q := 0.707
	al := math.Sin(w) / (2 * q)
	c := math.Cos(w)
	a0 := 1 + al
	return &biquad{b0: (1 - c) / 2 / a0, b1: (1 - c) / a0, b2: (1 - c) / 2 / a0, a1: -2 * c / a0, a2: (1 - al) / a0}
}

func highpass(fc float64) *biquad {
	w := 2 * math.Pi * fc / capRate
	q := 0.707
	al := math.Sin(w) / (2 * q)
	c := math.Cos(w)
	a0 := 1 + al
	return &biquad{b0: (1 + c) / 2 / a0, b1: -(1 + c) / a0, b2: (1 + c) / 2 / a0, a1: -2 * c / a0, a2: (1 - al) / a0}
}

// reverb is a Schroeder reverberator: four combs into two allpasses, decay = comb feedback.
func reverb(x []float64, wet, decay float64) []float64 {
	tail := int(0.6 * capRate)
	in := append(slices.Clone(x), make([]float64, tail)...)
	combs := []int{1557 * 2, 1617 * 2, 1491 * 2, 1422 * 2}
	out := make([]float64, len(in))
	fb := 0.6 + 0.35*decay
	for _, d := range combs {
		buf := make([]float64, d)
		for i, v := range in {
			y := buf[i%d]
			buf[i%d] = v + y*fb
			out[i] += y / 4
		}
	}
	for _, d := range []int{225 * 2, 556 * 2} {
		buf := make([]float64, d)
		g := 0.5
		for i, v := range out {
			b := buf[i%d]
			y := -v + b
			buf[i%d] = v + b*g
			out[i] = y
		}
	}
	for i := range in {
		out[i] = (1-wet)*in[i] + wet*out[i]*1.5
	}
	return out
}

// levelOf is SpeechLevel's statistic on float samples (full scale 1.0), threshold-free: the
// 75th percentile of the loudest half of the 80 ms chunks.
func levelOf(x []float64) float64 {
	var r []float64
	for i := 0; i+ChunkSamples <= len(x); i += ChunkSamples {
		var s float64
		for _, v := range x[i : i+ChunkSamples] {
			s += v * v
		}
		r = append(r, math.Sqrt(s/ChunkSamples))
	}
	slices.Sort(r)
	if len(r) == 0 {
		return -120
	}
	return 20 * math.Log10(r[min(len(r)-1, int(0.875*float64(len(r))))]+1e-12)
}

func scaleTo(x []float64, db float64) []float64 {
	g := math.Pow(10, (db-levelOf(x))/20)
	out := make([]float64, len(x))
	for i, v := range x {
		out[i] = v * g
	}
	return out
}

// place puts a dry utterance in a room: level, reverb, TV processing. rng picks the jitter
// and the TV's music bed.
func place(dry []float64, room string, rng *rand.Rand) []float64 {
	r := rooms[room]
	x := slices.Clone(dry)
	if r.tv {
		hp, lp := highpass(180), lowpass(5500)
		for i, v := range x {
			x[i] = math.Tanh(2.5*lp.run(hp.run(v))*4) / 4 // band-limited, compressed
		}
	}
	x = scaleTo(x, nearSpeechDBFS+r.gainDB+(rng.Float64()*2-1)*r.jitterDB)
	x = reverb(x, r.wet, r.decay)
	if r.tv {
		// A music bed under the speech, 14 dB down, as long as the clip.
		f0 := 110 * math.Pow(2, float64(rng.IntN(12))/12)
		bed := make([]float64, len(x))
		for i := range bed {
			t := float64(i) / capRate
			env := 0.6 + 0.4*math.Sin(2*math.Pi*0.5*t)
			bed[i] = env * (math.Sin(2*math.Pi*f0*t) + 0.5*math.Sin(2*math.Pi*f0*1.26*t) + 0.4*math.Sin(2*math.Pi*f0*1.5*t))
		}
		bed = scaleTo(bed, levelOf(x)-14)
		for i := range x {
			x[i] += bed[i]
		}
	}
	return x
}

// capture renders float audio as the mic would hand it over: room tone added, int16.
func capture(x []float64, rng *rand.Rand) []int16 {
	out := make([]int16, len(x))
	for i, v := range x {
		s := v*32768 + rng.NormFloat64()*noiseFloorRMS
		out[i] = int16(max(-32768, min(32767, math.Round(s))))
	}
	return out
}

func silence(secs float64) []float64 { return make([]float64, int(secs*capRate)) }

func chunksOf(pcm []int16) [][]int16 {
	var out [][]int16
	for i := 0; i+ChunkSamples <= len(pcm); i += ChunkSamples {
		out = append(out, pcm[i:i+ChunkSamples])
	}
	return out
}

// --- the harness ---

type gateEval struct {
	t      *testing.T
	tts    *sherpa.TTS
	spk    stt.SpeakerModel
	cache  map[string][]float64
	rng    *rand.Rand
	p      *Pipeline
	silSec float64 // FollowUp.SilenceSecs under test
}

type sherpaModel struct{ ex *sherpa.SpeakerExtractor }

func (s sherpaModel) ID() string { return "eres2net" }
func (s sherpaModel) Embed(x []float32, rate int) ([]float32, error) {
	return s.ex.Embed(x, rate)
}

func (g *gateEval) say(text string, sid int) []float64 {
	key := fmt.Sprintf("%d|%s", sid, text)
	if v, ok := g.cache[key]; ok {
		return v
	}
	s, err := g.tts.Generate(text, sid, 1.0)
	if err != nil {
		g.t.Fatalf("tts %q: %v", text, err)
	}
	up := stt.Resample(s, g.tts.SampleRate(), capRate)
	out := make([]float64, len(up))
	for i, v := range up {
		out[i] = float64(v)
	}
	g.cache[key] = out
	return out
}

// wake renders "Hey Jarvis." + the command near the mic and records the command with the
// wake's endpointing; it returns the session (threshold, wake clip, wake level) and the
// speaker_audio command-center would embed.
func (g *gateEval) wake(heyJarvis, command []float64) (*session, []int16) {
	preRMS := make([]float64, preWakeChunks)
	for i := range preRMS {
		preRMS[i] = noiseFloorRMS
	}
	th := g.p.Cfg.Endpointing.Threshold(preRMS)
	clip := capture(append(silence(2), heyJarvis...), g.rng)
	clip = clip[len(clip)-wakeClipChunks*ChunkSamples:]
	cmd := capture(slices.Concat(silence(0.3), command, silence(2.5)), g.rng)
	ch := make(chan []int16, len(cmd)/ChunkSamples+1)
	for _, c := range chunksOf(cmd) {
		ch <- c
	}
	s := &session{id: "eval", log: g.p.Log, chunks: ch, wakeClip: clip, threshold: th}
	pcm, _, err := g.p.record(context.Background(), s)
	if err != nil {
		g.t.Fatal(err)
	}
	if db, ok := SpeechLevel(pcm, th); ok {
		s.wakeLevel = &db
	}
	return s, slices.Concat(clip, Normalize(pcm))
}

// followUp plays an answer into the follow-up window and returns what the window recorded
// (nil: nothing) and whether the recording ended before the answer did.
func (g *gateEval) followUp(s *session, answer []float64) (pcm []int16, truncated bool) {
	lead, tail := 1.2, 3.0
	in := capture(slices.Concat(silence(lead), answer, silence(tail)), g.rng)
	// Where the answer's speech ends, in chunks from the start of the stream.
	lastLoud := 0
	for i, c := range chunksOf(in) {
		if RMS(c) >= s.threshold {
			lastLoud = i
		}
	}
	ch := make(chan []int16, len(in)/ChunkSamples+2)
	cs := chunksOf(in)
	s.chunks = ch
	g.p.listening = func(string) {
		for _, c := range cs {
			ch <- c
		}
	}
	g.p.Cfg.FollowUp.SilenceSecs = g.silSec
	out, err := g.p.listenFollowUp(context.Background(), s, 8, 1, true, nil)
	if err != nil {
		g.t.Fatal(err)
	}
	if out == nil {
		return nil, true
	}
	// listenFollowUp consumed some prefix of cs and recorded the last len(out) samples of it.
	consumed := len(cs) - len(ch)
	return out, consumed-1 < lastLoud
}

func (g *gateEval) measure(c *gateCase, s *session, speakerAudio []int16, answer []float64) {
	pcm, trunc := g.followUp(s, answer)
	c.Truncated = trunc
	if pcm == nil {
		return
	}
	c.RecordedSecs = float64(len(pcm)) / CaptureRate
	c.LevelDelta = levelVsWake(s, pcm)
	ref := g.voice(speakerAudio)
	start := time.Now()
	probe := g.voice(Normalize(pcm))
	c.EmbedMs = float64(time.Since(start).Microseconds()) / 1000
	c.ProbeSpeech = probe.SpeechSecs
	if sim, ok := ref.Similarity(probe); ok {
		sim = math.Round(sim*1000) / 1000
		c.VoiceSim = &sim
	}
}

// voice is command-center's computation: the 48 kHz upload decoded to 16 kHz, then EmbedVoice.
func (g *gateEval) voice(pcm []int16) stt.Voice {
	samples, err := stt.DecodeWAV(wavBytes(pcm))
	if err != nil {
		g.t.Fatal(err)
	}
	v, err := stt.EmbedVoice(g.spk, samples)
	if err != nil {
		g.t.Fatal(err)
	}
	return v
}

func newGateEval(t *testing.T) (*gateEval, string) {
	out := os.Getenv("JARVIS_GATE_EVAL")
	models := os.Getenv("JARVIS_SHERPA_MODELS")
	if out == "" || models == "" {
		t.Skip("JARVIS_GATE_EVAL and JARVIS_SHERPA_MODELS not set")
	}
	if err := sherpa.Load(filepath.Join(os.TempDir(), "jarvis-sherpa-test")); err != nil {
		t.Fatal(err)
	}
	tts, err := sherpa.NewKokoro(sherpa.KokoroConfig{Dir: filepath.Join(models, "kokoro-multi-lang-v1_0"),
		Lexicon: "lexicon-us-en.txt", Lang: "en-us", NumThreads: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tts.Close)
	ex, err := sherpa.NewSpeakerExtractor(sherpa.SpeakerConfig{Model: filepath.Join(models, "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx"), NumThreads: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ex.Close)
	cfg := DefaultConfig()
	p := &Pipeline{Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return &gateEval{t: t, tts: tts, spk: sherpaModel{ex}, cache: map[string][]float64{},
		rng: rand.New(rand.NewPCG(1, 2)), p: p, silSec: cfg.FollowUp.SilenceSecs}, out
}

func TestGateEval(t *testing.T) {
	g, out := newGateEval(t)
	const hey = "Hey Jarvis."
	var cases []gateCase
	sessions := map[string]struct {
		s  *session
		sa []int16
	}{}
	wakeFor := func(ctx string) (*session, []int16) {
		if w, ok := sessions[ctx]; ok {
			s := *w.s
			return &s, w.sa
		}
		sid := userVoices[ctx]
		s, sa := g.wake(place(g.say(hey, sid), "near", g.rng), place(g.say(gateContexts[ctx].Command, sid), "near", g.rng))
		sessions[ctx] = struct {
			s  *session
			sa []int16
		}{s, sa}
		c := *s
		return &c, sa
	}
	oi := 0
	for i, l := range gateLines {
		user := userVoices[l.ctx]
		sid, room := user, "near"
		switch l.cat {
		case "genuine_far":
			room = "turned"
		case "other_person":
			sid = otherVoices[oi%len(otherVoices)]
			oi++
			room = []string{"near", "mid", "far"}[i%3]
		case "tv":
			sid = otherVoices[oi%len(otherVoices)]
			oi++
			room = "tv"
		}
		s, sa := wakeFor(l.ctx)
		c := gateCase{ID: fmt.Sprintf("%s-%02d", l.cat, i), Category: l.cat, Context: l.ctx, Text: l.text,
			Voice: voiceNames[sid], Room: room}
		g.measure(&c, s, sa, place(g.say(l.text, sid), room, g.rng))
		cases = append(cases, c)
	}
	// Long answers with pauses, at the configured trailing silence and at the old 0.5 s.
	for _, sil := range []float64{0.5, g.p.Cfg.FollowUp.SilenceSecs} {
		g.silSec = sil
		for i, pl := range pausedLines {
			sid := userVoices[pl.ctx]
			var parts [][]float64
			for j, piece := range pl.pieces {
				parts = append(parts, g.say(piece, sid))
				if j < len(pl.gaps) {
					parts = append(parts, silence(pl.gaps[j]))
				}
			}
			s, sa := wakeFor(pl.ctx)
			c := gateCase{ID: fmt.Sprintf("long_pause-%02d-sil%.1f", i, sil), Category: "long_pause", Context: pl.ctx,
				Text: strings.Join(pl.pieces, " "), Voice: voiceNames[sid], Room: "near"}
			g.measure(&c, s, sa, place(slices.Concat(parts...), "near", g.rng))
			if sil == g.p.Cfg.FollowUp.SilenceSecs {
				cases = append(cases, c)
			}
			t.Logf("long_pause silence=%.1fs %-12s gaps=%v recorded=%.2fs truncated=%v", sil, pl.ctx, pl.gaps, c.RecordedSecs, c.Truncated)
		}
	}
	g.silSec = g.p.Cfg.FollowUp.SilenceSecs

	b, _ := json.MarshalIndent(cases, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "cases.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	var tbl strings.Builder
	fmt.Fprintf(&tbl, "%-22s %-14s %-7s %8s %8s %6s %5s %s\n", "id", "voice", "room", "level", "voice", "spch", "trunc", "text")
	for _, c := range cases {
		fmt.Fprintf(&tbl, "%-22s %-14s %-7s %8s %8s %6.2f %5v %s\n", c.ID, c.Voice, c.Room, fp(c.LevelDelta), fp(c.VoiceSim), c.ProbeSpeech, c.Truncated, c.Text)
	}
	t.Log("\n" + tbl.String())
	var ms []float64
	for _, c := range cases {
		if c.EmbedMs > 0 {
			ms = append(ms, c.EmbedMs)
		}
	}
	slices.Sort(ms)
	t.Logf("voice embedding of one answer (ERes2Net, 2 threads): p50 %.0f ms, max %.0f ms", pct(ms, 0.5), pct(ms, 1))

	gateEvalReal(t, g)
}

func fp(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *v)
}

// gateEvalReal is the same measurement on real voices: LibriSpeech speakers (each one's first
// utterance is the wake; their other utterances and other speakers' are the follow-ups, in
// every room) and node-mic recordings of a household member (enroll/01 is the wake; near/ and
// far/ are real follow-up-distance takes). It also measures the pauses inside real
// utterances, the evidence for the trailing silence.
func gateEvalReal(t *testing.T, g *gateEval) {
	libri := os.Getenv("JARVIS_GATE_LIBRI")
	if libri == "" {
		t.Log("JARVIS_GATE_LIBRI not set: real-voice tables skipped")
		return
	}
	spks, _ := filepath.Glob(filepath.Join(libri, "*"))
	sort.Strings(spks)
	load := func(p string) []float64 {
		f, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s, rate, err := audio.ReadWAV(bytes.NewReader(f))
		if err != nil {
			t.Fatal(err)
		}
		up := stt.Resample(s, rate, capRate)
		out := make([]float64, len(up))
		for i, v := range up {
			out[i] = float64(v)
		}
		return out
	}
	type res struct{ sims, levels []float64 }
	results := map[string]*res{}
	add := func(key string, c gateCase) {
		r := results[key]
		if r == nil {
			r = &res{}
			results[key] = r
		}
		if c.VoiceSim != nil {
			r.sims = append(r.sims, *c.VoiceSim)
		}
		if c.LevelDelta != nil {
			r.levels = append(r.levels, *c.LevelDelta)
		}
	}
	var gaps []float64
	truncAt := map[float64]int{}
	nUtt := 0
	for si, sp := range spks {
		files, _ := filepath.Glob(filepath.Join(sp, "*.wav"))
		sort.Strings(files)
		if len(files) < 4 {
			continue
		}
		first := load(files[0])
		// The wake: the first 1.2 s of the first utterance stand in for "hey jarvis", the
		// rest of it for the command.
		cut := min(len(first), int(1.2*capRate))
		s, sa := g.wake(place(first[:cut], "near", g.rng), place(first[cut:], "near", g.rng))
		for fi, f := range files[1:4] {
			u := load(f)
			for _, room := range []string{"near", "turned"} {
				c := gateCase{}
				ss := *s
				g.measure(&c, &ss, sa, place(u, room, g.rng))
				add("same/"+room, c)
			}
			other := load(filepath.Join(spks[(si+1+fi)%len(spks)], filepath.Base(must1(filepath.Glob(filepath.Join(spks[(si+1+fi)%len(spks)], "*.wav")))[1+fi])))
			for _, room := range []string{"near", "mid", "far", "tv"} {
				c := gateCase{}
				ss := *s
				g.measure(&c, &ss, sa, place(other, room, g.rng))
				add("other/"+room, c)
			}
		}
		// Pauses inside real read speech, and which trailing silence would cut them.
		for _, f := range files {
			u := place(load(f), "near", g.rng)
			pcm := capture(u, g.rng)
			cs := chunksOf(pcm)
			th := s.threshold
			first, last := -1, -1
			for i, c := range cs {
				if RMS(c) >= th {
					if first < 0 {
						first = i
					}
					last = i
				}
			}
			run := 0
			longest := 0
			for i := first; i >= 0 && i <= last; i++ {
				if RMS(cs[i]) < th {
					run++
				} else {
					if run > 0 {
						gaps = append(gaps, float64(run)*chunkSecs)
						longest = max(longest, run)
					}
					run = 0
				}
			}
			nUtt++
			for _, sil := range []float64{0.5, 0.8, 1.0, 1.2} {
				if longest >= max(1, frames(sil)) {
					truncAt[sil]++
				}
			}
		}
	}
	if real := os.Getenv("JARVIS_GATE_REAL"); real != "" {
		whos, _ := filepath.Glob(filepath.Join(real, "*"))
		for _, who := range whos {
			enroll, _ := filepath.Glob(filepath.Join(who, "enroll", "*.wav"))
			if len(enroll) == 0 {
				continue
			}
			sort.Strings(enroll)
			e := load(enroll[0])
			cut := min(len(e), int(1.2*capRate))
			// Real recordings already carry their room: no placement, only the level.
			s, sa := g.wake(scaleTo(e[:cut], nearSpeechDBFS), scaleTo(e[cut:], nearSpeechDBFS))
			gain := math.Pow(10, (nearSpeechDBFS-levelOf(e))/20) // one gain for the whole session
			for _, sub := range []string{"near", "far"} {
				fs, _ := filepath.Glob(filepath.Join(who, sub, "*.wav"))
				for _, f := range fs {
					u := load(f)
					for i := range u {
						u[i] *= gain
					}
					c := gateCase{}
					ss := *s
					g.measure(&c, &ss, sa, u)
					add("real_"+filepath.Base(who)+"/"+sub, c)
				}
			}
			// Impostors against the real wake: LibriSpeech near and Kokoro near.
			for i, sp := range spks[:min(10, len(spks))] {
				fs, _ := filepath.Glob(filepath.Join(sp, "*.wav"))
				c := gateCase{}
				ss := *s
				g.measure(&c, &ss, sa, place(load(fs[2]), "near", g.rng))
				add("real_"+filepath.Base(who)+"/impostor_libri", c)
				c = gateCase{}
				ss = *s
				g.measure(&c, &ss, sa, place(g.say("Can you add eggs to the list as well?", otherVoices[i%len(otherVoices)]), "near", g.rng))
				add("real_"+filepath.Base(who)+"/impostor_kokoro", c)
			}
		}
	}
	keys := make([]string, 0, len(results))
	for k := range results {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var tbl strings.Builder
	fmt.Fprintf(&tbl, "%-28s %4s  %-34s %4s  %s\n", "set", "n", "voice similarity min/p10/p50/p90/max", "n", "level vs wake dB min/p10/p50/p90/max")
	for _, k := range keys {
		r := results[k]
		fmt.Fprintf(&tbl, "%-28s %4d  %-34s %4d  %s\n", k, len(r.sims), dist(r.sims), len(r.levels), dist(r.levels))
	}
	t.Log("real voices\n" + tbl.String())
	dump := map[string]map[string][]float64{}
	for k, r := range results {
		dump[k] = map[string][]float64{"voice_similarity": r.sims, "level_vs_wake_db": r.levels}
	}
	if b, err := json.MarshalIndent(dump, "", " "); err == nil {
		_ = os.WriteFile(filepath.Join(os.Getenv("JARVIS_GATE_EVAL"), "real.json"), b, 0o644)
	}
	slices.Sort(gaps)
	t.Logf("pauses inside %d LibriSpeech utterances (%d gaps): p50=%.2f p90=%.2f p99=%.2f max=%.2f s; utterances with a pause that would end the recording: 0.5s=%d 0.8s=%d 1.0s=%d 1.2s=%d",
		nUtt, len(gaps), pct(gaps, 0.5), pct(gaps, 0.9), pct(gaps, 0.99), pct(gaps, 1), truncAt[0.5], truncAt[0.8], truncAt[1.0], truncAt[1.2])
}

func must1(v []string, _ error) []string { sort.Strings(v); return v }

func pct(x []float64, p float64) float64 {
	if len(x) == 0 {
		return math.NaN()
	}
	return x[min(len(x)-1, int(p*float64(len(x))))]
}

func dist(x []float64) string {
	if len(x) == 0 {
		return "-"
	}
	s := slices.Clone(x)
	slices.Sort(s)
	return fmt.Sprintf("%.2f/%.2f/%.2f/%.2f/%.2f", s[0], pct(s, 0.1), pct(s, 0.5), pct(s, 0.9), s[len(s)-1])
}

// TestGateEvalClips writes the live-run clips (a wake, follow-ups near/paused/TV) as 48 kHz
// WAVs to $JARVIS_GATE_CLIPS, for playing into a PipeWire null sink the agent listens to.
func TestGateEvalClips(t *testing.T) {
	dir := os.Getenv("JARVIS_GATE_CLIPS")
	if dir == "" {
		t.Skip("JARVIS_GATE_CLIPS not set")
	}
	g, _ := newGateEval(t)
	user := userVoices["time"]
	write := func(name string, x []float64) {
		f := make([]float32, len(x))
		for i, v := range x {
			f[i] = float32(max(-1, min(1, v)))
		}
		var b bytes.Buffer
		if err := audio.WriteWAV(&b, f, capRate); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	near := func(text string, sid int) []float64 { return place(g.say(text, sid), "near", g.rng) }
	write("wake.wav", slices.Concat(silence(0.5), near("Hey Jarvis.", user), silence(0.6), near("What time is it?", user), silence(1)))
	write("fu_date.wav", near("And what's the date today?", user))
	paused := slices.Concat(g.say("Can you also tell me,", user), silence(1.0), g.say("what day of the week", user),
		silence(0.9), g.say("it will be next Friday?", user))
	write("fu_paused.wav", place(paused, "near", g.rng))
	write("fu_tv.wav", place(g.say("Police say the suspect was last seen heading north on Interstate 95.", otherVoices[7]), "tv", g.rng))
	write("fu_other.wav", place(g.say("Mom, where did you put my phone charger?", otherVoices[2]), "mid", g.rng))
}
