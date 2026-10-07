package phone

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func exec(t *testing.T, tool *Tool, args map[string]any, turn servertools.Turn) *pyjson.Object {
	t.Helper()
	o := pyjson.NewObject()
	for k, v := range args {
		o.Set(k, v)
	}
	res, err := tool.Execute(context.Background(), servertools.Call{Name: ToolName, Args: o}, turn)
	if err != nil {
		t.Fatal(err)
	}
	return res.(*pyjson.Object)
}

func get(o *pyjson.Object, k string) any { v, _ := o.Get(k); return v }

func TestToolRefusalsAndAccept(t *testing.T) {
	e := newEnv(t)
	tool := e.s.Tool()
	if tool.Definition() == nil || tool.Name() != "make_phone_call" {
		t.Fatal("definition")
	}
	turn := servertools.Turn{ConversationID: "c1", HouseholdID: hh, Speaker: servertools.Speaker{UserID: 1}}
	args := map[string]any{"business": "Tony's Pizzeria", "goal": "order a large pie"}

	if r := exec(t, tool, map[string]any{"business": "x"}, turn); get(r, "error") != "missing_params" {
		t.Fatalf("missing: %v", r)
	}
	// Off by default, fail closed.
	if r := exec(t, tool, args, turn); get(r, "error") != "phone_calls_disabled" ||
		get(r, "message") != "Phone calls aren't set up on this Jarvis. A household admin can enable them in Household Settings." {
		t.Fatalf("disabled: %v", r)
	}
	e.enable()
	unknown := turn
	unknown.Speaker = servertools.Speaker{}
	if r := exec(t, tool, args, unknown); get(r, "error") != "no_identified_speaker" ||
		!strings.HasPrefix(get(r, "message").(string), "I couldn't tell who's asking") {
		t.Fatalf("no speaker: %v", r)
	}
	unknown.Speaker.RecognitionOff = true
	if r := exec(t, tool, args, unknown); !strings.HasPrefix(get(r, "message").(string), "Speaker recognition is off") {
		t.Fatalf("recognition off: %v", r)
	}
	if len(e.notify.all()) != 0 {
		t.Fatal("a refusal created a plan")
	}
	r := exec(t, tool, args, turn)
	if get(r, "status") != "accepted" ||
		get(r, "message") != "I've sent the call plan for Tony's Pizzeria to your phone — review it and tap Call now to dial." {
		t.Fatalf("accept: %v", r)
	}
	e.s.planWG.Wait()
	if len(e.notify.titled("📞 Call plan: Tony's Pizzeria")) != 1 {
		t.Fatalf("no plan card: %v", e.notify.all())
	}
}

