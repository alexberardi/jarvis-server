package audio

import (
	"bytes"
	"math"
	"testing"
)

func TestWAVRoundTrip(t *testing.T) {
	in := []float32{0, 0.5, -0.5, 1, -1, 0.25}
	var buf bytes.Buffer
	if err := WriteWAV(&buf, in, 24000); err != nil {
		t.Fatal(err)
	}
	out, rate, err := ReadWAV(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if rate != 24000 || len(out) != len(in) {
		t.Fatalf("rate=%d len=%d", rate, len(out))
	}
	for i := range in {
		if math.Abs(float64(out[i]-in[i])) > 1.0/32767 {
			t.Errorf("sample %d: got %v want %v", i, out[i], in[i])
		}
	}
}

func TestReadWAVDownmixesStereo(t *testing.T) {
	// 2 frames of 16-bit stereo: (16384, 0) and (-16384, -16384)
	pcm := []int16{16384, 0, -16384, -16384}
	wav := pcmWAV(t, pcm, 2, 16000)
	out, rate, err := ReadWAV(bytes.NewReader(wav))
	if err != nil {
		t.Fatal(err)
	}
	if rate != 16000 || len(out) != 2 {
		t.Fatalf("rate=%d len=%d", rate, len(out))
	}
	if math.Abs(float64(out[0]-0.25)) > 1e-3 || math.Abs(float64(out[1]+0.5)) > 1e-3 {
		t.Fatalf("downmix = %v", out)
	}
}

func TestReadWAVRejectsNonPCM16(t *testing.T) {
	wav := pcmWAV(t, []int16{0, 0}, 1, 16000)
	wav[34] = 8 // bits per sample
	if _, _, err := ReadWAV(bytes.NewReader(wav)); err == nil {
		t.Fatal("expected error for 8-bit WAV")
	}
}

func pcmWAV(t *testing.T, pcm []int16, channels, rate int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := writeHeader(&buf, len(pcm)*2, channels, rate); err != nil {
		t.Fatal(err)
	}
	for _, s := range pcm {
		buf.WriteByte(byte(s))
		buf.WriteByte(byte(uint16(s) >> 8))
	}
	return buf.Bytes()
}

func TestWritePCM16WAV(t *testing.T) {
	pcm := []byte{0x00, 0x40, 0x00, 0xC0} // 16384, -16384
	var buf bytes.Buffer
	if err := WritePCM16WAV(&buf, pcm, 1, 24000); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 44+len(pcm) {
		t.Fatalf("len %d", buf.Len())
	}
	out, rate, err := ReadWAV(bytes.NewReader(buf.Bytes()))
	if err != nil || rate != 24000 || len(out) != 2 || out[0] != 0.5 || out[1] != -0.5 {
		t.Fatalf("out=%v rate=%d err=%v", out, rate, err)
	}
}
