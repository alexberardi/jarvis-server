package sip

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// The registration supervisor (docs/cc/phone-sip.md §2, §3.1). diago's own register loop tears
// the registration down on one failed refresh and notices a dead PBX only at the next one;
// FreePBX's firewall bans a host that keeps failing. So: register; while registered, send
// OPTIONS to the registrar every KeepAlive and refresh before the granted expiry runs out; on
// any failure retry with exponential backoff; after MaxAuthFailures authentication failures
// stop for good with a hint. A 403 counts as one (a ban, or a Contact the PBX will not take),
// and so does a timeout once the PBX has refused us before: the real firewall drops a banned
// host's packets rather than answering. A PBX that times out without ever refusing is just
// down, and is retried forever. On shutdown: REGISTER with Expires: 0.

// regResult is one REGISTER attempt's outcome.
type regResult struct {
	err     error
	code    int           // the final status code, 0 for no response
	expires time.Duration // granted, on success
	rtt     time.Duration // the attempt's round trips (challenge and answer)
}

// authFailure reports a refusal that counts toward the stop: a wrong password (401/407 after
// we answered the challenge) or 403.
func (r regResult) authFailure() bool {
	return r.code == sip.StatusUnauthorized || r.code == sip.StatusProxyAuthRequired || r.code == sip.StatusForbidden
}

func (r regResult) timeout() bool {
	return r.code == 0 && r.err != nil
}

func (u *UA) supervise(ctx context.Context) {
	defer close(u.done)
	req := u.registerRequest()
	backoff := u.cfg.BackoffMin
	refused := false // the PBX refused us since the last success: a timeout now is a ban
	for {
		u.setStatus(func(s *Status) { s.Attempts++ })
		r := u.register(ctx, req, u.cfg.RegisterExpiry)
		if ctx.Err() != nil {
			u.unregister(req)
			return
		}
		if r.err == nil {
			refused, backoff = false, u.cfg.BackoffMin
			u.setStatus(func(s *Status) {
				s.State, s.LastError, s.Hint, s.AuthFailures = StateRegistered, "", "", 0
				s.LastRegister, s.RegisterRTT = time.Now(), r.rtt
			})
			u.cfg.Log.Info("sip: registered", "account", u.cfg.String(), "expires", r.expires)
			err := u.keep(ctx, r.expires)
			if ctx.Err() != nil {
				u.unregister(req)
				return
			}
			if err == nil {
				continue // refresh due
			}
			u.cfg.Log.Warn("sip: the PBX stopped answering", "err", err)
			r = regResult{err: err}
		}
		counts := r.authFailure() || (r.timeout() && refused)
		if r.authFailure() {
			refused = true
		}
		var stop bool
		u.setStatus(func(s *Status) {
			s.LastError = r.err.Error()
			s.Hint = hintFor(r)
			s.LastRegister, s.RegisterRTT = time.Now(), r.rtt
			if r.code == 0 {
				s.RegisterRTT = 0 // no answer: a timeout is not a round trip
			}
			if counts {
				s.AuthFailures++
			}
			stop = s.AuthFailures >= u.cfg.MaxAuthFailures
			s.State = StateRetrying
			if stop {
				s.State, s.Hint = StateStopped, HintBlocked
			}
		})
		if stop {
			u.cfg.Log.Error("sip: registration stopped after repeated refusals", "account", u.cfg.String(), "err", r.err)
			return
		}
		u.cfg.Log.Warn("sip: registration failed; retrying", "err", r.err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-u.after(backoff):
		case <-u.refresh: // Check: try now
		}
		backoff = min(backoff*2, u.cfg.BackoffMax)
	}
}

// after is the backoff's wait (Config.After, else time.After).
func (u *UA) after(d time.Duration) <-chan time.Time {
	if u.cfg.After != nil {
		return u.cfg.After(d)
	}
	return time.After(d)
}

// hintFor is a plain-words reading of a failed attempt.
func hintFor(r regResult) string {
	switch {
	case r.code == sip.StatusUnauthorized || r.code == sip.StatusProxyAuthRequired:
		return "The PBX did not accept the extension's password."
	case r.code == sip.StatusForbidden:
		return "The PBX refused this host (403): its firewall may have blocked it, or another device holds this extension."
	case r.code == sip.StatusNotFound:
		return "The PBX does not know this extension."
	case r.timeout():
		return "The PBX did not answer: check the server address and port, and that this host can reach it."
	}
	return ""
}

// keep holds a registration: OPTIONS every KeepAlive, and nil once the refresh is due (at
// 3/4 of the granted expiry, at least 5 s before it ends). An OPTIONS that gets no answer is
// an error; any answer, even 4xx, shows the PBX is there.
func (u *UA) keep(ctx context.Context, expires time.Duration) error {
	refresh := max(min(expires*3/4, expires-5*time.Second), time.Second)
	due := time.NewTimer(refresh)
	defer due.Stop()
	tick := time.NewTicker(u.cfg.KeepAlive)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-due.C:
			return nil
		case <-u.refresh: // Check: re-register now
			return nil
		case <-tick.C:
			if _, err := u.options(ctx); err != nil {
				return err
			}
		}
	}
}

func (u *UA) registerRequest() *sip.Request {
	req := sip.NewRequest(sip.REGISTER, u.registrar)
	aor := sip.Uri{Scheme: "sip", User: u.cfg.Extension, Host: u.registrar.Host}
	from := &sip.FromHeader{Address: aor, Params: sip.NewParams()}
	from.Params.Add("tag", sip.GenerateTagN(16))
	req.AppendHeader(from)
	req.AppendHeader(&sip.ToHeader{Address: aor, Params: sip.NewParams()})
	req.AppendHeader(u.contact.Clone())
	req.AppendHeader(sip.NewHeader("User-Agent", u.cfg.UserAgent))
	return req
}

