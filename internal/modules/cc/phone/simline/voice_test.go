package simline

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

// fakeSpeech is the sandbox's TTS and STT: TTS renders one second of a 440 Hz tone at 24 kHz
// per call, STT reports the clip it got.
type fakeSpeech struct {
	mu     sync.Mutex
	voices []string // the voice of each TTS call
	clips  [][]int16
	rates  []int
}

func (f *fakeSpeech) tts(_ context.Context, _, voice string) ([]int16, int, error) {
	f.mu.Lock()
	f.voices = append(f.voices, voice)
	f.mu.Unlock()
	pcm := make([]int16, 24000)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/24000))
	}
	return pcm, 24000, nil
}

func (f *fakeSpeech) Transcribe(_ context.Context, wav []byte, _ stt.TranscribeOptions) (stt.Result, error) {
	pcm := make([]int16, (len(wav)-44)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(wav[44+2*i:]))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clips = append(f.clips, pcm)
	f.rates = append(f.rates, int(binary.LittleEndian.Uint32(wav[24:])))
	return stt.Result{Text: fmt.Sprintf(" %d samples ", len(pcm))}, nil
}

func (f *fakeSpeech) snapshot() (voices []string, clips [][]int16, rates []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.voices...), append([][]int16{}, f.clips...), append([]int{}, f.rates...)
}

// clock is the call's frame clock: step sends one tick and returns the frame it produced.
type clock struct {
	mu    sync.Mutex
	t     time.Time
	ticks chan time.Time
}

func (k *clock) now() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.t
}

func (k *clock) step(c phone.Call) []int16 {
	k.mu.Lock()
	k.t = k.t.Add(20 * time.Millisecond)
	t := k.t
	k.mu.Unlock()
	k.ticks <- t
	return <-c.Frames()
}

type voiceRig struct {
	line  *Line
	call  phone.Call
	clk   *clock
	sp    *fakeSpeech
	turns chan Turn
	asked chan struct{}
}

func newVoiceRig(t *testing.T, reply string) *voiceRig {
	r := &voiceRig{clk: &clock{t: time.Unix(1000, 0), ticks: make(chan time.Time)}, sp: &fakeSpeech{},
		turns: make(chan Turn, 8), asked: make(chan struct{}, 8)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var tn Turn
		_ = json.NewDecoder(req.Body).Decode(&tn)
		r.turns <- tn
		_ = json.NewEncoder(w).Encode(Reply{Say: reply})
	}))
	t.Cleanup(srv.Close)
	r.line = New(srv.URL, nil)
	r.line.Voice = &Voice{STT: r.sp, TTS: r.sp.tts, FarVoice: FarVoice}
	r.line.tick, r.line.now = r.clk.ticks, r.clk.now
	r.line.asked = func() { r.asked <- struct{}{} }
	c, err := r.line.Dial(context.Background(), phone.DialRequest{SessionID: "s1", To: "+17325550100"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	r.call = c
	return r
}

func rms(f []int16) float64 {
	var s float64
	for _, v := range f {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(f)))
}

const quietFrames = int(quietAfterJarvis / (20 * time.Millisecond))

