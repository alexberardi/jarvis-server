package phone

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

// Support for a PBX we cannot see (docs/cc/phone-sip.md §6; decision
// 2026-10-22-sip-support-tools.md): each SIP account's registration status with plain hints,
// and the self-tests an install operator runs from the admin's Phone page. Each test runs in
// the background against one account's UA; the status shows its progress and result.
//
//   - connection: register again and send OPTIONS, with both round-trip times;
//   - echo: dial *43 (the PBX's echo test), play a known phrase, transcribe what comes back:
//     both audio directions, the codec, and the round-trip latency;
//   - digits: dial *65 (the PBX says the extension's number), transcribe, compare;
//   - call_me: the normal confirm card for a call to the operator's own phone, to check the
//     caller ID and dial format on the real trunk;
//   - inbound: watch for the next INVITE the PBX sends this extension (caller ID, codecs, the
//     audio address). Inbound calls are not answered yet (piece 5), so it is refused with 480
//     as every inbound call is, and the caller's audio level cannot be measured.

// SIP self-tests.
const (
	SIPTestConnection = "connection"
	SIPTestEcho       = "echo"
	SIPTestDigits     = "digits"
	SIPTestCallMe     = "call_me"
	SIPTestInbound    = "inbound"
)

// SIPTests are the self-tests, in the order the admin shows them.
var SIPTests = []string{SIPTestConnection, SIPTestEcho, SIPTestDigits, SIPTestCallMe, SIPTestInbound}

// Self-test run states.
const (
	SIPTestRunning = "running"
	SIPTestPassed  = "passed"
	SIPTestFailed  = "failed"
)

// The echo test's phrase, and what it takes to pass.
const (
	sipEchoPhrase = "Jarvis is checking that this phone line carries speech both ways."
	sipEchoMaxWER = 0.35
)

// Timings (tests shorten them).
var (
	sipTestTimeout     = 2 * time.Minute
	sipAnswerTimeout   = 20 * time.Second
	sipPromptQuiet     = 2 * time.Second  // the end of the PBX's echo-test instructions
	sipPromptMax       = 40 * time.Second // FreePBX's are about 22 s
	sipEchoTail        = 3 * time.Second  // recorded after the phrase's length
	sipDigitsMax       = 25 * time.Second
	sipInboundWindow   = 60 * time.Second
	sipInboundPoll     = 250 * time.Millisecond
	sipSpeechThreshold = 300.0 // frame RMS (of 32768) taken for sound
)

// Errors of StartSIPTest.
var (
	ErrSIPAccountNotFound = errors.New("no running SIP account on that local port")
	ErrSIPTestUnknown     = errors.New("unknown SIP self-test")
	ErrSIPTestBusy        = errors.New("a self-test is already running on this account")
)

// SIPTestInput is what a self-test needs from the operator: call_me's number and the
// household whose confirm card it is (the account's own household, else this one).
type SIPTestInput struct {
	Number      string
	HouseholdID string
	UserID      int64
}

// SIPTestRun is one self-test's progress and result.
type SIPTestRun struct {
	Test     string     `json:"test"`
	State    string     `json:"state"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Summary  string     `json:"summary"`
	Hint     string     `json:"hint,omitempty"`
	Steps    []string   `json:"steps"`

	RegisterRTTMs *int64          `json:"register_rtt_ms,omitempty"`
	OptionsRTTMs  *int64          `json:"options_rtt_ms,omitempty"`
	LatencyMs     *int64          `json:"latency_ms,omitempty"`
	Expected      string          `json:"expected,omitempty"`
	Heard         string          `json:"heard,omitempty"`
	WER           *float64        `json:"wer,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	Inbound       *SIPInboundView `json:"inbound,omitempty"`
}

// SIPInboundView is an inbound INVITE as the admin shows it.
type SIPInboundView struct {
	At         time.Time `json:"at"`
	Caller     string    `json:"caller"`
	CallerName string    `json:"caller_name,omitempty"`
	To         string    `json:"to"`
	Media      string    `json:"media,omitempty"`
	Codecs     []string  `json:"codecs"`
	Response   int       `json:"response"`
}

