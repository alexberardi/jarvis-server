package phone

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The SIP support surface (docs/cc/phone-sip.md §6) over fake UAs: the status view and each
// self-test. The real UA's half (Check, DialFeature, the inbound record, the trace) is tested
// against the fake PBX in package sip, and the whole on the L1 lane (tools/siplane).

// sipLineEnv runs one SIP account for hh (extension 100) and returns its fake UA.
func (e *env) sipLineEnv() *fakeUA {
	e.t.Helper()
	farm := e.sipEnv()
	sc := settings.Scope{HouseholdID: hh}
	e.set(SettingProvider, ProviderSIP, sc)
	e.setSIP(sc, "192.168.1.5", "100", "hh-secret-pw")
	e.s.reconcileSIP(context.Background())
	uas := farm.all()
	if len(uas) != 1 {
		e.t.Fatalf("%d UAs", len(uas))
	}
	return uas[0]
}

func shortSIPTimings(t *testing.T) {
	saved := []time.Duration{sipAnswerTimeout, sipPromptQuiet, sipPromptMax, sipEchoTail, sipDigitsMax, sipInboundWindow, sipInboundPoll}
	sipAnswerTimeout, sipPromptQuiet, sipPromptMax, sipEchoTail = time.Second, 500*time.Millisecond, 5*time.Second, 500*time.Millisecond
	sipDigitsMax, sipInboundWindow, sipInboundPoll = 2*time.Second, 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		sipAnswerTimeout, sipPromptQuiet, sipPromptMax, sipEchoTail = saved[0], saved[1], saved[2], saved[3]
		sipDigitsMax, sipInboundWindow, sipInboundPoll = saved[4], saved[5], saved[6]
	})
}

// waitRun waits for test's run on port to finish.
func (e *env) waitRun(port int, test string) SIPTestRun {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, a := range e.s.SIPAccounts() {
			if r, ok := a.Tests[test]; ok && a.LocalPort == port && r.State != SIPTestRunning {
				return r
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("%s did not finish: %+v", test, e.s.SIPAccounts())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func tone(n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = 3000
		} else {
			out[i] = -3000
		}
	}
	return out
}

// feed queues pcm on c as 20 ms frames.
func feed(c *fakeSIPCall, pcm []int16) {
	for i := 0; i < len(pcm); i += sip.FrameSamples {
		c.feed <- pcm[i:min(i+sip.FrameSamples, len(pcm))]
	}
}

func (c *fakeSIPCall) played() int {
	c.fakeCall.mu.Lock()
	defer c.fakeCall.mu.Unlock()
	return len(c.fakeCall.plays)
}

func TestSIPAccountsShowStatusAndNeverThePassword(t *testing.T) {
	e := newEnv(t)
	ua := e.sipLineEnv()
	ua.mu.Lock()
	ua.status = &sip.Status{State: sip.StateRetrying, LastError: "REGISTER: 403 Forbidden", Hint: "blocked",
		AuthFailures: 1, Attempts: 2, OptionsRTT: 12 * time.Millisecond, LastOptions: time.Now(), ActiveCalls: 1,
		LastInbound: sip.InboundInvite{At: time.Now(), Caller: "2125550199", Codecs: []string{"PCMU"}, Response: 480}}
	ua.mu.Unlock()
	got := e.s.SIPAccounts()
	if len(got) != 1 {
		t.Fatalf("%d accounts", len(got))
	}
	a := got[0]
	if a.State != "retrying" || a.HouseholdID != hh || a.Extension != "100" || a.LocalPort != 5062 ||
		a.OptionsRTTMs == nil || *a.OptionsRTTMs != 12 || a.ActiveCalls != 1 || a.LastInbound == nil || a.Hint != "blocked" {
		t.Fatalf("view %+v", a)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "hh-secret-pw") {
		t.Fatalf("the password is in the status: %s", b)
	}
}

func TestSIPAccountThatCouldNotStartHasAHint(t *testing.T) {
	e := newEnv(t)
	farm := e.sipEnv()
	farm.fail = errors.New(`sip: cannot resolve the PBX "pbx.nowhere": no such host`)
	sc := settings.Scope{HouseholdID: hh}
	e.set(SettingProvider, ProviderSIP, sc)
	e.setSIP(sc, "pbx.nowhere", "100", "pw")
	e.s.reconcileSIP(context.Background())
	got := e.s.SIPAccounts()
	if len(got) != 1 || got[0].State != "not_started" || !strings.Contains(got[0].Hint, "cannot find the PBX") {
		t.Fatalf("%+v", got)
	}
	// A self-test on it fails with the same hint rather than hanging.
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestConnection, SIPTestInput{}); err != nil {
		t.Fatal(err)
	}
	if r := e.waitRun(5062, SIPTestConnection); r.State != SIPTestFailed || !strings.Contains(r.Hint, "cannot find") {
		t.Fatalf("%+v", r)
	}
}

