package clip_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/discord/clip"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/discord/opusnative"
)

func TestOggRoundTrip(t *testing.T) {
	var in [][]byte
	for i := 0; i < 95; i++ { // more than one page, and one packet of 300 bytes to cross a lacing boundary
		n := 40 + i%7
		if i == 50 {
			n = 300
		}
		in = append(in, bytes.Repeat([]byte{byte(i)}, n))
	}
	out, err := clip.ReadOpusPackets(clip.WriteOpusPackets(in))
	if err != nil || len(out) != len(in) {
		t.Fatalf("got %d packets, err %v", len(out), err)
	}
	for i := range in {
		if !bytes.Equal(in[i], out[i]) {
			t.Fatalf("packet %d differs", i)
		}
	}
	if _, err := clip.ReadOpusPackets([]byte("not ogg at all, really not ogg")); err == nil {
		t.Fatal("garbage accepted")
	}
}

// Speech-shaped audio (a 300 Hz tone at 22.05 kHz mono, as a TTS engine might make) survives
// PCM to Ogg Opus to WAV with its length and loudness.
func TestPCMToOggToWAV(t *testing.T) {
	if err := opusnative.Load(); err != nil {
		t.Skip(err)
	}
	const rate, secs = 22050, 2
	pcm := make([]byte, rate*secs*2)
	for i := 0; i < rate*secs; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(9000*math.Sin(2*math.Pi*300*float64(i)/rate))))
	}
	ogg, dur, err := clip.PCMToOgg(pcm, 1, rate)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(dur-secs) > 0.05 || !bytes.HasPrefix(ogg, []byte("OggS")) {
		t.Fatalf("duration %.2f, header %q", dur, ogg[:4])
	}
	wav, err := clip.OggToWAV(ogg)
	if err != nil {
		t.Fatal(err)
	}
	samples, sr, err := audio.ReadWAV(bytes.NewReader(wav))
	if err != nil {
		t.Fatal(err)
	}
	if sr != 48000 {
		t.Fatalf("rate %d", sr)
	}
	var peak float64
	for _, s := range samples {
		peak = math.Max(peak, math.Abs(float64(s)))
	}
	got := float64(len(samples)) / 48000 // ReadWAV mixes the channels down
	if peak < 0.15 || math.Abs(got-secs) > 0.2 {
		t.Fatalf("peak %.2f, %.2f s of audio", peak, got)
	}
}