func inboundView(in sip.InboundInvite) *SIPInboundView {
	if in.At.IsZero() {
		return nil
	}
	codecs := in.Codecs
	if codecs == nil {
		codecs = []string{}
	}
	return &SIPInboundView{At: in.At.UTC(), Caller: in.Caller, CallerName: in.CallerName, To: in.To, Media: in.Media,
		Codecs: codecs, Response: in.Response}
}

// SIPAccountView is one SIP account's status for the admin. The password is never in it.
type SIPAccountView struct {
	LocalPort     int                   `json:"local_port"`
	Server        string                `json:"server"`
	Port          int                   `json:"port"`
	Transport     string                `json:"transport"`
	Extension     string                `json:"extension"`
	DialFormat    string                `json:"dial_format"`
	DialPrefix    string                `json:"dial_prefix"`
	HouseholdID   string                `json:"household_id"` // "" for the system account
	State         string                `json:"state"`        // a sip.State, or "not_started"
	Since         *time.Time            `json:"since,omitempty"`
	LastError     string                `json:"last_error"`
	Hint          string                `json:"hint"`
	AuthFailures  int                   `json:"auth_failures"`
	Attempts      int                   `json:"attempts"`
	RegisterRTTMs *int64                `json:"register_rtt_ms"`
	OptionsRTTMs  *int64                `json:"options_rtt_ms"`
	LastOptions   *time.Time            `json:"last_options,omitempty"`
	ActiveCalls   int                   `json:"active_calls"`
	LastInbound   *SIPInboundView       `json:"last_inbound,omitempty"`
	Tests         map[string]SIPTestRun `json:"tests"`
}

// sipTests holds each account's last run of each self-test, by local port.
type sipTests struct {
	mu   sync.Mutex
	runs map[int]map[string]*SIPTestRun
}

func ms(d time.Duration) *int64 {
	if d <= 0 {
		return nil
	}
	v := d.Milliseconds()
	if v == 0 {
		v = 1
	}
	return &v
}

// fmtMs is a round trip for people: "0.4 ms" on a LAN, "38 ms" further away.
func fmtMs(d time.Duration) string {
	if d < 10*time.Millisecond {
		return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

func tptr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// SIPAccounts is every running SIP account's status and last self-tests.
func (s *Service) SIPAccounts() []SIPAccountView {
	s.init()
	s.sip.mu.Lock()
	lines := make([]*sipLine, 0, len(s.sip.lines))
	for _, l := range s.sip.lines {
		lines = append(lines, l)
	}
	s.sip.mu.Unlock()
	slices.SortFunc(lines, func(a, b *sipLine) int { return a.acct.LocalPort - b.acct.LocalPort })
	out := make([]SIPAccountView, 0, len(lines))
	for _, l := range lines {
		a := l.acct
		v := SIPAccountView{LocalPort: a.LocalPort, Server: a.Server, Port: a.Port, Transport: a.Transport,
			Extension: a.Extension, DialFormat: a.DialFormat, DialPrefix: a.DialPrefix, HouseholdID: l.owner,
			State: "not_started", Tests: s.sipTestRuns(a.LocalPort)}
		if l.ua == nil {
			if l.err != nil {
				v.LastError = l.err.Error()
			}
			v.Hint = startHint(l.err)
		} else {
			st := l.ua.Status()
			v.State, v.Since, v.LastError, v.Hint = string(st.State), tptr(st.Since), st.LastError, st.Hint
			v.AuthFailures, v.Attempts, v.ActiveCalls = st.AuthFailures, st.Attempts, st.ActiveCalls
			v.RegisterRTTMs, v.OptionsRTTMs, v.LastOptions = ms(st.RegisterRTT), ms(st.OptionsRTT), tptr(st.LastOptions)
			v.LastInbound = inboundView(st.LastInbound)
		}
		out = append(out, v)
	}
	return out
}

// startHint is a plain reading of why an account's UA could not start.
func startHint(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "cannot resolve"):
		return "Jarvis cannot find the PBX by that name: check phone.sip.server (an IP address avoids DNS)."
	case strings.Contains(msg, "no route"):
		return "This host has no network route to the PBX: check the address, and that both are on the same network or VPN."
	case strings.Contains(msg, "address already in use") || strings.Contains(msg, "local port is taken"):
		return "Another program or SIP account uses this local port: give the account its own phone.sip.local_port."
	}
	return ""
}

