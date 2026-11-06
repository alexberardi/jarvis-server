package phone

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// memBlobs is an in-memory blob.Store whose Delete fails for keys in fail.
type memBlobs struct {
	mu   sync.Mutex
	objs map[string][]byte
	fail map[string]bool
}

func (m *memBlobs) Put(_ context.Context, key string, r io.Reader, _ string) (int64, error) {
	b, _ := io.ReadAll(r)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = b
	return int64(len(b)), nil
}

func (m *memBlobs) Get(_ context.Context, key string) (io.ReadCloser, blob.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, blob.Info{}, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), blob.Info{Key: key}, nil
}

func (m *memBlobs) Stat(_ context.Context, key string) (blob.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objs[key]; !ok {
		return blob.Info{}, blob.ErrNotFound
	}
	return blob.Info{Key: key}, nil
}

func (m *memBlobs) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail[key] {
		return errors.New("disk on fire")
	}
	delete(m.objs, key)
	return nil
}

func (m *memBlobs) List(context.Context, string) ([]blob.Info, error) { return nil, nil }

func (m *memBlobs) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objs[key]
	return ok
}

// endedSession stores a finished outbound call in household h that ended at ended, with a
// recording, a transcript and an outcome.
func (e *env) endedSession(h string, ended time.Time, blobs *memBlobs) (id, key string) {
	e.t.Helper()
	id = uuid4()
	key = recordingPrefix + h + "/" + id + ".wav"
	_, _ = blobs.Put(context.Background(), key, bytes.NewReader([]byte("RIFF")), "audio/wav")
	if _, err := e.d.Write.Exec(`INSERT INTO cc_phone_call_sessions (id, household_id, goal, state, transcript_json,
		outcome_json, audio_object_key, duration_seconds, created_at, in_call_at, ended_at)
		VALUES (?, ?, 'order pizza', 'done', '[{"speaker":"callee","text":"hi"}]', '{"summary":"ordered"}', ?, 120, ?, ?, ?)`,
		id, h, key, dbTime(ended.Add(-3*time.Minute)), dbTime(ended.Add(-2*time.Minute)), dbTime(ended)); err != nil {
		e.t.Fatal(err)
	}
	return id, key
}

func (e *env) oldInboundCall(h string, ended time.Time, blobs *memBlobs) (id, key string) {
	e.t.Helper()
	id = uuid4()
	key = inboundRecordingPrefix + h + "/inbound-" + id + ".wav"
	_, _ = blobs.Put(context.Background(), key, bytes.NewReader([]byte("RIFF")), "audio/wav")
	if _, err := e.d.Write.Exec(`INSERT INTO cc_phone_inbound_calls (id, household_id, message, summary, outcome,
		audio_object_key, started_at, ended_at, duration_seconds) VALUES (?, ?, 'call me back', 'Bob called', 'message', ?, ?, ?, 60)`,
		id, h, key, dbTime(ended.Add(-time.Minute)), dbTime(ended)); err != nil {
		e.t.Fatal(err)
	}
	return id, key
}

type retained struct {
	transcript, outcome, audio sql.NullString
	duration                   sql.NullInt64
}

func (e *env) retainedSession(id string) retained {
	e.t.Helper()
	var r retained
	if err := e.d.Read.QueryRow(`SELECT transcript_json, outcome_json, audio_object_key, duration_seconds
		FROM cc_phone_call_sessions WHERE id = ?`, id).Scan(&r.transcript, &r.outcome, &r.audio, &r.duration); err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) inboundAudio(id string) (audio, message sql.NullString) {
	e.t.Helper()
	if err := e.d.Read.QueryRow(`SELECT audio_object_key, message FROM cc_phone_inbound_calls WHERE id = ?`, id).
		Scan(&audio, &message); err != nil {
		e.t.Fatal(err)
	}
	return audio, message
}

