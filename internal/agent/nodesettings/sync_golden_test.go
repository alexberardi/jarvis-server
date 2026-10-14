package nodesettings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"testing"
)

// The app's "Sync to other nodes" against a desktop agent at either end. testdata/
// sync_golden.json is the Python node's own _build_secrets_list (the source's snapshot secrets,
// with and without include_values, for no user and two users) and _dispatch_secrets (the
// target applying the settings:secrets push), written by tools/golden/export_sync.py. The
// crypto around both is pinned in crypto_test.go (TestSealMatchesThePythonNode,
// TestOpenReadsMobilePushes).
type syncRow struct {
	Key    string `json:"key"`
	Scope  string `json:"scope"`
	UserID *int64 `json:"user_id"`
	Value  string `json:"value"`
}

type syncGolden struct {
	Specs       []SecretSpec `json:"specs"`
	SourceStore []syncRow    `json:"source_store"`
	Sources     []struct {
		IncludeValues bool            `json:"include_values"`
		UserID        *int64          `json:"user_id"`
		Secrets       json.RawMessage `json:"secrets"`
	} `json:"sources"`
	Targets []struct {
		Before []syncRow       `json:"before"`
		Push   json.RawMessage `json:"push"`
		Calls  []syncRow       `json:"set_secret_calls"`
	} `json:"targets"`
}

func loadSyncGolden(t *testing.T) syncGolden {
	t.Helper()
	b, err := os.ReadFile("testdata/sync_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g syncGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Sources) == 0 || len(g.Targets) == 0 {
		t.Fatal("empty golden")
	}
	return g
}

func asGeneric(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// As the source: the snapshot's secrets list is the Pi's, value for value.
func TestSyncSourceMatchesThePythonNode(t *testing.T) {
	g := loadSyncGolden(t)
	st, _ := OpenStore(t.TempDir(), "n1")
	for _, r := range g.SourceStore {
		if err := st.SetSecret(r.Key, r.Value, r.Scope, "", r.UserID); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range g.Sources {
		got := asGeneric(t, st.secretsList(g.Specs, c.IncludeValues, c.UserID))
		if want := asGeneric(t, c.Secrets); !reflect.DeepEqual(got, want) {
			t.Errorf("include_values=%v user=%v:\n got %v\nwant %v", c.IncludeValues, c.UserID, got, want)
		}
	}
}

// As the target: a push leaves the agent holding what the Pi holds after it (a value pushed as
// "" reads as unset on both: the Pi stores "", the agent drops the row).
func TestSyncTargetMatchesThePythonNode(t *testing.T) {
	g := loadSyncGolden(t)
	for i, c := range g.Targets {
		st, _ := OpenStore(t.TempDir(), "n1")
		want := map[string]string{}
		id := func(r syncRow) string {
			u := "-"
			if r.UserID != nil {
				u = fmt.Sprint(*r.UserID)
			}
			return r.Key + "|" + r.Scope + "|" + u
		}
		for _, r := range c.Before {
			_ = st.SetSecret(r.Key, r.Value, r.Scope, "", r.UserID)
			want[id(r)] = r.Value
		}
		for _, r := range c.Calls {
			want[id(r)] = r.Value
		}
		dec := json.NewDecoder(bytes.NewReader(c.Push))
		dec.UseNumber()
		var data map[string]any
		if err := dec.Decode(&data); err != nil {
			t.Fatal(err)
		}
		h := &Handler{NodeID: "n1", Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		if err := h.apply("settings:secrets", data); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		got := map[string]string{}
		for _, s := range st.s.Secrets {
			got[id(syncRow{Key: s.Key, Scope: s.Scope, UserID: s.UserID})] = s.Value
		}
		for k, v := range want {
			if v == "" {
				delete(want, k)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("push %s:\n got %v\nwant %v", c.Push, got, want)
		}
	}
}
