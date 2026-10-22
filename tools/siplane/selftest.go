package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

// The jarvis-selftest-* scenarios run jarvisd's own SIP self-tests (phone.RunSIPSelfTest, what
// the admin's Phone page starts) as 100 against the lane's Asterisk, with the sandbox's TTS
// and whisper: docs/cc/phone-sip.md §6 on a PBX configured like FreePBX 17.

// laneTTS and laneSTT are the sandbox's voice as the phone package's TTS and STT.
type laneTTS struct{ v *voice }

func (t laneTTS) SynthesizePCM(ctx context.Context, text string) ([]int16, int, error) {
	wav, err := t.v.Speak(ctx, text)
	if err != nil {
		return nil, 0, err
	}
	return wavPCM(wav)
}

type laneSTT struct{ v *voice }

// Transcribe takes the 8 kHz WAV jarvisd's STT gets and sends it at 16 kHz, as the lane's
// other recordings are.
func (s laneSTT) Transcribe(ctx context.Context, wav []byte, _ stt.TranscribeOptions) (stt.Result, error) {
	pcm, rate, err := wavPCM(wav)
	if err != nil {
		return stt.Result{}, err
	}
	text, err := s.v.Transcribe(ctx, live.WAV(live.Resample(pcm, rate, 16000), 16000))
	return stt.Result{Text: text}, err
}

// withJarvis runs f with jarvisd's UA registered as 100 (the stand-in steps aside).
func withJarvis(ctx context.Context, l *lane, r *report, f func(ua *sip.UA) error) error {
	restore, err := l.release(ctx)
	if err != nil {
		return err
	}
	defer restore()
	ua, err := l.jarvisUA(ctx, "127.0.0.1", jarvisPassword, r)
	if err != nil {
		return err
	}
	defer ua.Close(context.Background())
	if st := waitUA(ua, sip.StateRegistered, 15*time.Second); st.State != sip.StateRegistered {
		r.failf("not registered: %+v", st)
		return nil
	}
	return f(ua)
}

func noteRun(r *report, run phone.SIPTestRun) {
	r.notef("%s: %s: %s", run.Test, run.State, run.Summary)
	for _, s := range run.Steps {
		r.notef("  step: %s", s)
	}
	if run.Hint != "" {
		r.notef("  hint: %s", run.Hint)
	}
}

func selftestConnection(ctx context.Context, l *lane, r *report) error {
	return withJarvis(ctx, l, r, func(ua *sip.UA) error {
		run := phone.RunSIPSelfTest(ctx, ua, phone.SIPTestConnection, nil, nil)
		noteRun(r, run)
		if run.State != phone.SIPTestPassed || run.RegisterRTTMs == nil || run.OptionsRTTMs == nil {
			r.failf("connection self-test: %+v", run)
		}
		// The keepalive's OPTIONS round trip is in the status too.
		if st := ua.Status(); st.OptionsRTT <= 0 || st.RegisterRTT <= 0 {
			r.failf("status has no round trips: OPTIONS %v, REGISTER %v", st.OptionsRTT, st.RegisterRTT)
		}
		return nil
	})
}

func selftestEcho(ctx context.Context, l *lane, r *report) error {
	return withJarvis(ctx, l, r, func(ua *sip.UA) error {
		run := phone.RunSIPSelfTest(ctx, ua, phone.SIPTestEcho, laneTTS{l.voice}, laneSTT{l.voice})
		noteRun(r, run)
		if run.LatencyMs != nil && run.WER != nil {
			r.notef("echo: latency %d ms, WER %.2f, heard %q", *run.LatencyMs, *run.WER, run.Heard)
			l.keep("jarvis-selftest-echo.txt", fmt.Sprintf("latency_ms=%d wer=%.2f\nheard: %s\n", *run.LatencyMs, *run.WER, run.Heard))
		}
		switch {
		case run.State != phone.SIPTestPassed:
			r.failf("echo self-test %s: %s", run.State, run.Summary)
		case *run.WER > maxWER:
			r.failf("echo WER %.2f > %.2f", *run.WER, maxWER)
		case *run.LatencyMs <= 0 || *run.LatencyMs > 2000:
			r.failf("echo latency %d ms", *run.LatencyMs)
		}
		return nil
	})
}

