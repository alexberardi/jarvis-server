package computer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fakeSource is the browser extension's recorder: events sent while it runs reach emit.
type fakeSource struct {
	mu      sync.Mutex
	emit    func(json.RawMessage)
	started []string
	stopped []string
	failErr error
	// onStop sends the extension's last events, which arrive before its stop answers.
	onStop []string
}

func (s *fakeSource) Start(_ context.Context, id string, emit func(json.RawMessage)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		return s.failErr
	}
	s.started = append(s.started, id)
	s.emit = emit
	return nil
}

func (s *fakeSource) Stop(_ context.Context, id string) error {
	s.mu.Lock()
	emit, last := s.emit, s.onStop
	s.stopped = append(s.stopped, id)
	s.mu.Unlock()
	for _, e := range last {
		emit(json.RawMessage(e))
	}
	return nil
}

func (s *fakeSource) send(e string) {
	s.mu.Lock()
	emit := s.emit
	s.mu.Unlock()
	emit(json.RawMessage(e))
}

// fakeTimer is MaxRecording's timer, fired by hand.
type fakeTimer struct {
	mu      sync.Mutex
	d       time.Duration
	f       func()
	stopped bool
}

func (t *fakeTimer) after(d time.Duration, f func()) func() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.d, t.f = d, f
	return func() bool { t.mu.Lock(); defer t.mu.Unlock(); t.stopped = true; return true }
}

func (t *fakeTimer) fire() {
	t.mu.Lock()
	f := t.f
	t.mu.Unlock()
	f()
}

type recEnv struct {
	r        *Recorder
	src      *fakeSource
	timer    *fakeTimer
	activity *Activity
	sw       *Switch
	mu       sync.Mutex
	handed   []Trace
}

func newRecEnv(t *testing.T) *recEnv {
	t.Helper()
	e := &recEnv{src: &fakeSource{}, timer: &fakeTimer{}, activity: &Activity{}}
	e.sw = NewSwitch(true, e.activity)
	clock := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	e.r = &Recorder{Dir: filepath.Join(t.TempDir(), "jarvis-agent", "recordings"), Switch: e.sw, Activity: e.activity,
		Sources: map[string]Source{ModeBrowser: e.src},
		Stopped: func(tr Trace) { e.mu.Lock(); e.handed = append(e.handed, tr); e.mu.Unlock() },
		now:     func() time.Time { clock = clock.Add(time.Second); return clock }, after: e.timer.after}
	return e
}

func (e *recEnv) handedOn() []Trace {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Trace(nil), e.handed...)
}

func TestTraceFilePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes on Windows")
	}
	e := newRecEnv(t)
	// A directory left with a wider mode is narrowed.
	if err := os.MkdirAll(e.r.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.r.Dir, 0o755); err != nil { // whatever the umask made of it
		t.Fatal(err)
	}
	id, err := e.r.Start(context.Background(), ModeBrowser)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "rec_") || len(id) != len("rec_")+16 {
		t.Fatalf("id %q, want rec_ and 16 hex digits", id)
	}
	e.src.send(`{"kind":"click","seq":1}`)
	if _, err := e.r.Stop(id); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(e.r.Dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v %v, want 0700", st.Mode().Perm(), err)
	}
	fi, err := os.Stat(e.r.Path(id))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("trace mode %v %v, want 0600", fi.Mode().Perm(), err)
	}
	if filepath.Base(e.r.Path(id)) != id+".jsonl" {
		t.Fatalf("path %s", e.r.Path(id))
	}
}

