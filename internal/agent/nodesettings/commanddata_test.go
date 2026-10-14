package nodesettings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The full contract is pinned against the Python node in internal/agent/sidecar
// (TestCommandDataMatchesThePythonNode, with a real sidecar). These cover the agent's own paths:
// the household's off switch, a sidecar that fails, payloads with nothing to answer.

type fakeData struct {
	cmds      []DataCommand
	sumErr    error
	createErr error
	creates   []json.Number
}

func (f *fakeData) DataCommands() []DataCommand { return f.cmds }

func (f *fakeData) Summaries(_ context.Context, _ string, recs []Object) ([]map[string]any, error) {
	if f.sumErr != nil {
		return nil, f.sumErr
	}
	out := make([]map[string]any, len(recs))
	for i, r := range recs {
		if t, _ := r.Value("t").(string); t != "" {
			out[i] = map[string]any{"title": t, "subtitle": nil, "icon": "x"}
		}
	}
	return out, nil
}

func (f *fakeData) Create(_ context.Context, _ string, v Object, uid json.Number) (string, json.RawMessage, error) {
	f.creates = append(f.creates, uid)
	if f.createErr != nil {
		return "", nil, f.createErr
	}
	b, _ := v.MarshalJSON()
	return "k1", json.RawMessage(strings.Replace(string(b), "{", `{"_secret":1,`, 1)), nil
}

func newCD(t *testing.T, f *fakeData) (*CommandData, *Store) {
	t.Helper()
	st, err := OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	return &CommandData{Store: st, Commands: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }}, st
}

