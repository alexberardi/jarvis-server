package phone

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The SIP provider over fake UAs: settings, account resolution, the UA per account, dial
// format, and what a SIP call's end means for the session. The UA itself is tested against a
// fake PBX in package sip, and against Asterisk on the L1 lane (tools/siplane).

type fakeUA struct {
	acct   SIPAccount
	ready  error
	dialed chan string
	calls  chan *fakeCall

	mu     sync.Mutex
	closed bool
}

func (u *fakeUA) Ready() error { return u.ready }

func (u *fakeUA) Dial(_ context.Context, number string) (sipCall, error) {
	u.dialed <- number
	c := newFakeSIPCall(number)
	u.calls <- c.fakeCall
	return c, nil
}

func (u *fakeUA) Status() sip.Status { return sip.Status{State: sip.StateRegistered} }

func (u *fakeUA) Close(context.Context) {
	u.mu.Lock()
	u.closed = true
	u.mu.Unlock()
}

func (u *fakeUA) isClosed() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.closed
}

// fakeSIPCall is a fakeCall with package sip's surface.
type fakeSIPCall struct {
	*fakeCall
	mu     sync.Mutex
	end    sip.End
	detail string
}

func newFakeSIPCall(number string) *fakeSIPCall {
	return &fakeSIPCall{fakeCall: newFakeCall(DialRequest{SessionID: "sip-call-" + number})}
}

func (c *fakeSIPCall) Play(pcm []int16) (int64, error) { return c.fakeCall.Play(pcm, "") }
func (c *fakeSIPCall) Flush()                          { _ = c.fakeCall.Flush() }
func (c *fakeSIPCall) SendDTMF(string) error           { return sip.ErrUnsupported }
func (c *fakeSIPCall) End() (sip.End, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.end, c.detail
}

type uaFarm struct {
	mu      sync.Mutex
	started []*fakeUA
	fail    error
}

func (f *uaFarm) start(_ context.Context, a SIPAccount) (sipUA, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	u := &fakeUA{acct: a, dialed: make(chan string, 4), calls: make(chan *fakeCall, 4)}
	f.started = append(f.started, u)
	return u, nil
}

func (f *uaFarm) all() []*fakeUA {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeUA(nil), f.started...)
}

// sipEnv is an env whose households resolve their provider from settings, with UAs from farm.
func (e *env) sipEnv() *uaFarm {
	e.perHousehold()
	f := &uaFarm{}
	e.s.StartSIP = f.start
	return f
}

func (e *env) setSIP(sc settings.Scope, server, ext, password string) {
	e.t.Helper()
	e.set(SettingSIPServer, server, sc)
	e.set(SettingSIPExtension, ext, sc)
	e.set(SettingSIPPassword, password, sc)
}

func TestProviderSettingAcceptsSIP(t *testing.T) {
	e := newEnv(t)
	e.sipEnv()
	ctx := context.Background()
	def, ok := e.s.Settings.Definition(SettingProvider)
	if !ok || def.Default != ProviderTwilio || len(def.Options) != 2 || def.Options[1] != ProviderSIP {
		t.Fatalf("definition: %+v", def)
	}
	if err := e.s.Settings.Set(ctx, SettingProvider, "sip", settings.Scope{HouseholdID: hh}); err != nil {
		t.Fatalf("sip refused: %v", err)
	}
	// No SIP account anywhere: not configured, in SIP's words.
	_, err := e.s.Telephony(ctx, hh)
	if !errors.Is(err, ErrSIPNotConfigured) || !isNotConfigured(err) {
		t.Fatalf("Telephony: %v", err)
	}
	if msg := NotConfiguredMessage(err); strings.Contains(msg, "Twilio") || !strings.Contains(msg, "SIP") {
		t.Fatalf("message: %s", msg)
	}
	// A partial set is an error, not a mix of two levels.
	e.set(SettingSIPServer, "pbx.local", settings.Scope{HouseholdID: hh})
	_, err = e.s.Telephony(ctx, hh)
	var inc *IncompleteError
	if !errors.As(err, &inc) || inc.Provider != ProviderSIP || inc.Source != SourceHousehold ||
		strings.Join(inc.Missing, ",") != SettingSIPExtension+","+SettingSIPPassword {
		t.Fatalf("incomplete: %v", err)
	}
	if msg := NotConfiguredMessage(err); !strings.Contains(msg, "SIP") || strings.Contains(msg, "Twilio") {
		t.Fatalf("message: %s", msg)
	}
}