// register sends one REGISTER (and its digest answer) asking for expires; 0 unregisters.
func (u *UA) register(ctx context.Context, req *sip.Request, expires time.Duration) regResult {
	ctx, cancel := context.WithTimeout(ctx, u.cfg.RequestTimeout)
	defer cancel()
	req.RemoveHeader("Expires")
	exp := sip.ExpiresHeader(uint32(expires / time.Second))
	req.AppendHeader(&exp)
	req.RemoveHeader("Via")
	// Never resend the last attempt's credentials: with a wrong password each one is another
	// failure for the PBX's intrusion detection.
	req.RemoveHeader("Authorization")
	req.RemoveHeader("Proxy-Authorization")
	start := time.Now()
	res, err := u.client.Do(ctx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		return regResult{err: fmt.Errorf("no answer from %s: %w", u.registrar.HostPort(), err), rtt: time.Since(start)}
	}
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		res, err = u.client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{Username: u.cfg.Extension, Password: u.cfg.Password})
		if err != nil {
			return regResult{err: fmt.Errorf("no answer from %s: %w", u.registrar.HostPort(), err), rtt: time.Since(start)}
		}
	}
	rtt := time.Since(start)
	if res.StatusCode != sip.StatusOK {
		return regResult{code: res.StatusCode, err: fmt.Errorf("REGISTER: %d %s", res.StatusCode, res.Reason), rtt: rtt}
	}
	return regResult{code: res.StatusCode, expires: grantedExpiry(res, &u.contact, expires), rtt: rtt}
}

// grantedExpiry is what the registrar granted: our Contact's expires parameter, else the
// Expires header, else what we asked for.
func grantedExpiry(res *sip.Response, ours *sip.ContactHeader, asked time.Duration) time.Duration {
	for _, h := range res.GetHeaders("Contact") {
		c, ok := h.(*sip.ContactHeader)
		if !ok || c.Address.Host != ours.Address.Host || c.Address.Port != ours.Address.Port {
			continue
		}
		if v, ok := c.Params.Get("expires"); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return time.Duration(n) * time.Second
			}
		}
	}
	if h := res.GetHeader("Expires"); h != nil {
		if n, err := strconv.Atoi(h.Value()); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return asked
}

// unregister sends Expires: 0 for our Contact, best effort, within RequestTimeout.
func (u *UA) unregister(req *sip.Request) {
	if u.Status().AuthFailures > 0 && u.Status().State != StateRegistered {
		return // not registered; another refused REGISTER would only feed the firewall
	}
	r := u.register(context.Background(), req, 0)
	if r.err != nil {
		u.cfg.Log.Warn("sip: unregister failed", "err", r.err)
		return
	}
	u.cfg.Log.Info("sip: unregistered", "account", u.cfg.String())
}

// options pings the registrar; any final response counts as alive. It returns the round
// trip, also kept in the status.
func (u *UA) options(ctx context.Context) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, u.cfg.RequestTimeout)
	defer cancel()
	req := sip.NewRequest(sip.OPTIONS, u.registrar)
	req.AppendHeader(u.contact.Clone())
	req.AppendHeader(sip.NewHeader("User-Agent", u.cfg.UserAgent))
	start := time.Now()
	res, err := u.client.Do(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("OPTIONS to %s: no answer: %w", u.registrar.HostPort(), err)
	}
	if res == nil {
		return 0, errors.New("OPTIONS: no response")
	}
	rtt := time.Since(start)
	u.setStatus(func(s *Status) { s.LastOptions, s.OptionsRTT = time.Now(), rtt })
	return rtt, nil
}

// CheckResult is the connection self-test's outcome.
type CheckResult struct {
	Registered  bool
	RegisterRTT time.Duration
	OptionsRTT  time.Duration
	Error       string
	Hint        string
}

// Check is the connection self-test: register again now and ping the registrar with OPTIONS.
// A stopped registration is not retried (one more refusal only extends a firewall ban): the
// result says why it stopped and what to do.
func (u *UA) Check(ctx context.Context) CheckResult {
	st := u.Status()
	switch st.State {
	case StateStopped:
		return CheckResult{Error: st.LastError, Hint: st.Hint}
	case StateClosed:
		return CheckResult{Error: "the SIP connection to the PBX is shut down"}
	}
	t0 := time.Now()
	select {
	case u.refresh <- struct{}{}:
	default: // one is already pending
	}
	wait := time.NewTimer(2*u.cfg.RequestTimeout + time.Second)
	defer wait.Stop()
	for {
		changed := u.Changed()
		st = u.Status()
		if st.LastRegister.After(t0) {
			break
		}
		select {
		case <-changed:
		case <-wait.C:
			return CheckResult{Error: "the REGISTER did not finish in time", Hint: hintFor(regResult{err: errors.New("timeout")})}
		case <-ctx.Done():
			return CheckResult{Error: ctx.Err().Error()}
		}
	}
	res := CheckResult{Registered: st.State == StateRegistered, RegisterRTT: st.RegisterRTT, Error: st.LastError, Hint: st.Hint}
	if !res.Registered {
		return res
	}
	rtt, err := u.options(ctx)
	if err != nil {
		res.Error, res.Hint = err.Error(), hintFor(regResult{err: err})
		return res
	}
	res.OptionsRTT = rtt
	return res
}