func TestSIPConnectionTest(t *testing.T) {
	e := newEnv(t)
	ua := e.sipLineEnv()
	ua.check = sip.CheckResult{Registered: true, RegisterRTT: 40 * time.Millisecond, OptionsRTT: 9 * time.Millisecond}
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestConnection, SIPTestInput{}); err != nil {
		t.Fatal(err)
	}
	r := e.waitRun(5062, SIPTestConnection)
	if r.State != SIPTestPassed || *r.RegisterRTTMs != 40 || *r.OptionsRTTMs != 9 || r.Finished == nil {
		t.Fatalf("%+v", r)
	}
	ua.check = sip.CheckResult{Error: "REGISTER: 401 Unauthorized", Hint: "The PBX did not accept the extension's password."}
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestConnection, SIPTestInput{}); err != nil {
		t.Fatal(err)
	}
	r = e.waitRun(5062, SIPTestConnection)
	if r.State != SIPTestFailed || !strings.Contains(r.Hint, "password") || !strings.Contains(r.Summary, "401") {
		t.Fatalf("%+v", r)
	}
}

func TestSIPTestRequestsAreChecked(t *testing.T) {
	e := newEnv(t)
	e.sipLineEnv()
	ctx := context.Background()
	if _, err := e.s.StartSIPTest(ctx, 5062, "reboot-pbx", SIPTestInput{}); !errors.Is(err, ErrSIPTestUnknown) {
		t.Errorf("unknown test: %v", err)
	}
	if _, err := e.s.StartSIPTest(ctx, 6000, SIPTestConnection, SIPTestInput{}); !errors.Is(err, ErrSIPAccountNotFound) {
		t.Errorf("unknown port: %v", err)
	}
	var in *InputError
	for _, n := range []string{"", "911", "*43", "1-900-555-0100"} {
		if _, err := e.s.StartSIPTest(ctx, 5062, SIPTestCallMe, SIPTestInput{Number: n, UserID: 1}); !errors.As(err, &in) {
			t.Errorf("call_me to %q: %v", n, err)
		}
	}
}