// Jarvis's speech plays in real time; once it has played and gone quiet the driver gets the
// far end's transcription of it (8 kHz, markers removed) with the exact text alongside, and its
// reply comes back as the far voice's speech, paced a frame per tick, for the call's VAD.
func TestVoiceCallRoundTrip(t *testing.T) {
	r := newVoiceRig(t, "Papa V's, how can I help?")
	pcm, sr, err := r.line.SynthesizePCM(context.Background(), "Hi, this is Jarvis.")
	if err != nil || sr != 8000 || len(pcm) != 4+8000 {
		t.Fatalf("synthesized %d samples at %d (%v)", len(pcm), sr, err)
	}
	end, _ := r.call.Play(pcm, "t0")
	if end != int64(len(pcm)) {
		t.Fatalf("Play end = %d, want %d", end, len(pcm))
	}
	steps := 0
	for {
		pos, changed := r.call.Played()
		if pos >= end {
			break
		}
		r.clk.step(r.call)
		select {
		case <-changed:
		default:
			t.Fatal("Played did not signal its advance")
		}
		steps++
	}
	if steps != 51 {
		t.Fatalf("playback took %d frames, want 51 (real time)", steps)
	}
	for range quietFrames - 1 {
		r.clk.step(r.call)
	}
	select {
	case tn := <-r.turns:
		t.Fatalf("driver asked before Jarvis was quiet: %+v", tn)
	default:
	}
	r.clk.step(r.call)
	<-r.asked
	tn := <-r.turns
	if tn.Event != "spoke" || !tn.Voice || len(tn.Said) != 1 || tn.Said[0] != "Hi, this is Jarvis." || tn.Heard != "8004 samples" {
		t.Fatalf("driver got %+v", tn)
	}
	voices, clips, rates := r.sp.snapshot()
	if rates[0] != 8000 {
		t.Fatalf("transcribed at %d Hz", rates[0])
	}
	for _, v := range clips[0] {
		if v == markerA {
			t.Fatal("the marker reached the far end's transcription")
		}
	}
	if len(voices) != 2 || voices[0] != "" || voices[1] != FarVoice {
		t.Fatalf("TTS voices = %q, want Jarvis's then %s", voices, FarVoice)
	}

	vad := live.NewVAD(live.DefaultVADConfig())
	for i := 0; ; i++ {
		f := r.clk.step(r.call)
		if loud := rms(f) > 1000; loud != (i < 50) {
			t.Fatalf("frame %d rms %.0f: the reply should be 50 loud frames", i, rms(f))
		}
		if utt := vad.Feed(f, false); utt != nil {
			res, err := r.line.Transcribe(context.Background(), live.WAV(utt, 8000), stt.TranscribeOptions{})
			if err != nil || res.Text == "" {
				t.Fatalf("Transcribe: %q, %v", res.Text, err)
			}
			break
		}
		if i > 200 {
			t.Fatal("the VAD cut no utterance from the far end's speech")
		}
	}
	// The next turn tells the driver what Jarvis heard.
	if _, err := r.call.Play(pcm, "t1"); err != nil {
		t.Fatal(err)
	}
	for range 51 + quietFrames {
		r.clk.step(r.call)
	}
	<-r.asked
	tn = <-r.turns
	if len(tn.Understood) != 1 || tn.Understood[0] == "" {
		t.Fatalf("understood = %q", tn.Understood)
	}
}

// What Jarvis transcribed at the end of one call is not reported on the next.
func TestVoiceUnderstoodStaysWithItsCall(t *testing.T) {
	r := newVoiceRig(t, "")
	r.call.Close()
	if tn := <-r.turns; tn.Event != "ended" {
		t.Fatalf("got %+v", tn)
	}
	if _, err := r.line.Transcribe(context.Background(), live.WAV(make([]int16, 160), 8000), stt.TranscribeOptions{}); err != nil {
		t.Fatal(err)
	}
	c, _ := r.line.Dial(context.Background(), phone.DialRequest{SessionID: "s2"})
	defer c.Close()
	pcm, _, _ := r.line.SynthesizePCM(context.Background(), "Hello.")
	if _, err := c.Play(pcm, "t0"); err != nil {
		t.Fatal(err)
	}
	for range 51 + quietFrames {
		r.clk.step(c)
	}
	<-r.asked
	if tn := <-r.turns; len(tn.Understood) != 0 {
		t.Fatalf("the last call's transcription leaked: %q", tn.Understood)
	}
}

// A barge-in's Flush drops the unplayed audio: Played jumps to the end of the queue and the far
// end transcribes only what it heard.
func TestVoiceFlushCutsJarvisOff(t *testing.T) {
	r := newVoiceRig(t, "")
	pcm, _, _ := r.line.SynthesizePCM(context.Background(), "A long reply.")
	end, _ := r.call.Play(pcm, "t0")
	for range 10 {
		r.clk.step(r.call)
	}
	_, changed := r.call.Played()
	if err := r.call.Flush(); err != nil {
		t.Fatal(err)
	}
	<-changed
	if pos, _ := r.call.Played(); pos != end {
		t.Fatalf("after Flush Played = %d, want %d", pos, end)
	}
	for range quietFrames {
		r.clk.step(r.call)
	}
	<-r.asked
	if tn := <-r.turns; tn.Heard != "1600 samples" || len(tn.Said) != 1 {
		t.Fatalf("driver got %+v", tn)
	}
}