func selftestDigits(ctx context.Context, l *lane, r *report) error {
	return withJarvis(ctx, l, r, func(ua *sip.UA) error {
		run := phone.RunSIPSelfTest(ctx, ua, phone.SIPTestDigits, nil, laneSTT{l.voice})
		noteRun(r, run)
		if run.State != phone.SIPTestPassed || run.Expected != "100" {
			r.failf("*65 self-test %s: heard %q, expected %q", run.State, run.Heard, run.Expected)
		}
		return nil
	})
}

// selftestInbound: Alice (101) calls 100 while the inbound watch runs; jarvisd refuses the
// call (480) and the watch reports it with Alice's caller ID and her audio offer.
func selftestInbound(ctx context.Context, l *lane, r *report) error {
	a := l.party("alice")
	if err := l.say(ctx, a, "", 0, 10); err != nil {
		return err
	}
	return withJarvis(ctx, l, r, func(ua *sip.UA) error {
		cs, done, err := l.connect(ctx, "alice")
		if err != nil {
			return err
		}
		defer done()
		got := make(chan phone.SIPTestRun, 1)
		go func() { got <- phone.RunSIPSelfTest(ctx, ua, phone.SIPTestInbound, nil, nil) }()
		time.Sleep(time.Second)
		if err := cs["alice"].Command(ctx, "dial", l.uri("100")); err != nil {
			return err
		}
		run := <-got
		noteRun(r, run)
		waitClosed(ctx, r, 30*time.Second, cs, "alice")
		if run.State != phone.SIPTestPassed || run.Inbound == nil || run.Inbound.Caller != "101" || run.Inbound.Response != 480 {
			r.failf("inbound watch: %+v", run)
			return nil
		}
		if run.Inbound.Media == "" || len(run.Inbound.Codecs) == 0 {
			r.failf("the INVITE's audio offer was not read: %+v", run.Inbound)
		}
		return nil
	})
}

// selftestTrace: after a registration and a *43 call, the process-wide SIP trace holds both,
// under jarvisd's account, with the digest answer redacted and the password nowhere.
func selftestTrace(ctx context.Context, l *lane, r *report) error {
	return withJarvis(ctx, l, r, func(ua *sip.UA) error {
		c, err := ua.DialFeature(ctx, "*43")
		if err != nil {
			r.failf("dial *43: %v", err)
			return nil
		}
		select {
		case <-c.Started():
		case <-time.After(15 * time.Second):
		}
		time.Sleep(2 * time.Second)
		_ = c.Hangup(ctx)
		<-c.Done()
		var b strings.Builder
		sip.WriteTraceText(&b, sip.Trace())
		text := b.String()
		l.keep("jarvis-selftest-trace.txt", text)
		for _, want := range []string{"REGISTER sip:", "Authorization: Digest", `response="<redacted>"`, "INVITE sip:*43@", "BYE sip:"} {
			if !strings.Contains(text, want) {
				r.failf("the trace has no %q", want)
			}
		}
		if strings.Contains(text, jarvisPassword) {
			r.failf("the trace contains the extension's password")
		}
		for _, line := range strings.Split(text, "\n") {
			low := strings.ToLower(line)
			if strings.HasPrefix(low, "authorization:") && strings.Contains(low, "response=") && !strings.Contains(low, `response="<redacted>"`) {
				r.failf("a digest response survived: %s", line)
			}
		}
		r.notef("trace: %d dialogs, %d bytes, no password, digest responses redacted", len(sip.Trace()), len(text))
		return nil
	})
}
