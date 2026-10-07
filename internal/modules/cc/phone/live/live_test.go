package live

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Unit tests ported from the gateway's tests/ (test_mulaw, test_vad, test_tool_tokens,
// test_think_strip, test_speech_format, test_spoken_guard, test_twilio_provider,
// test_recording, test_escalation).

func ramp(lo, hi float64, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(lo + (hi-lo)*float64(i)/float64(n-1))
	}
	return out
}

func TestMulawRoundTrip(t *testing.T) {
	for _, c := range []struct {
		pcm    []int16
		maxErr int32
	}{{ramp(-30000, 30000, 4000), 1023}, {ramp(-100, 100, 500), 8}} {
		dec := MulawDecode(MulawEncode(c.pcm))
		for i, s := range c.pcm {
			d := int32(dec[i]) - int32(s)
			if d < 0 {
				d = -d
			}
			if d > c.maxErr {
				t.Fatalf("sample %d: %d -> %d", i, s, dec[i])
			}
		}
	}
	if len(MulawEncode(make([]int16, 160))) != 160 {
		t.Fatal("one byte per sample")
	}
}

func TestRMS(t *testing.T) {
	full := make([]int16, 160)
	for i := range full {
		full[i] = 1000
	}
	if RMS(make([]int16, 160)) != 0 || RMS(nil) != 0 || RMS(full) != 1000 {
		t.Fatal("rms")
	}
}

