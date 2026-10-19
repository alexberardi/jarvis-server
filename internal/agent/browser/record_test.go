package browser

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// sendRaw writes one JSON line as the fake extension, for messages Message has no fields for.
func (f *fakeExt) sendRaw(t *testing.T, m map[string]any) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.c.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

type sinkLog struct {
	mu  sync.Mutex
	evs []RecordEvent
}

func (s *sinkLog) add(e RecordEvent) {
	s.mu.Lock()
	s.evs = append(s.evs, e)
	s.mu.Unlock()
}

func (s *sinkLog) all() []RecordEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RecordEvent(nil), s.evs...)
}

func recordEvent(seq int, kind string, el map[string]any, extra map[string]any) map[string]any {
	m := map[string]any{"type": "event", "event": "record", "recording_id": "rec_1", "seq": seq, "ts": 1700000000000 + seq,
		"kind": kind, "url": "https://bank.test/login", "tab": 4}
	if el != nil {
		m["element"] = el
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestRecordEventsReachSink(t *testing.T) {
	h, path := startHub(t)
	var sink sinkLog
	h.SetRecordSink(sink.add)
	f := connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		if m.Cmd == "record_stop" {
			return ok(map[string]any{"events": 3})
		}
		return ok(map[string]any{"ok": true})
	})
	ts := NewTools(h)
	ctx := context.Background()
	if err := ts.RecordStart(ctx, "rec_1"); err != nil {
		t.Fatal(err)
	}
	f.sendRaw(t, recordEvent(1, "input", map[string]any{"role": "textbox", "name": "Username", "tag": "input", "type": "text",
		"selector": "#user", "frame": 0}, map[string]any{"value": "avery"}))
	f.sendRaw(t, recordEvent(2, "click", map[string]any{"role": "button", "name": "Sign in", "tag": "button", "type": "submit",
		"selector": `form "Login" > button "Sign in"`, "frame": 2}, nil))
	f.sendRaw(t, recordEvent(3, "navigate", nil, map[string]any{"url": "https://bank.test/accounts"}))
	waitFor(t, func() bool { return len(sink.all()) == 3 })
	n, err := ts.RecordStop(ctx, "rec_1")
	if err != nil || n != 3 {
		t.Fatalf("stop: %d, %v", n, err)
	}

	evs := sink.all()
	if e := evs[0]; e.RecordingID != "rec_1" || e.Seq != 1 || e.Kind != "input" || e.Value != "avery" || e.Browser != "firefox" ||
		e.Tab != 4 || e.TS != 1700000000001 || e.Element == nil || e.Element.Selector != "#user" || e.Element.Name != "Username" {
		t.Errorf("input event %+v (element %+v)", e, e.Element)
	}
	if e := evs[1]; e.Kind != "click" || e.Element.Role != "button" || e.Element.Frame != 2 || e.Element.Tag != "button" {
		t.Errorf("click event %+v (element %+v)", e, e.Element)
	}
	if e := evs[2]; e.Kind != "navigate" || e.URL != "https://bank.test/accounts" || e.Element != nil {
		t.Errorf("navigate event %+v", e)
	}
	cmds := f.received()
	if len(cmds) != 2 || cmds[0].Cmd != "record_start" || cmds[0].Args["recording_id"] != "rec_1" ||
		cmds[1].Cmd != "record_stop" || cmds[1].Args["recording_id"] != "rec_1" {
		t.Errorf("sent %+v", cmds)
	}

	// Without a sink, record events are dropped (and focus events still work).
	h.SetRecordSink(nil)
	f.sendRaw(t, recordEvent(4, "click", nil, nil))
	f.send(Message{Type: "event", Event: "focus", Focused: false})
	waitFor(t, func() bool { return !h.Browsers()[0].Focused })
	if len(sink.all()) != 3 {
		t.Error("an event reached a removed sink")
	}
}

// Defence in depth: the extension masks secret fields, and the agent drops a value it sent
// anyway, whatever marks the field as secret.
func TestMaskedValueNeverForwarded(t *testing.T) {
	h, path := startHub(t)
	var sink sinkLog
	h.SetRecordSink(sink.add)
	f := connectExt(t, h, path, "Chrome", true, nil)
	el := func(name, typ, auto string) map[string]any {
		return map[string]any{"role": "textbox", "name": name, "tag": "input", "type": typ, "autocomplete": auto, "selector": "#f"}
	}
	secret := []map[string]any{
		recordEvent(1, "input", el("Password", "password", ""), map[string]any{"value": "hunter2"}),
		recordEvent(2, "input", el("Code", "text", ""), map[string]any{"value": "123456", "masked": true}),
		recordEvent(3, "change", el("Number", "text", "billing cc-number"), map[string]any{"value": "4111111111111111"}),
		recordEvent(4, "input", el("Code", "text", "one-time-code"), map[string]any{"value": "654321"}),
		recordEvent(5, "input", el("Password", "text", "current-password"), map[string]any{"value": "hunter2"}),
		recordEvent(6, "input", el("New", "text", "new-password"), map[string]any{"value": "hunter3"}),
		recordEvent(7, "input", el("", "hidden", ""), map[string]any{"value": "csrf"}),
		recordEvent(8, "input", el("Card number", "text", ""), map[string]any{"value": "4111111111111111"}),
		recordEvent(9, "input", el("CVV", "tel", ""), map[string]any{"value": "123"}),
		recordEvent(10, "input", el("SSN", "text", ""), map[string]any{"value": "078-05-1120"}),
		recordEvent(11, "input", el("Your PIN", "tel", ""), map[string]any{"value": "1234"}),
		recordEvent(12, "input", el("Security code", "text", ""), map[string]any{"value": "999"}),
	}
	for _, m := range secret {
		f.sendRaw(t, m)
	}
	f.sendRaw(t, recordEvent(13, "input", el("Shipping name", "text", ""), map[string]any{"value": "Avery"}))
	waitFor(t, func() bool { return len(sink.all()) == len(secret)+1 })
	for _, e := range sink.all() {
		if e.Seq == 13 {
			if e.Value != "Avery" || e.Masked {
				t.Errorf("an ordinary field lost its value: %+v", e)
			}
			continue
		}
		if e.Value != "" || !e.Masked {
			t.Errorf("seq %d (%s, %+v): value %q, masked %v", e.Seq, e.Kind, e.Element, e.Value, e.Masked)
		}
	}
}

func TestRecordOutdatedExtension(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		return Message{Code: "unknown_command", Error: "unknown command " + m.Cmd}
	})
	ts := NewTools(h)
	err := ts.RecordStart(context.Background(), "rec_1")
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "extension_outdated" {
		t.Fatalf("got %v", err)
	}
	if err := ts.RecordStart(context.Background(), "bad id!"); !errors.Is(err, errBadArgs) {
		t.Errorf("bad recording id: %v", err)
	}
}