func TestSIPEchoTestDialsStar43AndMeasuresTheEcho(t *testing.T) {
	shortSIPTimings(t)
	e := newEnv(t)
	ua := e.sipLineEnv()
	e.stt.heard = []string{"Jarvis is checking that this phone line carries speech both ways."}
	calls := make(chan *fakeSIPCall, 1)
	var dialed []string
	ua.feature = func(code string) (sipCall, error) {
		dialed = append(dialed, code)
		c := newFakeSIPCall("feature-code-call")
		close(c.started)
		calls <- c
		return c, nil
	}
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestEcho, SIPTestInput{}); err != nil {
		t.Fatal(err)
	}
	c := <-calls
	// The PBX's instructions, then quiet: only then does Jarvis speak.
	feed(c, tone(8000))
	feed(c, make([]int16, 4000))
	eventually(t, "the phrase", func() bool { return c.played() == 1 })
	// The echo: 300 ms of nothing, then the phrase back, then quiet.
	feed(c, make([]int16, 2400))
	feed(c, tone(8000))
	feed(c, make([]int16, 16000))
	r := e.waitRun(5062, SIPTestEcho)
	if r.State != SIPTestPassed {
		t.Fatalf("%+v", r)
	}
	if strings.Join(dialed, ",") != "*43" {
		t.Fatalf("dialed %v", dialed)
	}
	if len(ua.dialed) != 0 {
		t.Fatal("the self-test went through the normal Dial path")
	}
	if r.LatencyMs == nil || *r.LatencyMs != 300 || r.WER == nil || *r.WER != 0 || r.Heard == "" {
		t.Fatalf("latency %v, WER %v, heard %q", r.LatencyMs, r.WER, r.Heard)
	}
	if spoken := e.tts.spoken(); len(spoken) != 1 || spoken[0] != sipEchoPhrase {
		t.Fatalf("spoke %v", spoken)
	}
}

func TestSIPEchoTestThatHearsNothingSaysWhereToLook(t *testing.T) {
	shortSIPTimings(t)
	e := newEnv(t)
	ua := e.sipLineEnv()
	calls := make(chan *fakeSIPCall, 1)
	ua.feature = func(string) (sipCall, error) {
		c := newFakeSIPCall("silent")
		close(c.started)
		calls <- c
		return c, nil
	}
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestEcho, SIPTestInput{}); err != nil {
		t.Fatal(err)
	}
	c := <-calls
	feed(c, tone(4000))
	feed(c, make([]int16, 4000))
	eventually(t, "the phrase", func() bool { return c.played() == 1 })
	feed(c, make([]int16, 40000))
	r := e.waitRun(5062, SIPTestEcho)
	if r.State != SIPTestFailed || !strings.Contains(r.Summary, "heard nothing") || !strings.Contains(r.Hint, "Direct Media") {
		t.Fatalf("%+v", r)
	}
}

func TestSIPFeatureCodeNotAnswered(t *testing.T) {
	shortSIPTimings(t)
	e := newEnv(t)
	ua := e.sipLineEnv()
	ua.feature = func(string) (sipCall, error) {
		c := newFakeSIPCall("refused")
		c.mu.Lock()
		c.end, c.detail = sip.EndNotFound, "404 Not Found"
		c.mu.Unlock()
		c.Close()
		return c, nil
	}
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestDigits, SIPTestInput{}); err != nil {
		t.Fatal(err)
	}
	r := e.waitRun(5062, SIPTestDigits)
	if r.State != SIPTestFailed || !strings.Contains(r.Summary, "404") || !strings.Contains(r.Hint, "Feature Codes") {
		t.Fatalf("%+v", r)
	}
}

func TestSIPDigitsTestComparesTheExtension(t *testing.T) {
	shortSIPTimings(t)
	for _, tc := range []struct {
		heard string
		pass  bool
	}{
		{"Your extension number is one zero zero.", true},
		{"your extension number is 100", true},
		{"Your extension number is one zero one.", false},
	} {
		e := newEnv(t)
		ua := e.sipLineEnv()
		e.stt.heard = []string{tc.heard}
		ua.feature = func(code string) (sipCall, error) {
			if code != "*65" {
				t.Errorf("dialed %s", code)
			}
			c := newFakeSIPCall("digits")
			close(c.started)
			go func() { feed(c, tone(8000)); feed(c, make([]int16, 8000)) }()
			return c, nil
		}
		if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestDigits, SIPTestInput{}); err != nil {
			t.Fatal(err)
		}
		r := e.waitRun(5062, SIPTestDigits)
		if (r.State == SIPTestPassed) != tc.pass || r.Expected != "100" || r.Heard != tc.heard {
			t.Errorf("%q: %+v", tc.heard, r)
		}
	}
}