func sine(freq float64, rate, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(8000 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

func TestResample(t *testing.T) {
	if got := Resample(make([]int16, 3200), 16000, 8000); len(got) != 1600 {
		t.Fatalf("16k->8k len %d", len(got))
	}
	if got := Resample(make([]int16, 800), 8000, 16000); len(got) != 1600 {
		t.Fatalf("8k->16k len %d", len(got))
	}
	if got := Resample(make([]int16, 1001), 24000, 8000); len(got) != 334 {
		t.Fatalf("24k->8k len %d (ceil)", len(got))
	}
	p := []int16{1, 2, 3}
	if got := Resample(p, 8000, 8000); &got[0] != &p[0] {
		t.Fatal("same rate is identity")
	}
	// A 1 kHz tone survives 24k -> 8k at about its level.
	out := Resample(sine(1000, 24000, 24000), 24000, 8000)
	if r := RMS(out[200 : len(out)-200]); math.Abs(r-8000/math.Sqrt2) > 300 {
		t.Fatalf("1 kHz passband rms %v", r)
	}
	// A 6 kHz tone is above the 4 kHz Nyquist of the output and must be filtered, not aliased.
	out = Resample(sine(6000, 24000, 24000), 24000, 8000)
	if r := RMS(out[200 : len(out)-200]); r > 400 {
		t.Fatalf("6 kHz leaked through as alias: rms %v", r)
	}
}

func TestWAV(t *testing.T) {
	pcm := ramp(-500, 499, 1000)
	w := WAV(pcm, 8000)
	if string(w[:4]) != "RIFF" || string(w[8:16]) != "WAVEfmt " || string(w[36:40]) != "data" {
		t.Fatal("header")
	}
	if binary.LittleEndian.Uint16(w[22:]) != 1 || binary.LittleEndian.Uint32(w[24:]) != 8000 || binary.LittleEndian.Uint16(w[34:]) != 16 {
		t.Fatal("format")
	}
	if !reflect.DeepEqual(PCMBytesToInt16(w[44:]), pcm) {
		t.Fatal("frames")
	}
}

func frame(v int16) []int16 {
	f := make([]int16, 160)
	for i := range f {
		f[i] = v
	}
	return f
}

var vadCfg = VADConfig{ThresholdRMS: 250, FrameMS: 20, HangoverMS: 100, StartFrames: 3, PrerollFrames: 5, MaxUtteranceS: 1}

func TestVAD(t *testing.T) {
	loud, quiet := frame(3000), frame(50)
	v := NewVAD(vadCfg)
	for range 200 {
		if v.Feed(quiet, false) != nil {
			t.Fatal("silence endpointed")
		}
	}
	v.Feed(loud, false)
	v.Feed(loud, false)
	v.Feed(quiet, false)
	if v.InSpeech() {
		t.Fatal("debounce")
	}
	for range 3 {
		v.Feed(loud, false)
	}
	if !v.InSpeech() {
		t.Fatal("three loud frames enter speech")
	}

	v = NewVAD(vadCfg)
	for range 4 {
		v.Feed(quiet, false)
	}
	for range 5 {
		v.Feed(loud, false)
	}
	var utt []int16
	for range vadCfg.hangoverFrames() {
		utt = v.Feed(quiet, false)
	}
	if utt == nil || len(utt) <= 5*160 || v.InSpeech() {
		t.Fatalf("endpoint with preroll: %d", len(utt))
	}
	// Preroll keeps the last 5 frames at speech onset (2 quiet + 3 loud), then 2 loud + 5 hangover.
	if len(utt) != (5+2+5)*160 {
		t.Fatalf("utterance frames %d", len(utt)/160)
	}

	v = NewVAD(vadCfg)
	got := false
	for range 60 {
		if v.Feed(loud, false) != nil {
			got = true
			break
		}
	}
	if !got {
		t.Fatal("max utterance forces endpoint")
	}

	v = NewVAD(vadCfg)
	for range 5 {
		v.Feed(loud, false)
	}
	if v.Feed(loud, true) != nil || v.InSpeech() {
		t.Fatal("suppress discards")
	}
	for range 20 {
		if v.Feed(quiet, false) != nil {
			t.Fatal("phantom utterance")
		}
	}
}

func parseAll(deltas ...string) (string, []ToolEvent) {
	var p TokenParser
	var text strings.Builder
	var evs []ToolEvent
	for _, d := range deltas {
		s, e := p.Feed(d)
		text.WriteString(s)
		evs = append(evs, e...)
	}
	text.WriteString(p.Flush())
	return text.String(), evs
}

func TestTokenParser(t *testing.T) {
	cases := []struct {
		in   []string
		text string
		evs  []ToolEvent
	}{
		{[]string{"Good", "bye now."}, "Goodbye now.", nil},
		{[]string{"Goodbye! ", "[HANGUP]"}, "Goodbye! ", []ToolEvent{Hangup{}}},
		{[]string{"Bye. [HA", "NG", "UP]"}, "Bye. ", []ToolEvent{Hangup{}}},
		{[]string{"One moment. [ESCALATE: only 6:30 available — ok?]"}, "One moment. ", []ToolEvent{Escalate{"only 6:30 available — ok?"}}},
		{[]string{"[ESCALATE: can we ", "do 7pm instead?]"}, "", []ToolEvent{Escalate{"can we do 7pm instead?"}}},
		{[]string{"[OUTCOME: booked Friday 7pm, conf #A12]", "[DTMF: 1#]"}, "", []ToolEvent{Outcome{"booked Friday 7pm, conf #A12"}, Dtmf{"1#"}}},
		{[]string{"The price [sic] is right."}, "The price [sic] is right.", nil},
		{[]string{"Wait [ESCALATE: never closed"}, "Wait [ESCALATE: never closed", nil},
		{[]string{"A [OUTCOME: x] B [HANGUP] C"}, "A  B  C", []ToolEvent{Outcome{"x"}, Hangup{}}},
		{[]string{"[hangup]"}, "[hangup]", nil},
	}
	for _, c := range cases {
		text, evs := parseAll(c.in...)
		if text != c.text || !reflect.DeepEqual(evs, c.evs) {
			t.Errorf("%q: got %q %v", c.in, text, evs)
		}
	}
	var p TokenParser
	if s, e := p.Feed("a [sic] b"); s != "a [sic] b" || e != nil {
		t.Errorf("unknown bracket must be released promptly: %q", s)
	}
	var q TokenParser
	s, _ := q.Feed("[ESCALATE: " + strings.Repeat("x", 600))
	if !strings.Contains(s+q.Flush(), "[ESCALATE: ") {
		t.Error("runaway bracket")
	}
}

func stripAll(deltas ...string) string {
	var s ThinkStripper
	var b strings.Builder
	for _, d := range deltas {
		b.WriteString(s.Feed(d))
	}
	return b.String() + s.Flush()
}

func TestThinkStripper(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"Hello ", "there."}, "Hello there."},
		{[]string{"<think>reasoning</think>Hi."}, "Hi."},
		{[]string{"<thi", "nk>secret ", "stuff</th", "ink>Hello."}, "Hello."},
		{[]string{"Sure. <think>hmm</think> Yes."}, "Sure.  Yes."},
		{[]string{"<think>a</think>One.<think>b</think>Two."}, "One.Two."},
		{[]string{"Okay. <think>never say this"}, "Okay. "},
		{[]string{"a <thin", "g> b"}, "a <thing> b"},
		{[]string{"5 < 7 and 9 > 3."}, "5 < 7 and 9 > 3."},
	} {
		if got := stripAll(c.in...); got != c.want {
			t.Errorf("%q: %q", c.in, got)
		}
	}
	var s ThinkStripper
	if s.Feed("Hello there, ") != "Hello there, " || s.Feed("friend.") != "friend." || s.Flush() != "" {
		t.Error("clean text must flow immediately")
	}
}

