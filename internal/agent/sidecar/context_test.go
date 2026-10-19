package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// The context requests end to end over a real sidecar: the declared operations from describe,
// and a query run in the command as the requesting user.
func TestSidecarAnswersContextRequests(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sc, err := Start(ctx, py, "testdata/context", log, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	c := &nodesettings.Context{Providers: sc, Log: log}
	ask := func(op, payload string) (string, map[string]any) {
		t.Helper()
		topic, body := c.Handle(ctx, "jarvis/nodes/n1/context/"+op, []byte(payload))
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("%s: %s: %v", op, body, err)
		}
		return topic, out
	}

	topic, got := ask("operations", `{"correlation_id":"c1"}`)
	if topic != "jarvis/nodes/n1/context/operations/response/c1" {
		t.Fatalf("topic = %q", topic)
	}
	b, _ := json.Marshal(got)
	want := `{"ok":true,"providers":[{"command_name":"agenda","operations":[` +
		`{"description":"Free/busy between two times","name":"availability","params_schema":{"end":{"required":true,"type":"string"},"start":{"required":true,"type":"string"}}},` +
		`{"description":"Always raises","name":"explode","params_schema":{}}]}]}`
	if string(b) != want {
		t.Fatalf("operations = %s\nwant %s", b, want)
	}

	_, got = ask("query", `{"correlation_id":"c2","operation":"availability","params":{"start":"09:00","end":"10:00"},"user_id":7}`)
	b, _ = json.Marshal(got)
	if want := `{"command_name":"agenda","data":{"busy":[["09:00","10:00"]],"user":7},"error":null,"ok":true,"operation":"availability"}`; string(b) != want {
		t.Fatalf("query = %s\nwant %s", b, want)
	}

	_, got = ask("query", `{"correlation_id":"c3","operation":"availability","params":{"start":"09:00"}}`)
	if got["error"] != "missing required params: end" {
		t.Fatalf("missing param = %v", got)
	}

	_, got = ask("query", `{"correlation_id":"c4","command":"agenda","operation":"explode"}`)
	if got["ok"] != false || got["error"] != "agenda.explode failed: calendar offline" {
		t.Fatalf("raise = %v", got)
	}

	_, got = ask("query", `{"correlation_id":"c5","command":"plain","operation":"availability"}`)
	if got["code"] != "no_provider" {
		t.Fatalf("plain = %v", got)
	}

	// The sidecar survives all of it.
	if _, got = ask("query", `{"correlation_id":"c6","operation":"availability","params":{"start":"a","end":"b"}}`); got["ok"] != true {
		t.Fatalf("after the errors = %v", got)
	}
}
