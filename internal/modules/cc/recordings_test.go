package cc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

const recBase = "/api/v0/recordings"

// fakeRecDeleter stands in for computer_record_delete: it records each ask and fails for the
// nodes in fail (an agent without the tool, or offline).
type fakeRecDeleter struct {
	mu    sync.Mutex
	asked []string // node/recording
	fail  map[string]bool
}

func (f *fakeRecDeleter) delete(_ context.Context, nodeID, recID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, nodeID+"/"+recID)
	if f.fail[nodeID] {
		return errors.New("unknown tool computer_record_delete")
	}
	return nil
}

func (f *fakeRecDeleter) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

func newRecEnv(t *testing.T) (*env, *fakeRecDeleter) {
	t.Helper()
	del := &fakeRecDeleter{fail: map[string]bool{"node-old": true}}
	e := newEnv(t, envOpts{noMQTT: true, configure: func(m *Module) { m.recordingDeleter = del.delete }})
	return e, del
}

func (e *env) seedRecording(owner int64, node string) Recording {
	e.t.Helper()
	rec, err := e.m.createRecording(context.Background(), Recording{HouseholdID: "hh-1", OwnerID: owner, NodeID: node,
		Mode: "browser", Trace: json.RawMessage(`[{"t":0,"kind":"navigate","url":"https://bank.example"}]`),
		Narration: "this is how I get my statement", Keyframes: 2})
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

func (e *env) recordingExists(id string) bool {
	e.t.Helper()
	rec, err := e.m.getRecording(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return rec != nil
}

func TestRetentionSweep(t *testing.T) {
	e, del := newRecEnv(t)
	ctx := context.Background()
	old := e.seedRecording(1, "node-old")   // its agent can't delete (no tool yet)
	oldOK := e.seedRecording(1, "node-new") // its agent can
	if want := old.CreatedAt.AddDate(0, 0, defaultRecordingRetention); !old.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at %v, want created + 30 days %v", old.ExpiresAt, want)
	}
	// A workflow learnt from the old recording outlives it.
	wf, err := e.m.createWorkflow(ctx, TaughtWorkflow{HouseholdID: "hh-1", OwnerID: 1, Name: "Statement",
		RecordingID: old.ID, Steps: []WStep{wfStep()}})
	if err != nil {
		t.Fatal(err)
	}

	e.advance(20 * 24 * time.Hour)
	fresh := e.seedRecording(1, "node-new")
	// The household shortens retention to 7 days: recordings stored from now on expire sooner.
	if err := e.m.settings.Set(ctx, settingRecordingRetention, int64(7), settings.Scope{HouseholdID: "hh-1"}); err != nil {
		t.Fatal(err)
	}
	short := e.seedRecording(1, "")
	if got := short.ExpiresAt.Sub(short.CreatedAt); got != 7*24*time.Hour {
		t.Fatalf("retention 7 days gave %v", got)
	}

	// Nothing has expired yet.
	if err := e.m.sweepRecordings(ctx); err != nil {
		t.Fatal(err)
	}
	if len(del.calls()) != 0 || !e.recordingExists(old.ID) {
		t.Fatalf("swept early: %v", del.calls())
	}

	e.advance(10*24*time.Hour + time.Minute) // old ones 30 days + a minute; fresh 10 days; short 10 days
	if err := e.m.sweepRecordings(ctx); err != nil {
		t.Fatalf("an agent's failure failed the sweep: %v", err)
	}
	for _, c := range []struct {
		rec  Recording
		kept bool
	}{{old, false}, {oldOK, false}, {fresh, true}, {short, false}} {
		if got := e.recordingExists(c.rec.ID); got != c.kept {
			t.Errorf("%s (node %q): kept=%v, want %v", c.rec.ID, c.rec.NodeID, got, c.kept)
		}
	}
	// Each agent that holds a copy was asked once; one without the tool doesn't stop the sweep.
	want := []string{"node-old/" + old.ID, "node-new/" + oldOK.ID}
	if got := del.calls(); !slices.Equal(got, want) {
		t.Errorf("agents asked %v, want %v", got, want)
	}
	got, err := e.m.getWorkflow(ctx, wf.ID)
	if err != nil || got == nil || got.RecordingID != "" {
		t.Errorf("workflow after its recording expired: %+v %v", got, err)
	}

	// The sweep is a daily cron trigger.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.m.deps.Scheduler = scheduler.New(e.d, queue.New(e.d, log), log)
	if err := e.m.startRecordings(ctx); err != nil {
		t.Fatal(err)
	}
	var kind, spec string
	if err := e.d.Read.QueryRow(`SELECT kind, spec FROM platform_triggers WHERE name = ?`, recordingSweepJob).Scan(&kind, &spec); err != nil {
		t.Fatal(err)
	}
	if kind != scheduler.KindCron || spec != `{"cron":"17 4 * * *"}` {
		t.Errorf("trigger %s %s", kind, spec)
	}
}

func TestRecordingsAreTheOwners(t *testing.T) {
	e, del := newRecEnv(t)
	owner := bearer(e.auth.addUser(1, "hh-1", authn.RoleMember))
	admin := bearer(e.auth.addUser(3, "hh-1", authn.RoleAdmin))
	outsider := bearer(e.auth.addUser(4, "hh-2", authn.RoleMember))
	rec := e.seedRecording(1, "node-new")

	l := e.do("GET", recBase+"?household_id=hh-1", nil, owner).want(200).json()["recordings"].([]any)
	if len(l) != 1 || l[0].(map[string]any)["id"] != rec.ID || l[0].(map[string]any)["trace"] != nil {
		t.Fatalf("owner's list (no traces): %v", l)
	}
	// The list counts the trace's events for the Recordings page (piece 27).
	if n := l[0].(map[string]any)["steps"]; n != float64(1) {
		t.Fatalf("steps = %v, want 1", n)
	}
	if got := e.do("GET", recBase+"/"+rec.ID, nil, owner).want(200).json(); got["narration"] != rec.Narration || got["trace"] == nil {
		t.Fatalf("owner GET: %v", got)
	}
	// Not even a household admin sees someone's recording; another household gets a 403 list.
	if n := len(e.do("GET", recBase+"?household_id=hh-1", nil, admin).want(200).json()["recordings"].([]any)); n != 0 {
		t.Errorf("admin lists %d", n)
	}
	e.do("GET", recBase+"/"+rec.ID, nil, admin).detail(404, "Recording not found")
	e.do("DELETE", recBase+"/"+rec.ID, nil, admin).want(404)
	e.do("GET", recBase+"?household_id=hh-1", nil, outsider).want(403)
	e.do("GET", recBase+"/"+rec.ID, nil, outsider).want(404)

	e.do("DELETE", recBase+"/"+rec.ID, nil, owner).want(200)
	if e.recordingExists(rec.ID) {
		t.Fatal("still there")
	}
	// The agent is asked in the background.
	deadline := time.Now().Add(5 * time.Second)
	for len(del.calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := del.calls(); !slices.Equal(got, []string{"node-new/" + rec.ID}) {
		t.Errorf("agent asked %v", got)
	}
}
