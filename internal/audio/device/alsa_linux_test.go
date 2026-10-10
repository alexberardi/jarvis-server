//go:build linux

package device

import (
	"os"
	"testing"
)

// The purego binding against the real libasound, on ALSA's "null" PCM (always defined in
// alsa-lib's config, no hardware needed). Skips where libasound isn't installed.
func TestALSANullDevice(t *testing.T) {
	if err := loadALSA(); err != nil {
		t.Skip(err)
	}
	c, err := OpenCapture("null", 48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]int16, 3840)
	for range 3 {
		if err := c.Read(buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal("Close must be idempotent")
	}
	p, err := OpenPlayback("null", 24000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Write(make([]int16, 4800)); err != nil {
		t.Fatal(err)
	}
	if err := p.Write(make([]int16, 3)); err != nil {
		t.Fatal(err)
	}
	if err := p.Drain(); err != nil {
		t.Fatal(err)
	}
	_ = p.Close()
	if _, err := OpenCapture("no-such-pcm-jarvis", 48000, 1); err == nil {
		t.Fatal("opening an unknown PCM must fail")
	}
}

// JARVIS_AUDIO_NATIVE=1 records 1 s from "default" and plays it back (a real desktop).
func TestNativeDefaultRoundTrip(t *testing.T) {
	if os.Getenv("JARVIS_AUDIO_NATIVE") == "" {
		t.Skip("set JARVIS_AUDIO_NATIVE=1 to use the real default devices")
	}
	c, err := OpenCapture("default", 48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, 48000)
	if err := c.Read(pcm); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	t.Logf("captured 1 s, overruns %d, RMS-ish peak %d", c.Overruns(), peak(pcm))
	p, err := OpenPlayback("default", 48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Write(pcm); err != nil {
		t.Fatal(err)
	}
	if err := p.Drain(); err != nil {
		t.Fatal(err)
	}
}

func peak(s []int16) int {
	m := 0
	for _, v := range s {
		m = max(m, int(v), -int(v))
	}
	return m
}
