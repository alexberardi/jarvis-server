//go:build windows

package device

import (
	"math"
	"os"
	"testing"
	"time"
	"unsafe"
)

// The Go mirrors of WAVEFORMATEX and WAVEHDR must match the C layout (mmeapi.h, 64-bit), and
// winmm must load.
func TestWinmmStructLayout(t *testing.T) {
	var f waveFormatEx
	for name, c := range map[string][2]uintptr{
		"nChannels": {unsafe.Offsetof(f.Channels), 2}, "nSamplesPerSec": {unsafe.Offsetof(f.SamplesPerSec), 4},
		"nAvgBytesPerSec": {unsafe.Offsetof(f.AvgBytesPerSec), 8}, "nBlockAlign": {unsafe.Offsetof(f.BlockAlign), 12},
		"wBitsPerSample": {unsafe.Offsetof(f.BitsPerSample), 14}, "cbSize": {unsafe.Offsetof(f.Size), 16},
	} {
		if c[0] != c[1] {
			t.Errorf("offsetof(WAVEFORMATEX.%s) = %d, want %d", name, c[0], c[1])
		}
	}
	var h waveHdr
	if s := unsafe.Sizeof(h); s != 48 {
		t.Errorf("sizeof(WAVEHDR) = %d, want 48", s)
	}
	for name, c := range map[string][2]uintptr{
		"dwBufferLength": {unsafe.Offsetof(h.BufferLength), 8}, "dwBytesRecorded": {unsafe.Offsetof(h.BytesRecorded), 12},
		"dwUser": {unsafe.Offsetof(h.User), 16}, "dwFlags": {unsafe.Offsetof(h.Flags), 24},
		"dwLoops": {unsafe.Offsetof(h.Loops), 28}, "lpNext": {unsafe.Offsetof(h.Next), 32}, "reserved": {unsafe.Offsetof(h.Reserved), 40},
	} {
		if c[0] != c[1] {
			t.Errorf("offsetof(WAVEHDR.%s) = %d, want %d", name, c[0], c[1])
		}
	}
	if err := procWaveInOpen.Find(); err != nil {
		t.Fatalf("winmm: %v", err)
	}
	if Backend() != "winmm" {
		t.Fatalf("Backend() = %q", Backend())
	}
}

// JARVIS_AUDIO_NATIVE=1: play a short, quiet (-40 dBFS) tone on the default output, then
// capture 1 s from the default input (silence if Windows' microphone privacy setting blocks
// desktop apps).
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
	c, err := OpenCapture("default", 16000, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]int16, 1280)
	peak := 0
	capStart := time.Now()
	for range 13 {
		if err := c.Read(buf); err != nil {
			t.Fatal(err)
		}
		for _, v := range buf {
			peak = max(peak, int(v), -int(v))
		}
	}
	if el := time.Since(capStart); el < 800*time.Millisecond {
		t.Fatalf("~1 s of audio captured in %v: it wasn't captured in real time", el)
	}
	t.Logf("played 0.5 s in %v; captured ~1 s, peak %d, overruns %d", time.Since(start), peak, c.Overruns())
}
