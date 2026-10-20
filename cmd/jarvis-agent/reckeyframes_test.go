package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
)

// fakeKeyframeSource is a desktop source whose keyframes the test writes: seq -> with a crop.
type fakeKeyframeSource struct {
	fakeRecSource
	dir    string
	frames map[int]bool
	id     string
}

func (s *fakeKeyframeSource) Start(ctx context.Context, id string, emit func(json.RawMessage)) error {
	s.id = id
	return s.fakeRecSource.Start(ctx, id, emit)
}

func (s *fakeKeyframeSource) Keyframes(id string) []computer.Keyframe {
	var out []computer.Keyframe
	for seq, crop := range s.frames {
		base := filepath.Join(s.dir, id+"."+strconv.Itoa(seq))
		_ = os.WriteFile(base+".jpg", []byte("frame "+strconv.Itoa(seq)), 0o600)
		k := computer.Keyframe{Seq: seq, Path: base + ".jpg"}
		if crop {
			_ = os.WriteFile(base+".crop.jpg", []byte("crop "+strconv.Itoa(seq)), 0o600)
			k.Crop = base + ".crop.jpg"
		}
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b computer.Keyframe) int { return a.Seq - b.Seq })
	return out
}

// newKeyframeEnv is newUploadEnv recording the desktop: clicks at 1 (with a crop) and 3, a key
// at 2, each with a keyframe.
func newKeyframeEnv(t *testing.T) (*uploadEnv, *fakeKeyframeSource) {
	e := newUploadEnv(t)
	src := &fakeKeyframeSource{dir: e.rec.Dir, frames: map[int]bool{1: true, 2: false, 3: false}}
	e.rec.Sources[computer.ModeDesktop] = src
	e.up.delay = time.Millisecond
	e.rec.KeyframesLater = e.up.later
	return e, src
}

func (e *uploadEnv) recordDesktop(t *testing.T, src *fakeKeyframeSource) string {
	t.Helper()
	id, err := e.rec.Start(context.Background(), computer.ModeDesktop)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{`{"seq":1,"kind":"click"}`, `{"seq":2,"kind":"key"}`, `{"seq":3,"kind":"click"}`} {
		src.emit(json.RawMessage(ev))
	}
	return id
}

func kfPaths(id string, parts ...string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, "/api/v0/computer/recordings/"+id+"/keyframes/"+p)
	}
	return out
}

// A recording stopped here goes up as the trace (without its sampled keyframes), then each
// click's keyframe and crop, once.
func TestClickKeyframesSentAfterTheTrace(t *testing.T) {
	e, src := newKeyframeEnv(t)
	e.connect(e.cc)
	id := e.recordDesktop(t, src)
	if _, err := e.rec.End(); err != nil {
		t.Fatal(err)
	}
	want := kfPaths(id, "1", "1/crop", "3")
	waitFor(t, "keyframes not sent", func() bool { return len(e.jarvisd.keyframes()) == len(want) })
	waitFor(t, "still to send", func() bool { return len(e.rec.KeyframesPending()) == 0 })
	if got := e.jarvisd.keyframes(); !slices.Equal(got, want) {
		t.Fatalf("keyframes %v, want %v", got, want)
	}
	if e.jarvisd.uploads()[id] != 1 || e.jarvisd.attachments() {
		t.Fatalf("trace uploads %v, with image_attachments %v", e.jarvisd.uploads(), e.jarvisd.attachments())
	}
	e.up.sweep(e.cc)
	if n := len(e.jarvisd.keyframes()); n != len(want) {
		t.Fatalf("sent again: %d", n)
	}
	// They stay on this computer too, until the recording is deleted.
	if _, err := os.Stat(filepath.Join(e.rec.Dir, id+".1.crop.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := e.rec.Delete(id); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(e.rec.Dir, id+".*")); len(left) != 0 {
		t.Fatalf("left after delete: %v", left)
	}
}

// jarvisd full (413) ends that recording's keyframes, the trace staying sent; a keyframe that
// fails (503) is tried again later, from the start.
func TestKeyframeRefusalsAndRetries(t *testing.T) {
	e, src := newKeyframeEnv(t)
	e.connect(e.cc)
	full := e.recordDesktop(t, src)
	e.jarvisd.setPut(kfPaths(full, "1")[0], http.StatusRequestEntityTooLarge)
	if _, err := e.rec.End(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a full recording still to send", func() bool { return len(e.rec.KeyframesPending()) == 0 })
	if len(e.jarvisd.keyframes()) != 0 || e.jarvisd.uploads()[full] != 1 || len(e.rec.Pending()) != 0 {
		t.Fatalf("keyframes %v, trace uploads %v, pending %v", e.jarvisd.keyframes(), e.jarvisd.uploads(), e.rec.Pending())
	}

	down := e.recordDesktop(t, src)
	e.jarvisd.setPut(kfPaths(down, "3")[0], http.StatusServiceUnavailable)
	if _, err := e.rec.End(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first keyframes not sent", func() bool { return len(e.jarvisd.keyframes()) == 2 })
	if p := e.rec.KeyframesPending(); len(p) != 1 || p[0].ID != down {
		t.Fatalf("pending after a 503: %+v", p)
	}
	e.jarvisd.setPut(kfPaths(down, "3")[0], 0)
	e.up.sweep(e.cc)
	if got, want := e.jarvisd.keyframes(), append(kfPaths(down, "1", "1/crop"), kfPaths(down, "1", "1/crop", "3")...); !slices.Equal(got, want) {
		t.Fatalf("keyframes %v, want %v", got, want)
	}
	if len(e.rec.KeyframesPending()) != 0 {
		t.Fatal("still pending")
	}
}

// computer_record_stop (jarvisd stopped it: it has the trace) sends only the keyframes, a
// moment later; a recording jarvisd doesn't have yet (404) is tried again, and given up a day
// on.
func TestKeyframesOfARecordingJarvisdStopped(t *testing.T) {
	e, src := newKeyframeEnv(t)
	e.connect(e.cc)
	id := e.recordDesktop(t, src)
	if _, err := e.rec.Stop(id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "keyframes not sent", func() bool { return len(e.jarvisd.keyframes()) == 3 })
	waitFor(t, "still to send", func() bool { return len(e.rec.KeyframesPending()) == 0 })
	if len(e.jarvisd.uploads()) != 0 {
		t.Fatalf("the trace went up again: %v", e.jarvisd.uploads())
	}

	gone := e.recordDesktop(t, src)
	e.jarvisd.setPut(kfPaths(gone, "1")[0], http.StatusNotFound)
	if _, err := e.rec.Stop(gone); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if p := e.rec.KeyframesPending(); len(p) != 1 {
		t.Fatalf("a 404 dropped the keyframes: %+v", p)
	}
	old := time.Now().Add(-keyframesGiveUp - time.Minute)
	if err := os.Chtimes(filepath.Join(e.rec.Dir, gone+".keyframes"), old, old); err != nil {
		t.Fatal(err)
	}
	e.up.sweep(e.cc)
	if p := e.rec.KeyframesPending(); len(p) != 0 {
		t.Fatalf("not given up: %+v", p)
	}
}