func TestPlanFromPhonebook(t *testing.T) {
	e := newEnv(t)
	e.enable()
	e.addContact("Tony's Pizzeria", "732-592-4183", false)
	e.provider.lineType = "mobile"
	e.set(SettingCallContext, `{"fields":[{"key":"callback_number","value":"908-555-1234"}]}`, settings.Scope{UserID: 1})

	id := e.s.CreatePlan(context.Background(), PlanRequest{Business: "tonys pizza", Goal: "order a large pie",
		HouseholdID: hh, UserID: uidPtr(1)})
	if id == "" {
		t.Fatal("no session")
	}
	s := e.session(id)
	if s.State != StateDraft || s.ResolvedNumber != "+17325924183" || s.ContactName != "Tony's Pizzeria" ||
		s.LineType != "mobile" || s.ExpiresAt.Sub(s.CreatedAt) != 20*time.Minute {
		t.Fatalf("session: %+v", s)
	}
	if !strings.Contains(s.Details, "Order a large cheese pizza for pickup.") ||
		!strings.Contains(s.Details, "- Callback number: 908-555-1234") {
		t.Fatalf("details: %q", s.Details)
	}
	// The draft request: background slot, reasoning off, 200 tokens, caller name included.
	d := e.llm.drafts[0]
	if d.Label != "background" || *d.ReasoningBudget != 0 || *d.MaxTokens != 200 || *d.Temperature != 0.3 ||
		*d.Messages[1].Content.Text != "Business: tonys pizza\nGoal: order a large pie\nCaller name: Alex" {
		t.Fatalf("draft request: %+v", d)
	}
	c := e.notify.titled("📞 Call plan: Tony's Pizzeria")[0]
	if c.Category != "phone_call" || *c.UserID != 1 || c.push.TargetType != "user" || c.push.TargetID != "1" ||
		c.push.Priority != "high" || c.Summary != "order a large pie Note: this appears to be a mobile number." {
		t.Fatalf("card: %+v", c)
	}
	// 88 < 95: the substitution is called out on the card (D40 11.Q5).
	if !strings.Contains(c.Body, "This number is from your phonebook. I matched **Tony's Pizzeria** for 'tonys pizza'") {
		t.Fatalf("body: %q", c.Body)
	}
	m := c.Metadata
	if m["editor_schema"] != 2 || m["number_source"] != "phonebook" || m["session_id"] != id || m["expires_at"] != "2026-10-06T12:20:00Z" {
		t.Fatalf("meta: %v", m)
	}
	fields := m["editable_fields"].([]any)
	if fields[0].(map[string]any)["data_key"] != "dialed_number" || fields[0].(map[string]any)["initial"] != "+17325924183" ||
		fields[1].(map[string]any)["data_key"] != "details" {
		t.Fatalf("editable: %v", fields)
	}
	ie := m["interactive_elements"].([]any)
	confirm := ie[0].(map[string]any)
	if confirm["command"] != "make_phone_call" || confirm["callback"] != "confirm_call" || confirm["target"] != "server" ||
		ie[1].(map[string]any)["callback"] != "cancel_call" {
		t.Fatalf("elements: %v", ie)
	}
}

func TestPlanRefusals(t *testing.T) {
	e := newEnv(t)
	e.enable()
	e.addContact("CVS Pharmacy", "732-592-4183", true)
	if id := e.s.CreatePlan(context.Background(), PlanRequest{Business: "CVS", Goal: "refill", HouseholdID: hh, UserID: uidPtr(1)}); id != "" {
		t.Fatal("do-not-call contact planned")
	}
	if c := e.notify.titled("📵 Call refused"); len(c) != 1 || c[0].Summary != "CVS Pharmacy is marked do-not-call for this household." {
		t.Fatalf("dnc card: %v", e.notify.all())
	}
	e.set(SettingCallsPerDay, int64(0), settings.Scope{HouseholdID: hh})
	if id := e.s.CreatePlan(context.Background(), PlanRequest{Business: "Joe's", Goal: "x", HouseholdID: hh}); id != "" {
		t.Fatal("over the daily cap")
	}
	if c := e.notify.titled("📵 Call not started"); len(c) != 1 || c[0].Summary != "The daily call limit for this household has been reached." ||
		c[0].push.TargetType != "household" {
		t.Fatalf("cap card: %v", e.notify.all())
	}
}

