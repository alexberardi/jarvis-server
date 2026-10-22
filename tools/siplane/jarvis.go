package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
)

// The jarvis-* scenarios run jarvisd's own SIP provider (internal/modules/cc/phone/sip, the
// UA a phone.provider=sip household gets) in this process as extension 100, in place of the
// baresip stand-in: it registers, is refused and stops, and places calls through the outbound
// route to the carrier. max_contacts=1 lets only one of them hold 100, so each scenario has the
// stand-in unregister first and register again after (jarvisd.go's release). The
// jarvis-inbound* scenarios (inbound.go) have it answer the calls routed to 100.

const jarvisPassword = "lane-jarvis-100"

// jarvisUA starts the provider's UA as 100 from addr (a loopback address) with password; opts
// change its config (an inbound handler).
func (l *lane) jarvisUA(ctx context.Context, addr, password string, r *report, opts ...func(*sip.Config)) (*sip.UA, error) {
	host, port, err := splitHostPort(l.env["SIP_LANE_SIP"])
	if err != nil {
		return nil, err
	}
	off, _ := strconv.Atoi(l.env["SIP_LANE_OFFSET"])
	cfg := sip.Config{Server: host, Port: port, Extension: "100", Password: password,
		LocalPort: 5073 + off, BindHost: addr, UserAgent: "Jarvis (siplane)",
		// The lane's firewall bans at 3 failures as FreePBX's does; the backoff is shortened
		// so the scenario does not wait minutes.
		BackoffMin: 500 * time.Millisecond, BackoffMax: 2 * time.Second,
		Log: slog.New(slog.NewTextHandler(&noteWriter{r: r}, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	for _, o := range opts {
		o(&cfg)
	}
	return sip.Start(ctx, cfg)
}

// noteWriter keeps the UA's log lines (registration, call ends) in the report under --out.
type noteWriter struct{ r *report }

func (w *noteWriter) Write(b []byte) (int, error) {
	line := strings.TrimSpace(string(b))
	if i := strings.Index(line, "msg="); i >= 0 {
		line = line[i:]
	}
	w.r.notef("jarvisd: %s", line)
	return len(b), nil
}

func splitHostPort(hp string) (string, int, error) {
	h, p, ok := strings.Cut(hp, ":")
	n, err := strconv.Atoi(p)
	if !ok || err != nil {
		return "", 0, fmt.Errorf("SIP_LANE_SIP %q", hp)
	}
	return h, n, nil
}

// release has the baresip stand-in give up 100 and returns the function that takes it back:
// baresip deletes its account (unregistering, Expires: 0) and creates it again from the
// lane's accounts file.
func (l *lane) release(ctx context.Context) (func(), error) {
	acct, err := os.ReadFile(filepath.Join(l.party("jarvis").dir, "accounts"))
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(acct))
	aor := strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(line, ">", 2)[0], "<"), ">")
	c, err := dialCtrl(ctx, l.party("jarvis").ctrl)
	if err != nil {
		return nil, err
	}
	if err := c.Command(ctx, "uadel", aor); err != nil {
		c.Close()
		return nil, fmt.Errorf("unregistering the stand-in: %w", err)
	}
	if err := l.waitContact(ctx, "100", "", false); err != nil {
		c.Close()
		return nil, fmt.Errorf("the stand-in did not let 100 go: %w", err)
	}
	return func() {
		defer c.Close()
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		_ = l.waitContact(bg, "100", "", false) // jarvisd's Expires: 0 has landed
		if err := c.Command(bg, "uanew", line); err != nil {
			fmt.Fprintf(os.Stderr, "siplane: the stand-in could not take 100 back: %v\n", err)
			return
		}
		_ = l.waitContact(bg, "100", "", true)
	}, nil
}

// waitContact waits until ext has (or has no) reachable contact, on port when port is set.
func (l *lane) waitContact(ctx context.Context, ext, port string, want bool) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		out, err := l.cli(ctx, "pjsip show contacts")
		if err != nil {
			return err
		}
		has := false
		for _, m := range contactLine.FindAllStringSubmatch(out, -1) {
			if m[1] == ext && (port == "" || strings.Contains(m[0], ":"+port)) {
				has = true
			}
		}
		if has == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("contact of %s on port %q present=%t, want %t:\n%s", ext, port, has, want, out)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func jarvisRegisters(ctx context.Context, l *lane, r *report) error {
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
	port := strconv.Itoa(ua.Config().LocalPort)
	if err := l.waitContact(ctx, "100", port, true); err != nil {
		r.failf("the PBX shows no contact for jarvisd: %v", err)
		return nil
	}
	out, _ := l.cli(ctx, "pjsip show contacts")
	l.keep("jarvis-registers-contacts.txt", out)
	// The PBX qualifies it (qualify_frequency) and our OPTIONS keepalive gets answers.
	time.Sleep(3 * time.Second)
	if st := ua.Status(); st.State != sip.StateRegistered {
		r.failf("registration dropped: %+v", st)
	}
	// Close unregisters (Expires: 0): the contact goes at once, so a restart is not refused
	// by max_contacts=1 with remove_existing=no.
	ua.Close(context.Background())
	if err := l.waitContact(ctx, "100", port, false); err != nil {
		r.failf("the contact stayed after shutdown: %v", err)
		return nil
	}
	// And the same account starts again on the same Contact.
	ua2, err := l.jarvisUA(ctx, "127.0.0.1", jarvisPassword, r)
	if err != nil {
		r.failf("restart: %v", err)
		return nil
	}
	defer ua2.Close(context.Background())
	if st := waitUA(ua2, sip.StateRegistered, 15*time.Second); st.State != sip.StateRegistered {
		r.failf("not registered after a restart: %+v", st)
		return nil
	}
	r.notef("jarvisd registered as 100 on port %s, kept it, unregistered on shutdown and registered again on the same port", port)
	return nil
}