func ask(t *testing.T, cd *CommandData, op, payload string) map[string]any {
	t.Helper()
	topic := "jarvis/nodes/n1/command-data/" + op
	reply, body := cd.Handle(context.Background(), topic, []byte(payload))
	if reply == "" {
		return nil
	}
	if !strings.HasPrefix(reply, topic+"/response/") {
		t.Fatalf("reply topic %q", reply)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return out
}

func errMsg(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	s, _ := e["message"].(string)
	return s
}

var notes = DataCommand{Name: "notes", Mode: "enabled", StorageName: "notes", SupportsCreate: true,
	Fields: json.RawMessage(`[{"name":"t","type":"string"},{"name":"ro","type":"string","editable":false}]`)}

func TestCommandDataHidesCommandsTheHouseholdTurnedOff(t *testing.T) {
	cd, st := newCD(t, &fakeData{cmds: []DataCommand{notes, {Name: "other", Mode: "enabled", StorageName: "other"}}})
	_ = st.SetEnabled("other", false)
	got := ask(t, cd, "commands", `{"correlation_id":"c"}`)
	if cmds := got["commands"].([]any); len(cmds) != 1 || cmds[0].(map[string]any)["command_name"] != "notes" {
		t.Fatalf("commands = %v", got)
	}
	if _, hasOK := got["ok"]; hasOK {
		t.Error("the Pi's commands answer has no ok key")
	}
	// As on the Pi, only the commands list hides it; its records stay reachable.
	if got := ask(t, cd, "schema", `{"correlation_id":"c","command_name":"other"}`); got["ok"] != true {
		t.Fatalf("schema of a turned-off command = %v", got)
	}
}

func TestCommandDataWithoutPackages(t *testing.T) {
	cd, _ := newCD(t, nil)
	cd.Commands = nil
	if got := ask(t, cd, "commands", `{"correlation_id":"c"}`); len(got["commands"].([]any)) != 0 {
		t.Fatalf("commands = %v", got)
	}
	if got := ask(t, cd, "list", `{"correlation_id":"c","command_name":"notes"}`); errMsg(got) != "command not found" {
		t.Fatalf("list = %v", got)
	}
}

// Nothing to answer: no correlation id, one that isn't a plain topic level, a payload that isn't
// an object, an op that doesn't exist. The Pi stays silent too (command-center then times out).
func TestCommandDataIgnoresWhatItCannotAnswer(t *testing.T) {
	cd, _ := newCD(t, &fakeData{cmds: []DataCommand{notes}})
	for op, p := range map[string]string{
		"list":   `{"command_name":"notes"}`,
		"get":    `{"correlation_id":"a/b","command_name":"notes","key":"x"}`,
		"schema": `["correlation_id"]`,
		"purge":  `{"correlation_id":"c"}`,
		"delete": `not json`,
	} {
		if reply, body := cd.Handle(context.Background(), "jarvis/nodes/n1/command-data/"+op, []byte(p)); reply != "" {
			t.Errorf("%s %s answered %s", op, p, body)
		}
	}
}

// A sidecar that can't summarise still lists the records, with the Pi's fallback row.
func TestCommandDataListFallsBackWhenSummariesFail(t *testing.T) {
	f := &fakeData{cmds: []DataCommand{notes}}
	cd, st := newCD(t, f)
	_ = st.SaveData("notes", "a", obj(t, `{"t":"A"}`), nil)
	_ = st.SaveData("notes", "b", obj(t, `{"x":1}`), nil)
	got := ask(t, cd, "list", `{"correlation_id":"c","command_name":"notes"}`)
	recs := got["records"].([]any)
	if s := recs[0].(map[string]any)["summary"].(map[string]any); s["title"] != "A" {
		t.Fatalf("summary = %v", s)
	}
	if s := recs[1].(map[string]any)["summary"].(map[string]any); s["title"] != "b" || s["icon"] != "alert-circle-outline" {
		t.Fatalf("fallback = %v", s)
	}
	f.sumErr = errors.New("sidecar gone")
	got = ask(t, cd, "list", `{"correlation_id":"c","command_name":"notes"}`)
	if s := got["records"].([]any)[0].(map[string]any)["summary"].(map[string]any); s["title"] != "a" {
		t.Fatalf("fallback without a sidecar = %v", s)
	}
}

func TestCommandDataCreate(t *testing.T) {
	f := &fakeData{cmds: []DataCommand{notes}}
	cd, _ := newCD(t, f)
	got := ask(t, cd, "create", `{"correlation_id":"c","command_name":"notes","data":{"t":"x","ro":"y","z":1},"requesting_user_id":7}`)
	rec := got["record"].(map[string]any)
	if got["ok"] != true || got["key"] != "k1" || rec["t"] != "x" || rec["ro"] != nil || rec["z"] != nil || rec["_secret"] != nil {
		t.Fatalf("create = %v", got)
	}
	// No user: the command gets null and refuses (the SDK fails closed).
	f.createErr = &ErrRejected{Message: "cannot create a record without a known user"}
	if got := ask(t, cd, "create", `{"correlation_id":"c","command_name":"notes","data":{}}`); errMsg(got) != "cannot create a record without a known user" {
		t.Fatalf("rejected = %v", got)
	}
	if f.creates[0] != "7" || f.creates[1] != "" {
		t.Fatalf("owners passed = %q", f.creates)
	}
	f.createErr = errors.New("sidecar gone")
	if got := ask(t, cd, "create", `{"correlation_id":"c","command_name":"notes","data":{}}`); errMsg(got) != "handler error: sidecar gone" {
		t.Fatalf("failure = %v", got)
	}
	if got := ask(t, cd, "create", `{"correlation_id":"c","command_name":"notes","data":{},"requesting_user_id":"7"}`); got["ok"] != false {
		t.Fatalf("a user id that isn't a number = %v", got)
	}
}

func TestCommandDataOwnership(t *testing.T) {
	cd, st := newCD(t, &fakeData{cmds: []DataCommand{notes}})
	_ = st.SaveData("notes", "mine", obj(t, `{"t":"m","user_id":7}`), nil)
	_ = st.SaveData("notes", "str", obj(t, `{"t":"s","user_id":"7"}`), nil)
	_ = st.SaveData("notes", "float", obj(t, `{"t":"f","user_id":7.0}`), nil)
	got := ask(t, cd, "list", `{"correlation_id":"c","command_name":"notes","requesting_user_id":7}`)
	var keys []string
	for _, r := range got["records"].([]any) {
		keys = append(keys, r.(map[string]any)["key"].(string))
	}
	// Python: 7 == 7.0, "7" != 7.
	if strings.Join(keys, ",") != "mine,float" {
		t.Fatalf("visible = %v", keys)
	}
	if got := ask(t, cd, "delete", `{"correlation_id":"c","command_name":"notes","key":"str","requesting_user_id":7}`); errMsg(got) != "record not found" {
		t.Fatalf("delete another's = %v", got)
	}
	if got := ask(t, cd, "update", `{"correlation_id":"c","command_name":"notes","key":"mine","patch":{"t":"M"},"requesting_user_id":7}`); got["record"].(map[string]any)["t"] != "M" {
		t.Fatalf("update = %v", got)
	}
}

func TestObjectKeepsOrder(t *testing.T) {
	o := obj(t, `{"b": 1, "a": {"y": [1, 2], "x": null}, "b": 2, "c": 1.50}`)
	if b, _ := o.MarshalJSON(); string(b) != `{"b":2,"a":{"y":[1,2],"x":null},"c":1.50}` {
		t.Fatalf("round trip = %s", b)
	}
	if n := obj(t, `null`); n.Len() != 0 {
		t.Fatal("null is an empty object")
	}
	var bad Object
	if err := json.Unmarshal([]byte(`[1]`), &bad); err == nil {
		t.Fatal("an array is not an object")
	}
	m, _ := ObjectOf(map[string]any{"z": 1, "a": "x"})
	if b, _ := m.MarshalJSON(); string(b) != `{"a":"x","z":1}` {
		t.Fatalf("ObjectOf = %s", b)
	}
}
