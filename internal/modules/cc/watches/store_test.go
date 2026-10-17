package watches

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

const hh = "hh-1"

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	// The watches package has no embed of its own: read the cc module's migrations from disk.
	if err := db.Migrate(ctx, d, "cc", os.DirFS(filepath.Join("..", "migrations"))); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 10, 17, 9, 30, 0, 123456789, time.UTC)}
	return &Store{DB: d, Now: c.now}, c
}

func uid(n int64) *int64 { return &n }

func mustCreate(t *testing.T, s *Store, w Watch) Watch {
	t.Helper()
	got, err := s.Create(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func signalWatch(kind string) Watch {
	return Watch{HouseholdID: hh, UserID: uid(7), Title: "Front door", SourceKind: SourceSignal,
		Source:     Source{Kind: kind, UserID: uid(7), Facts: map[string]string{"room": "hall"}},
		Condition:  Condition{Op: OpAbsentFor, Field: "state", Value: "closed", For: "30m"},
		FirePolicy: FireCooldown, CooldownSeconds: 600}
}

func TestStoreCreateAndGet(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	now := c.t.Truncate(time.Millisecond)

	web := webWatch()
	web.HouseholdID, web.UserID = hh, uid(7)
	web.State = StateDone // ignored: a new watch is active
	got := mustCreate(t, s, web)
	if !regexp.MustCompile(`^wt_[0-9a-f]{16}$`).MatchString(got.ID) {
		t.Fatalf("id %q", got.ID)
	}
	exp := now.Add(DefaultTTL)
	want := web
	want.ID, want.State, want.CadenceSeconds = got.ID, StateActive, 3600
	want.ExpiresAt, want.CreatedAt, want.UpdatedAt = &exp, now, now
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Create = %+v\nwant %+v", got, want)
	}
	read, err := s.Get(ctx, hh, got.ID)
	if err != nil || read == nil {
		t.Fatal(read, err)
	}
	if !reflect.DeepEqual(*read, want) {
		t.Fatalf("Get = %+v\nwant %+v", *read, want)
	}

	sw := signalWatch("door")
	at := now.Add(time.Hour)
	sw.ExpiresAt = &at
	sg := mustCreate(t, s, sw)
	want = sw
	want.ID, want.State, want.CreatedAt, want.UpdatedAt = sg.ID, StateActive, now, now
	read, err = s.Get(ctx, hh, sg.ID)
	if err != nil || read == nil {
		t.Fatal(read, err)
	}
	if !reflect.DeepEqual(*read, want) || read.CadenceSeconds != 0 {
		t.Fatalf("Get signal = %+v\nwant %+v", *read, want)
	}

	if w, err := s.Get(ctx, "hh-2", got.ID); err != nil || w != nil {
		t.Fatalf("another household's watch: %v, %v", w, err)
	}
	if w, err := s.Get(ctx, hh, "wt_missing"); err != nil || w != nil {
		t.Fatalf("missing: %v, %v", w, err)
	}
}

func TestStoreCreateRejectsInvalid(t *testing.T) {
	s, _ := newStore(t)
	w := webWatch()
	w.HouseholdID = hh
	w.Source.Extract = ""
	if _, err := s.Create(context.Background(), w); err == nil || err.Error() != "watch: a web watch needs what to read (extract)" {
		t.Fatalf("Create = %v", err)
	}
	if n, _ := s.CountLive(context.Background(), hh); n != 0 {
		t.Fatalf("stored %d", n)
	}
}

func ids(ws []Watch) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.ID)
	}
	return out
}

func TestStoreListCountSetState(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	w := webWatch()
	w.HouseholdID, w.UserID = hh, uid(7)
	a := mustCreate(t, s, w)
	c.t = c.t.Add(time.Second)
	w.UserID = uid(8)
	b := mustCreate(t, s, w)
	c.t = c.t.Add(time.Second)
	w.UserID = nil
	h := mustCreate(t, s, w)
	w.HouseholdID = "hh-2"
	mustCreate(t, s, w)

	all, err := s.List(ctx, hh, nil)
	if err != nil || !reflect.DeepEqual(ids(all), []string{a.ID, b.ID, h.ID}) {
		t.Fatalf("List all = %v, %v", ids(all), err)
	}
	mine, _ := s.List(ctx, hh, uid(8))
	if !reflect.DeepEqual(ids(mine), []string{b.ID}) {
		t.Fatalf("List user 8 = %v", ids(mine))
	}

	if ok, err := s.SetState(ctx, hh, a.ID, StatePaused); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := s.SetState(ctx, hh, b.ID, StateDone); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := s.SetState(ctx, "hh-2", h.ID, StateDone); ok || err != nil {
		t.Fatalf("another household's SetState = %v, %v", ok, err)
	}
	if _, err := s.SetState(ctx, hh, h.ID, "gone"); err == nil {
		t.Fatal("unknown state accepted")
	}
	got, _ := s.Get(ctx, hh, a.ID)
	if got.State != StatePaused {
		t.Fatalf("state %q", got.State)
	}

	live, _ := s.List(ctx, hh, nil, StateActive, StatePaused)
	if !reflect.DeepEqual(ids(live), []string{a.ID, h.ID}) {
		t.Fatalf("List live = %v", ids(live))
	}
	done, _ := s.List(ctx, hh, nil, StateDone)
	if !reflect.DeepEqual(ids(done), []string{b.ID}) {
		t.Fatalf("List done = %v", ids(done))
	}
	if n, err := s.CountLive(ctx, hh); n != 2 || err != nil {
		t.Fatalf("CountLive = %d, %v", n, err)
	}
}

