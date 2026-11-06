package phone

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
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
		{Key: "dental_member_id", Label: "Dental member ID", Value: "DX-77120", Tier: TierIfAsked, Category: CatMedical, Source: "dental_plan"},
		{Key: "hygienist", Label: "Preferred hygienist", Value: "Maria", Tier: TierState, Category: CatMedical, Source: "dental_plan"},
	}}
	e.llm.kinds = "medical"
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

// A call's brief carries General details and only the categories the model gives the call:
// a restaurant never gets the date of birth or the home address, a dentist gets the medical
// details but not the address. A saved address is Home even when stored as General.
func TestBriefCarriesOnlyTheCallsCategories(t *testing.T) {
	ctxJSON := `{"fields":[{"key":"callback_number","value":"732-555-0142"},
		{"key":"date_of_birth","value":"March 3, 1985"},
		{"key":"address","value":"12 Ocean Ave","category":"general"}]}`
	for _, c := range []struct {
		kinds      string
		has, hasnt []string
	}{
		{"dining", []string{"732-555-0142"}, []string{"March 3, 1985", "12 Ocean Ave"}},
		{"medical", []string{"732-555-0142", "March 3, 1985"}, []string{"12 Ocean Ave"}},
		{"", []string{"732-555-0142"}, []string{"March 3, 1985", "12 Ocean Ave"}},
		{"home, auto", []string{"12 Ocean Ave"}, []string{"March 3, 1985"}},
	} {
		e := newEnv(t)
		e.set(SettingCallContext, ctxJSON, settings.Scope{UserID: 1})
		e.llm.kinds = c.kinds
		id := e.s.CreatePlan(context.Background(), PlanRequest{Business: "Somewhere", Goal: "do a thing",
			HouseholdID: hh, UserID: uidPtr(1)})
		details := e.session(id).Details
		for _, v := range c.has {
			if !strings.Contains(details, v) {
				t.Errorf("%q call lacks %q:\n%s", c.kinds, v, details)
			}
		}
		for _, v := range c.hasnt {
			if strings.Contains(details, v) {
				t.Errorf("%q call has %q:\n%s", c.kinds, v, details)
			}
		}
	}
}
