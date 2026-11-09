package phone

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
)

var fuEastern = time.FixedZone("EST", -5*3600)

// Sunday 2026-11-08, 10:00 in the household's zone.
var fuNow = time.Date(2026, 11, 8, 10, 0, 0, 0, fuEastern)

func calendarCand() FollowUpCandidate {
	return FollowUpCandidate{NodeID: "n1", Command: "add_event", Callback: "create_event", Label: "Add to Calendar",
		When: "the call booked an appointment", IdempotencyParam: "idempotency_key", Params: []FollowUpParam{
			{Name: "title", Type: "string", Required: true},
			{Name: "start", Type: "datetime", Required: true},
			{Name: "duration_minutes", Type: "int"},
			{Name: "idempotency_key", Type: "string", Required: true},
		}}
}

func reminderCand() FollowUpCandidate {
	return FollowUpCandidate{NodeID: "n2", Command: "reminder", Callback: "set_at", Label: "Remind me",
		When: "there is something to do at a time", Params: []FollowUpParam{
			{Name: "text", Type: "string", Required: true},
			{Name: "at", Type: "datetime", Required: true},
		}}
}

var fuCall = []string{
	"Hi, I'm an automated AI assistant calling on behalf of Alex. This call may be recorded.",
	"Dr. House Family Practice, how can I help you?",
	"How about Wednesday at 1:30 p.m.?",
	"Wednesday at 1:30 p.m. works perfectly.",
}

func pick(action int, params map[string]any) followUpPick {
	return followUpPick{Action: action, Params: params}
}

func TestFollowUpsKeepAGroundedBooking(t *testing.T) {
	got := checkFollowUps([]followUpPick{pick(1, map[string]any{
		"title": "Physical at Dr. House Family Practice", "start": "2026-11-11T13:30:00-05:00", "duration_minutes": float64(30),
	})}, []FollowUpCandidate{calendarCand()}, fuCall, fuNow, nil, "s1")
	if len(got) != 1 {
		t.Fatalf("kept %d", len(got))
	}
	f := got[0]
	if f.Key != "call:s1:1" || f.Label != "Add to Calendar · Wed Nov 11, 1:30 PM" || f.NodeID != "n1" || f.BlastTier != "reversible" {
		t.Fatalf("follow-up: %+v", f)
	}
	if f.Params["start"] != "2026-11-11T13:30:00-05:00" || f.Params["duration_minutes"] != int64(30) ||
		f.Params["idempotency_key"] != "call:s1:1" {
		t.Fatalf("params: %v", f.Params)
	}
}

func TestFollowUpsDropTimesNobodySaid(t *testing.T) {
	for name, start := range map[string]string{
		"another time":      "2026-11-11T14:00:00-05:00",
		"another day":       "2026-11-12T13:30:00-05:00",
		"next week's":       "2026-11-18T13:30:00-05:00",
		"not a time at all": "Wednesday afternoon",
	} {
		got := checkFollowUps([]followUpPick{pick(1, map[string]any{"title": "Physical", "start": start})},
			[]FollowUpCandidate{calendarCand()}, fuCall, fuNow, nil, "s1")
		if len(got) != 0 {
			t.Errorf("%s (%s) kept: %+v", name, start, got)
		}
	}
}

func TestFollowUpsAllowAReminderBeforeASaidTime(t *testing.T) {
	cands := []FollowUpCandidate{reminderCand()}
	for at, want := range map[string]bool{
		"2026-11-10T18:00:00-05:00": true,  // the evening before
		"2026-11-11T12:30:00-05:00": true,  // an hour before
		"2026-11-11T13:30:00-05:00": true,  // at the time
		"2026-11-08T09:00:00-05:00": false, // already past
		"2026-11-09T09:00:00-05:00": false, // more than two days before
		"2026-11-11T15:00:00-05:00": false, // after it
	} {
		got := checkFollowUps([]followUpPick{pick(1, map[string]any{"text": "Physical at 1:30", "at": at})},
			cands, fuCall, fuNow, nil, "s1")
		if (len(got) == 1) != want {
			t.Errorf("reminder at %s: kept=%v, want %v", at, len(got) == 1, want)
		}
	}
}