func TestSpokenDigits(t *testing.T) {
	for in, want := range map[string]string{
		"your extension number is one zero one": "101",
		"Your extension number is 101.":         "101",
		"extension 1 0 1":                       "101",
		"oh two, five":                          "025",
		"nothing here":                          "",
	} {
		if got := spokenDigits(in); got != want {
			t.Errorf("spokenDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

// Call my phone is the normal call path: a confirm card in the operator's inbox, to the number
// they typed, that they confirm like any other call.
func TestSIPCallMeSendsTheNormalConfirmCard(t *testing.T) {
	e := newEnv(t)
	e.sipLineEnv()
	e.enable()
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestCallMe, SIPTestInput{Number: "(212) 555-0142", UserID: 1}); err != nil {
		t.Fatal(err)
	}
	r := e.waitRun(5062, SIPTestCallMe)
	if r.State != SIPTestPassed || r.SessionID == "" {
		t.Fatalf("%+v", r)
	}
	cards := e.notify.titled("📞 Call plan")
	if len(cards) != 1 {
		t.Fatalf("%d confirm cards: %+v", len(cards), e.notify.all())
	}
	sess := e.session(r.SessionID)
	if sess.ResolvedNumber != "+12125550142" || sess.State != StateDraft || sess.HouseholdID != hh {
		t.Fatalf("session %+v", sess)
	}
	if !strings.Contains(cards[0].Body, "the number you entered") {
		t.Fatalf("card %+v", cards[0])
	}
}

func TestSIPCallMeNeedsPhoneCallsOn(t *testing.T) {
	e := newEnv(t)
	e.sipLineEnv()
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestCallMe, SIPTestInput{Number: "2125550142", UserID: 1}); err != nil {
		t.Fatal(err)
	}
	if r := e.waitRun(5062, SIPTestCallMe); r.State != SIPTestFailed || !strings.Contains(r.Summary, "turned off") {
		t.Fatalf("%+v", r)
	}
}

func TestSIPInboundWatch(t *testing.T) {
	shortSIPTimings(t)
	e := newEnv(t)
	ua := e.sipLineEnv()
	sipInboundWindow = 5 * time.Second
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestInbound, SIPTestInput{}); err != nil {
		t.Fatal(err)
	}
	// One test at a time per account.
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestConnection, SIPTestInput{}); !errors.Is(err, ErrSIPTestBusy) {
		t.Fatalf("second test: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	ua.mu.Lock()
	ua.status = &sip.Status{State: sip.StateRegistered, LastInbound: sip.InboundInvite{At: time.Now(), Caller: "2125550199",
		CallerName: "Alice", To: "100", Media: "192.168.1.5:10000", Codecs: []string{"PCMU", "PCMA"}, Response: 480}}
	ua.mu.Unlock()
	r := e.waitRun(5062, SIPTestInbound)
	if r.State != SIPTestPassed || r.Inbound == nil || r.Inbound.Caller != "2125550199" ||
		!strings.Contains(r.Summary, "2125550199 (Alice)") || !strings.Contains(r.Summary, "PCMU, PCMA") {
		t.Fatalf("%+v", r)
	}
}

func TestSIPInboundWatchTimesOut(t *testing.T) {
	shortSIPTimings(t)
	e := newEnv(t)
	e.sipLineEnv()
	if _, err := e.s.StartSIPTest(context.Background(), 5062, SIPTestInbound, SIPTestInput{}); err != nil {
		t.Fatal(err)
	}
	if r := e.waitRun(5062, SIPTestInbound); r.State != SIPTestFailed || !strings.Contains(r.Hint, "Inbound Routes") {
		t.Fatalf("%+v", r)
	}
}

func TestWordErrorRateWithin(t *testing.T) {
	if w := wordErrorRateWithin("the quick fox", "echo test. The quick fox. Goodbye"); w != 0 {
		t.Fatalf("WER %v", w)
	}
	if w := wordErrorRateWithin("the quick fox", "the slow fox"); w < 0.3 || w > 0.34 {
		t.Fatalf("WER %v", w)
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
