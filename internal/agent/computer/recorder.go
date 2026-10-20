package computer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// Recording (plan P3 piece 29): "Jarvis, watch me", the status icon's "Record a macro…" or
// `jarvis-agent computer record start` records what the user does, so a workflow can be learnt
// from it (jarvisd, piece 30). The trace is kept on this computer in
// <agent dir>/recordings/<id>.jsonl (the file 0600, the directory 0700) until jarvisd's
// retention sweep asks for it to go (computer_record_delete). While it runs, the island and
// the status icon say "Recording", and any stop (the icon's "Stop Jarvis", the CLI, voice
// "stop", computer control turned off) ends it.

// Recording modes.
const (
	ModeBrowser = "browser" // the browser extension's structured events (piece 28)
	ModeDesktop = "desktop" // the real desktop, with keyframes (pieces 36, 37)
)

const (
	// MaxRecording is the longest a recording runs: then it stops by itself.
	MaxRecording = 30 * time.Minute
	// MaxTraceBytes caps a trace's events (it must fit in one upload); later events are
	// dropped and the trace says truncated.
	MaxTraceBytes = 768 << 10

	sourceStopTimeout = 10 * time.Second
)

// Why a recording stopped (Trace.Reason).
const (
	StopAsked       = "stopped"      // Stop: the user said they're done
	StopFromOutside = "stopped_here" // the status icon, the CLI, voice "stop", the switch
	StopMaxDuration = "max_duration" // MaxRecording passed
)

var (
	// ErrNotRecording: no recording with that id is going on.
	ErrNotRecording = errors.New("no recording is going on")
	// ErrDesktopRecording: this computer has no desktop recording (macOS has it, piece 36);
	// the browser works.
	ErrDesktopRecording = errors.New("I can't record the desktop on this computer yet: I can watch what you do in the browser")
	// ErrBusy: Jarvis is already recording or controlling this computer.
	ErrBusy = errors.New("Jarvis is already using this computer")
)

var recordingIDPattern = regexp.MustCompile(`^rec_[A-Za-z0-9_-]{1,60}$`)

// Source records one mode. Start sends each of the user's actions to emit (one JSON object)
// until Stop; once Stop returns, emit is not called for id again.
type Source interface {
	Start(ctx context.Context, id string, emit func(json.RawMessage)) error
	Stop(ctx context.Context, id string) error
}

// KeyframeSource is a Source that takes keyframes (the desktop): once Stop returns, Keyframes
// hands over recording id's.
type KeyframeSource interface {
	Keyframes(id string) []Keyframe
}

// Keyframe is a desktop recording's picture of the screen at one event, kept on this computer
// (<id>.<seq>.jpg next to the trace, 0600).
type Keyframe struct {
	Seq  int    `json:"seq"`
	Path string `json:"-"`
}

// Trace is a stopped recording.
type Trace struct {
	ID        string            `json:"recording_id"`
	Mode      string            `json:"mode"`
	StartedAt time.Time         `json:"started_at"`
	StoppedAt time.Time         `json:"stopped_at"`
	Reason    string            `json:"reason"`
	Events    []json.RawMessage `json:"events"`
	Keyframes []Keyframe        `json:"-"`
	Truncated bool              `json:"truncated,omitempty"`
}

// Recorder runs one recording at a time.
type Recorder struct {
	// Dir holds the traces (<agent dir>/recordings).
	Dir string
	// Activity shows the recording and stops it. Computer control's switch doesn't gate
	// recording (COMPUTER-CONTROL.md C11): each one is an explicit act, shown while it runs.
	Activity *Activity
	// Sources by mode; a mode without one can't be recorded.
	Sources map[string]Source
	// Stopped gets a trace the user stopped some other way than Stop (End, Activity's stop,
	// MaxRecording): the agent uploads it. It must not block for long. The trace is pending
	// (Pending) until the agent says it was sent (Sent).
	Stopped func(Trace)
	Log     *slog.Logger

	// now and after are the clock (tests replace them).
	now   func() time.Time
	after func(d time.Duration, f func()) (stop func() bool)

	mu  sync.Mutex
	cur *recording
}

