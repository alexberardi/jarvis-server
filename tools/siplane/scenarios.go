package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
)

// maxWER is how far a transcript may be from what was said, word error rate within the
// recording (wordErrorRateWithin): G.711 at 8 kHz through whisper on the CPU.
const maxWER = 0.25

// report is what one scenario found: notes (what was heard) and failures.
type report struct {
	notes, failures []string
}

func (r *report) notef(format string, a ...any) { r.notes = append(r.notes, fmt.Sprintf(format, a...)) }
func (r *report) failf(format string, a ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, a...))
}

// scenario is one FreePBX behaviour checked on the lane. Run returns an error when the lane
// itself failed (docker, TTS); expectations that do not hold go to the report.
type scenario struct {
	Name, What string
	Run        func(ctx context.Context, l *lane, r *report) error
}

var scenarios = []scenario{
	{"registered", "100 and 101 are registered; the PBX runs FreePBX 17's extension defaults", registered},
	{"echo", "*43, the echo test: what Alice says comes back to her (after FreePBX's 22 s prompt)", echo},
	{"speak-extension", "*65 says the caller's extension number", speakExtension},
	{"internal-call", "Alice calls 100: both directions of audio, through direct media", func(ctx context.Context, l *lane, r *report) error {
		return twoWay(ctx, l, r, "internal-call", "alice", "jarvis", "100",
			"Hi Jarvis, this is Alice at the front desk.", "Hello Alice, how can I help you today?")
	}},
	{"inbound-route", "the carrier calls the Jarvis DID: the inbound route rings 100, audio both ways", func(ctx context.Context, l *lane, r *report) error {
		return twoWay(ctx, l, r, "inbound-route", "carrier", "jarvis", "5551230000",
			"Hello, I am calling about the delivery for tomorrow morning.", "You have reached the house assistant. Who is calling, please?")
	}},
	{"outbound-route", "100 calls a 10-digit number: the outbound route reaches the carrier, audio both ways", func(ctx context.Context, l *lane, r *report) error {
		return twoWay(ctx, l, r, "outbound-route", "jarvis", "carrier", "2125550100",
			"Hi, I would like to book a table for two at seven.", "Sure, we have a table for two at seven tonight.")
	}},
	{"ring-group", "the carrier calls the ring-group DID: 101 and 100 ring, 100 answers, 101 stops ringing", ringGroup},
	{"call-waiting", "a second call to 100 while it is on a call still reaches it (FreePBX: call waiting on)", callWaiting},
	{"busy-no-call-waiting", "with call waiting off, a second call to a busy 100 gets 486 Busy Here", busyNoCallWaiting},
	{"trunk-busy", "an outbound call the trunk refuses busy: 486 Busy Here (SIPp)", func(ctx context.Context, l *lane, r *report) error {
		return sippCall(ctx, l, r, "uac-busy.xml", "2125550486")
	}},
	{"no-answer", "an outbound call nobody answers: 183 with an announcement, then 503, never 200 (SIPp)", func(ctx context.Context, l *lane, r *report) error {
		return sippCall(ctx, l, r, "uac-noanswer.xml", "2125550480")
	}},
	{"all-trunks-busy", "all trunks busy: 200 OK, then the \"all circuits are busy\" recording", allTrunksBusy},
	{"contact-taken", "on 102 (Remove Existing = no, an older install's setting) a second device registering with the right password is refused 403 while the first holds it", contactTaken},
	{"wrong-password-ban", "3 wrong passwords from one address: banned, the right password gets 403 (Responsive Firewall stand-in)", wrongPasswordBan},
	// jarvisd's own SIP provider as 100 (jarvis.go): the stand-in steps aside for each.
	{"jarvis-registers", "jarvisd's SIP provider registers as 100, unregisters on shutdown, registers again on the same port", jarvisRegisters},
	{"jarvis-wrong-password", "jarvisd with a wrong password stops after exactly 3 attempts, with the firewall hint", jarvisWrongPassword},
	{"jarvis-outbound", "jarvisd dials a 10-digit number through the outbound route: audio both ways, transcribed", jarvisOutbound},
	{"jarvis-busy", "jarvisd's call the trunk refuses busy ends busy (486), never answered", jarvisOutcome("2125550486", sip.EndBusy)},
	{"jarvis-no-answer", "jarvisd's call nobody answers (183 + 503) ends no answer, never answered", jarvisOutcome("2125550480", sip.EndNoAnswer)},
	{"jarvis-all-trunks-busy", "all trunks busy: jarvisd's call is answered and what it hears is taken for a recording", jarvisAllTrunksBusy},
	{"jarvis-inbound", "the carrier calls the Jarvis DID: jarvisd answers after the answer delay (caller from PAI), says the disclosure and hears the message", jarvisInbound},
	{"jarvis-inbound-withheld", "a caller who withheld their number (pres prohib): no PAI, an anonymous From (trust_id_outbound=no); jarvisd records no caller ID, answers and hears the message", jarvisInboundWithheld},
	{"jarvis-inbound-ring-group", "the ring-group DID: Alice picks up during jarvisd's answer delay; jarvisd's leg is cancelled, never answered", jarvisInboundRingGroup},
	{"jarvis-inbound-transfer", "jarvisd transfers the caller to Alice (101) with REFER: it waits for the final NOTIFY (200 once 101 rings), hangs up its leg, and the caller and Alice hear each other", jarvisInboundTransfer},
	{"jarvis-inbound-transfer-fails", "a transfer to a busy number (the outbound route, trunk busy): the final NOTIFY says 486, the PBX gives the caller busy, and jarvisd hangs up its emptied leg", jarvisInboundTransferFails},
	{"jarvis-inbound-busy", "a second call while jarvisd is on one: its concurrent cap refuses it 486 and the caller gets busy (voicemail)", jarvisInboundBusy},
	// jarvisd's SIP self-tests (selftest.go; docs/cc/phone-sip.md §6), what the admin's Phone page runs.
	{"jarvis-selftest-connection", "the connection self-test re-registers and pings the PBX, with both round-trip times", selftestConnection},
	{"jarvis-selftest-echo", "the echo self-test dials *43, plays a phrase after the prompt and understands what comes back", selftestEcho},
	{"jarvis-selftest-digits", "the *65 self-test hears the PBX say 100", selftestDigits},
	{"jarvis-selftest-inbound", "the inbound watch sees Alice's call to 100 (refused 480) with her caller ID and audio offer", selftestInbound},
	{"jarvis-selftest-trace", "the SIP trace holds the REGISTER and a *43 call, the digest answer redacted, the password nowhere", selftestTrace},
}