func TestPlanWebSearch(t *testing.T) {
	e := newEnv(t)
	e.enable()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><p>Tony's Pizzeria, 12800 Frederick Rd, West Friendship, MD 21794.
			Call (732) 592-4183 or 908-555-1234</p></body></html>`)
	}))
	defer page.Close()
	e.s.Fetch = &servertools.Fetcher{Blocked: func(netip.Addr) bool { return false }}
	e.search.results = []servertools.SearchResult{{Title: "Tony's", URL: page.URL + "/tonys", Snippet: ""}}

	// Web search off → an honest miss, still a draft so the user can type the number.
	id := e.s.CreatePlan(context.Background(), PlanRequest{Business: "Tony's Pizzeria", Goal: "book a table", HouseholdID: hh, UserID: uidPtr(1)})
	if id == "" || e.session(id).ResolvedNumber != "" {
		t.Fatal("miss should draft with no number")
	}
	c := e.notify.all()[0]
	if !strings.Contains(c.Body, "web search is off for your household") || c.Metadata["number_source"] != "none" {
		t.Fatalf("miss card: %q %v", c.Body, c.Metadata)
	}
	// A scheduling goal always carries the fill-in times line (no calendar provider here).
	if !strings.HasSuffix(e.session(id).Details, "Acceptable times: (fill in your availability before calling)") {
		t.Fatalf("details: %q", e.session(id).Details)
	}

	e.set(settingWebSearch, true, settings.Scope{HouseholdID: hh})
	e.set(settingLocation, "Freehold, NJ", settings.Scope{HouseholdID: hh})
	// A do-not-call number in the household is never returned by the search (D40 11.Q4).
	e.addContact("Somebody Else", "+17325924183", true)
	id = e.s.CreatePlan(context.Background(), PlanRequest{Business: "Tonys", Goal: "order", HouseholdID: hh, UserID: uidPtr(1)})
	s := e.session(id)
	if s.ResolvedNumber != "+19085551234" || s.ContactAddress != "12800 Frederick Rd, West Friendship, MD 21794" {
		t.Fatalf("search session: %+v", s)
	}
	if e.search.queries[0] != "Tonys Freehold, NJ phone number" {
		t.Fatalf("query: %v", e.search.queries)
	}
	c = e.notify.all()[1]
	if !strings.HasPrefix(c.Body, "⚠️ This result is in MD but your household is in NJ") ||
		!strings.Contains(c.Body, "I found this number via web search (searched near Freehold, NJ) — check it before calling. Source: "+page.URL+"/tonys") ||
		c.Metadata["number_source"] != "web" {
		t.Fatalf("web card: %q", c.Body)
	}
}

// draft makes a plan with a phonebook number and returns its session id.
func (e *env) draft() string {
	e.t.Helper()
	e.addContact("Tony's Pizzeria", "732-592-4183", false)
	id := e.s.CreatePlan(context.Background(), PlanRequest{Business: "Tony's Pizzeria", Goal: "order a large pie",
		HouseholdID: hh, UserID: uidPtr(1), ErrandID: "run-1", ErrandStep: uidPtr(2)})
	if id == "" {
		e.t.Fatal("no draft")
	}
	return id
}

func confirmData(id, number string) map[string]any {
	return map[string]any{"session_id": id, "dialed_number": number, "details": "Order a large pie."}
}

func TestConfirmGuards(t *testing.T) {
	e := newEnv(t)
	e.enable()
	id := e.draft()
	ctx := context.Background()
	cb := func(d map[string]any) CallbackResult {
		return e.s.ConfirmCall(ctx, CallbackContext{HouseholdID: hh, UserID: 2, Data: d})
	}
	// Another household can't see the plan.
	if r := e.s.ConfirmCall(ctx, CallbackContext{HouseholdID: otherHH, UserID: 9, Data: confirmData(id, "732-592-4183")}); r.Error != "Call plan not found" {
		t.Fatalf("cross-household: %+v", r)
	}
	if r := cb(confirmData(id, "911")); r.Error != "Emergency and service numbers can't be called." || e.session(id).State != StateDraft {
		t.Fatalf("emergency: %+v", r)
	}
	e.addContact("Blocked Co", "908-555-1234", true)
	if r := cb(confirmData(id, "(908) 555-1234")); r.Error != "That number is marked do-not-call for this household." {
		t.Fatalf("dnc by number: %+v", r)
	}
	if r := cb(map[string]any{"session_id": id, "dialed_number": "732-592-4183", "details": "  "}); r.Error != "The call details can't be empty." {
		t.Fatalf("empty details: %+v", r)
	}
	e.advance(21 * time.Minute)
	if r := cb(confirmData(id, "732-592-4183")); r.Error != "This call plan expired. Ask Jarvis again to get a fresh one." {
		t.Fatalf("expired: %+v", r)
	}
	if s := e.session(id); s.State != StateExpired || s.EndedAt.IsZero() {
		t.Fatalf("state: %s", s.State)
	}
	// Single use: a second tap is a friendly no-op.
	if r := cb(confirmData(id, "732-592-4183")); !r.Success || r.ContextData["inbox"].(map[string]any)["summary"] != "This call plan is already expired." {
		t.Fatalf("second tap: %+v", r)
	}
	waitFor(t, "errand resume", func() bool { e.errands.mu.Lock(); defer e.errands.mu.Unlock(); return len(e.errands.snaps) == 1 })
	if sn := e.errands.snaps[0]; sn.ErrandID != "run-1" || *sn.ErrandStep != 2 || sn.State != StateExpired {
		t.Fatalf("snap: %+v", sn)
	}

	// Gate turned off between plan and tap → declined.
	id2 := e.s.CreatePlan(ctx, PlanRequest{Business: "Tony's Pizzeria", Goal: "x", HouseholdID: hh, UserID: uidPtr(1)})
	e.set(SettingEnabled, false, settings.Scope{HouseholdID: hh})
	if r := cb(confirmData(id2, "732-592-4183")); r.Error != "Phone calls are disabled for this household." || e.session(id2).State != StateDeclined {
		t.Fatalf("gate off: %+v %s", r, e.session(id2).State)
	}
}

func TestConfirmWithoutProviderFailsHonestly(t *testing.T) {
	e := newEnv(t)
	e.enable()
	id := e.draft()
	e.s.Provider = nil // no provider and no credentials anywhere (AD6)
	r := e.s.ConfirmCall(context.Background(), CallbackContext{HouseholdID: hh, UserID: 2, Data: confirmData(id, "732-592-4183")})
	if !strings.HasPrefix(r.Error, "Phone calls aren't set up for this household yet") {
		t.Fatalf("result: %+v", r)
	}
	if s := e.session(id); s.State != StateDraft || s.ConfirmedBy != nil {
		t.Fatalf("session: %+v", s)
	}
}

func TestClaimCASSingleWinner(t *testing.T) {
	e := newEnv(t)
	id := e.draft()
	if _, err := e.d.Write.Exec(`UPDATE cc_phone_call_sessions SET state = 'confirmed' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := e.s.claimDial(context.Background(), id); err == nil && ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 || e.session(id).State != StateDialing {
		t.Fatalf("wins=%d", wins)
	}
	if ok, _ := e.s.claimDial(context.Background(), e.draftAgain()); ok {
		t.Fatal("claimed a draft")
	}
}