type recording struct {
	trace      Trace
	src        Source
	f          *os.File
	size       int
	stopping   bool
	stopTimer  func() bool
	removeStop func()
}

func (r *Recorder) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Recorder) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// Path is where recording id's trace is kept.
func (r *Recorder) Path(id string) string { return filepath.Join(r.Dir, id+".jsonl") }

// Start starts recording in mode and returns the recording's id.
func (r *Recorder) Start(ctx context.Context, mode string) (string, error) {
	if mode != ModeBrowser && mode != ModeDesktop {
		return "", fmt.Errorf("unknown recording mode %q: want browser or desktop", mode)
	}
	src := r.Sources[mode]
	switch {
	case src == nil && mode == ModeDesktop:
		return "", ErrDesktopRecording
	case src == nil:
		return "", fmt.Errorf("can't record the %s on this computer", mode)
	}
	if k, _ := r.Activity.Status(); k != Idle {
		return "", ErrBusy
	}
	r.mu.Lock()
	if r.cur != nil {
		r.mu.Unlock()
		return "", ErrBusy
	}
	id := newRecordingID()
	f, err := r.create(id)
	if err != nil {
		r.mu.Unlock()
		return "", err
	}
	rec := &recording{trace: Trace{ID: id, Mode: mode, StartedAt: r.clock().UTC(), Events: []json.RawMessage{}}, src: src, f: f}
	head, _ := json.Marshal(map[string]any{"recording_id": id, "mode": mode, "started_at": rec.trace.StartedAt})
	_, err = f.Write(append(head, '\n'))
	if err != nil {
		r.mu.Unlock()
		r.discard(f, id)
		return "", err
	}
	r.cur = rec // before the source starts: its first events may come at once
	r.mu.Unlock()

	if err := src.Start(ctx, id, func(e json.RawMessage) { r.emit(id, e) }); err != nil {
		r.mu.Lock()
		r.cur = nil
		r.mu.Unlock()
		r.discard(f, id)
		return "", err
	}
	r.Activity.Set(Recording, "Watching what you do")
	stop := func(reason string) {
		if tr, err := r.stop(id, reason); err == nil {
			r.handOn(tr)
		}
	}
	r.mu.Lock()
	if r.cur != rec || rec.stopping { // stopped already (Stop raced the source's start)
		r.mu.Unlock()
		if k, _ := r.Activity.Status(); k == Recording {
			r.Activity.Set(Idle, "")
		}
		return id, nil
	}
	rec.removeStop = r.Activity.OnStop(func() { stop(StopFromOutside) })
	rec.stopTimer = r.afterFunc(MaxRecording, func() { stop(StopMaxDuration) })
	r.mu.Unlock()
	r.log().Info("recording started", "recording", id, "mode", mode)
	return id, nil
}

func (r *Recorder) afterFunc(d time.Duration, f func()) func() bool {
	if r.after != nil {
		return r.after(d, f)
	}
	return time.AfterFunc(d, f).Stop
}

