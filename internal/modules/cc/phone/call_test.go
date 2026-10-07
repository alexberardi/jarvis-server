package phone

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// A fake Twilio: it receives the dial, connects to the media WebSocket the way Twilio does
// (signed wss URL, start event with the session parameter), and plays caller audio.

var streamURLRE = regexp.MustCompile(`<Stream url="([^"]+)">`)

type twilioSim struct {
	t       *testing.T
	e       *env
	conn    *websocket.Conn
	wssURL  string
	session string
}

// answer waits for the dial and connects the media stream.
func (e *env) answer(t *testing.T, sessionID string) *twilioSim {
	t.Helper()
	var call startedCall
	select {
	case call = <-e.provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("no dial")
	}
	if call.to != "+17325924183" {
		t.Fatalf("dialed %q", call.to)
	}
	m := streamURLRE.FindStringSubmatch(call.twiml)
	if m == nil || !strings.HasPrefix(m[1], "wss://phone.example"+MediaPath) ||
		!strings.Contains(call.twiml, `<Parameter name="session_id" value="`+sessionID+`"/>`) {
		t.Fatalf("twiml: %s", call.twiml)
	}
	sim := &twilioSim{t: t, e: e, wssURL: m[1], session: sessionID}
	conn, resp, err := sim.dial(m[1], live.ComputeSignature(authToken, m[1], nil))
	if err != nil {
		t.Fatalf("ws dial: %v %v", err, resp)
	}
	sim.conn = conn
	t.Cleanup(func() { conn.Close() })
	sim.send(map[string]any{"event": "connected"})
	sim.send(map[string]any{"event": "start", "start": map[string]any{"streamSid": "MZ1", "callSid": "CA1",
		"customParameters": map[string]any{"session_id": sessionID}}})
	return sim
}

func (sim *twilioSim) dial(wss, sig string) (*websocket.Conn, *http.Response, error) {
	path := strings.TrimPrefix(wss, "wss://phone.example")
	h := http.Header{}
	if sig != "" {
		h.Set("X-Twilio-Signature", sig)
	}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(sim.e.srv.URL, "http")+path, h)
}

func (sim *twilioSim) send(v any) {
	sim.t.Helper()
	if err := sim.conn.WriteJSON(v); err != nil {
		sim.t.Fatal(err)
	}
}

// untilMark reads outbound messages until the named mark, returning how many media frames came.
func (sim *twilioSim) untilMark(name string) int {
	sim.t.Helper()
	frames := 0
	_ = sim.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var msg map[string]any
		if err := sim.conn.ReadJSON(&msg); err != nil {
			sim.t.Fatalf("waiting for mark %s: %v", name, err)
		}
		if msg["streamSid"] != "MZ1" {
			sim.t.Fatalf("stream sid: %v", msg)
		}
		switch msg["event"] {
		case "media":
			frames++
		case "mark":
			if msg["mark"].(map[string]any)["name"] == name {
				return frames
			}
		}
	}
}

func frame(amp float64) string {
	pcm := make([]int16, 160)
	for i := range pcm {
		pcm[i] = int16(amp * math.Sin(2*math.Pi*440*float64(i)/8000))
	}
	return base64.StdEncoding.EncodeToString(live.MulawEncode(pcm))
}

// speak plays one caller utterance: speech, then enough silence for the VAD to endpoint.
func (sim *twilioSim) speak() {
	sim.t.Helper()
	for i := 0; i < 10; i++ {
		sim.send(map[string]any{"event": "media", "media": map[string]any{"payload": frame(4000)}})
	}
	for i := 0; i < 45; i++ {
		sim.send(map[string]any{"event": "media", "media": map[string]any{"payload": frame(0)}})
	}
}

func (sim *twilioSim) ack(mark string) {
	sim.send(map[string]any{"event": "mark", "streamSid": "MZ1", "mark": map[string]any{"name": mark}})
}

// expectClosed waits for the server to close the stream.
func (sim *twilioSim) expectClosed() {
	sim.t.Helper()
	_ = sim.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := sim.conn.ReadMessage(); err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				sim.t.Fatal("stream not closed")
			}
			return
		}
	}
}

func (e *env) confirmed(t *testing.T) string {
	t.Helper()
	e.enable()
	id := e.draft()
	r := e.s.ConfirmCall(context.Background(), CallbackContext{HouseholdID: hh, UserID: 2, Data: confirmData(id, "732-592-4183")})
	if !r.Success || r.ContextData["inbox"].(map[string]any)["title"] != "📞 Calling Tony's Pizzeria…" {
		t.Fatalf("confirm: %+v", r)
	}
	return id
}

