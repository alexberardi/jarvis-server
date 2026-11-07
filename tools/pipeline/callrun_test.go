package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/simline"
)

// call-report --recheck runs the checks on a record alone.
func TestRecordFactsRecheck(t *testing.T) {
	yes := true
	rec := &CallRecord{Request: "book a table", Expect: CallExpect{Achieved: &yes}, Number: "+17325550111", Ended: true, State: "done",
		Card:  inboxItem{Title: "✅ Call finished"},
		Lines: []callLine{{Who: "host", Text: "Can I get a callback number?"}, {Who: "jarvis", Text: "Call 7325550111."}}}
	for _, c := range runChecks(rec.Expect, rec.facts(), time.Second) {
		if !c.Pass {
			t.Errorf("%s: %s", c.Name, c.Detail) // the dialed number stands in for the contact: known, not invented
		}
	}
}

func TestCtxGateRunsOnlySharedDetailsTogether(t *testing.T) {
	g := &ctxGate{}
	g.acquire("a")
	g.acquire("a") // same details: at once
	got := make(chan bool)
	go func() { g.acquire("b"); got <- true }()
	select {
	case <-got:
		t.Fatal("b ran beside a")
	case <-time.After(50 * time.Millisecond):
	}
	g.release()
	g.release()
	<-got
	g.release()
}

// The callee routes turns by session and keeps the line quiet after [HOLD].
func TestCalleeSessionsAndHold(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []map[string]string `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		say := "Bright Smiles, this is Dana."
		if strings.Contains(req.Messages[0]["content"], "persona A") {
			say = "Can you hold one sec? [HOLD]"
			if strings.Contains(req.Messages[len(req.Messages)-1]["content"], "back on the line") {
				say = "Sorry about that, thanks for holding."
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": say}}}})
	}))
	defer model.Close()
	cl := &callee{tap: model.URL, model: "live", out: io.Discard}
	a := cl.expect("sa", "persona A", io.Discard)
	b := cl.expect("sb", "persona B", io.Discard)
	turn := func(session, event string, said ...string) simline.Reply {
		body, _ := json.Marshal(simline.Turn{SessionID: session, Event: event, Said: said})
		w := httptest.NewRecorder()
		cl.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/turn", bytes.NewReader(body)))
		var r simline.Reply
		_ = json.NewDecoder(w.Body).Decode(&r)
		return r
	}
	if r := turn("sa", "spoke", "Hi, I'd like a table"); r.Say != "Can you hold one sec?" {
		t.Errorf("A: %+v", r)
	}
	if r := turn("sb", "spoke", "Hi"); r.Say != "Bright Smiles, this is Dana." {
		t.Errorf("B: %+v", r)
	}
	if r := turn("sa", "spoke", "Sure"); r.Say != "" {
		t.Errorf("on hold, A spoke: %+v", r)
	}
	if r := turn("sa", "silence"); r.Say != "Sorry about that, thanks for holding." {
		t.Errorf("back from hold: %+v", r)
	}
	turn("sa", "ended")
	select {
	case <-a.ended:
	default:
		t.Error("A's end not seen")
	}
	select {
	case <-b.ended:
		t.Error("B ended with A")
	default:
	}
	lines, _ := cl.snapshot(a)
	if len(lines) != 5 || lines[0].Who != "jarvis" || lines[1].Who != "host" || lines[3].Who != "event" {
		t.Errorf("A's lines %+v", lines)
	}
}
