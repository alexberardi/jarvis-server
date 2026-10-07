package main

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/update"
)

// A10 F24: `jarvisd upgrade` printed "downloading" and then nothing until the download was
// done; it now shows the download moving, with or without a known size.
func TestProgressPrinter(t *testing.T) {
	var b strings.Builder
	p := progressPrinter(&b)
	p(update.Progress{Step: update.StepChecking})
	p(update.Progress{Step: update.StepDownloading, Total: 1000})
	for done := int64(0); done <= 1000; done += 25 {
		p(update.Progress{Step: update.StepDownloading, Done: done, Total: 1000})
	}
	p(update.Progress{Step: update.StepVerifying})
	want := "checking\ndownloading 0%\ndownloading 10%\ndownloading 20%\ndownloading 30%\ndownloading 40%\ndownloading 50%\n" +
		"downloading 60%\ndownloading 70%\ndownloading 80%\ndownloading 90%\ndownloading 100%\nverifying\n"
	if b.String() != want {
		t.Fatalf("known size:\n%s", b.String())
	}

	// No size (a server without Content-Length): megabytes, every 10.
	b.Reset()
	p = progressPrinter(&b)
	p(update.Progress{Step: update.StepDownloading})
	for done := int64(0); done <= 25<<20; done += 1 << 20 {
		p(update.Progress{Step: update.StepDownloading, Done: done})
	}
	if got := b.String(); got != "downloading\ndownloading 10 MB\ndownloading 20 MB\n" {
		t.Fatalf("unknown size:\n%s", got)
	}
}