func TestSIPAccountResolvesHouseholdThenSystem(t *testing.T) {
	e := newEnv(t)
	farm := e.sipEnv()
	ctx := context.Background()
	e.set(SettingProvider, "sip", settings.Scope{})
	e.setSIP(settings.Scope{}, "10.0.0.2", "200", "sys-secret")
	e.set(SettingSIPDialPrefix, "9", settings.Scope{})

	tel, err := e.s.Telephony(ctx, otherHH)
	if err != nil || tel.Source != SourceSystem {
		t.Fatalf("system: %+v %v", tel, err)
	}
	p := tel.Provider.(*sipProvider)
	want := SIPAccount{Server: "10.0.0.2", Port: 5060, Transport: "udp", Extension: "200", Password: "sys-secret",
		LocalPort: 5062, DialFormat: DialFormat10, DialPrefix: "9"}
	if p.acct != want {
		t.Fatalf("account %v, want %v", p.acct, want)
	}
	if strings.Contains(p.acct.String(), "sys-secret") || strings.Contains(fmtV(p.acct), "sys-secret") {
		t.Fatal("the account prints its password")
	}

	// A household with its own PBX: all of its values come from its level, not the system's
	// prefix.
	e.setSIP(settings.Scope{HouseholdID: hh}, "192.168.1.5", "100", "hh-secret")
	e.set(SettingSIPLocalPort, int64(5070), settings.Scope{HouseholdID: hh})
	e.set(SettingSIPDialFormat, DialFormatE164, settings.Scope{HouseholdID: hh})
	tel, err = e.s.Telephony(ctx, hh)
	if err != nil || tel.Source != SourceHousehold {
		t.Fatalf("household: %+v %v", tel, err)
	}
	p = tel.Provider.(*sipProvider)
	if p.acct.Server != "192.168.1.5" || p.acct.LocalPort != 5070 || p.acct.DialPrefix != "" || p.acct.DialFormat != DialFormatE164 {
		t.Fatalf("household account %v", p.acct)
	}
	if tel.SigningKey != "" {
		t.Fatal("SIP signs nothing")
	}
	// Each account got its own UA.
	if n := len(farm.all()); n != 2 {
		t.Fatalf("%d UAs started, want 2", n)
	}
	if p.LineType(ctx, "+12125550100") != "unknown" {
		t.Fatal("line type")
	}
}

func TestSIPSettingsAreValidatedAndThePasswordMasked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sc := settings.Scope{HouseholdID: hh}
	for key, bad := range map[string]any{
		SettingSIPTransport: "tls", SettingSIPDialFormat: "7-digit", SettingSIPDialPrefix: "9#",
		SettingSIPPort: int64(70000), SettingSIPLocalPort: int64(0),
	} {
		if err := e.s.Settings.Set(ctx, key, bad, sc); !errors.Is(err, settings.ErrInvalidValue) {
			t.Errorf("%s=%v accepted: %v", key, bad, err)
		}
	}
	e.set(SettingSIPPassword, "hunter2", sc)
	list, err := e.s.Settings.List(ctx, sc, "phone_calls")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.Key == SettingSIPPassword && (r.Value == "hunter2" || !r.IsSecret) {
			t.Fatalf("password listed as %+v", r)
		}
	}
}

func TestFormatDial(t *testing.T) {
	for _, tc := range []struct{ in, format, prefix, want string }{
		{"+12125550100", DialFormat10, "", "2125550100"},
		{"(212) 555-0100", "", "", "2125550100"},
		{"+12125550100", DialFormat11, "", "12125550100"},
		{"212.555.0100", DialFormatE164, "", "+12125550100"},
		{"+12125550100", DialFormat10, "9", "92125550100"},
		{"+12125550100", DialFormat11, "9", "912125550100"},
	} {
		got, err := FormatDial(tc.in, tc.format, tc.prefix)
		if err != nil || got != tc.want {
			t.Errorf("FormatDial(%q, %q, %q) = %q %v, want %q", tc.in, tc.format, tc.prefix, got, err, tc.want)
		}
	}
	// Still NANP only, and never emergency or premium numbers.
	for _, in := range []string{"911", "+19005550100", "+442071234567", "*43"} {
		if got, err := FormatDial(in, DialFormat10, ""); err == nil {
			t.Errorf("FormatDial(%q) = %q", in, got)
		}
	}
	if _, err := FormatDial("+12125550100", "7-digit", ""); err == nil {
		t.Error("unknown format accepted")
	}
}