func (e *env) draftAgain() string {
	id := e.s.CreatePlan(context.Background(), PlanRequest{Business: "Tony's Pizzeria", Goal: "x", HouseholdID: hh, UserID: uidPtr(1)})
	return id
}

func TestCancelDraftAndConfirmed(t *testing.T) {
	e := newEnv(t)
	e.enable()
	id := e.draft()
	r := e.s.CancelCall(context.Background(), CallbackContext{HouseholdID: hh, UserID: 1, Data: map[string]any{"session_id": id}})
	if !r.Success || r.ContextData["inbox"].(map[string]any)["title"] != "🚫 Call cancelled" || e.session(id).State != StateDeclined {
		t.Fatalf("cancel: %+v", r)
	}
	// D40 11.Q3: a confirmed (not yet claimed) call is declined and then loses the claim.
	id2 := e.draftAgain()
	if _, err := e.d.Write.Exec(`UPDATE cc_phone_call_sessions SET state = 'confirmed' WHERE id = ?`, id2); err != nil {
		t.Fatal(err)
	}
	e.s.CancelCall(context.Background(), CallbackContext{HouseholdID: hh, UserID: 1, Data: map[string]any{"session_id": id2}})
	if ok, _ := e.s.claimDial(context.Background(), id2); ok || e.session(id2).State != StateDeclined {
		t.Fatal("cancelled confirmed call still claimable")
	}
}