func (s *Service) sipTestRuns(port int) map[string]SIPTestRun {
	s.tests.mu.Lock()
	defer s.tests.mu.Unlock()
	out := map[string]SIPTestRun{}
	for name, r := range s.tests.runs[port] {
		c := *r
		c.Steps = append([]string{}, r.Steps...)
		out[name] = c
	}
	return out
}

// StartSIPTest starts test on the account on local port port and returns its first state; the
// account's status shows its progress. One test at a time per account.
func (s *Service) StartSIPTest(ctx context.Context, port int, test string, in SIPTestInput) (SIPTestRun, error) {
	s.init()
	if !slices.Contains(SIPTests, test) {
		return SIPTestRun{}, fmt.Errorf("%w %q (%s)", ErrSIPTestUnknown, test, strings.Join(SIPTests, ", "))
	}
	s.sip.mu.Lock()
	line := s.sip.lines[port]
	s.sip.mu.Unlock()
	if line == nil {
		return SIPTestRun{}, ErrSIPAccountNotFound
	}
	if test == SIPTestCallMe {
		if _, err := NormalizeUS(in.Number); err != nil {
			return SIPTestRun{}, &InputError{Msg: "Enter your own phone number (US or Canada): " + err.Error()}
		}
		if line.owner != "" {
			in.HouseholdID = line.owner
		}
		if in.HouseholdID == "" {
			return SIPTestRun{}, &InputError{Msg: "Pick a household for the confirm card."}
		}
	}
	run := &SIPTestRun{Test: test, State: SIPTestRunning, Started: s.now(), Steps: []string{}}
	s.tests.mu.Lock()
	if s.tests.runs == nil {
		s.tests.runs = map[int]map[string]*SIPTestRun{}
	}
	for _, r := range s.tests.runs[port] {
		if r.State == SIPTestRunning {
			s.tests.mu.Unlock()
			return SIPTestRun{}, ErrSIPTestBusy
		}
	}
	if s.tests.runs[port] == nil {
		s.tests.runs[port] = map[string]*SIPTestRun{}
	}
	s.tests.runs[port][test] = run
	first := *run
	s.tests.mu.Unlock()

	t := &testRun{s: s, run: run}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		tctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx()), sipTestTimeout)
		defer cancel()
		defer func() {
			if p := recover(); p != nil {
				s.log().Error("phone: SIP self-test panicked", "test", test, "panic", p)
				t.fail("The test stopped on an internal error.", "")
			}
		}()
		if line.ua == nil {
			t.fail("The SIP account is not running: "+errString(line.err), startHint(line.err))
			return
		}
		switch test {
		case SIPTestConnection:
			s.connectionTest(tctx, t, line)
		case SIPTestEcho:
			s.echoTest(tctx, t, line)
		case SIPTestDigits:
			s.digitsTest(tctx, t, line)
		case SIPTestCallMe:
			s.callMeTest(tctx, t, line, in)
		case SIPTestInbound:
			s.inboundTest(tctx, t, line)
		}
	}()
	return first, nil
}

// InputError is a self-test request the operator must correct (400).
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func errString(err error) string {
	if err == nil {
		return "not started yet"
	}
	return err.Error()
}

// testRun updates one run under the service's lock.
type testRun struct {
	s   *Service
	run *SIPTestRun
}

func (t *testRun) update(f func(r *SIPTestRun)) {
	t.s.tests.mu.Lock()
	defer t.s.tests.mu.Unlock()
	f(t.run)
}