func TestSIPCallDialsInTheDialFormatAndRunsTheLoop(t *testing.T) {
	e := newEnv(t)
	farm := e.sipEnv()
	e.set(SettingProvider, "sip", settings.Scope{HouseholdID: hh})
	e.setSIP(settings.Scope{HouseholdID: hh}, "pbx.local", "100", "secret")
	e.set(SettingSIPDialFormat, DialFormat11, settings.Scope{HouseholdID: hh})
	e.set(SettingSIPDialPrefix, "9", settings.Scope{HouseholdID: hh})
	// No public URL: SIP needs none (only Twilio's media stream does).
	e.s.Options.PublicURL, e.s.Options.PublicWSSURL = "", ""
	e.stt.heard = []string{"Tony's Pizza, how can I help?"}
	e.llm.replies = []string{"Hi! That's all, thanks. Goodbye. [HANGUP]"}
	e.llm.assess = `{"summary": "Said hello.", "goal_achieved": true}`

	id := e.confirmed(t)
	waitFor(t, "a UA", func() bool { return len(farm.all()) == 1 })
	ua := farm.all()[0]
	var number string
	select {
	case number = <-ua.dialed:
	case <-time.After(5 * time.Second):
		t.Fatal("no dial")
	}
	if number != "917325924183" {
		t.Fatalf("dialed %q, want the 11-digit number after the 9", number)
	}
	c := <-ua.calls
	c.answer()
	e.waitState(t, id, StateInCall)
	c.say()
	waitFor(t, "goodbye", func() bool { return len(c.played()) == 2 })
	c.advance()
	e.waitState(t, id, StateDone)
	e.s.Wait()
}

func TestSIPNotRegisteredFailsBeforeTheDisclosure(t *testing.T) {
	e := newEnv(t)
	farm := e.sipEnv()
	farm.fail = errors.New("sip: listening on 0.0.0.0:5062/udp: address already in use")
	e.set(SettingProvider, "sip", settings.Scope{HouseholdID: hh})
	e.setSIP(settings.Scope{HouseholdID: hh}, "pbx.local", "100", "secret")
	id := e.confirmed(t)
	s := e.waitState(t, id, StateFailed)
	if !strings.Contains(s.ErrorMessage, "address already in use") {
		t.Fatalf("reason: %q", s.ErrorMessage)
	}
	if len(e.tts.spoken()) != 0 {
		t.Fatalf("spoke the disclosure for a call that couldn't be placed: %v", e.tts.spoken())
	}
}

func TestACallThatEndsBeforeTheAnswerFailsWithItsReason(t *testing.T) {
	for _, tc := range []struct {
		end    sip.End
		detail string
		want   string
	}{
		{sip.EndBusy, "486 Busy Here", "the line was busy (486 Busy Here)"},
		{sip.EndNoAnswer, "503 Service Unavailable after early media", "no answer (503 Service Unavailable after early media)"},
		{sip.EndNotFound, "404 Not Found", "the number could not be reached as dialed"},
		{sip.EndFailed, "403 Forbidden", "the phone system could not place the call (403 Forbidden)"},
	} {
		t.Run(string(tc.end), func(t *testing.T) {
			e := newEnv(t)
			farm := e.sipEnv()
			e.s.StreamStartTimeout = time.Minute // the end, not the timeout, fails it
			e.set(SettingProvider, "sip", settings.Scope{HouseholdID: hh})
			e.setSIP(settings.Scope{HouseholdID: hh}, "pbx.local", "100", "secret")
			id := e.confirmed(t)
			waitFor(t, "a UA", func() bool { return len(farm.all()) == 1 })
			ua := farm.all()[0]
			<-ua.dialed
			fc := <-ua.calls
			// The adapter's call is the fakeSIPCall around fc; end it as package sip would.
			sc := lastSIPCall(t, e, id)
			sc.mu.Lock()
			sc.end, sc.detail = tc.end, tc.detail
			sc.mu.Unlock()
			fc.Close()
			s := e.waitState(t, id, StateFailed)
			if !strings.Contains(s.ErrorMessage, tc.want) {
				t.Fatalf("reason %q, want %q", s.ErrorMessage, tc.want)
			}
			if len(e.tts.spoken()) != 1 {
				t.Fatalf("spoken %v: only the prewarmed disclosure, never played", e.tts.spoken())
			}
		})
	}
}