func TestFollowUpsResolveTomorrow(t *testing.T) {
	lines := []string{"Okay, I can do tomorrow at one o'clock, does that work?"}
	got := checkFollowUps([]followUpPick{pick(1, map[string]any{"title": "Brakes", "start": "2026-11-09T13:00:00-05:00"})},
		[]FollowUpCandidate{calendarCand()}, lines, fuNow, nil, "s1")
	if len(got) != 1 {
		t.Fatal("tomorrow at one o'clock did not ground Monday 1 PM")
	}
}

func TestFollowUpsCheckParams(t *testing.T) {
	start := "2026-11-11T13:30:00-05:00"
	restricted := []live.RestrictedField{{Key: "insurance_member_id", Label: "Insurance member ID", Value: "W123456789"}}
	for name, params := range map[string]map[string]any{
		"undeclared param":       {"title": "Physical", "start": start, "notes": "x"},
		"missing required":       {"start": start},
		"wrong type":             {"title": "Physical", "start": start, "duration_minutes": "thirty"},
		"fraction for an int":    {"title": "Physical", "start": start, "duration_minutes": 30.5},
		"private detail in text": {"title": "Physical, member W123456789", "start": start},
		"overlong string":        {"title": strings.Repeat("x", 201), "start": start},
	} {
		got := checkFollowUps([]followUpPick{pick(1, params)}, []FollowUpCandidate{calendarCand()}, fuCall, fuNow, restricted, "s1")
		if len(got) != 0 {
			t.Errorf("%s kept: %+v", name, got[0].Params)
		}
	}
}

func TestFollowUpsAtMostThreeOncePerAction(t *testing.T) {
	cands := []FollowUpCandidate{calendarCand(), reminderCand(), reminderCand(), reminderCand()}
	cands[2].Callback, cands[3].Callback = "set_at_2", "set_at_3"
	rem := map[string]any{"text": "Physical", "at": "2026-11-10T18:00:00-05:00"}
	cal := map[string]any{"title": "Physical", "start": "2026-11-11T13:30:00-05:00"}
	got := checkFollowUps([]followUpPick{pick(1, cal), pick(1, cal), pick(9, cal), pick(2, rem), pick(3, rem), pick(4, rem)},
		cands, fuCall, fuNow, nil, "s1")
	if len(got) != 3 || got[0].Command != "add_event" || got[1].Callback != "set_at" || got[2].Callback != "set_at_2" {
		t.Fatalf("got %+v", got)
	}
	if got[1].Key != "call:s1:2" || got[1].Label != "Remind me · Tue Nov 10, 6:00 PM" {
		t.Fatalf("second: %+v", got[1])
	}
}

func TestParseFollowUpPicks(t *testing.T) {
	got := parseFollowUpPicks("Sure.\n```json\n{\"followups\": [{\"action\": 2, \"params\": {\"text\": \"x\"}}]}\n```")
	if len(got) != 1 || got[0].Action != 2 || got[0].Params["text"] != "x" {
		t.Fatalf("got %+v", got)
	}
	for _, junk := range []string{"", "none", "{not json}", `{"followups": "all of them"}`} {
		if got := parseFollowUpPicks(junk); len(got) != 0 {
			t.Errorf("%q: %+v", junk, got)
		}
	}
}

func TestFollowUpElementsAreDispatcherConfirms(t *testing.T) {
	els := followUpElements([]FollowUp{{Key: "call:s1:1", NodeID: "n1", Command: "add_event", Callback: "create_event",
		Label: "Add to Calendar · Wed Nov 11, 1:30 PM", BlastTier: "reversible",
		Params: map[string]any{"title": "Physical", "duration_minutes": int64(30)}}})
	el := els[0].(map[string]any)
	if el["command"] != ProposableCommand || el["callback"] != "execute" || el["target"] != "server" || el["kind"] != "confirm" {
		t.Fatalf("element: %v", el)
	}
	data := el["data"].(map[string]any)
	a := data["_action"].(map[string]any)
	if a["target_command"] != "add_event" || a["target_callback"] != "create_event" || a["node_id"] != "n1" ||
		a["idempotency_key"] != "call:s1:1" || data["title"] != "Physical" || data["duration_minutes"] != "30" {
		t.Fatalf("data: %v", data)
	}
}