func (t *testRun) step(format string, a ...any) {
	t.update(func(r *SIPTestRun) { r.Steps = append(r.Steps, fmt.Sprintf(format, a...)) })
}

func (t *testRun) finish(state, summary, hint string) {
	now := t.s.now()
	t.update(func(r *SIPTestRun) {
		if r.State != SIPTestRunning {
			return
		}
		r.State, r.Summary, r.Hint, r.Finished = state, summary, hint, &now
	})
}

func (t *testRun) pass(summary string)       { t.finish(SIPTestPassed, summary, "") }
func (t *testRun) fail(summary, hint string) { t.finish(SIPTestFailed, summary, hint) }

// --- connection ---

func (s *Service) connectionTest(ctx context.Context, t *testRun, line *sipLine) {
	t.step("Registering again with %s:%d as extension %s", line.acct.Server, line.acct.Port, line.acct.Extension)
	res := line.ua.Check(ctx)
	t.update(func(r *SIPTestRun) { r.RegisterRTTMs, r.OptionsRTTMs = ms(res.RegisterRTT), ms(res.OptionsRTT) })
	if !res.Registered {
		t.fail("Not registered: "+orNone(res.Error), res.Hint)
		return
	}
	t.step("Registered in %s", fmtMs(res.RegisterRTT))
	if res.Error != "" {
		t.fail("Registered, but the PBX did not answer OPTIONS: "+res.Error, res.Hint)
		return
	}
	t.step("OPTIONS answered in %s", fmtMs(res.OptionsRTT))
	t.pass(fmt.Sprintf("Registered (%s) and the PBX answers OPTIONS (%s round trip).",
		fmtMs(res.RegisterRTT), fmtMs(res.OptionsRTT)))
}

func orNone(s string) string {
	if s == "" {
		return "no reason given"
	}
	return s
}

// --- calls to a feature code ---

// featureCall dials code and waits for the answer.
func (s *Service) featureCall(ctx context.Context, t *testRun, line *sipLine, code string) (sipCall, bool) {
	t.step("Dialing %s", code)
	c, err := line.ua.DialFeature(ctx, code)
	if err != nil {
		t.fail("Could not place the call: "+err.Error(), line.ua.Status().Hint)
		return nil, false
	}
	select {
	case <-c.Started():
		t.step("Answered; audio is arriving")
		return c, true
	case <-c.Done():
		end, detail := c.End()
		t.fail(fmt.Sprintf("The PBX did not answer %s: %s (%s).", code, end, detail), featureHint(code, end))
	case <-time.After(sipAnswerTimeout):
		c.Close()
		t.fail(fmt.Sprintf("%s was not answered within %s.", code, sipAnswerTimeout), featureHint(code, sip.EndNoAnswer))
	case <-ctx.Done():
		c.Close()
		t.fail("The test timed out.", "")
	}
	return nil, false
}

func featureHint(code string, end sip.End) string {
	what := map[string]string{"*43": "the echo test", "*65": "\"say extension number\""}[code]
	switch end {
	case sip.EndNotFound, sip.EndFailed:
		return fmt.Sprintf("Check that %s is %s on your PBX and enabled (FreePBX: Admin → Feature Codes), "+
			"and that this extension's context may dial feature codes.", code, what)
	case sip.EndNoMedia, sip.EndTransport:
		return "The call was set up but no audio arrived: check that the PBX can send RTP to this host " +
			"(firewall, NAT; FreePBX: Direct Media = no on the extension)."
	}
	return ""
}

// frameRMS is a frame's loudness.
func frameRMS(f []int16) float64 {
	if len(f) == 0 {
		return 0
	}
	var sum float64
	for _, v := range f {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(f)))
}

// onset is the index of the first sample of the first loud 20 ms window in pcm, or -1.
func onset(pcm []int16) int {
	for i := 0; i+sip.FrameSamples <= len(pcm); i += sip.FrameSamples {
		if frameRMS(pcm[i:i+sip.FrameSamples]) > sipSpeechThreshold {
			return i
		}
	}
	return -1
}