func jarvisWrongPassword(ctx context.Context, l *lane, r *report) error {
	unban := func() {
		l.docker(context.WithoutCancel(ctx), "exec", l.env["SIP_LANE_ASTERISK"], "lane-firewall", "clear")
	}
	unban()
	defer unban()
	ua, err := l.jarvisUA(ctx, bannedAddr, "not-the-password", r)
	if err != nil {
		return err
	}
	defer ua.Close(context.Background())
	st := waitUA(ua, sip.StateStopped, 30*time.Second)
	if st.State != sip.StateStopped {
		r.failf("still trying after %d attempts: %+v", st.Attempts, st)
		return nil
	}
	if st.Attempts != 3 || st.AuthFailures != 3 {
		r.failf("stopped after %d attempts (%d refused), want exactly 3", st.Attempts, st.AuthFailures)
	}
	if !strings.Contains(st.Hint, "Firewall") {
		r.failf("hint %q does not point at the PBX firewall", st.Hint)
	}
	// Nothing more goes out: a 4th attempt would only extend the ban.
	time.Sleep(5 * time.Second)
	if n := ua.Status().Attempts; n != 3 {
		r.failf("%d attempts after the stop", n)
	}
	ban, _ := l.docker(ctx, "exec", l.env["SIP_LANE_ASTERISK"], "cat", "/var/lib/asterisk/lane/pjsip_ban.conf")
	r.notef("stopped after %d attempts: %s; last error %q; lane firewall banned %s: %t", st.Attempts, st.State,
		st.LastError, bannedAddr, strings.Contains(ban, "deny="+bannedAddr+"/32"))
	if err := ua.Ready(); err == nil || !strings.Contains(err.Error(), "Firewall") {
		r.failf("Ready: %v", err)
	}
	// The connection self-test on a stopped registration says why, and sends nothing.
	run := phone.RunSIPSelfTest(ctx, ua, phone.SIPTestConnection, nil, nil)
	r.notef("connection self-test after the stop: %s: %s (%s)", run.State, run.Summary, run.Hint)
	if run.State != phone.SIPTestFailed || !strings.Contains(run.Hint, "Firewall") {
		r.failf("connection self-test: %+v", run)
	}
	if n := ua.Status().Attempts; n != 3 {
		r.failf("the self-test sent a REGISTER after the stop (%d attempts)", n)
	}
	return nil
}

func waitUA(ua *sip.UA, want sip.State, d time.Duration) sip.Status {
	deadline := time.After(d)
	for {
		st := ua.Status()
		if st.State == want {
			return st
		}
		select {
		case <-ua.Changed():
		case <-deadline:
			return st
		}
	}
}

