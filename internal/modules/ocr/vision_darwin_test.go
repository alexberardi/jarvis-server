//go:build darwin

package ocr

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestNativeAppleVision reads a rendered line with the real Vision framework (macOS only).
func TestNativeAppleVision(t *testing.T) {
	e, err := newNativeAppleVision()
	if err != nil {
		t.Fatalf("Vision did not load: %v", err)
	}
	ctx := context.Background()
	if !e.Available(ctx) {
		t.Fatal("unavailable")
	}
	if d := diagnose(ctx, e); !d.Available || d.Reason != "ok" {
		t.Fatalf("diagnose: %+v", d)
	}
	img := testPNG("HELLO JARVIS")
	w, h, _ := imageSize(img)
	for _, hints := range [][]string{nil, {"en"}, {"zz-not-a-language"}} {
		r, err := e.Recognize(ctx, Image{Data: img, ContentType: "image/png"}, Options{LanguageHints: hints, ReturnBoxes: true})
		if err != nil {
			t.Fatalf("hints %v: %v", hints, err)
		}
		if !strings.Contains(strings.ToUpper(r.Text), "HELLO") || len(r.Blocks) == 0 {
			t.Fatalf("hints %v: %+v", hints, r)
		}
		b := r.Blocks[0]
		if b.Confidence <= 0 || b.Confidence > 1 || len(b.BBox) != 4 || b.BBox[0] < 0 || b.BBox[1] < 0 ||
			b.BBox[0]+b.BBox[2] > float64(w)+1 || b.BBox[1]+b.BBox[3] > float64(h)+1 {
			t.Fatalf("hints %v: block %+v in %dx%d", hints, b, w, h)
		}
	}

	// Garbage and empty input are errors, not crashes.
	if _, err := e.Recognize(ctx, Image{Data: []byte("not an image")}, Options{}); err == nil {
		t.Fatal("garbage should fail")
	}
	if _, err := e.Recognize(ctx, Image{}, Options{}); err == nil {
		t.Fatal("empty should fail")
	}
	// A context that is already done is not run.
	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.Recognize(done, Image{Data: img}, Options{}); err == nil {
		t.Fatal("cancelled context should fail")
	}

	// Many in a row and concurrently: no leak-driven crash, every call answers.
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			cctx, cc := context.WithTimeout(ctx, time.Minute)
			defer cc()
			_, err := e.Recognize(cctx, Image{Data: img}, Options{ReturnBoxes: true})
			errs <- err
		}()
	}
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