func TestSentenceSplitter(t *testing.T) {
	var s SentenceSplitter
	var got []string
	for _, d := range []string{"Hi", " there. How", " are you?  Fine… ok", "!"} {
		got = append(got, s.Feed(d)...)
	}
	got = append(got, s.Flush()...)
	want := []string{"Hi there.", "How are you?", "Fine…", "ok!"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
	if !reflect.DeepEqual(splitSentences("a. b"), []string{"a.", "b"}) || len(splitSentences("3.5 x")) != 1 {
		t.Fatal("split")
	}
}

func TestFormatForSpeech(t *testing.T) {
	for _, c := range [][2]string{
		{"Your member ID is ZQ-0001234.", "Z as in Zebra, Q as in Queen, 0, 0, 0, 1, 2, 3, 4"},
		{"The policy number is 7654321.", "7, 6, 5, 4, 3, 2, 1"},
		{"It is spelled S-M-I-T-H.", "S as in Sam, M as in Mary, I as in Igloo, T as in Tom, H as in Henry"},
		{"Confirmation is ABC12345.", "A as in Apple, B as in Boy, C as in Cat, 1, 2, 3, 4, 5"},
		{"id zq0001234 please", "Z as in Zebra, Q as in Queen, 0"},
	} {
		if got := FormatForSpeech(c[0]); !strings.Contains(got, c[1]) {
			t.Errorf("%q -> %q", c[0], got)
		}
	}
	if !strings.HasSuffix(FormatForSpeech("It is ZQ-0001234."), "4.") {
		t.Error("trailing punctuation")
	}
	for _, s := range []string{"The appointment is Tuesday at 4pm.", "It was booked for 2026.", "Party of 4 at 7:30 please.",
		"That will be $45 at pickup.", "I live at 123 Main Street.", "See you in 30 minutes.", "Order number 12 is ready.",
		"", "It is 9 9 1 2 3 4 5.", "Hi, I'm calling to confirm the appointment for Jordan."} {
		if got := FormatForSpeech(s); got != s {
			t.Errorf("%q changed to %q", s, got)
		}
	}
}

func TestGuard(t *testing.T) {
	got := ParseRestricted([]any{map[string]any{"key": "address", "label": "Address", "value": "742 Evergreen Ave"}})
	if !reflect.DeepEqual(got, []RestrictedField{{"address", "Address", "742 Evergreen Ave"}}) {
		t.Fatal(got)
	}
	for _, raw := range []any{nil, "", map[string]any{}, []any{}, []any{"nope"}, []any{map[string]any{"key": "a"}}, []any{map[string]any{"value": "v"}}} {
		if ParseRestricted(raw) != nil {
			t.Errorf("%v should yield nothing", raw)
		}
	}
	if f := ParseRestricted([]any{map[string]any{"key": "gate_code", "value": 4417.0}}); f[0].Label != "gate_code" || f[0].Value != "4417" {
		t.Errorf("%+v", f)
	}
	member := RestrictedField{"insurance_member_id", "Insurance member ID", "XZ-9912345"}
	callback := RestrictedField{"callback_number", "Callback number", "(908) 555-0147"}
	address := RestrictedField{"address", "Address", "742 Evergreen Ave, Springfield, IL 62704"}
	for _, c := range []struct {
		s, v string
		want bool
	}{
		{"My member ID is XZ-9912345.", member.Value, true},
		{"It's 9085550147.", callback.Value, true},
		{"We're at 742 Evergreen Avenue.", address.Value, true},
		{"Sure, that works for us.", member.Value, false},
		{"Oh, great — see you at six.", callback.Value, false},
	} {
		if Mentions(c.s, c.v) != c.want {
			t.Errorf("Mentions(%q, %q) != %v", c.s, c.v, c.want)
		}
	}
	hit := FindRestricted("I'm at 742 Evergreen Avenue, reach me at 908-555-0147.", []RestrictedField{member, callback, address})
	if len(hit) != 2 || hit[0].Key != "callback_number" || hit[1].Key != "address" {
		t.Errorf("%+v", hit)
	}
	msgs := ClassifierMessages("what is it?", []RestrictedField{member, callback, address})
	all := msgs[0][1] + msgs[1][1]
	for _, f := range []RestrictedField{member, callback, address} {
		if strings.Contains(all, f.Value) || !strings.Contains(all, f.Label) {
			t.Errorf("classifier must see labels, never values: %s", f.Key)
		}
	}
	if !strings.Contains(all, "/no_think") {
		t.Error("no_think must ride along")
	}
	if len(ParseVerdict("<think>Okay, let's see. The user asked", 2)) != 0 {
		t.Error("truncated reasoning yields nothing")
	}
}

func TestTwilioWire(t *testing.T) {
	ev := ParseWSMessage([]byte(`{"event":"start","start":{"streamSid":"MZ123","callSid":"CA456","customParameters":{"session_id":"sess-1"}}}`))
	s, ok := ev.(StreamStart)
	if !ok || s.StreamSID != "MZ123" || s.CallSID != "CA456" || !s.CallSIDSet || s.Params["session_id"] != "sess-1" {
		t.Fatalf("%+v", ev)
	}
	enc := MulawEncode(frame(1000))
	ev = ParseWSMessage([]byte(`{"event":"media","media":{"payload":"` + base64.StdEncoding.EncodeToString(enc) + `"}}`))
	a, ok := ev.(InboundAudio)
	if !ok || len(a.PCM) != 160 || math.Abs(float64(a.PCM[0])-1000) >= 64 {
		t.Fatalf("%+v", ev)
	}
	if m, ok := ParseWSMessage([]byte(`{"event":"mark","mark":{"name":"t1"}}`)).(MarkReceived); !ok || m.Name != "t1" {
		t.Fatal("mark")
	}
	if _, ok := ParseWSMessage([]byte(`{"event":"stop"}`)).(StreamStop); !ok {
		t.Fatal("stop")
	}
	if ParseWSMessage([]byte(`{"event":"connected"}`)) != nil || ParseWSMessage([]byte("not json")) != nil {
		t.Fatal("ignorable")
	}
	msgs := MediaMessages("MZ1", make([]int16, 400))
	if len(msgs) != 3 {
		t.Fatal(len(msgs))
	}
	for i, want := range []int{160, 160, 80} {
		p, _ := base64.StdEncoding.DecodeString(msgs[i]["media"].(map[string]any)["payload"].(string))
		if len(p) != want || msgs[i]["event"] != "media" || msgs[i]["streamSid"] != "MZ1" {
			t.Fatal("frame split")
		}
	}
	if MarkMessage("MZ1", "t3")["mark"].(map[string]any)["name"] != "t3" ||
		!reflect.DeepEqual(ClearMessage("MZ1"), map[string]any{"event": "clear", "streamSid": "MZ1"}) {
		t.Fatal("mark/clear")
	}
}

func TestTwilioSignature(t *testing.T) {
	const tok = "test-auth-token"
	wss := "wss://calls.example.com/media/tok123"
	https := "https://calls.example.com/media/tok123"
	sig := ComputeSignature(tok, wss, nil)
	if ComputeSignature(tok, https, nil) == sig {
		t.Fatal("naive https validation must differ")
	}
	if !ValidateWSSignature(tok, https, sig) || !ValidateWSSignature(tok, wss, sig) {
		t.Fatal("scheme fix")
	}
	if ValidateWSSignature("other", https, sig) || ValidateWSSignature(tok, "https://calls.example.com/media/other", sig) ||
		ValidateWSSignature(tok, https, "") {
		t.Fatal("must reject")
	}
	if !ValidateWSSignature(tok, "http://h/x", ComputeSignature(tok, "ws://h/x", nil)) {
		t.Fatal("ws/http pair")
	}
}

func TestTokenRegistry(t *testing.T) {
	var r TokenRegistry
	tok := r.Issue("sess-1")
	r.BindCallSID(tok, "CA789")
	p, ok := r.Claim(tok)
	if !ok || p.SessionID != "sess-1" || p.CallSID != "CA789" {
		t.Fatal("claim")
	}
	if _, ok := r.Claim(tok); ok {
		t.Fatal("single use")
	}
	if _, ok := r.Claim("bogus"); ok {
		t.Fatal("unknown")
	}
	seen := map[string]bool{}
	for range 50 {
		tk := r.Issue("s")
		if len(tk) < 32 || seen[tk] {
			t.Fatal("tokens unique and long")
		}
		seen[tk] = true
	}
	t2 := r.Issue("x")
	r.Revoke(t2)
	if _, ok := r.Claim(t2); ok {
		t.Fatal("revoked")
	}
	start := func(sid, call string, set bool) StreamStart {
		return StreamStart{CallSID: call, CallSIDSet: set, Params: map[string]string{"session_id": sid}}
	}
	if !ValidateStreamStart(start("sess-1", "CA1", true), PendingSession{"sess-1", "CA1"}) ||
		ValidateStreamStart(start("sess-2", "CA1", true), PendingSession{"sess-1", "CA1"}) ||
		ValidateStreamStart(start("sess-1", "CA9", true), PendingSession{"sess-1", "CA1"}) ||
		ValidateStreamStart(start("sess-1", "", false), PendingSession{"sess-1", "CA1"}) ||
		!ValidateStreamStart(start("sess-1", "CA9", true), PendingSession{"sess-1", ""}) ||
		ValidateStreamStart(StreamStart{}, PendingSession{SessionID: ""}) {
		t.Fatal("binding")
	}
}

func TestTokenRegistryConcurrent(t *testing.T) {
	var r TokenRegistry
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk := r.Issue("s")
			r.BindCallSID(tk, "c")
			r.Claim(tk)
		}()
	}
	wg.Wait()
}

