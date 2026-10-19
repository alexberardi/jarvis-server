package nodesettings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// The real sidecar's side (describe's context_operations, context_query) is tested in
// internal/agent/sidecar; these cover the handler's own paths over a fake.

type fakeContext struct {
	cmds  []ContextCommand
	err   error
	calls []string
	users []*int64
	res   string
}

func (f *fakeContext) ContextCommands() []ContextCommand { return f.cmds }

func (f *fakeContext) ContextQuery(_ context.Context, command, operation string, params json.RawMessage, uid *int64) (Object, error) {
	f.calls = append(f.calls, command+"."+operation+" "+string(params))
	f.users = append(f.users, uid)
	if f.err != nil {
		return Object{}, f.err
	}
	res := f.res
	if res == "" {
		res = `{"ok":true,"data":{"free":true},"error":null}`
	}
	var o Object
	return o, json.Unmarshal([]byte(res), &o)
}

const availability = `{"name":"availability","description":"free/busy","params_schema":{"start":{"type":"string","required":true},"end":{"type":"string","required":true},"tz":{"type":"string"}}}`

func newContext(t *testing.T) (*Context, *fakeContext, *Store) {
	t.Helper()
	st, err := OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeContext{cmds: []ContextCommand{
		{Name: "tell_joke"},
		{Name: "calendar", Operations: []json.RawMessage{json.RawMessage(availability)}},
		{Name: "other_calendar", Operations: []json.RawMessage{json.RawMessage(availability)}},
	}}
	return &Context{Store: st, Providers: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, f, st
}

const ctxTopic = "jarvis/nodes/n1/context/"

func askContext(t *testing.T, c *Context, op, payload string) map[string]any {
	t.Helper()
	topic, body := c.Handle(context.Background(), ctxTopic+op, []byte(payload))
	if want := ctxTopic + op + "/response/c1"; topic != want {
		t.Fatalf("response topic = %q, want %q", topic, want)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return out
}

func TestContextOperationsListsProviders(t *testing.T) {
	c, _, st := newContext(t)
	if err := st.SetEnabled("other_calendar", false); err != nil {
		t.Fatal(err)
	}
	_, b := c.Handle(context.Background(), ctxTopic+"operations", []byte(`{"correlation_id":"c1"}`))
	want := `{"ok":true,"providers":[{"command_name":"calendar","operations":[` + availability + `]}]}`
	if string(b) != want {
		t.Fatalf("operations = %s\nwant %s", b, want)
	}
}

func TestContextQueryRunsTheOperation(t *testing.T) {
	c, f, _ := newContext(t)
	got := askContext(t, c, "query", `{"correlation_id":"c1","operation":"availability","params":{"start":"9","end":"17"},"user_id":7}`)
	if got["ok"] != true || got["command_name"] != "calendar" || got["operation"] != "availability" {
		t.Fatalf("query = %v", got)
	}
	if d, _ := got["data"].(map[string]any); d["free"] != true {
		t.Fatalf("data = %v", got["data"])
	}
	if len(f.calls) != 1 || f.calls[0] != `calendar.availability {"start":"9","end":"17"}` || f.users[0] == nil || *f.users[0] != 7 {
		t.Fatalf("calls %v users %v", f.calls, f.users)
	}
}

func TestContextQueryNamedCommand(t *testing.T) {
	c, f, _ := newContext(t)
	askContext(t, c, "query", `{"correlation_id":"c1","command":"other_calendar","operation":"availability","params":{"start":"9","end":"17"},"user_id":"8"}`)
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "other_calendar.") || *f.users[0] != 8 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestContextQueryFailures(t *testing.T) {
	for name, tc := range map[string]struct{ payload, err, code string }{
		"no operation":       {`{"correlation_id":"c1"}`, "operation is required", ""},
		"operation a number": {`{"correlation_id":"c1","operation":5}`, "operation is required", ""},
		"params a list":      {`{"correlation_id":"c1","operation":"availability","params":[1]}`, "params must be an object", ""},
		"no provider":        {`{"correlation_id":"c1","operation":"weather"}`, "no installed command provides 'weather'", "no_provider"},
		"command lacks op":   {`{"correlation_id":"c1","command":"tell_joke","operation":"availability"}`, "no installed command provides 'availability'", "no_provider"},
		"not installed":      {`{"correlation_id":"c1","command":"nope","operation":"availability"}`, "command 'nope' not installed", ""},
		"missing params":     {`{"correlation_id":"c1","operation":"availability","params":{"end":"1"}}`, "missing required params: start", ""},
		"missing all":        {`{"correlation_id":"c1","operation":"availability"}`, "missing required params: start, end", ""},
	} {
		t.Run(name, func(t *testing.T) {
			c, f, _ := newContext(t)
			got := askContext(t, c, "query", tc.payload)
			if got["ok"] != false || got["error"] != tc.err {
				t.Fatalf("answer = %v, want error %q", got, tc.err)
			}
			if code, _ := got["code"].(string); code != tc.code {
				t.Fatalf("code = %q, want %q", code, tc.code)
			}
			if len(f.calls) != 0 {
				t.Fatalf("the operation ran: %v", f.calls)
			}
		})
	}
}

func TestContextQueryProviderRaises(t *testing.T) {
	c, f, _ := newContext(t)
	f.err = errors.New("token expired")
	got := askContext(t, c, "query", `{"correlation_id":"c1","operation":"availability","params":{"start":"9","end":"17"}}`)
	if got["ok"] != false || got["error"] != "calendar.availability failed: token expired" {
		t.Fatalf("answer = %v", got)
	}
	if f.users[0] != nil {
		t.Fatal("no user_id must reach the provider as no user")
	}
}

func TestContextAnswerTooLarge(t *testing.T) {
	c, f, _ := newContext(t)
	f.res = `{"ok":true,"data":{"blob":"` + strings.Repeat("x", MaxContextResponse) + `"},"error":null}`
	got := askContext(t, c, "query", `{"correlation_id":"c1","operation":"availability","params":{"start":"9","end":"17"}}`)
	if got["ok"] != false || got["error"] != "context result too large" {
		t.Fatalf("answer = %v", got)
	}
}

func TestContextCrashStillAnswers(t *testing.T) {
	c, _, _ := newContext(t)
	c.Providers = panicky{}
	got := askContext(t, c, "operations", `{"correlation_id":"c1"}`)
	if got["ok"] != false || !strings.HasPrefix(got["error"].(string), "context handler failed: ") {
		t.Fatalf("answer = %v", got)
	}
}

type panicky struct{}

func (panicky) ContextCommands() []ContextCommand { panic("sidecar gone") }
func (panicky) ContextQuery(context.Context, string, string, json.RawMessage, *int64) (Object, error) {
	panic("unreachable")
}

func TestContextNothingToAnswer(t *testing.T) {
	c, _, _ := newContext(t)
	for name, tc := range map[string]struct{ op, payload string }{
		"unknown op":        {"teleport", `{"correlation_id":"c1"}`},
		"not json":          {"query", `{`},
		"not an object":     {"query", `[1]`},
		"no correlation id": {"query", `{"operation":"availability"}`},
		"wildcard in id":    {"operations", `{"correlation_id":"a/#"}`},
	} {
		t.Run(name, func(t *testing.T) {
			if topic, body := c.Handle(context.Background(), ctxTopic+tc.op, []byte(tc.payload)); topic != "" || body != nil {
				t.Fatalf("answered %q %s; there is no one to answer", topic, body)
			}
		})
	}
}

func TestContextNoProviders(t *testing.T) {
	c := &Context{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	got := askContext(t, c, "operations", `{"correlation_id":"c1"}`)
	if got["ok"] != true || len(got["providers"].([]any)) != 0 {
		t.Fatalf("operations = %v", got)
	}
	got = askContext(t, c, "query", `{"correlation_id":"c1","operation":"availability"}`)
	if got["code"] != "no_provider" {
		t.Fatalf("query = %v", got)
	}
}