func TestReaperWindows(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.set(SettingMaxConcurrent, int64(10), settings.Scope{HouseholdID: hh})
	mk := func(state string, confirmedAgo, heartbeatAgo time.Duration) string {
		id := e.draftAgain()
		if id == "" {
			t.Fatal("no draft")
		}
		now := e.now()
		if _, err := e.d.Write.Exec(`UPDATE cc_phone_call_sessions SET state = ?, confirmed_at = ?, heartbeat_at = ?, in_call_at = ? WHERE id = ?`,
			state, dbTime(now.Add(-confirmedAgo)), dbTime(now.Add(-heartbeatAgo)), dbTime(now.Add(-heartbeatAgo)), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.addContact("Tony's Pizzeria", "732-592-4183", false)
	ringing := mk(StateDialing, 100*time.Second, 100*time.Second) // oddity 4: not reaped mid-ring
	lostDial := mk(StateDialing, 200*time.Second, 200*time.Second)
	live := mk(StateInCall, 30*time.Second, 10*time.Second)
	stale := mk(StateInCall, 120*time.Second, 90*time.Second)
	long := mk(StateWrapup, 800*time.Second, 5*time.Second)
	stuck := mk(StateConfirmed, 6*time.Minute, 6*time.Minute) // oddity 5
	fresh := mk(StateConfirmed, time.Minute, time.Minute)
	draft := e.draftAgain()
	if _, err := e.d.Write.Exec(`UPDATE cc_phone_call_sessions SET expires_at = ? WHERE id = ?`, dbTime(e.now().Add(-time.Second)), draft); err != nil {
		t.Fatal(err)
	}

	if n := e.s.Reap(ctx); n != 5 {
		t.Fatalf("reaped %d", n)
	}
	want := map[string]string{ringing: StateDialing, lostDial: StateFailed, live: StateInCall, stale: StateFailed,
		long: StateFailed, stuck: StateFailed, fresh: StateConfirmed, draft: StateExpired}
	for id, st := range want {
		if got := e.session(id).State; got != st {
			t.Errorf("%s: got %s want %s", id[:8], got, st)
		}
	}
	if e.session(long).ErrorMessage != "call exceeded the time limit" || e.session(stale).ErrorMessage != "lost contact with the call" ||
		e.session(stuck).ErrorMessage != "couldn't start the call" {
		t.Fatal("reasons")
	}
	summaries := map[string]int{}
	for _, c := range e.notify.titled("⚠️ Call ended: Tony's Pizzeria") {
		summaries[c.Summary]++
	}
	if summaries["The call was ended because Jarvis lost contact with the call."] != 2 ||
		summaries["The call was ended because Jarvis call exceeded the time limit."] != 1 ||
		summaries["The call was ended because Jarvis couldn't start the call."] != 1 {
		t.Fatalf("cards: %v", summaries)
	}
	// A session with a measured call has its duration counted toward the monthly cap.
	if d := e.session(stale).Duration; d == nil || *d != 90 {
		t.Fatalf("duration: %v", d)
	}
}

func TestPurgeUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	draftID := e.draft()
	done := e.draftAgain()
	if _, err := e.d.Write.Exec(`UPDATE cc_phone_call_sessions SET state = 'done', confirmed_by = 1 WHERE id = ?`, done); err != nil {
		t.Fatal(err)
	}
	e.set(SettingCallContext, `{"fields":[]}`, settings.Scope{UserID: 1})
	tx, err := e.d.Write.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := PurgeUser(ctx, tx, "cc_settings", 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.session(ctx, e.d.Read, draftID); err != sql.ErrNoRows {
		t.Fatalf("draft survived: %v", err)
	}
	if s := e.session(done); s.UserID != nil || s.ConfirmedBy != nil {
		t.Fatalf("not de-identified: %+v", s)
	}
	var n int
	_ = e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_settings WHERE key = ? AND user_id = 1`, SettingCallContext).Scan(&n)
	var contacts int
	_ = e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_phone_contacts`).Scan(&contacts)
	if n != 0 || contacts != 1 {
		t.Fatalf("settings=%d contacts=%d", n, contacts)
	}
}
