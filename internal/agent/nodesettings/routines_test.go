package nodesettings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
)

// routinesCC answers GET /api/v0/nodes/{id}/routines with body, or fails with err.
type routinesCC struct {
	body  string
	err   error
	paths []string
}

func (f *routinesCC) Do(_ context.Context, method, path string, _, out any) error {
	f.paths = append(f.paths, method+" "+path)
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.body), out)
}

func newRoutines(t *testing.T, protected ...string) (*Routines, *Store) {
	t.Helper()
	st, err := OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	return &Routines{NodeID: "n1", Store: st, Protected: func() []string { return protected },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, st
}

func save(t *testing.T, st *Store, slug, def string) {
	t.Helper()
	var o Object
	if err := json.Unmarshal([]byte(def), &o); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveData(RoutineCommand, slug, o, nil); err != nil {
		t.Fatal(err)
	}
}

func routines(st *Store) map[string]string {
	out := map[string]string{}
	for _, r := range st.Records(RoutineCommand, time.Now()) {
		b, _ := r.Data.MarshalJSON()
		out[r.Key] = string(b)
	}
	return out
}

func TestRoutinesPullAppliesAndPrunes(t *testing.T) {
	r, st := newRoutines(t, "pantry_party")
	save(t, st, "movie_night", `{"trigger_phrases":["old"]}`)
	save(t, st, "gone", `{"trigger_phrases":["deleted in the app"]}`)
	save(t, st, "good_morning", `{"trigger_phrases":["a default"]}`)
	save(t, st, "pantry_party", `{"trigger_phrases":["from a package"]}`)
	cc := &routinesCC{body: `{"routines":{
		"movie_night":{"trigger_phrases":["movie time"],"steps":[{"command":"control_device","args":{"action":"turn_off"}}]},
		"leaving":{"trigger_phrases":["i'm leaving"],"steps":[]}}}`}
	n, err := r.Pull(context.Background(), cc)
	if err != nil || n != 2 {
		t.Fatalf("Pull = %d, %v; want 2 applied", n, err)
	}
	if !slices.Equal(cc.paths, []string{"GET /api/v0/nodes/n1/routines"}) {
		t.Fatalf("calls = %v", cc.paths)
	}
	got := routines(st)
	want := map[string]string{
		"movie_night":  `{"trigger_phrases":["movie time"],"steps":[{"command":"control_device","args":{"action":"turn_off"}}]}`,
		"leaving":      `{"trigger_phrases":["i'm leaving"],"steps":[]}`,
		"good_morning": `{"trigger_phrases":["a default"]}`,      // a default is never pruned
		"pantry_party": `{"trigger_phrases":["from a package"]}`, // nor a package's routine
	}
	if len(got) != len(want) {
		t.Fatalf("routines = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("routine/%s = %s, want %s", k, got[k], v)
		}
	}
}

func TestRoutinesPullKeepsEverythingWhenUnreachable(t *testing.T) {
	r, st := newRoutines(t)
	save(t, st, "movie_night", `{"trigger_phrases":["movie time"]}`)
	n, err := r.Pull(context.Background(), &routinesCC{err: errors.New("dial tcp: connection refused")})
	if err == nil || n != 0 {
		t.Fatalf("Pull = %d, %v; want the error", n, err)
	}
	if got := routines(st); got["movie_night"] == "" || len(got) != 1 {
		t.Fatalf("routines = %v; an unreachable command-center must not prune", got)
	}
}

func TestRoutinesPullEmptyHousehold(t *testing.T) {
	// A node in no household gets {"routines": {}}: the server's routines go, the defaults stay.
	r, st := newRoutines(t)
	save(t, st, "movie_night", `{}`)
	save(t, st, "nightly_briefing", `{}`)
	if _, err := r.Pull(context.Background(), &routinesCC{body: `{"routines":{}}`}); err != nil {
		t.Fatal(err)
	}
	if got := routines(st); len(got) != 1 || got["nightly_briefing"] == "" {
		t.Fatalf("routines = %v, want only the default", got)
	}
}

func TestRoutinesNudgePulls(t *testing.T) {
	r, st := newRoutines(t)
	r.Handle(context.Background(), &routinesCC{body: `{"routines":{"a":{"steps":[]}}}`}, []byte(`{"event":"routines_changed","household_id":"h1"}`))
	if got := routines(st); got["a"] == "" {
		t.Fatalf("routines = %v after a nudge", got)
	}
}