func TestStopReturnsTrace(t *testing.T) {
	e := newRecEnv(t)
	var shown []Kind
	e.activity.Show(func(k Kind, _ string) { shown = append(shown, k) })
	id, err := e.r.Start(context.Background(), ModeBrowser)
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := e.activity.Status(); k != Recording {
		t.Fatalf("activity %q while recording", k)
	}
	if _, err := e.r.Start(context.Background(), ModeBrowser); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second start: %v", err)
	}
	e.src.send(`{"kind":"navigate","seq":1,"url":"https://bank.example"}`)
	e.src.send(`not json`) // dropped
	e.src.send(`{"kind":"click","seq":2}`)
	e.src.onStop = []string{`{"kind":"download","seq":3}`} // the last one, just before stop answers
	if _, err := e.r.Stop("rec_other"); !errors.Is(err, ErrNotRecording) {
		t.Fatalf("stop of another id: %v", err)
	}
	tr, err := e.r.Stop(id)
	if err != nil {
		t.Fatal(err)
	}
	if tr.ID != id || tr.Mode != ModeBrowser || tr.Reason != StopAsked || len(tr.Events) != 3 || !tr.StoppedAt.After(tr.StartedAt) {
		t.Fatalf("trace %+v", tr)
	}
	if !strings.Contains(string(tr.Events[2]), "download") {
		t.Fatalf("the last event: %s", tr.Events[2])
	}
	if k, _ := e.activity.Status(); k != Idle || shown[len(shown)-1] != Idle {
		t.Fatalf("activity %q after stop (shown %v)", k, shown)
	}
	if len(e.src.stopped) != 1 || !e.timer.stopped {
		t.Fatalf("source stopped %v, timer stopped %v", e.src.stopped, e.timer.stopped)
	}
	if len(e.handedOn()) != 0 {
		t.Fatal("Stop's caller has the trace: nothing is handed on")
	}
	e.src.send(`{"kind":"click","seq":4}`) // after stop: ignored
	if _, err := e.r.Stop(id); !errors.Is(err, ErrNotRecording) {
		t.Fatalf("a second stop: %v", err)
	}
	// The file: a header, the events, a footer.
	f, _ := os.Open(e.r.Path(id))
	var lines []string
	for sc := bufio.NewScanner(f); sc.Scan(); {
		lines = append(lines, sc.Text())
	}
	f.Close() // before Delete: Windows won't remove an open file
	if len(lines) != 5 || !strings.Contains(lines[0], `"recording_id":"`+id) || !strings.Contains(lines[4], `"reason":"stopped"`) {
		t.Fatalf("file %q", lines)
	}

	// Delete removes the local copy; again is no error; a bad id is.
	if err := e.r.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.r.Path(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("still there: %v", err)
	}
	if err := e.r.Delete(id); err != nil {
		t.Fatalf("deleting a missing copy: %v", err)
	}
	if err := e.r.Delete("../node"); err == nil {
		t.Fatal("a path as an id")
	}
}

func TestMaxDuration(t *testing.T) {
	e := newRecEnv(t)
	id, err := e.r.Start(context.Background(), ModeBrowser)
	if err != nil {
		t.Fatal(err)
	}
	if e.timer.d != MaxRecording || MaxRecording != 30*time.Minute {
		t.Fatalf("timer %v", e.timer.d)
	}
	e.src.send(`{"kind":"click","seq":1}`)
	e.timer.fire()
	got := e.handedOn()
	if len(got) != 1 || got[0].ID != id || got[0].Reason != StopMaxDuration || len(got[0].Events) != 1 {
		t.Fatalf("handed on %+v", got)
	}
	if k, _ := e.activity.Status(); k != Idle || e.r.Current() != "" {
		t.Fatalf("still recording: %q %q", k, e.r.Current())
	}
}

func TestOutsideStopHandsTraceOn(t *testing.T) {
	e := newRecEnv(t)
	id, _ := e.r.Start(context.Background(), ModeBrowser)
	if !e.activity.Stop() { // the status icon's "Stop Jarvis"
		t.Fatal("nothing was going on")
	}
	if got := e.handedOn(); len(got) != 1 || got[0].ID != id || got[0].Reason != StopFromOutside {
		t.Fatalf("handed on %+v", got)
	}
	// End: the icon's "Stop recording".
	id, _ = e.r.Start(context.Background(), ModeBrowser)
	if tr, err := e.r.End(); err != nil || tr.ID != id {
		t.Fatalf("end %+v %v", tr, err)
	}
	if got := e.handedOn(); len(got) != 2 || got[1].ID != id || got[1].Reason != StopAsked {
		t.Fatalf("handed on %+v", got)
	}
	if _, err := e.r.End(); !errors.Is(err, ErrNotRecording) {
		t.Fatalf("end with nothing going on: %v", err)
	}
	// Turning computer control off stops a recording too.
	e.r.Start(context.Background(), ModeBrowser)
	_ = e.sw.Set(false)
	if got := e.handedOn(); len(got) != 3 || got[2].Reason != StopFromOutside {
		t.Fatalf("handed on %+v", got)
	}
}

