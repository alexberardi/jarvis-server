package computer

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// What a recording reads besides the input, on this desktop, read-only (no input, no capture):
// the open windows and the front window's tree, as the start event and the first result read
// them. It logs counts and timings, never a title or a name.
func TestCaptureReadLive(t *testing.T) {
	if os.Getenv("JARVIS_RECORD_CAPTURE_LIVE") != "1" {
		t.Skip("JARVIS_RECORD_CAPTURE_LIVE=1 reads this desktop's window list and the front window's accessibility tree")
	}
	src, ok := NewDesktopSource(t.TempDir(), nil, nil).(*DesktopSource)
	if !ok {
		t.Fatal("no desktop source here")
	}
	var raw [][]byte
	d := &deskRecorder{id: "rec_live", p: src.Platform, now: time.Now, log: slogDiscard(),
		emit: func(b json.RawMessage) { raw = append(raw, b) }}
	began := time.Now()
	d.begin()
	if len(raw) != 1 {
		t.Fatalf("no start event (%d events)", len(raw))
	}
	var e DesktopEvent
	if err := json.Unmarshal(raw[0], &e); err != nil {
		t.Fatal(err)
	}
	t.Logf("start: %d windows, %d bytes; front window tree: %d named elements; %v", len(e.Windows), len(raw[0]), len(d.seen), time.Since(began).Round(time.Millisecond))
	if len(e.Windows) == 0 || !e.Windows[0].Focused {
		t.Fatalf("start names no front window")
	}
	d.owed = 1
	again := time.Now()
	d.result()
	if len(raw) != 2 {
		t.Fatal("no result")
	}
	t.Logf("result: %d bytes; %v", len(raw[1]), time.Since(again).Round(time.Millisecond))
}