// create makes the trace's file: 0600, in a 0700 directory.
func (r *Recorder) create(id string) (*os.File, error) {
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(r.Dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(r.Path(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func (r *Recorder) discard(f *os.File, id string) {
	_ = f.Close()
	_ = os.Remove(r.Path(id))
}

// emit keeps one event of recording id.
func (r *Recorder) emit(id string, e json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.cur
	if rec == nil || rec.trace.ID != id || !json.Valid(e) {
		return
	}
	if rec.size+len(e) > MaxTraceBytes {
		rec.trace.Truncated = true
		return
	}
	line := append(append([]byte(nil), e...), '\n')
	if _, err := rec.f.Write(line); err != nil {
		r.log().Warn("recording: couldn't save an event", "recording", id, "err", err)
	}
	rec.size += len(e)
	rec.trace.Events = append(rec.trace.Events, line[:len(line)-1:len(line)-1])
}

// Stop stops recording id ("" for the one going on) and returns its trace. The caller has
// the trace (the computer_record_stop tool), so Stopped isn't called.
func (r *Recorder) Stop(id string) (Trace, error) { return r.stop(id, StopAsked) }

// End stops the recording going on and hands its trace to Stopped (the status icon's "Stop
// recording", `jarvis-agent computer record stop`).
func (r *Recorder) End() (Trace, error) {
	tr, err := r.stop("", StopAsked)
	if err == nil {
		r.handOn(tr)
	}
	return tr, err
}

// Current is the recording going on ("" for none).
func (r *Recorder) Current() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		return ""
	}
	return r.cur.trace.ID
}

func (r *Recorder) stop(id, reason string) (Trace, error) {
	r.mu.Lock()
	rec := r.cur
	if rec == nil || rec.stopping || (id != "" && rec.trace.ID != id) {
		r.mu.Unlock()
		return Trace{}, ErrNotRecording
	}
	rec.stopping = true
	r.mu.Unlock()

	// The source's last events arrive before its stop returns, so they are still kept.
	ctx, cancel := context.WithTimeout(context.Background(), sourceStopTimeout)
	if err := rec.src.Stop(ctx, rec.trace.ID); err != nil {
		r.log().Warn("recording: the source didn't stop cleanly; keeping what it sent", "recording", rec.trace.ID, "err", err)
	}
	cancel()
	var keyframes []Keyframe
	if ks, ok := rec.src.(KeyframeSource); ok {
		keyframes = ks.Keyframes(rec.trace.ID)
	}

	r.mu.Lock()
	r.cur = nil
	tr := rec.trace
	tr.Keyframes = keyframes
	tr.StoppedAt, tr.Reason = r.clock().UTC(), reason
	stopTimer, removeStop := rec.stopTimer, rec.removeStop
	r.mu.Unlock()
	if stopTimer != nil {
		stopTimer()
	}
	if removeStop != nil {
		removeStop()
	}
	tail, _ := json.Marshal(map[string]any{"stopped_at": tr.StoppedAt, "reason": reason, "events": len(tr.Events), "truncated": tr.Truncated})
	_, werr := rec.f.Write(append(tail, '\n'))
	if err := errors.Join(werr, rec.f.Close()); err != nil {
		r.log().Warn("recording: couldn't finish the trace file", "recording", tr.ID, "err", err)
	}
	if k, _ := r.Activity.Status(); k == Recording {
		r.Activity.Set(Idle, "")
	}
	r.log().Info("recording stopped", "recording", tr.ID, "reason", reason, "events", len(tr.Events), "truncated", tr.Truncated)
	return tr, nil
}

// Delete removes recording id's local copy, its keyframes and its pending marker (stopping it
// first, without handing it on, if it is the one going on). A copy already gone is no error.
func (r *Recorder) Delete(id string) error {
	if !recordingIDPattern.MatchString(id) {
		return fmt.Errorf("bad recording id %q", id)
	}
	if r.Current() == id {
		_, _ = r.stop(id, StopAsked)
	}
	kf, _ := filepath.Glob(filepath.Join(r.Dir, id+".*.jpg"))
	for _, k := range kf {
		_ = os.Remove(k)
	}
	_ = os.Remove(r.pendingPath(id))
	err := os.Remove(r.Path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// SampleKeyframes picks at most n keyframes spread over the recording, first and last kept.
func SampleKeyframes(k []Keyframe, n int) []Keyframe {
	if len(k) <= n {
		return k
	}
	if n <= 0 {
		return nil
	}
	if n == 1 {
		return k[:1]
	}
	out := make([]Keyframe, n)
	for i := range n {
		out[i] = k[i*(len(k)-1)/(n-1)]
	}
	return out
}

// newRecordingID is "rec_" and 16 hex digits, as jarvisd's ids are.
func newRecordingID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "rec_" + hex.EncodeToString(b[:])
}
