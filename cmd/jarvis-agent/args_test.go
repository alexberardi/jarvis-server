package main

import (
	"flag"
	"strings"
	"testing"
)

func TestStrayArgsAreRefused(t *testing.T) {
	fs, f := newRunFlags(flag.ContinueOnError)
	// The user's case: everything after "--" was silently dropped, voice stayed off.
	if err := fs.Parse([]string{"--", "--voice"}); err != nil {
		t.Fatal(err)
	}
	err := noStrayArgs(fs)
	if err == nil || !strings.Contains(err.Error(), `jarvis-agent run --voice`) {
		t.Fatalf("got %v", err)
	}
	if *f.voiceOn {
		t.Fatal("voice on")
	}
	fs, _ = newRunFlags(flag.ContinueOnError)
	_ = fs.Parse([]string{"stray"})
	if err := noStrayArgs(fs); err == nil || strings.Contains(err.Error(), "flags go before") {
		t.Fatalf("got %v", err)
	}
	fs, _ = newRunFlags(flag.ContinueOnError)
	_ = fs.Parse([]string{"--voice", "--no-overlay"})
	if err := noStrayArgs(fs); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRefusesStrayArgs(t *testing.T) {
	var out strings.Builder
	if err := installCmd(t.Context(), []string{"--dry-run", "--", "--no-start"}, &out); err == nil {
		t.Fatal("accepted a stray argument")
	}
}