// lastSIPCall is the live call's SIP call behind its adapter.
func lastSIPCall(t *testing.T, e *env, id string) *fakeSIPCall {
	t.Helper()
	var c Call
	waitFor(t, "the live call", func() bool {
		if rt := e.s.runtime(id); rt != nil {
			c = rt.activeCall()
		}
		return c != nil
	})
	return c.(*sipCallAdapter).c.(*fakeSIPCall)
}

func TestARecordedAnnouncementFailsTheCall(t *testing.T) {
	// FreePBX with all trunks busy answers 200 OK and plays "all circuits are busy".
	e := newEnv(t)
	line := e.neutralLine()
	e.stt.heard = []string{"All circuits are busy now. Please try your call again later."}
	e.llm.replies = []string{"Hello? [HANGUP]"}
	id := e.confirmed(t)
	c := nextCall(t, line)
	c.answer()
	e.waitState(t, id, StateInCall)
	c.say()
	waitFor(t, "media closed", func() bool { c.advance(); return c.ended() })
	s := e.waitState(t, id, StateFailed)
	if !strings.Contains(s.ErrorMessage, "recorded announcement") || !strings.Contains(s.ErrorMessage, "circuits are busy") {
		t.Fatalf("reason %q", s.ErrorMessage)
	}
	e.llm.mu.Lock()
	streams := len(e.llm.streams)
	e.llm.mu.Unlock()
	if streams != 0 {
		t.Fatalf("the model was asked %d times to talk to a recording", streams)
	}
	e.s.Wait()
}

func TestIsAnnouncement(t *testing.T) {
	for _, s := range []string{
		"All circuits are busy now. Please try your call again later.",
		"We're sorry, the number you have dialed is not in service.",
		"Your call cannot be completed as dialed.",
		"The number you dialed has been disconnected.",
	} {
		if !live.IsAnnouncement(s) {
			t.Errorf("%q not an announcement", s)
		}
	}
	for _, s := range []string{
		"Thanks for calling Tony's, how can I help?",
		"Hello?",
		"We're busy right now, can you hold?",
	} {
		if live.IsAnnouncement(s) {
			t.Errorf("%q taken for an announcement", s)
		}
	}
}

func TestSIPAccountsFollowTheSettingsAndUnregisterOnShutdown(t *testing.T) {
	e := newEnv(t)
	farm := e.sipEnv()
	e.s.SIPReconcileEvery = 20 * time.Millisecond
	e.set(SettingProvider, "sip", settings.Scope{HouseholdID: hh})
	e.setSIP(settings.Scope{HouseholdID: hh}, "pbx.local", "100", "secret")
	ctx, cancel := context.WithCancel(context.Background())
	e.s.Start(ctx)
	// Started with cc: registered before any call.
	waitFor(t, "the household's UA", func() bool { return len(farm.all()) == 1 })
	first := farm.all()[0]
	if st := e.s.SIPStatuses(); len(st) != 1 || st[0].Status.State != sip.StateRegistered || st[0].Account.Extension != "100" {
		t.Fatalf("statuses %+v", st)
	}
	// A new password is a new account: the old UA goes (unregistering), a new one starts.
	e.set(SettingSIPPassword, "new-secret", settings.Scope{HouseholdID: hh})
	waitFor(t, "the new UA", func() bool { return len(farm.all()) == 2 })
	waitFor(t, "the old UA closed", first.isClosed)
	// Back to Twilio: no SIP account wanted.
	e.set(SettingProvider, "twilio", settings.Scope{HouseholdID: hh})
	second := farm.all()[1]
	waitFor(t, "the UA closed", second.isClosed)
	// Shutdown closes what runs.
	e.set(SettingProvider, "sip", settings.Scope{HouseholdID: hh})
	waitFor(t, "a third UA", func() bool { return len(farm.all()) == 3 })
	cancel()
	e.s.Wait()
	if !farm.all()[2].isClosed() {
		t.Fatal("shutdown left a UA registered")
	}
}

func fmtV(a SIPAccount) string { return strings.TrimSpace(strings.Join([]string{a.GoString()}, "")) }