// jarvisOutbound: jarvisd dials a 10-digit number (phone.sip.dial_format) through the outbound
// route; the carrier answers. Each side says a sentence, and each side's recording is
// transcribed: both audio directions through Asterisk.
func jarvisOutbound(ctx context.Context, l *lane, r *report) error {
	const jarvisSays = "Hi, this is Jarvis calling to book a table for two at seven."
	const carrierSays = "Sure, we have a table for two at seven tonight."
	const total = 10.0
	c := l.party("carrier")
	if err := l.say(ctx, c, carrierSays, 1, total); err != nil {
		return err
	}
	tts, err := l.voice.Speak(ctx, jarvisSays)
	if err != nil {
		return fmt.Errorf("TTS: %w", err)
	}
	ours, rate, err := wavPCM(tts)
	if err != nil {
		return err
	}
	ours = live.Resample(ours, rate, 8000)

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
	number, err := phone.FormatDial("+12125550100", phone.DialFormat10, "")
	if err != nil {
		return err
	}
	cs, done, err := l.connect(ctx, "carrier")
	if err != nil {
		return err
	}
	defer done()
	call, err := ua.Dial(ctx, number)
	if err != nil {
		r.failf("dial %s: %v", number, err)
		return nil
	}
	defer call.Close()
	select {
	case <-call.Started():
	case <-call.Done():
		end, detail := call.End()
		r.failf("the call to %s ended before it started: %s (%s)", number, end, detail)
		return nil
	case <-time.After(20 * time.Second):
		r.failf("the call to %s did not start", number)
		return nil
	}
	r.notef("jarvisd dialed %s (10-digit): answered, the carrier's audio is flowing", number)
	time.Sleep(time.Second)
	if _, err := call.Play(ours); err != nil {
		r.failf("play: %v", err)
	}
	// Record until the carrier hangs up (its WAV is total seconds long).
	var heard []int16
	timeout := time.After(time.Duration(total+15) * time.Second)
record:
	for {
		select {
		case f, ok := <-call.Frames():
			if !ok {
				break record
			}
			heard = append(heard, f...)
		case <-timeout:
			r.failf("the call did not end")
			break record
		}
	}
	end, detail := call.End()
	r.notef("the call ended: %s (%s) after %.1f s of the carrier's audio", end, detail, float64(len(heard))/8000)
	if end != sip.EndRemote {
		r.failf("end %q, want the carrier hanging up", end)
	}
	waitClosed(ctx, r, 10*time.Second, cs, "carrier")
	// What jarvisd heard, through the sandbox's whisper (at 16 kHz, as the far ends are).
	wav := live.WAV(live.Resample(heard, 8000, 16000), 16000)
	if l.out != "" {
		os.WriteFile(filepath.Join(l.out, "jarvis-outbound-jarvisd.wav"), wav, 0o644)
	}
	text, err := l.voice.Transcribe(ctx, wav)
	if err != nil {
		return err
	}
	w, _ := bestWER([]string{carrierSays}, text)
	r.notef("jarvisd heard: %q (WER %.2f against %q)", text, w, carrierSays)
	l.keep("jarvis-outbound-jarvisd.txt", text+"\n")
	if w > maxWER {
		r.failf("jarvisd heard %q, WER %.2f > %.2f", text, w, maxWER)
	}
	return l.expectHeard(ctx, r, "jarvis-outbound", c, jarvisSays)
}

// jarvisOutcome dials a number the lane's PBX refuses and checks the outcome jarvisd reports.
func jarvisOutcome(number string, want sip.End) func(ctx context.Context, l *lane, r *report) error {
	return func(ctx context.Context, l *lane, r *report) error {
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
		call, err := ua.Dial(ctx, number)
		if err != nil {
			r.failf("dial: %v", err)
			return nil
		}
		defer call.Close()
		select {
		case <-call.Done():
		case <-time.After(60 * time.Second):
			r.failf("the call to %s did not end", number)
			return nil
		}
		end, detail := call.End()
		r.notef("%s: %s (%s), answered=%t", number, end, detail, call.Answered())
		if end != want || call.Answered() {
			r.failf("%s ended %q (answered=%t), want %q, never answered", number, end, call.Answered(), want)
		}
		return nil
	}
}

// jarvisAllTrunksBusy: FreePBX answers 200 and plays "all circuits are busy". The call
// starts (it is answered); the first utterance is what the call loop takes for a recording.
func jarvisAllTrunksBusy(ctx context.Context, l *lane, r *report) error {
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
	call, err := ua.Dial(ctx, "2125550200")
	if err != nil {
		r.failf("dial: %v", err)
		return nil
	}
	defer call.Close()
	var heard []int16
	timeout := time.After(40 * time.Second)
loop:
	for {
		select {
		case f, ok := <-call.Frames():
			if !ok {
				break loop
			}
			heard = append(heard, f...)
		case <-timeout:
			break loop
		}
	}
	if !call.Answered() {
		end, detail := call.End()
		r.failf("not answered: %s (%s)", end, detail)
		return nil
	}
	text, err := l.voice.Transcribe(ctx, live.WAV(live.Resample(heard, 8000, 16000), 16000))
	if err != nil {
		return err
	}
	r.notef("answered (200 OK); jarvisd heard %q; a recorded announcement: %t", text, live.IsAnnouncement(text))
	if !live.IsAnnouncement(text) {
		r.failf("%q is not taken for a recording: the call loop would talk to it", text)
	}
	return nil
}

// wavPCM reads a 16-bit mono PCM WAV (the sandbox's TTS).
func wavPCM(b []byte) ([]int16, int, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, errors.New("TTS: not a WAV")
	}
	rate := 0
	for i := 12; i+8 <= len(b); {
		id, n := string(b[i:i+4]), int(binary.LittleEndian.Uint32(b[i+4:]))
		body := b[i+8:]
		if n > len(body) {
			n = len(body)
		}
		switch id {
		case "fmt ":
			if n >= 16 {
				if ch, bits := binary.LittleEndian.Uint16(body[2:]), binary.LittleEndian.Uint16(body[14:]); ch != 1 || bits != 16 {
					return nil, 0, fmt.Errorf("TTS: %d channels, %d bits", ch, bits)
				}
				rate = int(binary.LittleEndian.Uint32(body[4:]))
			}
		case "data":
			if rate == 0 {
				return nil, 0, errors.New("TTS: data before fmt")
			}
			return live.PCMBytesToInt16(body[:n]), rate, nil
		}
		i += 8 + n + n%2
	}
	return nil, 0, errors.New("TTS: no data chunk")
}