func samples(d time.Duration) int { return int(d.Seconds() * 8000) }

// waitPromptEnd reads the far end's audio until it has spoken and then been quiet for
// sipPromptQuiet, or for sipPromptMax: the PBX's instructions before the echo begins.
func waitPromptEnd(ctx context.Context, c sipCall) error {
	spoke, quiet, total := false, 0, 0
	for total < samples(sipPromptMax) {
		select {
		case f, ok := <-c.Frames():
			if !ok {
				return errors.New("the call ended")
			}
			total += len(f)
			if frameRMS(f) > sipSpeechThreshold {
				spoke, quiet = true, 0
			} else if quiet += len(f); spoke && quiet >= samples(sipPromptQuiet) {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// record reads n samples of the far end's audio (less if the call ends).
func record(ctx context.Context, c sipCall, n int) []int16 {
	var out []int16
	for len(out) < n {
		select {
		case f, ok := <-c.Frames():
			if !ok {
				return out
			}
			out = append(out, f...)
		case <-ctx.Done():
			return out
		}
	}
	return out
}

func (s *Service) transcribe(ctx context.Context, pcm []int16) (string, error) {
	res, err := s.STT.Transcribe(ctx, live.WAV(pcm, 8000), stt.TranscribeOptions{})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Text), nil
}

// --- echo (*43) ---

func (s *Service) echoTest(ctx context.Context, t *testRun, line *sipLine) {
	if s.TTS == nil || s.STT == nil {
		t.fail("The echo test needs text to speech and speech to text, and one of them is not running.", "")
		return
	}
	t.update(func(r *SIPTestRun) { r.Expected = sipEchoPhrase })
	pcm, rate, err := s.TTS.SynthesizePCM(ctx, sipEchoPhrase)
	if err != nil {
		t.fail("Text to speech failed: "+err.Error(), "")
		return
	}
	phrase := live.Resample(pcm, rate, 8000)
	spokeAt := max(onset(phrase), 0)
	c, ok := s.featureCall(ctx, t, line, "*43")
	if !ok {
		return
	}
	defer c.Close()
	t.step("Waiting for the PBX's instructions to end")
	if err := waitPromptEnd(ctx, c); err != nil {
		t.fail("The echo test ended before Jarvis could speak: "+err.Error(), "")
		return
	}
	t.step("Playing the test phrase")
	if _, err := c.Play(phrase); err != nil {
		t.fail("Could not play the test phrase: "+err.Error(), "")
		return
	}
	// The inbound audio runs in real time, so a sample count is time since the phrase went out.
	back := record(ctx, c, len(phrase)+samples(sipEchoTail))
	_ = c.Hangup(ctx)
	heardAt := onset(back)
	if heardAt < 0 {
		t.fail("Jarvis played the phrase and heard nothing come back.",
			"Audio from Jarvis is not reaching the PBX, or the PBX's audio is not reaching Jarvis: "+
				"check firewalls and NAT between them, and set Direct Media = no on the extension.")
		return
	}
	latency := time.Duration(max(heardAt-spokeAt, 0)) * time.Second / 8000
	t.update(func(r *SIPTestRun) { r.LatencyMs = ms(max(latency, time.Millisecond)) })
	t.step("Sound came back after %s; transcribing", fmtMs(latency))
	text, err := s.transcribe(ctx, back)
	if err != nil {
		t.fail("Speech to text failed: "+err.Error(), "")
		return
	}
	w := wordErrorRateWithin(sipEchoPhrase, text)
	t.update(func(r *SIPTestRun) { r.Heard, r.WER = text, &w })
	if w > sipEchoMaxWER {
		t.fail(fmt.Sprintf("The echo came back unclear (%.0f%% of words wrong): %q.", w*100, text),
			"Audio is getting through but is garbled: check the extension's codecs (ulaw and alaw) and the network for packet loss.")
		return
	}
	t.pass(fmt.Sprintf("Both audio directions work: the phrase came back after %s and was understood (%.0f%% of words wrong).",
		fmtMs(latency), w*100))
}

// --- digits (*65) ---

func (s *Service) digitsTest(ctx context.Context, t *testRun, line *sipLine) {
	if s.STT == nil {
		t.fail("This test needs speech to text, which is not running.", "")
		return
	}
	want := line.acct.Extension
	t.update(func(r *SIPTestRun) { r.Expected = want })
	c, ok := s.featureCall(ctx, t, line, "*65")
	if !ok {
		return
	}
	defer c.Close()
	t.step("Listening to the PBX")
	heard := record(ctx, c, samples(sipDigitsMax))
	_ = c.Hangup(ctx)
	if onset(heard) < 0 {
		t.fail("The PBX answered but Jarvis heard nothing.",
			"The PBX's audio is not reaching Jarvis: check firewalls and NAT, and set Direct Media = no on the extension.")
		return
	}
	text, err := s.transcribe(ctx, heard)
	if err != nil {
		t.fail("Speech to text failed: "+err.Error(), "")
		return
	}
	t.update(func(r *SIPTestRun) { r.Heard = text })
	got := spokenDigits(text)
	if !strings.Contains(got, want) {
		t.fail(fmt.Sprintf("The PBX said %q; Jarvis expected the extension number %s.", text, want),
			"If the words are clear but the number is different, the PBX knows this device as another extension. "+
				"If they are unclear, check the codecs and the network.")
		return
	}
	t.pass(fmt.Sprintf("Jarvis heard the PBX say its extension number, %s.", want))
}

var digitWords = map[string]string{"zero": "0", "oh": "0", "o": "0", "one": "1", "two": "2", "too": "2", "three": "3",
	"four": "4", "for": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9"}

// spokenDigits is the digits said in text, in order: "one zero one", "101", "1 0 1".
func spokenDigits(text string) string {
	var b strings.Builder
	for _, w := range words(text) {
		if d, ok := digitWords[w]; ok {
			b.WriteString(d)
			continue
		}
		for _, r := range w {
			if unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// --- call my phone ---

func (s *Service) callMeTest(ctx context.Context, t *testRun, line *sipLine, in SIPTestInput) {
	hh := in.HouseholdID
	tel, err := s.Telephony(ctx, hh)
	if err != nil {
		t.fail("This household cannot place calls: "+NotConfiguredMessage(err), "")
		return
	}
	p, isSIP := tel.Provider.(*sipProvider)
	if !isSIP || p.acct.LocalPort != line.acct.LocalPort {
		t.fail("This household's calls do not go out through this SIP account (check phone.provider and its SIP settings).", "")
		return
	}
	if !s.boolSetting(ctx, SettingEnabled, hh) {
		t.fail("Phone calls are turned off for this household (phone_calls.enabled).", "")
		return
	}
	uid := in.UserID
	id, err := s.createPlan(ctx, PlanRequest{
		Business: "Your phone (SIP test)", HouseholdID: hh, UserID: &uid, Number: in.Number,
		Goal: "A test call from Jarvis through the PBX: tell the person this is a test of the phone line and " +
			"ask them to check the caller ID they see, then say goodbye.",
	})
	if err != nil {
		t.fail("Could not prepare the call: "+err.Error(), "")
		return
	}
	if id == "" {
		t.fail("The call was not prepared: see the card in your inbox (a call cap or do-not-call).", "")
		return
	}
	t.update(func(r *SIPTestRun) { r.SessionID = id })
	t.step("Confirm card sent")
	t.pass("A confirm card is in your Jarvis inbox and app. Tap Call now; when your phone rings, check the " +
		"caller ID it shows. A wrong number there is set on the PBX (FreePBX: the extension's Outbound CID).")
}

// --- inbound watch ---

func (s *Service) inboundTest(ctx context.Context, t *testRun, line *sipLine) {
	since := line.ua.Status().LastInbound.At
	t.step("Call this extension (or your Jarvis number) now; watching for %s", sipInboundWindow)
	deadline := time.After(sipInboundWindow)
	tick := time.NewTicker(sipInboundPoll)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			in := line.ua.Status().LastInbound
			if !in.At.After(since) {
				continue
			}
			v := inboundView(in)
			t.update(func(r *SIPTestRun) { r.Inbound = v })
			who := in.Caller
			if in.CallerName != "" {
				who = fmt.Sprintf("%s (%s)", in.Caller, in.CallerName)
			}
			media := "no audio offer"
			if in.Media != "" {
				media = fmt.Sprintf("audio from %s (%s)", in.Media, strings.Join(in.Codecs, ", "))
			}
			t.pass(fmt.Sprintf("The PBX sent a call from %s to %s, %s. %s", orNone(who), in.To, media,
				inboundOutcome(in.Response)))
			return
		case <-deadline:
			t.fail(fmt.Sprintf("No call reached Jarvis in %s.", sipInboundWindow),
				"Check the PBX's inbound route (FreePBX: Connectivity → Inbound Routes) or the ring group that "+
					"should reach this extension, and that the extension shows as available (it answers OPTIONS).")
			return
		case <-ctx.Done():
			t.fail("The test timed out.", "")
			return
		}
	}
}

// inboundOutcome says what Jarvis did with the call the inbound watch saw.
func inboundOutcome(code int) string {
	switch code {
	case 200:
		return "Jarvis answered it."
	case 486:
		return "Jarvis was busy (its call caps), so it refused it and the PBX moved on to its next destination."
	case 487:
		return "The caller hung up, or another phone answered, before Jarvis did."
	case 0:
		return ""
	}
	return fmt.Sprintf("Jarvis refused it (%d) and the PBX moved on to its next destination. Inbound calls are "+
		"off unless the household turns them on in the phone settings.", code)
}

// --- word error rate ---

// words splits text for WER: lower case, punctuation dropped (tools/siplane does the same).
func words(s string) []string {
	ws := strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(s, "’", "'")), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	for i := range ws {
		ws[i] = strings.Trim(ws[i], "'")
	}
	return ws
}

// wordErrorRateWithin is said's word error rate against the best-matching stretch of heard:
// the PBX's own words around the echo are not errors.
func wordErrorRateWithin(said, heard string) float64 {
	a, b := words(said), words(heard)
	if len(a) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			sub := prev[j-1]
			if a[i-1] != b[j-1] {
				sub++
			}
			cur[j] = min(sub, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	best := math.MaxInt
	for _, d := range prev {
		best = min(best, d)
	}
	return float64(best) / float64(len(a))
}

// SIPTrace is every SIP account's recent signalling, redacted (package sip's trace).
func (s *Service) SIPTrace() []sip.TraceDialog { return sip.Trace() }

// RunSIPSelfTest runs one self-test against ua and waits for its result: tools/siplane runs
// jarvisd's own self-tests this way on the L1 lane. call_me needs a household's confirm card
// and is not available here.
func RunSIPSelfTest(ctx context.Context, ua *sip.UA, test string, tts TTS, speech STT) SIPTestRun {
	cfg := ua.Config()
	s := &Service{TTS: tts, STT: speech, Log: cfg.Log}
	line := &sipLine{acct: SIPAccount{Server: cfg.Server, Port: cfg.Port, Transport: cfg.Transport,
		Extension: cfg.Extension, LocalPort: cfg.LocalPort}, ua: realUA{ua}}
	run := &SIPTestRun{Test: test, State: SIPTestRunning, Started: s.now(), Steps: []string{}}
	t := &testRun{s: s, run: run}
	switch test {
	case SIPTestConnection:
		s.connectionTest(ctx, t, line)
	case SIPTestEcho:
		s.echoTest(ctx, t, line)
	case SIPTestDigits:
		s.digitsTest(ctx, t, line)
	case SIPTestInbound:
		s.inboundTest(ctx, t, line)
	default:
		t.fail(fmt.Sprintf("%s cannot run outside jarvisd", test), "")
	}
	t.s.tests.mu.Lock()
	defer t.s.tests.mu.Unlock()
	out := *run
	return out
}