func (e *env) waitState(t *testing.T, id, state string) *Session {
	t.Helper()
	var s *Session
	waitFor(t, "state "+state, func() bool { s = e.session(id); return s.State == state })
	return s
}

func TestLiveCallEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.s.Now = nil // real time: the call measures its own duration
	e.stt.heard = []string{"Thanks for calling Tony's, how can I help?", "Sure, a large pie, ready in 20 minutes. Goodbye!"}
	e.llm.replies = []string{
		"<think>plan the order</think>Hi, I'd like to order a large cheese pie for pickup. ",
		"Great, thank you! [OUTCOME: large cheese pie ready in 20 minutes] Goodbye. [HANGUP]",
	}
	e.llm.assess = `{"summary": "Ordered a large pie for pickup in 20 minutes.", "goal_achieved": true}`
	id := e.confirmed(t)
	sim := e.answer(t, id)

	if n := sim.untilMark("t0"); n == 0 {
		t.Fatal("no disclosure audio")
	}
	if got := e.tts.spoken()[0]; got != "Hi, I'm an automated AI assistant calling on behalf of Alex. This call may be recorded." {
		t.Fatalf("disclosure: %q", got)
	}
	s := e.waitState(t, id, StateInCall)
	if s.CallSID != "CA1" || s.InCallAt.IsZero() {
		t.Fatalf("in_call: %+v", s)
	}

	sim.speak()
	if sim.untilMark("t1") == 0 {
		t.Fatal("no reply audio")
	}
	sim.speak()
	sim.untilMark("t2")
	sim.ack("t2") // playback finished: the agent hangs up
	sim.expectClosed()

	s = e.waitState(t, id, StateDone)
	e.s.Wait()
	var turns []map[string]any
	if err := json.Unmarshal([]byte(s.TranscriptJSON), &turns); err != nil || len(turns) != 2 {
		t.Fatalf("transcript: %s", s.TranscriptJSON)
	}
	if turns[0]["said"] != "Hi, I'd like to order a large cheese pie for pickup." ||
		turns[1]["heard"] != "Sure, a large pie, ready in 20 minutes. Goodbye!" {
		t.Fatalf("turns: %v", turns)
	}
	if ev := turns[1]["events"].([]any); len(ev) != 2 || ev[0] != "outcome" || ev[1] != "hangup" {
		t.Fatalf("events: %v", ev)
	}
	var outcome map[string]any
	_ = json.Unmarshal([]byte(s.OutcomeJSON), &outcome)
	if outcome["goal_achieved"] != true || outcome["turns"] != float64(2) ||
		outcome["facts"].([]any)[0] != "large cheese pie ready in 20 minutes" {
		t.Fatalf("outcome: %v", outcome)
	}
	if s.Duration == nil {
		t.Fatal("duration not measured")
	}
	// Think blocks are never spoken; the live model saw the disclosure as its first turn.
	for _, txt := range e.tts.spoken() {
		if strings.Contains(txt, "plan the order") {
			t.Fatal("think content spoken")
		}
	}
	first := e.llm.streams[0]
	if *first[1].Content.Text != "Hi, I'm an automated AI assistant calling on behalf of Alex. This call may be recorded." ||
		*first[2].Content.Text != "Thanks for calling Tony's, how can I help? /no_think" {
		t.Fatalf("messages: %v %v", *first[1].Content.Text, *first[2].Content.Text)
	}
	c := e.notify.titled("✅ Call finished: Tony's Pizzeria")
	if len(c) != 1 || c[0].Summary != "Ordered a large pie for pickup in 20 minutes." ||
		!strings.Contains(c[0].Body, "What the business said:\n- large cheese pie ready in 20 minutes") {
		t.Fatalf("outcome card: %v", e.notify.all())
	}
	// Auto-save: the existing contact is refreshed, never duplicated.
	var n int
	var verified string
	_ = e.d.Read.QueryRow(`SELECT COUNT(*), MAX(verified_at) FROM cc_phone_contacts WHERE household_id = ?`, hh).Scan(&n, &verified)
	if n != 1 || verified == "" {
		t.Fatalf("contacts=%d verified=%q", n, verified)
	}
	waitFor(t, "errand resume", func() bool {
		e.errands.mu.Lock()
		defer e.errands.mu.Unlock()
		return len(e.errands.snaps) == 1 && e.errands.snaps[0].State == StateDone
	})
}

