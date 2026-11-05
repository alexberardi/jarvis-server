package phone

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeCalendar struct{ ans AvailabilityAnswer }

func (f fakeCalendar) Availability(context.Context, string, *int64, time.Time, time.Time) (AvailabilityAnswer, error) {
	return f.ans, nil
}

type fakeNodeContext struct{ fields []ContextField }

func (f fakeNodeContext) CallContext(context.Context, string, *int64, string, string) []ContextField {
	return f.fields
}

// A plan carries what the household's commands share: the calendar's windows as the times
// envelope, a command's call_context fields in the brief, both credited on the card; the
// give-if-asked ones stay with the session for the guard, while still in the details.
func TestPlanCarriesNodeContext(t *testing.T) {
	e := newEnv(t)
	e.addContact("Bright Smiles Dental", "+17325550199", false)
	e.s.Calendar = fakeCalendar{AvailabilityAnswer{OK: true, Free: []string{"Thu Nov 12 2-5pm"},
		Busy: []string{"Tue Nov 10 9am-5pm (Work)"}, Source: "get_calendar_events",
		SourceDescription: "Your free and busy times for the next week."}}
	e.s.NodeContext = fakeNodeContext{[]ContextField{
		{Key: "dental_member_id", Label: "Dental member ID", Value: "DX-77120", Tier: TierIfAsked, Source: "dental_plan"},
		{Key: "hygienist", Label: "Preferred hygienist", Value: "Maria", Tier: TierState, Source: "dental_plan"},
	}}
	id := e.s.CreatePlan(context.Background(), PlanRequest{Business: "Bright Smiles Dental",
		Goal: "book a cleaning next week", HouseholdID: hh, UserID: uidPtr(1)})
	sess := e.session(id)
	for _, want := range []string{"Acceptable times: Thu Nov 12 2-5pm", "Do not book: Tue Nov 10 9am-5pm (Work)",
		"Dental member ID: DX-77120", "Preferred hygienist: Maria"} {
		if !strings.Contains(sess.Details, want) {
			t.Fatalf("details lack %q:\n%s", want, sess.Details)
		}
	}
	if body := e.notify.all()[0].Body; !strings.Contains(body,
		"Shared with this plan by your commands: Calendar events (your free and busy times for the next week); Dental plan.") {
		t.Fatalf("card: %q", body)
	}
	if !strings.Contains(sess.NodeContextJSON, "DX-77120") || strings.Contains(sess.NodeContextJSON, "Maria") {
		t.Fatalf("guarded: %s", sess.NodeContextJSON)
	}
	guarded := func() bool {
		for _, f := range e.s.restrictedFor(context.Background(), sess) {
			if f.Key == "dental_member_id" {
				return true
			}
		}
		return false
	}
	if !guarded() {
		t.Fatal("the member ID isn't guarded")
	}
	sess.Details = strings.ReplaceAll(sess.Details, "Dental member ID: DX-77120", "")
	if guarded() {
		t.Fatal("a field deleted from the card is still guarded")
	}
}

// The calendar's times replace a drafted line that only asked for them.
func TestCalendarTimesReplaceAnAvailabilityPlaceholder(t *testing.T) {
	got := stripTimesPlaceholder("Book a cleaning for Jordan.\nIf asked: (provide your availability for next week)\nIf asked: name is Jordan.")
	if got != "Book a cleaning for Jordan.\nIf asked: name is Jordan." {
		t.Fatalf("got %q", got)
	}
}
