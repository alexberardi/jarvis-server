package sip

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/sipgo"
)

// Blind transfer (docs/cc/phone-sip.md §3.4, §4 step 4): an answered inbound call is handed to
// another extension or number on the PBX with REFER. diago's Refer returns on the 202, which
// only says the PBX will try, so Transfer waits for the final NOTIFY sipfrag (RFC 3515) and
// hangs up our leg only once it says 2xx. A refused REFER leaves the call as it was, ours to go
// on with. After a 202, Asterisk (so FreePBX) has already taken the caller off our leg into its
// dialplan (the L1 lane shows it: a busy target's Busy() runs on the caller's channel), so a
// failure or no final NOTIFY leaves the call ours only while the caller's audio still reaches
// us; when it has stopped, the PBX has the caller and Transfer hangs up our empty leg.

// ErrTransferAttempted is a second transfer on one call: each call gets one attempt.
var ErrTransferAttempted = errors.New("sip: this call has already been transferred once")

// TransferError is a transfer that did not complete. The call is still up unless it is Done.
type TransferError struct {
	// Code is the PBX's refusal of the REFER (Accepted false) or the target's final status in
	// the NOTIFY (Accepted true); 0 when there was none (a timeout, the call ended).
	Code int
	// Accepted is true once the PBX answered the REFER 202: it tried.
	Accepted bool
	// Gone is true when the caller is no longer on our leg (the PBX has them, or the call
	// ended): the call is over, and Transfer hung up what was left of it.
	Gone   bool
	Detail string
}

func (e *TransferError) Error() string { return "sip: transfer failed: " + e.Detail }

// Transfer hands the call to target, an extension or number on the PBX (digits, optionally
// after a +; never a URI or a feature code), with a blind REFER, and waits for the outcome.
// nil means the PBX reported the target answering: our leg is hung up and the call ends
// EndTransferred. Only an answered inbound call can be transferred, once.
func (c *Call) Transfer(ctx context.Context, target string) error {
	if c.srv == nil {
		return ErrUnsupported
	}
	if !dialable(target) {
		return fmt.Errorf("sip: bad transfer target %q", target)
	}
	c.mu.Lock()
	switch {
	case c.end != EndNone:
		c.mu.Unlock()
		return fmt.Errorf("sip: call ended (%s)", c.end)
	case c.xfer != nil:
		c.mu.Unlock()
		return ErrTransferAttempted
	}
	xfer := make(chan struct{})
	c.xfer = xfer
	c.mu.Unlock()
	defer close(xfer)

	log := c.ua.cfg.Log.With("call_id", c.id, "target", target)
	to := c.ua.registrar.Clone()
	to.User = target
	final := make(chan int, 1)
	onNotify := func(code int) {
		log.Info("sip: transfer progress", "notify", code)
		if code >= 200 {
			select {
			case final <- code:
			default:
			}
		}
	}
	rctx, cancel := context.WithTimeout(ctx, c.ua.cfg.RequestTimeout)
	err := c.srv.ReferOptions(rctx, *to, diago.ReferServerOptions{OnNotify: onNotify})
	cancel()
	if err != nil {
		te := &TransferError{Detail: "the phone system refused the transfer: " + err.Error()}
		var vr sipgo.ErrDialogResponse
		var pr *sipgo.ErrDialogResponse
		switch {
		case errors.As(err, &vr):
			te.Code, te.Detail = vr.Res.StatusCode, fmt.Sprintf("the phone system refused the transfer (%d %s)", vr.Res.StatusCode, vr.Res.Reason)
		case errors.As(err, &pr):
			te.Code, te.Detail = pr.Res.StatusCode, fmt.Sprintf("the phone system refused the transfer (%d %s)", pr.Res.StatusCode, pr.Res.Reason)
		}
		log.Warn("sip: transfer refused", "err", err)
		return te
	}
	log.Info("sip: transfer accepted (202); waiting for the final NOTIFY", "wait", c.ua.cfg.TransferWait)

	timer := time.NewTimer(c.ua.cfg.TransferWait)
	defer timer.Stop()
	code := 0
	select {
	case code = <-final:
	case <-c.dlg.Context().Done():
		// A final NOTIFY that came just before the PBX's BYE still counts.
		select {
		case code = <-final:
		default:
			c.endWith(EndRemote, "the phone system ended the call during the transfer, before reporting its result")
			log.Warn("sip: the call ended during the transfer, before its final NOTIFY")
			return &TransferError{Accepted: true, Gone: true, Detail: "the phone system ended the call before it reported the transfer's result"}
		}
	case <-timer.C:
		return c.transferFailed(log, &TransferError{Accepted: true,
			Detail: fmt.Sprintf("the phone system did not report the transfer's result within %s", c.ua.cfg.TransferWait)})
	case <-ctx.Done():
		return c.transferFailed(log, &TransferError{Accepted: true, Detail: "the transfer was abandoned: " + ctx.Err().Error()})
	}
	if code < 200 || code >= 300 {
		return c.transferFailed(log, &TransferError{Code: code, Accepted: true,
			Detail: fmt.Sprintf("the transfer target did not take the call (%d)", code)})
	}
	detail := fmt.Sprintf("transferred to %s", target)
	c.endWith(EndTransferred, detail)
	hctx, hcancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	if err := c.dlg.Hangup(hctx); err != nil && c.dlg.Context().Err() == nil {
		log.Warn("sip: hanging up our leg after the transfer failed", "err", err)
	}
	hcancel()
	c.finish(EndTransferred, detail)
	log.Info("sip: call transferred")
	return nil
}

// callerAudioWait is how long a failed transfer waits for the caller's audio before it decides
// the PBX has taken them off our leg. RTP flows every 20 ms while a PBX bridges the caller to us.
const callerAudioWait = 2 * time.Second

// transferFailed decides whether the call is still ours after an accepted transfer failed: it
// is while the caller's audio still arrives; otherwise our leg is hung up and te says Gone.
func (c *Call) transferFailed(log *slog.Logger, te *TransferError) error {
	since := time.Now()
	t := time.NewTicker(frameDur)
	defer t.Stop()
	deadline := time.After(callerAudioWait)
	for {
		if c.lastRTP.Load() > since.UnixNano() {
			log.Warn("sip: the transfer failed — the caller is still on the call", "detail", te.Detail)
			return te
		}
		select {
		case <-t.C:
			continue
		case <-c.done:
		case <-deadline:
		}
		break
	}
	te.Gone = true
	detail := "the transfer failed and the phone system had already taken the caller: " + te.Detail
	log.Warn("sip: the transfer failed and the caller's audio has stopped — hanging up our leg", "detail", te.Detail)
	c.endWith(EndClosed, detail)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = c.dlg.Hangup(ctx)
	cancel()
	c.finish(EndClosed, detail)
	return te
}

// transferring is the running transfer's done channel, nil when none is running.
func (c *Call) transferring() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.xfer == nil {
		return nil
	}
	select {
	case <-c.xfer:
		return nil
	default:
		return c.xfer
	}
}
