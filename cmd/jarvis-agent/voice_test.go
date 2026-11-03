package main

import (
	"flag"
	"testing"
	"time"
)

func TestWakeDebounceFlag(t *testing.T) {
	for _, c := range []struct {
		args []string
		want time.Duration
	}{
		{nil, 8 * time.Second},
		{[]string{"--wake-debounce", "3s"}, 3 * time.Second},
		{[]string{"--wake-debounce", "100ms"}, 500 * time.Millisecond},
		{[]string{"--wake-debounce", "0"}, 500 * time.Millisecond},
		{[]string{"--wake-debounce", "-2s"}, 500 * time.Millisecond},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		f := addVoiceFlags(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		if got := wakeDebounce(f.debounce); got != c.want {
			t.Errorf("%v → %v, want %v", c.args, got, c.want)
		}
		if c.args == nil && (f.onsetWait != 1500*time.Millisecond || f.listenSilence != 1.2) {
			t.Errorf("--listen-onset-wait / --listen-silence default to %v / %v, want 1.5s / 1.2", f.onsetWait, f.listenSilence)
		}
		if c.args == nil && f.preRoll != time.Second {
			t.Errorf("--listen-preroll defaults to %v, want 1s", f.preRoll)
		}
	}
}