func TestStoreRecordCheckAndFire(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	w := webWatch()
	w.HouseholdID = hh
	a := mustCreate(t, s, w)

	c.t = c.t.Add(time.Minute)
	t1 := c.t.Truncate(time.Millisecond)
	if err := s.RecordCheck(ctx, a.ID, "$549", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, hh, a.ID)
	if got.LastValue != "$549" || got.LastError != "" || got.LastCheckedAt == nil || !got.LastCheckedAt.Equal(t1) {
		t.Fatalf("after check: %+v", got)
	}

	c.t = c.t.Add(time.Minute)
	t2 := c.t.Truncate(time.Millisecond)
	if err := s.RecordCheck(ctx, a.ID, "", "fetch failed"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, hh, a.ID)
	if got.LastValue != "$549" || got.LastError != "fetch failed" || !got.LastCheckedAt.Equal(t2) {
		t.Fatalf("after failed check: %+v", got)
	}
	if err := s.RecordCheck(ctx, a.ID, "$499", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, hh, a.ID)
	if got.LastValue != "$499" || got.LastError != "" {
		t.Fatalf("error not cleared: %+v", got)
	}

	if err := s.RecordFire(ctx, a.ID, StateDone); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, hh, a.ID)
	if got.State != StateDone || got.LastFiredAt == nil || !got.LastFiredAt.Equal(t2) || !got.UpdatedAt.Equal(t2) {
		t.Fatalf("after fire: %+v", got)
	}
}

func TestStoreActiveSignalWatches(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	door := mustCreate(t, s, signalWatch("door"))
	mustCreate(t, s, signalWatch("motion"))
	paused := mustCreate(t, s, signalWatch("door"))
	if _, err := s.SetState(ctx, hh, paused.ID, StatePaused); err != nil {
		t.Fatal(err)
	}
	other := signalWatch("door")
	other.HouseholdID = "hh-2"
	mustCreate(t, s, other)
	web := webWatch()
	web.HouseholdID = hh
	mustCreate(t, s, web)

	got, err := s.ActiveSignalWatches(ctx, hh, "door")
	if err != nil || !reflect.DeepEqual(ids(got), []string{door.ID}) {
		t.Fatalf("ActiveSignalWatches = %v, %v", ids(got), err)
	}
	if got[0].Source.Facts["room"] != "hall" {
		t.Fatalf("source not decoded: %+v", got[0].Source)
	}
}

func TestStoreExpireDue(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	mk := func(in time.Duration, state string) Watch {
		w := signalWatch("door")
		at := c.t.Add(in)
		w.ExpiresAt = &at
		got := mustCreate(t, s, w)
		if state != StateActive {
			if _, err := s.SetState(ctx, hh, got.ID, state); err != nil {
				t.Fatal(err)
			}
		}
		return got
	}
	active := mk(time.Hour, StateActive)
	paused := mk(2*time.Hour, StatePaused)
	mk(time.Hour, StateDone)
	later := mk(3*time.Hour, StateActive)

	c.t = c.t.Add(2 * time.Hour) // exactly paused's expiry: due
	got, err := s.ExpireDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{active.ID: true, paused.ID: true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("ExpireDue = %v; want %v and %v", got, active.ID, paused.ID)
	}
	for id, state := range map[string]string{active.ID: StateDone, paused.ID: StateDone, later.ID: StateActive} {
		w, _ := s.Get(ctx, hh, id)
		if w.State != state {
			t.Errorf("%s: state %q; want %q", id, w.State, state)
		}
	}
	if again, _ := s.ExpireDue(ctx); len(again) != 0 {
		t.Fatalf("second ExpireDue = %v", again)
	}
}

func TestStorePurgeUser(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	mine := signalWatch("door")
	theirs := signalWatch("door")
	theirs.UserID = uid(8)
	house := signalWatch("door")
	house.UserID = nil
	mustCreate(t, s, mine)
	c.t = c.t.Add(time.Second)
	b := mustCreate(t, s, theirs)
	c.t = c.t.Add(time.Second)
	h := mustCreate(t, s, house)

	tx, err := s.DB.Write.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeUser(ctx, tx, 7); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	left, _ := s.List(ctx, hh, nil)
	if !reflect.DeepEqual(ids(left), []string{b.ID, h.ID}) {
		t.Fatalf("after purge: %v", ids(left))
	}
}
