package computer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A trace handed on to Stopped (to be uploaded to jarvisd) is pending until it is sent: an
// empty <id>.pending (0600) next to it says so, so one stopped while the agent isn't connected,
// or whose upload failed, is sent later (Pending, Sent).

func (r *Recorder) pendingPath(id string) string { return filepath.Join(r.Dir, id+".pending") }

// handOn marks tr pending and hands it to Stopped.
func (r *Recorder) handOn(tr Trace) {
	if r.Stopped == nil {
		return
	}
	if err := writePrivate(r.pendingPath(tr.ID), nil); err != nil {
		r.log().Warn("recording: couldn't mark the trace to be sent", "recording", tr.ID, "err", err)
	}
	r.Stopped(tr)
}

// Pending is the traces handed on and not sent yet, read back from their files, oldest first.
// A marker whose trace is gone is dropped.
func (r *Recorder) Pending() []Trace {
	markers, _ := filepath.Glob(filepath.Join(r.Dir, "rec_*.pending"))
	var out []Trace
	for _, m := range markers {
		id := strings.TrimSuffix(filepath.Base(m), ".pending")
		if !recordingIDPattern.MatchString(id) {
			continue
		}
		tr, err := r.LoadTrace(id)
		if errors.Is(err, os.ErrNotExist) {
			r.Sent(id)
			continue
		}
		if err != nil {
			r.log().Warn("recording: couldn't read a trace to send", "recording", id, "err", err)
			continue
		}
		out = append(out, tr)
	}
	slices.SortStableFunc(out, func(a, b Trace) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Sent clears recording id's pending marker.
func (r *Recorder) Sent(id string) {
	if err := os.Remove(r.pendingPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.log().Warn("recording: couldn't clear the to-send marker", "recording", id, "err", err)
	}
}

// LoadTrace reads a stopped recording back from its file: the head line, the events, the tail
// line (stop writes it), and the keyframes next to it.
func (r *Recorder) LoadTrace(id string) (Trace, error) {
	if !recordingIDPattern.MatchString(id) {
		return Trace{}, fmt.Errorf("bad recording id %q", id)
	}
	b, err := os.ReadFile(r.Path(id))
	if err != nil {
		return Trace{}, err
	}
	lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	if len(lines) < 2 {
		return Trace{}, fmt.Errorf("recording %s wasn't finished", id)
	}
	tr := Trace{Events: []json.RawMessage{}}
	if err := json.Unmarshal(lines[0], &tr); err != nil {
		return Trace{}, fmt.Errorf("recording %s: %w", id, err)
	}
	var tail struct {
		StoppedAt time.Time `json:"stopped_at"`
		Reason    string    `json:"reason"`
		Truncated bool      `json:"truncated"`
	}
	if err := json.Unmarshal(lines[len(lines)-1], &tail); err != nil || tail.StoppedAt.IsZero() {
		return Trace{}, fmt.Errorf("recording %s wasn't finished", id)
	}
	tr.StoppedAt, tr.Reason, tr.Truncated = tail.StoppedAt, tail.Reason, tail.Truncated
	for _, l := range lines[1 : len(lines)-1] {
		tr.Events = append(tr.Events, json.RawMessage(l))
	}
	if tr.ID != id {
		return Trace{}, fmt.Errorf("recording %s: the file says %q", id, tr.ID)
	}
	kf, _ := filepath.Glob(filepath.Join(r.Dir, id+".*.jpg"))
	for _, p := range kf {
		seq, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), id+"."), ".jpg"))
		if err == nil {
			tr.Keyframes = append(tr.Keyframes, Keyframe{Seq: seq, Path: p})
		}
	}
	slices.SortFunc(tr.Keyframes, func(a, b Keyframe) int { return a.Seq - b.Seq })
	return tr, nil
}