func TestSweepRetentionPerHousehold(t *testing.T) {
	e := newEnv(t)
	blobs := &memBlobs{objs: map[string][]byte{}, fail: map[string]bool{}}
	e.s.Blobs = blobs
	ctx := context.Background()
	now := e.now()
	day := 24 * time.Hour
	// hh keeps the default 30 days; otherHH keeps 7.
	e.set(SettingRetentionDays, int64(7), settings.Scope{HouseholdID: otherHH})

	old, oldKey := e.endedSession(hh, now.Add(-31*day), blobs)
	recent, recentKey := e.endedSession(hh, now.Add(-29*day), blobs)
	other, otherKey := e.endedSession(otherHH, now.Add(-8*day), blobs)
	oldIn, oldInKey := e.oldInboundCall(hh, now.Add(-31*day), blobs)
	recentIn, recentInKey := e.oldInboundCall(hh, now.Add(-2*day), blobs)
	live := uuid4() // an active call has no ended_at and is never touched
	if _, err := e.d.Write.Exec(`INSERT INTO cc_phone_call_sessions (id, household_id, goal, state, transcript_json, created_at)
		VALUES (?, ?, 'g', 'in_call', '[]', ?)`, live, hh, dbTime(now.Add(-40*day))); err != nil {
		t.Fatal(err)
	}

	if err := e.s.SweepRetention(ctx); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{old, other} {
		r := e.retainedSession(id)
		if r.transcript.Valid || r.audio.Valid {
			t.Errorf("%s: transcript/audio kept past retention: %+v", id, r)
		}
		if r.outcome.String != `{"summary":"ordered"}` || r.duration.Int64 != 120 {
			t.Errorf("%s: outcome or duration lost (caps and history need them): %+v", id, r)
		}
	}
	for _, k := range []string{oldKey, otherKey, oldInKey} {
		if blobs.has(k) {
			t.Errorf("blob %s not deleted", k)
		}
	}
	if r := e.retainedSession(recent); !r.transcript.Valid || r.audio.String != recentKey || !blobs.has(recentKey) {
		t.Errorf("recent call lost its data: %+v", r)
	}
	if a, msg := e.inboundAudio(oldIn); a.Valid || msg.String != "call me back" {
		t.Errorf("old inbound: audio %v, message %v", a, msg)
	}
	if a, _ := e.inboundAudio(recentIn); a.String != recentInKey || !blobs.has(recentInKey) {
		t.Errorf("recent inbound lost its audio: %v", a)
	}
	if r := e.retainedSession(live); !r.transcript.Valid {
		t.Error("active call's transcript cleared")
	}

	// Lowering hh's retention to 1 day reaps the 29-day-old call on the next pass.
	e.set(SettingRetentionDays, int64(1), settings.Scope{HouseholdID: hh})
	if err := e.s.SweepRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if r := e.retainedSession(recent); r.transcript.Valid || r.audio.Valid || blobs.has(recentKey) {
		t.Errorf("recent call kept after retention lowered: %+v", r)
	}
	if a, _ := e.inboundAudio(recentIn); a.Valid {
		t.Errorf("2-day-old inbound kept with 1-day retention: %v", a)
	}
}

func TestSweepRetentionZeroAndClockAdvance(t *testing.T) {
	e := newEnv(t)
	blobs := &memBlobs{objs: map[string][]byte{}, fail: map[string]bool{}}
	e.s.Blobs = blobs
	ctx := context.Background()
	id, key := e.endedSession(hh, e.now().Add(-time.Minute), blobs)

	// Default 30 days: kept until the clock passes the window.
	if err := e.s.SweepRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if !blobs.has(key) {
		t.Fatal("1-minute-old call reaped under the 30-day default")
	}
	e.advance(30 * 24 * time.Hour)
	if err := e.s.SweepRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if blobs.has(key) {
		t.Fatal("call not reaped once 30 days had passed")
	}

	// 0 (and anything below) keeps nothing once the call has ended.
	for _, v := range []int64{0, -5} {
		e.set(SettingRetentionDays, v, settings.Scope{HouseholdID: hh})
		id, key = e.endedSession(hh, e.now(), blobs)
		if err := e.s.SweepRetention(ctx); err != nil {
			t.Fatal(err)
		}
		if r := e.retainedSession(id); r.transcript.Valid || r.audio.Valid || blobs.has(key) {
			t.Errorf("retention %d kept a just-ended call: %+v", v, r)
		}
	}
}

func TestSweepRetentionRetriesFailedBlobDelete(t *testing.T) {
	e := newEnv(t)
	blobs := &memBlobs{objs: map[string][]byte{}, fail: map[string]bool{}}
	e.s.Blobs = blobs
	ctx := context.Background()
	id, key := e.endedSession(hh, e.now().Add(-40*24*time.Hour), blobs)
	inID, inKey := e.oldInboundCall(hh, e.now().Add(-40*24*time.Hour), blobs)
	blobs.fail[key], blobs.fail[inKey] = true, true

	if err := e.s.SweepRetention(ctx); err != nil {
		t.Fatal(err)
	}
	r := e.retainedSession(id)
	if r.transcript.Valid {
		t.Error("transcript kept because the blob delete failed")
	}
	if r.audio.String != key {
		t.Errorf("failed blob delete lost track of the key: %+v", r)
	}
	if a, _ := e.inboundAudio(inID); a.String != inKey {
		t.Errorf("failed inbound blob delete lost track of the key: %v", a)
	}

	delete(blobs.fail, key)
	delete(blobs.fail, inKey)
	if err := e.s.SweepRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if r := e.retainedSession(id); r.audio.Valid || blobs.has(key) {
		t.Errorf("retry did not delete the blob: %+v", r)
	}
	if a, _ := e.inboundAudio(inID); a.Valid || blobs.has(inKey) {
		t.Errorf("retry did not delete the inbound blob: %v", a)
	}
}
