//go:build darwin

package device

import (
	"math"
	"os"
	"testing"
	"time"
	"unsafe"
)

// The Go mirrors of AudioStreamBasicDescription and AudioQueueBuffer must match the C layout
// (CoreAudioBaseTypes.h, AudioQueue.h on 64-bit).
func TestAudioQueueStructLayout(t *testing.T) {
	if s := unsafe.Sizeof(asbd{}); s != 40 {
		t.Fatalf("sizeof(AudioStreamBasicDescription) = %d, want 40", s)
	}
	var b aqBuffer
	for name, got := range map[string]uintptr{
		"mAudioData": unsafe.Offsetof(b.Data), "mAudioDataByteSize": unsafe.Offsetof(b.ByteSize),
		"mUserData": unsafe.Offsetof(b.UserData), "mPacketDescriptionCapacity": unsafe.Offsetof(b.PacketDescCapacity),
		"mPacketDescriptions": unsafe.Offsetof(b.PacketDescs), "mPacketDescriptionCount": unsafe.Offsetof(b.PacketDescCount),
	} {
		want := map[string]uintptr{"mAudioData": 8, "mAudioDataByteSize": 16, "mUserData": 24,
			"mPacketDescriptionCapacity": 32, "mPacketDescriptions": 40, "mPacketDescriptionCount": 48}[name]
		if got != want {
			t.Errorf("offsetof(%s) = %d, want %d", name, got, want)
		}
	}
	if err := loadAQ(); err != nil {
		t.Fatalf("AudioToolbox: %v", err)
	}
}

// JARVIS_AUDIO_NATIVE=1: play a short, quiet (-40 dBFS) tone on the default output, then
// capture 1 s from the default input (all zeros without the microphone permission).
func TestNativeDefaultRoundTrip(t *testing.T) {
	if os.Getenv("JARVIS_AUDIO_NATIVE") == "" {
		t.Skip("set JARVIS_AUDIO_NATIVE=1 to use the real default devices")
	}
	p, err := OpenPlayback("default", 24000, 1)
	if err != nil {
		t.Fatal(err)
	}
	tone := make([]int16, 24000/2)
	for i := range tone {
		tone[i] = int16(300 * math.Sin(2*math.Pi*440*float64(i)/24000))
	}
	start := time.Now()
	if err := p.Write(tone); err != nil {
		t.Fatal(err)
	}
	if err := p.Drain(); err != nil {
		t.Fatal(err)
	}
	_ = p.Close()
	if el := time.Since(start); el < 400*time.Millisecond {
		t.Fatalf("0.5 s of audio drained in %v: it wasn't played in real time", el)
	}
	c, err := OpenCapture("default", 48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]int16, 3840)
	peak := 0
	for range 13 {
		if err := c.Read(buf); err != nil {
			t.Fatal(err)
		}
		for _, v := range buf {
			peak = max(peak, int(v), -int(v))
		}
	}
	t.Logf("played 0.5 s in %v; captured ~1 s, peak %d, overruns %d", time.Since(start), peak, c.Overruns())
}
