package cc

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// sigWatchEnv is a household with watches on, members 5 and 6, a node to post signals from and a
// scheduler for the absent_for timer.
type sigWatchEnv struct {
	*sigEnv
	node testNode
}

func newSigWatchEnv(t *testing.T) *sigWatchEnv {
	t.Helper()
	se := newWatchEnv(t)
	se.m.deps.Scheduler = scheduler.New(se.d, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	se.auth.addUser(5, sigHH, authn.RoleMember)
	se.auth.addUser(6, sigHH, authn.RoleMember)
	se.set(settingWatchesEnabled, true, sigHH)
	return &sigWatchEnv{sigEnv: se, node: se.createNode("n1", sigHH)}
}

// signalWatch stores an active signal watch owned by user 5.
func (se *sigWatchEnv) signalWatch(src watches.Source, cond watches.Condition) watches.Watch {
	se.t.Helper()
	uid := int64(5)
	w, err := se.m.watches.Create(context.Background(), watches.Watch{HouseholdID: sigHH, UserID: &uid, Title: "Signal watch",
		SourceKind: watches.SourceSignal, Source: src, Condition: cond, FirePolicy: watches.FireEveryChange})
	if err != nil {
		se.t.Fatal(err)
	}
	return w
}

// post sends a signal of kind with data from the node, scoped to user (0: none), and waits for
// its reactions.
func (se *sigWatchEnv) post(kind string, user int64, data map[string]any) {
	se.t.Helper()
	var extra map[string]any
	if user != 0 {
		extra = map[string]any{"scope": map[string]any{"user_id": user}}
	}
	b := signal(kind, kind+":k", extra)
	b["data"] = data
	se.do("POST", "/api/v0/signals", b, se.node.h()).want(200)
	se.wait()
}

func (se *sigWatchEnv) absentArmed(id string) bool {
	st, err := se.m.deps.Scheduler.Status(context.Background(), watchTrigger(id)+":absent")
	return err == nil && !st.NextFireAt.IsZero()
}

// runAbsent runs the absent trigger's job with the payload the trigger carries.
func (se *sigWatchEnv) runAbsent(id string) {
	se.t.Helper()
	var payload []byte
	if err := se.d.Read.QueryRow(`SELECT payload FROM platform_triggers WHERE name = ?`, watchTrigger(id)+":absent").Scan(&payload); err != nil {
		se.t.Fatal(err)
	}
	if _, err := se.m.runWatchAbsentJob(context.Background(), queue.Job{Payload: payload}); err != nil {
		se.t.Fatal(err)
	}
}

var awayEquals = watches.Condition{Op: watches.OpEquals, Field: "state", Value: "away"}

func TestSignalWatchUserFilter(t *testing.T) {
	se := newSigWatchEnv(t)
	uid := int64(5)
	w := se.signalWatch(watches.Source{Kind: "presence.left", UserID: &uid}, awayEquals)
	se.post("presence.left", 6, map[string]any{"state": "away"})
	if a := se.alerts(); len(a) != 0 {
		t.Fatal("another member's signal fired the watch", a)
	}
	se.post("presence.left", 5, map[string]any{"state": "away"})
	a := se.alerts()
	if len(a) != 1 || a[0]["watch_id"] != w.ID || a[0]["value"] != "away" {
		t.Fatal(a)
	}
	if got := se.watch(sigHH, w.ID); got.LastValue != "away" {
		t.Fatal(got.LastValue)
	}
}

func TestSignalWatchStatuses(t *testing.T) {
	se := newSigWatchEnv(t)
	ctx := context.Background()
	uid := int64(5)
	rc := reactionCtx{HouseholdID: sigHH, UserID: &uid, Kind: "presence.left", Facts: `{"state": "away"}`}
	if s := se.m.reactWatch(ctx, rc); s != "no_watch" {
		t.Fatal(s)
	}
	se.signalWatch(watches.Source{Kind: "presence.left"}, awayEquals)
	if s := se.m.reactWatch(ctx, reactionCtx{HouseholdID: sigHH, Kind: "watch.web", Facts: "{}"}); s != "skip" {
		t.Fatal(s)
	}
	se.set(settingWatchesEnabled, false, sigHH)
	if s := se.m.reactWatch(ctx, rc); s != "disabled" {
		t.Fatal(s)
	}
	se.post("presence.left", 5, map[string]any{"state": "away"})
	if a := se.alerts(); len(a) != 0 {
		t.Fatal("watches are off", a)
	}
	se.set(settingWatchesEnabled, true, sigHH)
	if s := se.m.reactWatch(ctx, rc); s != "observed:1" {
		t.Fatal(s)
	}
}

func TestSignalWatchFactsFilter(t *testing.T) {
	se := newSigWatchEnv(t)
	se.signalWatch(watches.Source{Kind: "presence.left", Facts: map[string]string{"room": "garage"}}, awayEquals)
	se.post("presence.left", 5, map[string]any{"state": "away", "room": "kitchen"})
	if a := se.alerts(); len(a) != 0 {
		t.Fatal("a non-matching fact fired the watch", a)
	}
	se.post("presence.left", 5, map[string]any{"state": "away", "room": "garage"})
	if a := se.alerts(); len(a) != 1 {
		t.Fatal(a)
	}
}

func TestSignalWatchAbsentFor(t *testing.T) {
	se := newSigWatchEnv(t)
	w := se.signalWatch(watches.Source{Kind: "door.state"},
		watches.Condition{Op: watches.OpAbsentFor, Field: "state", Value: "closed", For: "10m"})
	se.post("door.state", 0, map[string]any{"state": "open"})
	if !se.absentArmed(w.ID) {
		t.Fatal("an open door did not arm the absent timer")
	}
	st, _ := se.m.deps.Scheduler.Status(context.Background(), watchTrigger(w.ID)+":absent")
	if want := se.clock().Add(10 * time.Minute); !st.NextFireAt.Equal(want) {
		t.Fatal(st.NextFireAt, want)
	}
	if got := se.watch(sigHH, w.ID); got.LastValue != "open" {
		t.Fatal(got.LastValue)
	}
	// A second open signal keeps the first deadline.
	se.advance(time.Minute)
	se.post("door.state", 0, map[string]any{"state": "open"})
	if st2, _ := se.m.deps.Scheduler.Status(context.Background(), watchTrigger(w.ID)+":absent"); !st2.NextFireAt.Equal(st.NextFireAt) {
		t.Fatal("re-armed", st2.NextFireAt, st.NextFireAt)
	}
	se.advance(9 * time.Minute)
	se.runAbsent(w.ID)
	a := se.alerts()
	if len(a) != 1 || a[0]["watch_id"] != w.ID || a[0]["value"] != "open" {
		t.Fatal(a)
	}
	if items := se.items(); items[0].Summary != "state hasn't been closed for 10m" {
		t.Fatal(items[0].Summary)
	}

	// open, then closed: the timer is disarmed and a late run says nothing.
	w2 := se.signalWatch(watches.Source{Kind: "door.state", Facts: map[string]string{"door": "back"}},
		watches.Condition{Op: watches.OpAbsentFor, Field: "state", Value: "closed", For: "10m"})
	se.post("door.state", 0, map[string]any{"state": "open", "door": "back"})
	if !se.absentArmed(w2.ID) {
		t.Fatal("not armed")
	}
	var payload []byte
	if err := se.d.Read.QueryRow(`SELECT payload FROM platform_triggers WHERE name = ?`, watchTrigger(w2.ID)+":absent").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	se.post("door.state", 0, map[string]any{"state": "closed", "door": "back"})
	if se.absentArmed(w2.ID) {
		t.Fatal("a closed door left the timer armed")
	}
	se.advance(10 * time.Minute)
	before := len(se.alerts())
	if _, err := se.m.runWatchAbsentJob(context.Background(), queue.Job{Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if a := se.alerts(); len(a) != before {
		t.Fatal("a closed door fired", a)
	}
}

func TestSignalWatchAlongsideAutomation(t *testing.T) {
	ae := newAutoEnv(t, "automatic", false, nodeReport())
	ae.set(settingHouseholdTimezone, "America/New_York", sigHH)
	ae.set(settingWatchesEnabled, true, sigHH)
	uid := int64(5)
	if _, err := ae.m.watches.Create(context.Background(), watches.Watch{HouseholdID: sigHH, UserID: &uid, Title: "Left home",
		SourceKind: watches.SourceSignal, Source: watches.Source{Kind: "presence.left", UserID: &uid}, Condition: awayEquals,
		FirePolicy: watches.FireOnce}); err != nil {
		t.Fatal(err)
	}
	ae.leave()
	if !ae.agent.called("tool:lock_door") {
		t.Fatal("the automation did not run")
	}
	if a := ae.alerts(); len(a) != 1 {
		t.Fatal(a)
	}
}
