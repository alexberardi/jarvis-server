package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
)

type fakePhone struct {
	started []phone.SIPTestInput
	tests   []string
	err     error
}

func (f *fakePhone) SIPAccounts() []phone.SIPAccountView {
	return []phone.SIPAccountView{{LocalPort: 5062, Server: "pbx.lan", Extension: "100", State: "registered",
		Tests: map[string]phone.SIPTestRun{}}}
}

func (f *fakePhone) StartSIPTest(_ context.Context, port int, test string, in phone.SIPTestInput) (phone.SIPTestRun, error) {
	if f.err != nil {
		return phone.SIPTestRun{}, f.err
	}
	f.started, f.tests = append(f.started, in), append(f.tests, test)
	return phone.SIPTestRun{Test: test, State: phone.SIPTestRunning, Steps: []string{}}, nil
}

func (f *fakePhone) SIPTrace() []sip.TraceDialog {
	return []sip.TraceDialog{{CallID: "c1@jarvis", Account: "100@pbx.lan:5060", Kind: sip.TraceCall, Method: "INVITE",
		Started: time.Now(), Messages: []sip.TraceMessage{{At: time.Now(), Dir: "sent", Transport: "UDP",
			Local: "10.0.0.5:5062", Remote: "10.0.0.1:5060", FirstLine: "INVITE sip:*43@pbx.lan SIP/2.0",
			Text: "INVITE sip:*43@pbx.lan SIP/2.0\r\nCall-ID: c1@jarvis\r\n\r\n"}}}}
}

func TestSIPStatusRoute(t *testing.T) {
	e := newBFF(t)
	if w := send(e.mux, "GET", "/api/phone/sip", "", root...); w.Code != 503 {
		t.Fatalf("without cc: %d", w.Code)
	}
	e.m.Phone = &fakePhone{}
	w := send(e.mux, "GET", "/api/phone/sip", "", root...)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	v := decode(t, w)
	accts, _ := v["accounts"].([]any)
	if len(accts) != 1 || len(v["tests"].([]any)) != 5 || len(v["feature_codes"].([]any)) != 2 {
		t.Fatalf("%v", v)
	}
	if w := send(e.mux, "GET", "/api/phone/sip", "", "Authorization", "Bearer member"); w.Code != 403 {
		t.Fatalf("member: %d", w.Code)
	}
}

func TestSIPTestRoute(t *testing.T) {
	e := newBFF(t)
	f := &fakePhone{}
	e.m.Phone = f
	w := send(e.mux, "POST", "/api/phone/sip/5062/tests/connection", "", root...)
	if w.Code != 202 || f.tests[0] != "connection" || f.started[0].UserID != 1 {
		t.Fatalf("%d %s %+v", w.Code, w.Body, f.started)
	}
	w = send(e.mux, "POST", "/api/phone/sip/5062/tests/call_me", `{"number":"2125550142","household_id":"hh-home"}`,
		append(root, "Content-Type", "application/json")...)
	if w.Code != 202 || f.started[1].Number != "2125550142" || f.started[1].HouseholdID != "hh-home" {
		t.Fatalf("%d %s %+v", w.Code, w.Body, f.started)
	}
	for _, c := range []struct {
		err  error
		code int
	}{
		{phone.ErrSIPAccountNotFound, 404}, {phone.ErrSIPTestUnknown, 404}, {phone.ErrSIPTestBusy, 409},
		{&phone.InputError{Msg: "Enter your own phone number"}, 400},
	} {
		f.err = c.err
		if w := send(e.mux, "POST", "/api/phone/sip/5062/tests/echo", "", root...); w.Code != c.code {
			t.Errorf("%v: %d, want %d", c.err, w.Code, c.code)
		}
	}
	f.err = nil
	if w := send(e.mux, "POST", "/api/phone/sip/notaport/tests/echo", "", root...); w.Code != 404 {
		t.Fatalf("bad port: %d", w.Code)
	}
	if w := send(e.mux, "POST", "/api/phone/sip/5062/tests/echo", "{nope", append(root, "Content-Type", "application/json")...); w.Code != 400 {
		t.Fatalf("bad body: %d", w.Code)
	}
}

func TestSIPTraceRouteExportsText(t *testing.T) {
	e := newBFF(t)
	e.m.Phone = &fakePhone{}
	w := send(e.mux, "GET", "/api/phone/sip/trace", "", root...)
	if w.Code != 200 || len(decode(t, w)["dialogs"].([]any)) != 1 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = send(e.mux, "GET", "/api/phone/sip/trace?format=text", "", root...)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), `attachment; filename="jarvis-sip-trace-`) {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	if !strings.Contains(w.Body.String(), "INVITE sip:*43@pbx.lan SIP/2.0") {
		t.Fatalf("export:\n%s", w.Body)
	}
}
