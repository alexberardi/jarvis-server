package cc

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

type fixedTZ string

func (z fixedTZ) HouseholdTimezone(context.Context, string) string { return string(z) }

// TestSignalJobsOnTheScheduler: the loops are persisted triggers (D27), reactions are queue
// jobs, and the journal card is a per-household cron in the household timezone.
func TestSignalJobsOnTheScheduler(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, scheduler.MigrationModule, scheduler.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, "cc", Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := queue.New(d, log)
	sch := scheduler.New(d, q, log)
	fa := newFakeAuth()
	n := &fakeNotifier{}
	m := &Module{Auth: fa, Users: fa, Nodes: fa, AdminKey: testAdminKey, MQTT: MQTTOptions{Disabled: true},
		Notify: n, HouseholdClock: fixedTZ("America/New_York")}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: log, Queue: q, Scheduler: sch})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpx.Middleware(log, "command-center", mux))
	t.Cleanup(srv.Close)
	e := &env{t: t, m: m, auth: fa, srv: srv, d: d, now: time.Now()}

	for _, name := range []string{signalSweepJob, attentionCleanupJob} {
		st, err := sch.Status(ctx, name)
		if err != nil || st.NextFireAt.IsZero() {
			t.Fatalf("%s: %v %v", name, st, err)
		}
	}

	tok := fa.addUser(5, "hh1", authn.RoleMember)
	if err := m.Settings().Set(ctx, settingSignalAutomations, `{"presence.left": {"instruction": "lock", "enabled": true}}`,
		settings.Scope{HouseholdID: "hh1"}); err != nil {
		t.Fatal(err)
	}
	e.do("POST", "/api/v0/mobile/presence", map[string]any{"household_id": "hh1", "state": "away"}, bearer(tok)).want(200)
	var jobs int
	d.Read.QueryRow(`SELECT COUNT(*) FROM platform_jobs WHERE type = ?`, signalReactionJob).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("reaction jobs %d", jobs)
	}

	node := e.createNode("n1", "hh1")
	if err := m.Settings().Set(ctx, settingAttention, true, settings.Scope{HouseholdID: "hh1"}); err != nil {
		t.Fatal(err)
	}
	e.do("POST", "/api/v0/node/inbox-item", map[string]any{"title": "Hi"}, node.h()).want(200)
	st, err := sch.Status(ctx, attentionJournalJob+":hh1")
	if err != nil {
		t.Fatal(err)
	}
	ny, _ := time.LoadLocation("America/New_York")
	if got := st.NextFireAt.In(ny); got.Hour() != 21 || got.Minute() != 0 {
		t.Fatalf("journal fires at %v", got)
	}
	fire, _ := json.Marshal(scheduler.Fire{Trigger: attentionJournalJob + ":hh1", Payload: []byte("hh1")})
	if _, err := m.runJournalJob(ctx, queue.Job{Payload: fire}); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	last := n.items[len(n.items)-1]
	n.mu.Unlock()
	if last.Category != journalCategory || last.Summary != "Delivered 1, withheld 0 in the last 24h" {
		t.Fatal(last)
	}
}