func TestMediaGates(t *testing.T) {
	e := newEnv(t)
	e.s.StreamStartTimeout = 500 * time.Millisecond
	id := e.confirmed(t)
	call := <-e.provider.started
	wss := streamURLRE.FindStringSubmatch(call.twiml)[1]
	sim := &twilioSim{t: t, e: e}

	// A bad signature is refused before the upgrade, and the token is spent.
	if _, resp, err := sim.dial(wss, "bogus"); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad signature: %v", err)
	}
	if _, resp, err := sim.dial(wss, live.ComputeSignature(authToken, wss, nil)); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("replayed token accepted: %v", err)
	}
	// No stream → the call fails honestly and is hung up.
	s := e.waitState(t, id, StateFailed)
	if s.ErrorMessage != "no media stream within 60s (no answer?)" {
		t.Fatalf("reason: %q", s.ErrorMessage)
	}
	if c := e.notify.titled("⚠️ Call failed: Tony's Pizzeria"); len(c) != 1 || c[0].Summary != s.ErrorMessage {
		t.Fatalf("failure card: %v", e.notify.all())
	}
	e.s.Wait()
	if got := e.provider.endedCalls(); len(got) != 1 || got[0] != "CA1" {
		t.Fatalf("ended: %v", got)
	}
}

func TestStreamBindingMismatchCloses(t *testing.T) {
	e := newEnv(t)
	e.s.StreamStartTimeout = 300 * time.Millisecond
	id := e.confirmed(t)
	call := <-e.provider.started
	wss := streamURLRE.FindStringSubmatch(call.twiml)[1]
	sim := &twilioSim{t: t, e: e}
	conn, _, err := sim.dial(wss, live.ComputeSignature(authToken, wss, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sim.conn = conn
	sim.send(map[string]any{"event": "start", "start": map[string]any{"streamSid": "MZ1", "callSid": "CA-other",
		"customParameters": map[string]any{"session_id": id}}})
	_, _, err = conn.ReadMessage()
	if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != 4403 {
		t.Fatalf("close: %v", err)
	}
	e.waitState(t, id, StateFailed)
}

func TestDisclosureIsAHardGate(t *testing.T) {
	e := newEnv(t)
	e.tts.fail = true
	id := e.confirmed(t)
	s := e.waitState(t, id, StateFailed)
	if s.ErrorMessage != "disclosure synthesis failed (TTS down?)" {
		t.Fatalf("reason: %q", s.ErrorMessage)
	}
	select {
	case <-e.provider.started:
		t.Fatal("dialed without a disclosure")
	default:
	}
}

func TestCancelLiveCall(t *testing.T) {
	e := newEnv(t)
	e.llm.assess = `{"summary": "Nothing happened.", "goal_achieved": false}`
	id := e.confirmed(t)
	sim := e.answer(t, id)
	sim.untilMark("t0")
	e.waitState(t, id, StateInCall)

	// D40 11.Q3: "End the call" really ends it.
	r := e.s.CancelCall(context.Background(), CallbackContext{HouseholdID: hh, UserID: 1, Data: map[string]any{"session_id": id}})
	if !r.Success {
		t.Fatalf("cancel: %+v", r)
	}
	s := e.session(id)
	if s.State != StateFailed || s.ErrorMessage != "cancelled by user" {
		t.Fatalf("session: %+v", s)
	}
	sim.expectClosed()
	e.s.Wait()
	if got := e.provider.endedCalls(); len(got) == 0 || got[0] != "CA1" {
		t.Fatalf("not hung up: %v", got)
	}
	// The late wrap-up outcome is stored but posts no second card (D40 11.Q11).
	if len(e.notify.titled("⚠️ Call finished")) != 0 || len(e.notify.titled("✅ Call finished")) != 0 {
		t.Fatalf("unexpected card: %v", e.notify.all())
	}
	if e.session(id).OutcomeJSON == "" {
		t.Fatal("late outcome not stored")
	}
}

func TestLateSuccessPostsCorrectiveCard(t *testing.T) {
	e := newEnv(t)
	id := e.draft()
	if _, err := e.d.Write.Exec(`UPDATE cc_phone_call_sessions SET state = 'failed' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := e.s.recordOutcome(context.Background(), id, map[string]any{"summary": "Booked.", "goal_achieved": true}, ""); err != nil {
		t.Fatal(err)
	}
	c := e.notify.titled("✅ Call finished")
	if len(c) != 1 || c[0].Summary != "Actually, the call finished: Booked." || e.session(id).State != StateFailed {
		t.Fatalf("cards: %v", e.notify.all())
	}
}

func TestEscalationRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.stt.heard = []string{"What time would you like?"}
	e.llm.replies = []string{
		"Let me check on that. [ESCALATE: what time works for the pickup?]",
		"Six PM works, thank you.",
	}
	id := e.confirmed(t)
	sim := e.answer(t, id)
	sim.untilMark("t0")
	e.waitState(t, id, StateInCall)
	sim.speak()

	var card sentCard
	waitFor(t, "escalation card", func() bool {
		c := e.notify.titled("📞 The call needs your input")
		if len(c) == 1 {
			card = c[0]
		}
		return len(c) == 1
	})
	if card.Summary != `They asked: "what time works for the pickup?"` || card.Metadata["editor_schema"] != 2 {
		t.Fatalf("card: %+v", card)
	}
	// A member of another household can't answer it.
	if r := e.s.AnswerEscalation(context.Background(), CallbackContext{HouseholdID: otherHH, UserID: 9,
		Data: map[string]any{"session_id": id, "answer": "now"}}); r.Success {
		t.Fatal("cross-household answer delivered")
	}
	r := e.s.AnswerEscalation(context.Background(), CallbackContext{HouseholdID: hh, UserID: 1,
		Data: map[string]any{"session_id": id, "answer": "6pm"}})
	if !r.Success {
		t.Fatalf("answer: %+v", r)
	}
	sim.untilMark("t1")
	waitFor(t, "second generation", func() bool { e.llm.mu.Lock(); defer e.llm.mu.Unlock(); return len(e.llm.streams) == 2 })
	msgs := e.llm.streams[1]
	if got := *msgs[len(msgs)-1].Content.Text; got != "[Alex answered your question: 6pm]" {
		t.Fatalf("answer message: %q", got)
	}
	// No window open now: a late tap is told the call may have ended.
	if r := e.s.AnswerEscalation(context.Background(), CallbackContext{HouseholdID: hh, UserID: 1,
		Data: map[string]any{"session_id": id, "answer": "7pm"}}); r.Error != "Couldn't reach the call — it may have just ended." {
		t.Fatalf("late answer: %+v", r)
	}
}

func TestGuardSuppressesUnaskedSecret(t *testing.T) {
	e := newEnv(t)
	e.set(SettingCallContext, `{"fields":[{"key":"insurance_member_id","value":"XZ-9912345"}]}`, settings.Scope{UserID: 1})
	e.stt.heard = []string{"Can I get a name for the order?"}
	e.llm.replies = []string{"It's for Alex. The member ID is XZ-9912345."}
	e.llm.verdict = "NONE"
	id := e.confirmed(t)
	sim := e.answer(t, id)
	sim.untilMark("t0")
	e.waitState(t, id, StateInCall)
	sim.speak()
	sim.untilMark("t1")
	for _, txt := range e.tts.spoken() {
		if strings.Contains(txt, "9912345") || strings.Contains(txt, "9, 9, 1, 2") {
			t.Fatalf("secret reached TTS: %q", txt)
		}
	}
	waitFor(t, "turn record", func() bool { return e.session(id).TranscriptJSON != "" })
	var turns []map[string]any
	_ = json.Unmarshal([]byte(e.session(id).TranscriptJSON), &turns)
	if turns[0]["said"] != "It's for Alex." || !strings.Contains(e.session(id).TranscriptJSON, "guard_suppressed") {
		t.Fatalf("turn: %v", turns[0])
	}
	if e.llm.classifys != 1 {
		t.Fatalf("classifier calls: %d", e.llm.classifys)
	}
}

func TestMaxCallSecondsHangsUp(t *testing.T) {
	e := newEnv(t)
	e.set(SettingMaxCallSeconds, int64(1), settings.Scope{HouseholdID: hh})
	e.llm.assess = `{"summary": "Ran out of time.", "goal_achieved": false}`
	id := e.confirmed(t)
	sim := e.answer(t, id)
	sim.untilMark("t0")
	waitFor(t, "REST hang-up", func() bool { return len(e.provider.endedCalls()) == 1 })
	sim.send(map[string]any{"event": "stop"}) // what Twilio does after a REST hang-up
	sim.expectClosed()
	s := e.waitState(t, id, StateDone)
	if c := e.notify.titled("⚠️ Call finished: Tony's Pizzeria"); len(c) != 1 || s.Duration == nil {
		t.Fatalf("outcome: %v", e.notify.all())
	}
}

func TestGateOffEndsLiveCall(t *testing.T) {
	e := newEnv(t)
	e.s.HeartbeatInterval = 20 * time.Millisecond
	id := e.confirmed(t)
	sim := e.answer(t, id)
	sim.untilMark("t0")
	e.waitState(t, id, StateInCall)
	e.set(SettingEnabled, false, settings.Scope{HouseholdID: hh})
	s := e.waitState(t, id, StateFailed)
	if s.ErrorMessage != "phone calls were turned off" {
		t.Fatalf("reason: %q", s.ErrorMessage)
	}
	sim.expectClosed()
}
