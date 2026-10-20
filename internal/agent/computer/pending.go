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
// or whose upload failed, is sent later (Pending, Sent). Its click keyframes go up after the
// trace (#344), however it was stopped: an empty <id>.keyframes says they are still to send
// (KeyframesPending, KeyframesSent).

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

// keyframesPath marks recording id's click keyframes as still to send.
func (r *Recorder) keyframesPath(id string) string { return filepath.Join(r.Dir, id+".keyframes") }

// markKeyframes marks tr's click keyframes to be sent, when it has any; it reports whether.
func (r *Recorder) markKeyframes(tr Trace) bool {
	if len(ClickKeyframes(tr)) == 0 {
		return false
	}
	if err := writePrivate(r.keyframesPath(tr.ID), nil); err != nil {
		r.log().Warn("recording: couldn't mark the keyframes to be sent", "recording", tr.ID, "err", err)
		return false
	}
	return true
}

// KeyframesPending is the recordings whose click keyframes aren't sent yet, oldest first.
func (r *Recorder) KeyframesPending() []Trace {
	markers, _ := filepath.Glob(filepath.Join(r.Dir, "rec_*.keyframes"))
	var out []Trace
	for _, m := range markers {
		id := strings.TrimSuffix(filepath.Base(m), ".keyframes")
		if !recordingIDPattern.MatchString(id) {
			continue
		}
		tr, err := r.LoadTrace(id)
		if errors.Is(err, os.ErrNotExist) {
			r.KeyframesSent(id)
			continue
		}
		if err != nil {
			r.log().Warn("recording: couldn't read a trace whose keyframes are to send", "recording", id, "err", err)
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

// KeyframesMarked is when recording id's keyframes were marked to send (zero: they aren't).
func (r *Recorder) KeyframesMarked(id string) time.Time {
	fi, err := os.Stat(r.keyframesPath(id))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// KeyframesSent clears recording id's keyframes marker (sent, or given up on).
func (r *Recorder) KeyframesSent(id string) {
	if err := os.Remove(r.keyframesPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.log().Warn("recording: couldn't clear the keyframes marker", "recording", id, "err", err)
	}
}

// ClickKeyframes is tr's keyframes taken at a click (the event of the same seq), by seq: those
// jarvisd keeps with the recording.
func ClickKeyframes(tr Trace) []Keyframe {
	if len(tr.Keyframes) == 0 {
		return nil
	}
	clicks := map[int]bool{}
	for _, raw := range tr.Events {
		var e struct {
			Seq  int    `json:"seq"`
			Kind string `json:"kind"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Kind == InClick {
			clicks[e.Seq] = true
		}
	}
	var out []Keyframe
	for _, k := range tr.Keyframes {
		if clicks[k.Seq] {
			out = append(out, k)
		}
	}
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
		if err != nil {
			continue // a crop (<id>.<seq>.crop.jpg)
		}
		k := Keyframe{Seq: seq, Path: p}
		if crop := strings.TrimSuffix(p, ".jpg") + cropSuffix; fileExists(crop) {
			k.Crop = crop
		}
		tr.Keyframes = append(tr.Keyframes, k)
	}
	slices.SortFunc(tr.Keyframes, func(a, b Keyframe) int { return a.Seq - b.Seq })
	return tr, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
