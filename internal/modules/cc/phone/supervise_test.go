package phone

import (
	"context"
	"testing"
)

// A cancelled call is hung up at the provider even when the media stream has already closed.
// Cancelling closes the socket, which ends the stream, so supervise often sees both rt.done
// and ctx.Done ready, and select picks one at random: on the rt.done branch it used to return
// without EndCall (TestCancelLiveCall failed on CI with "not hung up: []"). Both channels are
// ready before supervise runs, and repeated, so the old random pick can't pass by luck.
func TestSuperviseHangsUpCancelledCallAfterStreamEnded(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 32; i++ {
		p := newFakeProvider()
		ctx, cancel := context.WithCancel(context.Background())
		rt := &callRuntime{sessionID: "s", maxSeconds: 3600, ctx: ctx, cancel: cancel,
			tel: Telephony{Provider: p}, started: make(chan struct{}), done: make(chan struct{}), callSID: "CA1"}
		cancel()
		rt.markDone()
		e.s.supervise(ctx, rt)
		if got := p.endedCalls(); len(got) != 1 || got[0] != "CA1" {
			t.Fatalf("run %d: not hung up: %v", i, got)
		}
	}
	// A stream that ended by itself (the callee hung up) needs no hang-up.
	p := newFakeProvider()
	rt := &callRuntime{sessionID: "s", maxSeconds: 3600, ctx: context.Background(), cancel: func() {},
		tel: Telephony{Provider: p}, started: make(chan struct{}), done: make(chan struct{}), callSID: "CA2"}
	rt.markDone()
	e.s.supervise(context.Background(), rt)
	if got := p.endedCalls(); len(got) != 0 {
		t.Fatalf("hung up a call that already ended: %v", got)
	}
}
