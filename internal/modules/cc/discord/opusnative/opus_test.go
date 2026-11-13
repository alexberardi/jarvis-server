package opusnative

import (
	"math"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	if err := Load(); err != nil {
		t.Skip(err)
	}
	enc, err := NewEncoder()
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var peak float64
	for f := 0; f < 10; f++ { // a few frames so the codec warms up
		pcm := make([]int16, FrameSize*Channels)
		for i := 0; i < FrameSize; i++ {
			v := int16(12000 * math.Sin(2*math.Pi*440*float64(f*FrameSize+i)/SampleRate))
			pcm[2*i], pcm[2*i+1] = v, v
		}
		pkt, err := enc.Encode(pcm)
		if err != nil {
			t.Fatal(err)
		}
		out, err := dec.Decode(pkt)
		if err != nil || len(out) != FrameSize*Channels {
			t.Fatalf("decode: %v len=%d", err, len(out))
		}
		for _, s := range out {
			peak = math.Max(peak, math.Abs(float64(s)))
		}
	}
	if peak < 5000 {
		t.Fatalf("decoded tone too quiet: peak %.0f", peak)
	}
}