// within is ctx with a timeout, for one wait.
func within(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

// connect opens the named far ends' ctrl_tcp before a call, so no event is missed.
func (l *lane) connect(ctx context.Context, names ...string) (map[string]*ctrl, func(), error) {
	cs := map[string]*ctrl{}
	done := func() {
		for _, c := range cs {
			c.Close()
		}
	}
	for _, n := range names {
		c, err := dialCtrl(ctx, l.party(n).ctrl)
		if err != nil {
			done()
			return nil, nil, err
		}
		cs[n] = c
	}
	return cs, done, nil
}

// expectHeard transcribes p's recording and checks it against what was said.
func (l *lane) expectHeard(ctx context.Context, r *report, scenario string, p party, said ...string) error {
	text, err := l.heard(ctx, p, scenario)
	if err != nil {
		return err
	}
	w, i := bestWER(said, text)
	r.notef("%s heard: %q (WER %.2f against %q)", p.name, text, w, said[max(i, 0)])
	l.keep(scenario+"-"+p.name+".txt", text+"\n")
	if w > maxWER {
		r.failf("%s heard %q, WER %.2f > %.2f against %q", p.name, text, w, maxWER, said[max(i, 0)])
	}
	return nil
}

// waitClosed waits until each far end's call has ended.
func waitClosed(ctx context.Context, r *report, d time.Duration, cs map[string]*ctrl, names ...string) map[string]event {
	got := map[string]event{}
	for _, n := range names {
		w, cancel := within(ctx, d)
		e, err := cs[n].Wait(w, "CALL_CLOSED")
		cancel()
		if err != nil {
			r.failf("%s: the call did not end: %v", n, err)
			continue
		}
		got[n] = e
	}
	return got
}

// twoWay is a call from one far end to a number another answers, each saying something:
// both recordings are transcribed, so both audio directions are proven.
func twoWay(ctx context.Context, l *lane, r *report, name, from, to, number, fromSays, toSays string) error {
	const total = 9
	pf, pt := l.party(from), l.party(to)
	if err := l.say(ctx, pf, fromSays, 1, total); err != nil {
		return err
	}
	if err := l.say(ctx, pt, toSays, 1, total); err != nil {
		return err
	}
	cs, done, err := l.connect(ctx, from, to)
	if err != nil {
		return err
	}
	defer done()
	if err := cs[from].Command(ctx, "dial", l.uri(number)); err != nil {
		return err
	}
	w, cancel := within(ctx, 15*time.Second)
	_, err = cs[from].Wait(w, "CALL_ESTABLISHED")
	cancel()
	if err != nil {
		r.failf("%s calling %s: not answered: %v", from, number, err)
		return nil
	}
	waitClosed(ctx, r, (total+20)*time.Second, cs, from, to)
	if err := l.expectHeard(ctx, r, name, pt, fromSays); err != nil {
		return err
	}
	return l.expectHeard(ctx, r, name, pf, toSays)
}

var contactLine = regexp.MustCompile(`Contact:\s+(\d+)/\S+\s+\S+\s+(Avail|NonQual)`)

func registered(ctx context.Context, l *lane, r *report) error {
	out, err := l.cli(ctx, "pjsip show contacts")
	if err != nil {
		return err
	}
	l.keep("contacts.txt", out)
	reg := map[string]bool{}
	for _, m := range contactLine.FindAllStringSubmatch(out, -1) {
		reg[m[1]] = true
	}
	for _, ext := range []string{"100", "101"} {
		if !reg[ext] {
			r.failf("extension %s has no reachable contact", ext)
		}
	}
	ep, err := l.cli(ctx, "pjsip show endpoint 100")
	if err != nil {
		return err
	}
	aor, err := l.cli(ctx, "pjsip show aor 100")
	if err != nil {
		return err
	}
	l.keep("endpoint-100.txt", ep+"\n"+aor)
	shown := shownSettings(ep)
	for k, v := range shownSettings(aor) {
		shown[k] = v
	}
	if bad := missingDefaults(shown); len(bad) > 0 {
		r.failf("endpoint 100 lacks FreePBX defaults: %s", strings.Join(bad, ", "))
	}
	r.notef("registered: %d extensions; endpoint 100: direct_media=%s timers=%s rtp_timeout=%s send_pai=%s max_contacts=%s remove_existing=%s qualify_frequency=%s",
		len(reg), shown["direct_media"], shown["timers"], shown["rtp_timeout"], shown["send_pai"], shown["max_contacts"], shown["remove_existing"], shown["qualify_frequency"])
	return nil
}

func echo(ctx context.Context, l *lane, r *report) error {
	const said = "The echo test repeats every word I say."
	// demo-echotest plays for 22 s after a 1 s wait; Echo() starts after it.
	const lead, total = 24, 32
	a := l.party("alice")
	if err := l.say(ctx, a, said, lead, total); err != nil {
		return err
	}
	cs, done, err := l.connect(ctx, "alice")
	if err != nil {
		return err
	}
	defer done()
	if err := cs["alice"].Command(ctx, "dial", l.uri("*43")); err != nil {
		return err
	}
	waitClosed(ctx, r, (total+20)*time.Second, cs, "alice")
	if !cs["alice"].Saw("CALL_ESTABLISHED") {
		r.failf("*43 was not answered: %s", strings.Join(cs["alice"].Seen(), " "))
		return nil
	}
	return l.expectHeard(ctx, r, "echo", a, said)
}

func speakExtension(ctx context.Context, l *lane, r *report) error {
	a := l.party("alice")
	if err := l.say(ctx, a, "", 0, 15); err != nil {
		return err
	}
	cs, done, err := l.connect(ctx, "alice")
	if err != nil {
		return err
	}
	defer done()
	if err := cs["alice"].Command(ctx, "dial", l.uri("*65")); err != nil {
		return err
	}
	// The PBX hangs up after saying it (about 8 s).
	ev := waitClosed(ctx, r, 35*time.Second, cs, "alice")
	if !cs["alice"].Saw("CALL_ESTABLISHED") {
		r.failf("*65 was not answered: %s", strings.Join(cs["alice"].Seen(), " "))
		return nil
	}
	r.notef("alice's call ended: %s", ev["alice"].Param)
	return l.expectHeard(ctx, r, "speak-extension", a,
		"your extension number is one zero one", "your extension number is 101", "your extension number is 1 0 1")
}

func ringGroup(ctx context.Context, l *lane, r *report) error {
	const said, total = "Thanks for calling, the ring group reached Jarvis.", 9
	c, j := l.party("carrier"), l.party("jarvis")
	if err := l.say(ctx, c, "", 0, total); err != nil {
		return err
	}
	if err := l.say(ctx, j, said, 1, total); err != nil {
		return err
	}
	cs, done, err := l.connect(ctx, "carrier", "jarvis", "alice")
	if err != nil {
		return err
	}
	defer done()
	if err := cs["carrier"].Command(ctx, "dial", l.uri("5551230600")); err != nil {
		return err
	}
	waitClosed(ctx, r, (total+20)*time.Second, cs, "carrier", "jarvis", "alice")
	switch a := cs["alice"]; {
	case !a.Saw("CALL_INCOMING"):
		r.failf("101 did not ring (ring all): %s", strings.Join(a.Seen(), " "))
	case a.Saw("CALL_ESTABLISHED"):
		r.failf("101 was connected though 100 answered: %s", strings.Join(a.Seen(), " "))
	default:
		r.notef("101 rang and stopped when 100 answered: %s", strings.Join(a.Seen(), " "))
	}
	if !cs["jarvis"].Saw("CALL_ESTABLISHED") {
		r.failf("100 did not answer: %s", strings.Join(cs["jarvis"].Seen(), " "))
		return nil
	}
	return l.expectHeard(ctx, r, "ring-group", c, said)
}

// secondCall puts Alice on a call with 100, then has the carrier call 100's DID, and returns
// the far ends' event logs and how the carrier's call ended.
func secondCall(ctx context.Context, l *lane, r *report) (map[string]*ctrl, func(), event, error) {
	if err := l.say(ctx, l.party("alice"), "", 0, 14); err != nil {
		return nil, nil, event{}, err
	}
	if err := l.say(ctx, l.party("jarvis"), "", 0, 14); err != nil {
		return nil, nil, event{}, err
	}
	if err := l.say(ctx, l.party("carrier"), "", 0, 5); err != nil {
		return nil, nil, event{}, err
	}
	cs, done, err := l.connect(ctx, "alice", "jarvis", "carrier")
	if err != nil {
		return nil, nil, event{}, err
	}
	if err := cs["alice"].Command(ctx, "dial", l.uri("100")); err != nil {
		done()
		return nil, nil, event{}, err
	}
	w, cancel := within(ctx, 15*time.Second)
	_, err = cs["jarvis"].Wait(w, "CALL_ESTABLISHED")
	cancel()
	if err != nil {
		r.failf("the first call to 100 was not answered: %v", err)
		done()
		return nil, nil, event{}, errFirstCall
	}
	time.Sleep(time.Second)
	// 100 lets the second call ring: auto-answering it would put the first on hold, and
	// baresip 1.1.0 can segfault replacing the call's player for the hold on a loaded machine.
	// Ringing is all call waiting has to show; the carrier hangs up once it has.
	if err := cs["jarvis"].Command(ctx, "answermode", "manual"); err != nil {
		done()
		return nil, nil, event{}, err
	}
	restore := func() {
		w, cancel := within(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := cs["jarvis"].Command(w, "answermode", "auto"); err != nil {
			r.failf("100 back to auto answer: %v", err)
		}
	}
	if err := cs["carrier"].Command(ctx, "dial", l.uri("5551230000")); err != nil {
		restore()
		done()
		return nil, nil, event{}, err
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if cs["carrier"].Saw("CALL_CLOSED") {
			break
		}
		if cs["jarvis"].Calls("incoming") >= 2 {
			if err := cs["carrier"].Command(ctx, "hangup", ""); err != nil {
				r.failf("carrier hanging up the second call: %v", err)
			}
			break
		}
	}
	ev := waitClosed(ctx, r, 30*time.Second, cs, "carrier")
	restore()
	waitClosed(ctx, r, 30*time.Second, cs, "alice")
	return cs, done, ev["carrier"], nil
}

var errFirstCall = fmt.Errorf("first call failed")

func callWaiting(ctx context.Context, l *lane, r *report) error {
	cs, done, closed, err := secondCall(ctx, l, r)
	if err == errFirstCall {
		return nil
	}
	if err != nil {
		return err
	}
	defer done()
	if n := cs["jarvis"].Calls("incoming"); n < 2 {
		r.failf("the second call did not reach 100 (%d incoming calls; carrier's call ended %q)", n, closed.Param)
	} else {
		r.notef("100 got the second call while on the first (call waiting); carrier's call ended %q", closed.Param)
	}
	return nil
}

func busyNoCallWaiting(ctx context.Context, l *lane, r *report) error {
	if _, err := l.cli(ctx, "database put CW 100 DISABLED"); err != nil {
		return err
	}
	defer l.cli(context.WithoutCancel(ctx), "database del CW 100")
	cs, done, closed, err := secondCall(ctx, l, r)
	if err == errFirstCall {
		return nil
	}
	if err != nil {
		return err
	}
	defer done()
	if !strings.HasPrefix(closed.Param, "486") {
		r.failf("the carrier's call to a busy 100 ended %q, want 486 Busy Here", closed.Param)
	}
	if n := cs["jarvis"].Calls("incoming"); n != 1 {
		r.failf("100 got %d calls; with call waiting off the second must not ring it", n)
	}
	r.notef("carrier's call to the busy 100: %q", closed.Param)
	return nil
}

func sippCall(ctx context.Context, l *lane, r *report, xml, number string) error {
	if err := l.sipp(ctx, xml, "127.0.0.1", "-s", number, "-key", "caller", "100", "-au", "100", "-ap", "lane-jarvis-100"); err != nil {
		r.failf("%v", err)
		return nil
	}
	r.notef("SIPp %s calling %s: the PBX answered as the scenario expects", xml, number)
	return nil
}

func allTrunksBusy(ctx context.Context, l *lane, r *report) error {
	j := l.party("jarvis")
	if err := l.say(ctx, j, "", 0, 20); err != nil {
		return err
	}
	cs, done, err := l.connect(ctx, "jarvis")
	if err != nil {
		return err
	}
	defer done()
	if err := cs["jarvis"].Command(ctx, "dial", l.uri("2125550200")); err != nil {
		return err
	}
	ev := waitClosed(ctx, r, 40*time.Second, cs, "jarvis")
	if !cs["jarvis"].Saw("CALL_ESTABLISHED") {
		r.failf("not answered (want 200 OK, then the recording): %s", strings.Join(cs["jarvis"].Seen(), " "))
		return nil
	}
	r.notef("answered (200 OK), then the PBX hung up: %q", ev["jarvis"].Param)
	return l.expectHeard(ctx, r, "all-trunks-busy", j,
		"all circuits are busy now please try your call later", "all circuits are busy now please try your call again later")
}

// holderAddr is where contactTaken's first device registers 102 from; the second comes from
// 127.0.0.1, so the two contacts differ (rewrite_contact takes the source address).
const holderAddr = "127.0.0.2"

func contactTaken(ctx context.Context, l *lane, r *report) error {
	ext := contactTakenExt
	creds := []string{"-key", "caller", ext, "-au", ext, "-ap", "lane-desk-" + ext}
	register := func(addr, expires string) error {
		return l.sipp(ctx, "register.xml", addr, append(creds, "-key", "expires", expires)...)
	}
	if err := register(holderAddr, "60"); err != nil {
		r.failf("the first device did not register %s: %v", ext, err)
		return nil
	}
	defer register(holderAddr, "0") // the next run's first device registers again
	if err := l.sipp(ctx, "register-contact-taken.xml", "127.0.0.1", creds...); err != nil {
		r.failf("%v", err)
		return nil
	}
	out, err := l.cli(ctx, "pjsip show aor "+ext)
	if err != nil {
		return err
	}
	l.keep("contact-taken-aor-"+ext+".txt", out)
	if !strings.Contains(out, holderAddr+":") {
		r.failf("%s's contact is no longer the first device's:\n%s", ext, out)
	}
	r.notef("a second contact for %s was refused 403 (remove_existing=no); the first device's stays", ext)
	return nil
}

// The banned address: anything on loopback but 127.0.0.1, so the far ends keep working.
const bannedAddr = "127.0.0.66"

func wrongPasswordBan(ctx context.Context, l *lane, r *report) error {
	unban := func() {
		l.docker(context.WithoutCancel(ctx), "exec", l.env["SIP_LANE_ASTERISK"], "lane-firewall", "clear")
	}
	unban()
	defer unban()
	for i := 1; i <= 3; i++ {
		if err := l.sipp(ctx, "register-wrong-password.xml", bannedAddr, "-key", "caller", "101", "-au", "101", "-ap", "not-the-password"); err != nil {
			r.failf("wrong password %d: %v", i, err)
			return nil
		}
	}
	banned := false
	for range 20 {
		out, err := l.docker(ctx, "exec", l.env["SIP_LANE_ASTERISK"], "cat", "/var/lib/asterisk/lane/pjsip_ban.conf")
		if err != nil {
			return err
		}
		if strings.Contains(out, "deny="+bannedAddr+"/32") {
			banned = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !banned {
		r.failf("%s was not banned after 3 failed authentications", bannedAddr)
		return nil
	}
	if err := l.sipp(ctx, "register-banned.xml", bannedAddr, "-key", "caller", "101", "-au", "101", "-ap", "lane-alice-101"); err != nil {
		r.failf("after the ban: %v", err)
		return nil
	}
	out, err := l.cli(ctx, "pjsip show contacts")
	if err != nil {
		return err
	}
	if !contactLine.MatchString(out) || !strings.Contains(out, "101/") {
		r.failf("the ban took 101's own phone off too:\n%s", out)
	}
	r.notef("3 wrong passwords from %s: banned; the right password then got 403; 127.0.0.1's phones unaffected", bannedAddr)
	return nil
}
