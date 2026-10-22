package device

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// fakeMic reads a constant sample, or fails once readErr is set.
type fakeMic struct {
	name    string
	sample  int16
	readErr error
	reads   int
	closed  bool
	xruns   int
}

func (f *fakeMic) Read(buf []int16) error {
	if f.readErr != nil {
		return f.readErr
	}
	f.reads++
	for i := range buf {
		buf[i] = f.sample
	}
	return nil
}
func (f *fakeMic) Overruns() int { return f.xruns }
func (f *fakeMic) Close() error  { f.closed = true; return nil }

type fallback struct{ name, reason string }

// switchOver is a Switch between "mic" (sample 100) and "ec" (sample 200), recording opens and
// fallbacks; failOpen names devices whose open fails.
func switchOver(t *testing.T, failOpen ...string) (*Switch, map[string][]*fakeMic, *[]fallback) {
	t.Helper()
	opened := map[string][]*fakeMic{}
	var falls []fallback
	s := NewSwitch(16000, 1)
	s.Open = func(name string, rate, ch int) (Capture, error) {
		if rate != 16000 || ch != 1 {
			t.Fatalf("open %s at %d/%d", name, rate, ch)
		}
		if slices.Contains(failOpen, name) {
			return nil, errors.New("no such PCM")
		}
		m := &fakeMic{name: name, sample: map[string]int16{"mic": 100, "ec": 200}[name]}
		opened[name] = append(opened[name], m)
		return m, nil
	}
	s.OnFallback = func(name, reason string, _ error) { falls = append(falls, fallback{name, reason}) }
	return s, opened, &falls
}

func start(t *testing.T, s *Switch) {
	t.Helper()
	if err := s.Start("mic"); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, s *Switch) int16 {
	t.Helper()
	buf := make([]int16, 1280)
	if err := s.Read(buf); err != nil {
		t.Fatal(err)
	}
	return buf[0]
}

func TestSwitchMovesBetweenReadsOnly(t *testing.T) {
	s, opened, falls := switchOver(t)
	start(t, s)
	if got := read(t, s); got != 100 {
		t.Fatalf("base read %d", got)
	}
	s.Use("ec")
	if s.Current() != "ec" || len(opened["ec"]) != 0 {
		t.Fatalf("Use opened at once (current %q, opens %d): it must wait for the next Read", s.Current(), len(opened["ec"]))
	}
	if got := read(t, s); got != 200 {
		t.Fatalf("after Use(ec) read %d", got)
	}
	if !opened["mic"][0].closed {
		t.Fatal("the base device stayed open alongside the other")
	}
	s.Use("")
	if got := read(t, s); got != 100 || !opened["ec"][0].closed || len(opened["mic"]) != 2 {
		t.Fatalf("back to base: read %d, ec closed %v, mic opens %d", got, opened["ec"][0].closed, len(opened["mic"]))
	}
	if len(*falls) != 0 {
		t.Fatalf("fallbacks %v", *falls)
	}
}

func TestSwitchFallsBackWhenTheOtherDeviceWontOpen(t *testing.T) {
	s, _, falls := switchOver(t, "ec")
	start(t, s)
	s.Use("ec")
	if got := read(t, s); got != 100 {
		t.Fatalf("read %d, want the base mic's", got)
	}
	if s.Current() != "mic" || !slices.Equal(*falls, []fallback{{"ec", "open"}}) {
		t.Fatalf("current %q, fallbacks %v", s.Current(), *falls)
	}
}

func TestSwitchFallsBackOnAReadErrorWithoutSurfacingIt(t *testing.T) {
	s, opened, falls := switchOver(t)
	start(t, s)
	s.Use("ec")
	read(t, s)
	opened["ec"][0].readErr = errors.New("pulse went away")
	if got := read(t, s); got != 100 {
		t.Fatalf("read %d, want the base mic's", got)
	}
	if !slices.Equal(*falls, []fallback{{"ec", "read"}}) || !opened["ec"][0].closed {
		t.Fatalf("fallbacks %v, ec closed %v", *falls, opened["ec"][0].closed)
	}
}

func TestSwitchBaseErrorsAreTheCallers(t *testing.T) {
	s, opened, _ := switchOver(t)
	start(t, s)
	read(t, s)
	opened["mic"][0].readErr = errors.New("unplugged")
	if err := s.Read(make([]int16, 4)); err == nil {
		t.Fatal("a base read error was swallowed")
	}
	s2, _, _ := switchOver(t, "mic")
	if err := s2.Start("mic"); err == nil {
		t.Fatal("a base that won't open primed")
	}
}

func TestSwitchSilentWatchdog(t *testing.T) {
	s, opened, falls := switchOver(t)
	start(t, s)
	s.SilentFor = 400 * time.Millisecond // 5 reads of 1280 samples at 16 kHz is 400 ms
	s.Use("ec")
	read(t, s)
	opened["ec"][0].sample = 1 // within the floor
	for range 4 {
		read(t, s)
	}
	if len(*falls) != 0 {
		t.Fatalf("fell back after 320 ms: %v", *falls)
	}
	opened["ec"][0].sample = -40 // a real sound resets the count
	read(t, s)
	opened["ec"][0].sample = 0
	for range 4 {
		read(t, s)
	}
	if len(*falls) != 0 {
		t.Fatalf("the count survived a sound: %v", *falls)
	}
	read(t, s)
	if !slices.Equal(*falls, []fallback{{"ec", "silent"}}) || s.Current() != "mic" {
		t.Fatalf("fallbacks %v, current %q", *falls, s.Current())
	}
	if got := read(t, s); got != 100 {
		t.Fatalf("after the watchdog read %d", got)
	}
	// The base itself is never watched: a quiet room is not a dead mic.
	opened["mic"][1].sample = 0 // reopened by the fallback
	for range 20 {
		read(t, s)
	}
	if len(*falls) != 1 {
		t.Fatalf("the base was watched: %v", *falls)
	}
}

func TestSwitchCloseReopensAndKeepsOverruns(t *testing.T) {
	s, opened, _ := switchOver(t)
	start(t, s)
	read(t, s)
	opened["mic"][0].xruns = 2
	if err := s.Close(); err != nil || !opened["mic"][0].closed {
		t.Fatal("close")
	}
	read(t, s)
	opened["mic"][1].xruns = 1
	if s.Overruns() != 3 || len(opened["mic"]) != 2 {
		t.Fatalf("overruns %d, opens %d", s.Overruns(), len(opened["mic"]))
	}
}