// A call with a follow-up candidate: the model's pick is checked, stored with the outcome, shown
// as the card's button, and recognised by the dispatcher's gate for that action only.
func TestCallFollowUpsReachTheCard(t *testing.T) {
	e := newEnv(t)
	cand := FollowUpCandidate{NodeID: "n1", Command: "add_event", Callback: "create_event", Label: "Add to Calendar",
		When: "the call booked a time", Params: []FollowUpParam{{Name: "title", Type: "string", Required: true},
			{Name: "start", Type: "datetime", Required: true}}}
	e.s.FollowUpCandidates = func(context.Context, string) []FollowUpCandidate { return []FollowUpCandidate{cand} }
	e.stt.heard = []string{"Thanks for calling Tony's, how can I help?", "Sure, I can book you tomorrow at 2 pm. Goodbye!"}
	e.llm.replies = []string{"Hi, I'd like to book a table for two. ", "Great, thank you! [OUTCOME: table tomorrow at 2 pm] Goodbye. [HANGUP]"}
	e.llm.assess = `{"summary": "Booked a table for tomorrow at 2 pm.", "goal_achieved": true}`
	// The env's clock is Tuesday 2026-10-06 12:00 UTC: tomorrow at 2 pm is Wednesday 14:00.
	e.llm.followups = `{"followups": [{"action": 1, "params": {"title": "Table at Tony's", "start": "2026-10-07T14:00:00Z"}},
		{"action": 1, "params": {"title": "Again", "start": "2026-10-07T14:00:00Z"}}]}`
	id := e.confirmed(t)
	sim := e.answer(t, id)
	sim.untilMark("t0")
	e.waitState(t, id, StateInCall)
	sim.speak()
	sim.untilMark("t1")
	sim.speak()
	sim.untilMark("t2")
	sim.ack("t2")
	sim.expectClosed()
	s := e.waitState(t, id, StateDone)
	e.s.Wait()

	if len(e.llm.picks) != 1 {
		t.Fatalf("picks: %d", len(e.llm.picks))
	}
	c := e.notify.titled("✅ Call finished: Tony's Pizzeria")
	if len(c) != 1 {
		t.Fatalf("cards: %v", e.notify.all())
	}
	b, _ := json.Marshal(c[0].Metadata["interactive_elements"])
	var els []map[string]any
	_ = json.Unmarshal(b, &els)
	if len(els) != 1 || els[0]["label"] != "Add to Calendar · Wed Oct 7, 2:00 PM" {
		t.Fatalf("buttons: %s", b)
	}
	key := "call:" + id + ":1"
	ctx := context.Background()
	if !e.s.IssuedFollowUp(ctx, hh, key, "add_event", "create_event", "n1") {
		t.Fatalf("issued key not recognised; outcome %s", s.OutcomeJSON)
	}
	for name, ok := range map[string]bool{
		"another household": e.s.IssuedFollowUp(ctx, "other-hh", key, "add_event", "create_event", "n1"),
		"another command":   e.s.IssuedFollowUp(ctx, hh, key, "unlock_door", "create_event", "n1"),
		"another node":      e.s.IssuedFollowUp(ctx, hh, key, "add_event", "create_event", "n2"),
		"another key":       e.s.IssuedFollowUp(ctx, hh, "call:"+id+":2", "add_event", "create_event", "n1"),
		"not a call key":    e.s.IssuedFollowUp(ctx, hh, "proposal-123", "add_event", "create_event", "n1"),
	} {
		if ok {
			t.Errorf("%s passed the gate", name)
		}
	}
}

func TestFollowUpsIdempotencyKeyIsTheServers(t *testing.T) {
	got := checkFollowUps([]followUpPick{pick(1, map[string]any{"title": "Physical", "start": "2026-11-11T13:30:00-05:00",
		"idempotency_key": "model_made_this_up"})}, []FollowUpCandidate{calendarCand()}, fuCall, fuNow, nil, "s1")
	if len(got) != 1 || got[0].Params["idempotency_key"] != "call:s1:1" {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(followUpInstruction([]FollowUpCandidate{calendarCand()}, "", fuNow), "idempotency_key") {
		t.Fatal("the prompt offers the idempotency param to the model")
	}
}
