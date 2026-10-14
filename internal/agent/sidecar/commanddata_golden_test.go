package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// The app's "Stored data" tab against a desktop agent answers exactly as against a Pi.
// testdata/command_data_golden.json is the Python node's own command_data_handler.py (with its
// real repository and storage backend) answering a script of app requests over the fixture
// commands in testdata/browser (tools/golden/export_command_data.py). Here the agent answers
// the same requests over the same seeded records, with the same commands running in the
// sidecar, and every answer must match: same records, same order, same summaries, same
// errors, and each record's keys in the same order.
func TestCommandDataMatchesThePythonNode(t *testing.T) {
	py := python(t)
	raw, err := os.ReadFile("testdata/command_data_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		NodeID string `json:"node_id"`
		Seed   []struct {
			Storage   string              `json:"storage"`
			Key       string              `json:"key"`
			Record    nodesettings.Object `json:"record"`
			ExpiresAt *time.Time          `json:"expires_at"`
		} `json:"seed"`
		Steps []struct {
			Op       string          `json:"op"`
			Request  json.RawMessage `json:"request"`
			Response json.RawMessage `json:"response"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	st, err := nodesettings.OpenStore(t.TempDir(), golden.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range golden.Seed {
		if err := st.SaveData(s.Storage, s.Key, s.Record, s.ExpiresAt); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sc, err := Start(context.Background(), py, "testdata/browser", log, st)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	cd := &nodesettings.CommandData{Store: st, Commands: sc, Log: log}

	var created []string
	unmask := func(b []byte) []byte {
		for i, k := range created {
			b = bytes.ReplaceAll(b, []byte(placeholder(i)), []byte(k))
		}
		return b
	}
	for i, step := range golden.Steps {
		topic := "jarvis/nodes/" + golden.NodeID + "/command-data/" + step.Op
		reply, body := cd.Handle(context.Background(), topic, unmask(step.Request))
		if string(step.Response) == "null" {
			if reply != "" {
				t.Errorf("step %d (%s): answered %s on %s; the Pi doesn't answer", i, step.Op, body, reply)
			}
			continue
		}
		var req struct {
			CID string `json:"correlation_id"`
		}
		_ = json.Unmarshal(step.Request, &req)
		if want := topic + "/response/" + req.CID; reply != want {
			t.Fatalf("step %d (%s): reply topic %q, want %q", i, step.Op, reply, want)
		}
		var resp struct {
			OK  bool   `json:"ok"`
			Key string `json:"key"`
		}
		_ = json.Unmarshal(body, &resp)
		if step.Op == "create" && resp.OK && !strings.Contains(resp.Key, "@") && !slices.Contains(created, resp.Key) {
			created = append(created, resp.Key)
		}
		for j, k := range created {
			body = bytes.ReplaceAll(body, []byte(k), []byte(placeholder(j)))
		}
		if got, want := generic(t, body), generic(t, step.Response); !reflect.DeepEqual(got, want) {
			t.Errorf("step %d (%s %s):\n got %s\nwant %s", i, step.Op, step.Request, body, step.Response)
			continue
		}
		if got, want := keyOrders(t, body), keyOrders(t, step.Response); !reflect.DeepEqual(got, want) {
			t.Errorf("step %d (%s): record key order\n got %v\nwant %v", i, step.Op, got, want)
		}
	}
}

func placeholder(i int) string { return "<created:" + string(rune('1'+i)) + ">" }

func generic(t *testing.T, b []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return v
}

// keyOrders are the key orders of the records in an answer (a list's data, a get's, update's
// or create's record).
func keyOrders(t *testing.T, b []byte) [][]string {
	t.Helper()
	var r struct {
		Record  json.RawMessage `json:"record"`
		Records []struct {
			Data nodesettings.Object `json:"data"`
		} `json:"records"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	var out [][]string
	if len(r.Record) > 0 && r.Record[0] == '{' {
		var o nodesettings.Object
		if err := json.Unmarshal(r.Record, &o); err != nil {
			t.Fatal(err)
		}
		out = append(out, o.Keys())
	}
	for _, rec := range r.Records {
		out = append(out, rec.Data.Keys())
	}
	return out
}