func tone(n int, v int16) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestRecorder(t *testing.T) {
	var r Recorder
	r.AddInbound(tone(100, 0))
	r.AddOutbound(tone(50, 2000))
	r.AddInbound(tone(100, 1000))
	m := r.Mix()
	if len(m) != 200 || m[99] != 0 || m[100] != 3000 || m[150] != 1000 {
		t.Fatal("anchored mix")
	}
	var r2 Recorder
	r2.AddInbound(tone(10, 0))
	r2.AddOutbound(tone(100, 500))
	if len(r2.Mix()) != 110 {
		t.Fatal("extend")
	}
	var r3 Recorder
	r3.AddOutbound(tone(10, 30000))
	r3.AddInbound(tone(10, 30000))
	if r3.Mix()[0] != 32767 {
		t.Fatal("clip")
	}
	if w := r3.WAV(); binary.LittleEndian.Uint32(w[24:]) != 8000 {
		t.Fatal("wav rate")
	}
}

func TestEscalationWindow(t *testing.T) {
	e := &EscalationWindow{Timeout: 2 * time.Second}
	if e.Deliver("x") {
		t.Fatal("no window")
	}
	if !e.Open() || e.Open() {
		t.Fatal("one at a time")
	}
	go func() {
		time.Sleep(10 * time.Millisecond)
		e.Deliver("6:30 is fine")
	}()
	ans, ok := e.Wait(context.Background())
	if !ok || ans != "6:30 is fine" || e.IsOpen() {
		t.Fatalf("%q %v", ans, ok)
	}
	e.Timeout = 20 * time.Millisecond
	e.Open()
	if _, ok := e.Wait(context.Background()); ok || e.IsOpen() {
		t.Fatal("timeout closes")
	}
	if e.Deliver("late") {
		t.Fatal("late answer")
	}
	e.Open()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := e.Wait(ctx); ok {
		t.Fatal("ctx")
	}
}