func TestStartRefusals(t *testing.T) {
	e := newRecEnv(t)
	ctx := context.Background()
	if _, err := e.r.Start(ctx, ModeDesktop); !errors.Is(err, ErrDesktopRecording) {
		t.Fatalf("desktop: %v", err)
	}
	if _, err := e.r.Start(ctx, "voice"); err == nil {
		t.Fatal("an unknown mode")
	}
	e.activity.Set(Controlling, "Ordering the blue mug")
	if _, err := e.r.Start(ctx, ModeBrowser); !errors.Is(err, ErrBusy) {
		t.Fatalf("while controlling: %v", err)
	}
	e.activity.Set(Idle, "")
	e.src.failErr = errors.New("no browser extension connected")
	if _, err := e.r.Start(ctx, ModeBrowser); err == nil || !strings.Contains(err.Error(), "extension") {
		t.Fatalf("source failed: %v", err)
	}
	if left, _ := os.ReadDir(e.r.Dir); len(left) != 0 || e.r.Current() != "" {
		t.Fatalf("a failed start left %v / %q", left, e.r.Current())
	}
	if k, _ := e.activity.Status(); k != Idle {
		t.Fatalf("activity %q", k)
	}
	e.src.failErr = nil
	_ = e.sw.Set(false)
	if _, err := e.r.Start(ctx, ModeBrowser); !errors.Is(err, ErrOff) {
		t.Fatalf("switch off: %v", err)
	}
}

func TestTraceCapped(t *testing.T) {
	e := newRecEnv(t)
	id, _ := e.r.Start(context.Background(), ModeBrowser)
	big := `{"kind":"input","value":"` + strings.Repeat("x", 1000) + `"}`
	for range MaxTraceBytes/len(big) + 10 {
		e.src.send(big)
	}
	tr, _ := e.r.Stop(id)
	if !tr.Truncated || len(tr.Events) != MaxTraceBytes/len(big) {
		t.Fatalf("truncated %v, %d events", tr.Truncated, len(tr.Events))
	}
}

func TestSampleKeyframes(t *testing.T) {
	var k []Keyframe
	for i := range 10 {
		k = append(k, Keyframe{Seq: i})
	}
	seqs := func(k []Keyframe) (out []int) {
		for _, f := range k {
			out = append(out, f.Seq)
		}
		return out
	}
	if got := seqs(SampleKeyframes(k, 4)); len(got) != 4 || got[0] != 0 || got[3] != 9 {
		t.Fatalf("sampled %v", got)
	}
	if got := SampleKeyframes(k[:3], 4); len(got) != 3 {
		t.Fatalf("fewer than n: %v", got)
	}
}

func TestRecordTools(t *testing.T) {
	e := newRecEnv(t)
	tools := &RecordTools{R: e.r}
	ctx := context.Background()
	if n := len(tools.Available(ctx)); n != 3 {
		t.Fatalf("%d tools", n)
	}
	out, _ := tools.Call(ctx, "computer_record_start", map[string]any{}, node.Request{})
	id, _ := out["recording_id"].(string)
	if out["success"] != true || out["mode"] != ModeBrowser || id == "" {
		t.Fatalf("start %v", out)
	}
	e.src.send(`{"kind":"click","seq":1}`)
	out, _ = tools.Call(ctx, "computer_record_stop", map[string]any{"recording_id": id}, node.Request{})
	b, _ := json.Marshal(out)
	if out["success"] != true || out["events"] != 1 || !strings.Contains(string(b), `"trace":[{"kind":"click","seq":1}]`) || out["image_attachments"] != nil {
		t.Fatalf("stop %s", b)
	}
	out, _ = tools.Call(ctx, "computer_record_stop", map[string]any{}, node.Request{})
	if out["success"] != false || out["error_code"] != "not_recording" {
		t.Fatalf("stop again %v", out)
	}
	out, _ = tools.Call(ctx, "computer_record_delete", map[string]any{"recording_id": id}, node.Request{})
	if out["success"] != true {
		t.Fatalf("delete %v", out)
	}
	_ = e.sw.Set(false)
	out, _ = tools.Call(ctx, "computer_record_start", map[string]any{"mode": "browser"}, node.Request{})
	if out["error_code"] != "computer_control_off" {
		t.Fatalf("off %v", out)
	}
	if _, ok := tools.Call(ctx, "computer_act", nil, node.Request{}); ok {
		t.Fatal("handled another tool")
	}
}

func TestTraceOutputKeyframes(t *testing.T) {
	dir := t.TempDir()
	var k []Keyframe
	for i := range 6 {
		p := filepath.Join(dir, strconv.Itoa(i)+".jpg")
		_ = os.WriteFile(p, []byte{0xff, 0xd8, byte(i)}, 0o600)
		k = append(k, Keyframe{Seq: i, Path: p})
	}
	out := TraceOutput(Trace{ID: "rec_1", Mode: ModeDesktop, Keyframes: k})
	if atts, _ := out["image_attachments"].([]any); len(atts) != MaxKeyframeAttachments || out["keyframes"] != 6 {
		t.Fatalf("attachments %d, keyframes %v", len(atts), out["keyframes"])
	}
}
