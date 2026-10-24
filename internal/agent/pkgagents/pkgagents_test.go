package pkgagents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// fakeSidecar is the sidecar in service: its agents, each run recorded, a run's answer by name.
type fakeSidecar struct {
	agents []sidecar.AgentDef
	ran    []string
	alerts map[string][]json.RawMessage
	fail   map[string]error
}

func (f *fakeSidecar) Agents() []sidecar.AgentDef { return f.agents }

func (f *fakeSidecar) RunAgent(_ context.Context, name string) ([]json.RawMessage, error) {
	f.ran = append(f.ran, name)
	if err := f.fail[name]; err != nil {
		return nil, err
	}
	return f.alerts[name], nil
}

func newScheduler(sc *fakeSidecar, now *time.Time) (*Scheduler, *sdkhost.Alerts) {
	q := &sdkhost.Alerts{Now: func() time.Time { return *now }}
	return &Scheduler{Sidecar: sc, Alerts: q, Now: func() time.Time { return *now }}, q
}

// The user's QA (2026-10-23): comfyui_jobs (every 20 s, run_on_startup) never ran on the Go
// node. Each agent of the sidecar in service runs at once when it says run_on_startup, else
// one interval after it is first seen, then every interval; one at a time, on the check tick.
func TestSchedulerRunsEachAgentOnItsSchedule(t *testing.T) {
	now := time.Date(2026, 10, 23, 22, 12, 0, 0, time.UTC)
	sc := &fakeSidecar{agents: []sidecar.AgentDef{
		{Name: "comfyui_jobs", Package: "comfyui", Interval: 20 * time.Second, RunOnStartup: true},
		{Name: "news", Package: "news", Interval: 60 * time.Second},
	}}
	s, _ := newScheduler(sc, &now)
	ctx := context.Background()
	at := func(d time.Duration, want ...string) {
		t.Helper()
		now = now.Add(d)
		sc.ran = nil
		s.Tick(ctx)
		if strings.Join(sc.ran, ",") != strings.Join(want, ",") {
			t.Fatalf("+%v ran %v, want %v", d, sc.ran, want)
		}
	}
	at(0, "comfyui_jobs")
	at(CheckEvery)
	at(CheckEvery, "comfyui_jobs")
	at(CheckEvery)
	at(CheckEvery, "comfyui_jobs")
	at(CheckEvery)
	at(CheckEvery, "comfyui_jobs", "news")

	// Uninstalled (a new sidecar without it): it stops; installed again, it starts over.
	sc.agents = sc.agents[1:]
	at(2*time.Minute, "news")
	sc.agents = append(sc.agents, sidecar.AgentDef{Name: "comfyui_jobs", Package: "comfyui", Interval: 20 * time.Second, RunOnStartup: true})
	at(CheckEvery, "comfyui_jobs")
}

// What a run returns goes into the node's alert queue (as the Python node's scheduler adds
// get_alerts() to its AlertQueueService); one that can't be read is dropped.
func TestSchedulerQueuesAnAgentsAlerts(t *testing.T) {
	now := time.Date(2026, 10, 23, 22, 12, 0, 0, time.UTC)
	sc := &fakeSidecar{
		agents: []sidecar.AgentDef{{Name: "storms", Package: "weather", Interval: time.Minute, RunOnStartup: true}},
		alerts: map[string][]json.RawMessage{"storms": {
			json.RawMessage(`{"id":"a1","source_agent":"storms","title":"Storm at 6","summary":"Bring the chairs in","priority":3,` +
				`"created_at":"2026-10-23T22:12:00+00:00","expires_at":"2026-10-23T23:12:00.123456+00:00","dedupe_key":"storm:1","category":"weather","target_user_id":null}`),
			json.RawMessage(`{"title": 5}`),
		}},
	}
	s, q := newScheduler(sc, &now)
	s.Tick(context.Background())
	got := q.Pending()
	if len(got) != 1 || got[0].Title != "Storm at 6" || got[0].Priority != 3 || got[0].SourceAgent != "storms" ||
		got[0].DedupeKey == nil || *got[0].DedupeKey != "storm:1" {
		t.Fatalf("queued = %+v", got)
	}
}

// Health: an agent whose last FailAfter runs failed makes its package failing (the package
// list's health), with the error; a good run clears it. A failed run is retried at its interval.
func TestSchedulerReportsFailingAgents(t *testing.T) {
	now := time.Date(2026, 10, 23, 22, 12, 0, 0, time.UTC)
	sc := &fakeSidecar{
		agents: []sidecar.AgentDef{{Name: "comfyui_jobs", Package: "comfyui", Interval: 20 * time.Second, RunOnStartup: true}},
		fail:   map[string]error{"comfyui_jobs": errors.New("RuntimeError: no ComfyUI")},
	}
	s, _ := newScheduler(sc, &now)
	ctx := context.Background()
	for i := 0; i < FailAfter; i++ {
		if len(s.Failing()) != 0 {
			t.Fatalf("failing after %d runs: %v", i, s.Failing())
		}
		sc.ran = nil
		s.Tick(ctx)
		if len(sc.ran) != 1 {
			t.Fatalf("run %d: ran %v", i, sc.ran)
		}
		now = now.Add(10 * time.Second)
		s.Tick(ctx) // not yet due again
		now = now.Add(10 * time.Second)
	}
	if f := s.Failing(); len(f) != 1 || !strings.Contains(f["comfyui"], "comfyui_jobs: RuntimeError: no ComfyUI") {
		t.Fatalf("failing = %v", f)
	}
	sc.fail = nil
	s.Tick(ctx)
	if f := s.Failing(); len(f) != 0 {
		t.Fatalf("failing after a good run = %v", f)
	}
}

// Run ticks until ctx ends.
func TestSchedulerRunStopsWithItsContext(t *testing.T) {
	now := time.Date(2026, 10, 23, 22, 12, 0, 0, time.UTC)
	sc := &fakeSidecar{agents: []sidecar.AgentDef{{Name: "a", Package: "p", Interval: time.Second, RunOnStartup: true}}}
	s, _ := newScheduler(sc, &now)
	ticks := make(chan time.Time)
	s.After = func(time.Duration) <-chan time.Time { return ticks }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	ticks <- now // the first tick ran, and Run waits again
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
