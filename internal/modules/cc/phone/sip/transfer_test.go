package sip

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

// referPBX is a fake PBX whose inbound call to the UA is answered, with audio flowing, and
// whose REFER handling each test scripts: respond(req) answers the REFER, then after() runs
// (NOTIFYs, a BYE) on the call.
func referPBX(t *testing.T, cfg func(*Config), respond func(req *sip.Request) int, after func(pc *pbxCall)) (*fakePBX, *pbxCall, *Call) {
	t.Helper()
	answered := make(chan *Call, 1)
	p := newPBX(t, "secret")
	c := p.config(t, "secret")
	c.Inbound = func(context.Context, Incoming) Admission {
		return Admission{Answered: func(c *Call) { answered <- c }}
	}
	if cfg != nil {
		cfg(&c)
	}
	u := startUA(t, c)
	waitState(t, u, StateRegistered, 3*time.Second)
	var pc *pbxCall
	ready := make(chan struct{})
	p.setRefer(func(req *sip.Request, tx sip.ServerTransaction) {
		<-ready
		code := respond(req)
		tx.Respond(sip.NewResponseFromRequest(req, code, statusText(code), nil))
		if code == sip.StatusAccepted && after != nil {
			go after(pc)
		}
	})
	pc = p.callUA(t, u)
	close(ready)
	if ok, code := pc.waitFinal(t, 5*time.Second); !ok {
		t.Fatalf("not answered: %d", code)
	}
	call := <-answered
	pc.far.stream(func(int) []int16 { return tone(FrameSamples, 8000) })
	select {
	case <-call.Started():
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	drain(call)
	return p, pc, call
}

// notify sends the PBX's in-dialog NOTIFY of the transfer's progress (RFC 3515 sipfrag).
func (c *pbxCall) notify(t *testing.T, code int, final bool) {
	req := sip.NewRequest(sip.NOTIFY, c.d.InviteResponse.Contact().Address)
	req.AppendHeader(sip.NewHeader("Event", "refer"))
	state := "active;expires=60"
	if final {
		state = "terminated;reason=noresource"
	}
	req.AppendHeader(sip.NewHeader("Subscription-State", state))
	req.AppendHeader(sip.NewHeader("Content-Type", "message/sipfrag;version=2.0"))
	req.SetBody(fmt.Appendf(nil, "SIP/2.0 %d %s\r\n", code, statusText(code)))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := c.d.Do(ctx, req)
	if err != nil || res.StatusCode != 200 {
		t.Errorf("NOTIFY %d: %v %v", code, res, err)
	}
}

func transfer(t *testing.T, c *Call, target string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.Transfer(ctx, target)
}

func TestTransferWaitsForTheFinalNotifyThenHangsUp(t *testing.T) {
	var referTo string
	p, _, c := referPBX(t, nil, func(req *sip.Request) int {
		if h := req.GetHeader("Refer-To"); h != nil {
			referTo = h.Value()
		}
		return sip.StatusAccepted
	}, func(pc *pbxCall) {
		pc.notify(t, 100, false)
		time.Sleep(200 * time.Millisecond)
		pc.notify(t, 180, false)
		time.Sleep(200 * time.Millisecond)
		pc.notify(t, 200, true)
	})
	start := time.Now()
	if err := transfer(t, c, "101"); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if took := time.Since(start); took < 400*time.Millisecond {
		t.Fatalf("returned after %s: before the final NOTIFY (diago's Refer returns on 202)", took)
	}
	if want := fmt.Sprintf("<sip:101@%s:%d>", p.host, p.port); referTo != want {
		t.Fatalf("Refer-To %q, want %q: the target on the registrar", referTo, want)
	}
	if end, detail := waitDone(t, c, 3*time.Second); end != EndTransferred {
		t.Fatalf("end %q (%s), want transferred", end, detail)
	}
	eventually(t, 3*time.Second, "our BYE at the PBX", func() bool { return p.got(sip.BYE, c.ID()) })
}

func TestARefusedTransferLeavesTheCallUp(t *testing.T) {
	_, pc, c := referPBX(t, nil, func(*sip.Request) int { return 603 }, nil)
	err := transfer(t, c, "101")
	var te *TransferError
	if !errors.As(err, &te) || te.Code != 603 || te.Accepted {
		t.Fatalf("err %v, want a refused transfer (603)", err)
	}
	assertStillUp(t, pc, c)
}

func TestATransferThatFailsAtTheTargetLeavesTheCallUp(t *testing.T) {
	_, pc, c := referPBX(t, nil, func(*sip.Request) int { return sip.StatusAccepted }, func(pc *pbxCall) {
		pc.notify(t, 100, false)
		pc.notify(t, 486, true)
	})
	err := transfer(t, c, "101")
	var te *TransferError
	if !errors.As(err, &te) || te.Code != 486 || !te.Accepted || te.Gone {
		t.Fatalf("err %+v, want the target's 486 after the 202, the caller still on the call", err)
	}
	assertStillUp(t, pc, c)
}

// Asterisk takes the caller off our leg when it accepts the REFER: after a failure their audio
// has stopped, and our empty leg is hung up rather than talked into.
func TestAFailedTransferWhoseCallerIsGoneHangsUp(t *testing.T) {
	p, _, c := referPBX(t, func(c *Config) { c.NoRTPTimeout = 10 * time.Second }, func(*sip.Request) int { return sip.StatusAccepted }, func(pc *pbxCall) {
		pc.far.close()
		time.Sleep(100 * time.Millisecond)
		pc.notify(t, 486, true)
	})
	err := transfer(t, c, "2125550486")
	var te *TransferError
	if !errors.As(err, &te) || te.Code != 486 || !te.Gone {
		t.Fatalf("err %+v, want the target's 486 with the caller gone", err)
	}
	if end, _ := waitDone(t, c, time.Second); end != EndClosed { // before the no-RTP watchdog
		t.Fatalf("end %q, want closed (our hangup)", end)
	}
	eventually(t, 3*time.Second, "our BYE at the PBX", func() bool { return p.got(sip.BYE, c.ID()) })
}

func TestATransferWithNoFinalNotifyTimesOutAndLeavesTheCallUp(t *testing.T) {
	_, pc, c := referPBX(t, func(c *Config) { c.TransferWait = 500 * time.Millisecond },
		func(*sip.Request) int { return sip.StatusAccepted },
		func(pc *pbxCall) { pc.notify(t, 100, false) })
	start := time.Now()
	err := transfer(t, c, "101")
	var te *TransferError
	if !errors.As(err, &te) || te.Code != 0 || !te.Accepted || te.Gone {
		t.Fatalf("err %+v, want a timeout after the 202, the caller still on the call", err)
	}
	if took := time.Since(start); took < 500*time.Millisecond || took > 3*time.Second {
		t.Fatalf("returned after %s, want the 500ms wait", took)
	}
	assertStillUp(t, pc, c)
}

func TestThePBXEndingTheCallMidTransferIsNotASuccess(t *testing.T) {
	_, _, c := referPBX(t, nil, func(*sip.Request) int { return sip.StatusAccepted }, func(pc *pbxCall) {
		pc.notify(t, 100, false)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = pc.d.Bye(ctx)
	})
	err := transfer(t, c, "101")
	var te *TransferError
	if !errors.As(err, &te) || !te.Accepted || !te.Gone {
		t.Fatalf("err %+v, want an unconfirmed transfer with the caller gone", err)
	}
	if end, _ := waitDone(t, c, 3*time.Second); end != EndRemote {
		t.Fatalf("end %q, want remote", end)
	}
}

func TestTransferTargetsAreDigitsOnlyAndOneAttemptPerCall(t *testing.T) {
	var refers atomic.Int32
	_, pc, c := referPBX(t, nil, func(*sip.Request) int { refers.Add(1); return 603 }, nil)
	for _, bad := range []string{"", "*72", "#", "sip:101@evil.example", "101@evil.example", "1 01", "12345678901234567890123456789012345"} {
		if err := transfer(t, c, bad); err == nil || errors.As(err, new(*TransferError)) {
			t.Fatalf("target %q: err %v, want it refused before any REFER", bad, err)
		}
	}
	if n := refers.Load(); n != 0 {
		t.Fatalf("%d REFERs for bad targets", n)
	}
	_ = transfer(t, c, "101")
	if err := transfer(t, c, "102"); !errors.Is(err, ErrTransferAttempted) {
		t.Fatalf("second transfer: %v, want ErrTransferAttempted", err)
	}
	if n := refers.Load(); n != 1 {
		t.Fatalf("%d REFERs, want 1", n)
	}
	assertStillUp(t, pc, c)
}

func TestAPlacedCallCannotBeTransferred(t *testing.T) {
	c := &Call{}
	if err := c.Transfer(context.Background(), "101"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err %v, want ErrUnsupported", err)
	}
}

// assertStillUp checks the call goes on after a failed transfer: not ended, audio both ways.
func assertStillUp(t *testing.T, pc *pbxCall, c *Call) {
	t.Helper()
	select {
	case <-c.Done():
		end, detail := c.End()
		t.Fatalf("the call ended: %s (%s)", end, detail)
	case <-time.After(100 * time.Millisecond):
	}
	pc.far.reset()
	if _, err := c.Play(tone(FrameSamples*10, 8000)); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "our audio at the caller after the transfer failed", func() bool {
		n := 0
		for _, pk := range pc.far.packets() {
			if loud(pk) {
				n++
			}
		}
		return n >= 5
	})
}

func statusText(code int) string {
	return map[int]string{100: "Trying", 180: "Ringing", 200: "OK", 202: "Accepted", 486: "Busy Here", 603: "Decline"}[code]
}
