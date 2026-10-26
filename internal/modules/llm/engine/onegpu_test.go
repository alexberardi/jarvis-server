package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/engines"
)

// oneCard makes the rig's GPU hold one engine only: a second one placed next to it doesn't fit.
func oneCard(g *rig) *[]string {
	var asked []string
	g.r.Fits = func(_ context.Context, c LabelConfig, f Flavour, along []LabelConfig) string {
		var names []string
		for _, a := range along {
			names = append(names, a.Label)
		}
		asked = append(asked, c.Label+"|"+string(f)+"|"+strings.Join(names, ","))
		if len(along) == 0 {
			return ""
		}
		return "RTX 3080 Ti (12288 MB) can't hold " + c.Label + " next to " + strings.Join(names, ", ")
	}
	return &asked
}

// #562: live and background on one model, differing only in load settings the engine can
// serve both with (parallel, context, KV cache, flash attention), on a card that holds one
// copy: background runs on live's engine instead of starting a second copy that dies of OOM.
func TestOneGPUSharesInsteadOfSecondCopy(t *testing.T) {
	g := newRig(t)
	asked := oneCard(g)
	m := g.model("qwen.gguf")
	live := g.llm(LabelLive, m)
	g.src.set(live)
	bg := live
	bg.Label, bg.Parallel, bg.Context, bg.KVCacheType, bg.FlashAttn = LabelBackground, 1, 32768, "q8_0", "on"
	g.src.set(bg)

	epLive := g.ready(LabelLive)
	epBg := g.ready(LabelBackground)
	if epBg.Engine != epLive.Engine {
		t.Fatalf("two engines started for one model on one card: %s, %s", epLive.Engine, epBg.Engine)
	}
	if n := len(g.starts()); n != 1 {
		t.Fatalf("%d engine starts, want 1", n)
	}
	// Background's requests are shaped by the engine it really runs on.
	if epBg.ContextLength != live.Context || epBg.Parallel != live.Parallel {
		t.Fatalf("background endpoint %+v, want live's context and slots", epBg)
	}
	if want := "live|cuda|"; len(*asked) == 0 || (*asked)[0] != want {
		t.Fatalf("fit asked %v, want live first with nothing alongside", *asked)
	}
	ls := statusOf(g.r, LabelBackground)
	if ls.State != "ready" || !strings.Contains(ls.Note, epLive.Engine) || !strings.Contains(ls.Note, "can't hold background next to live") {
		t.Fatalf("background status %+v: the note must say it shares live's engine, and why", ls)
	}
	// Reconciling again keeps it there: no second start, no churn.
	g.r.Reconcile(context.Background())
	if ep := g.ready(LabelBackground); ep.Engine != epLive.Engine || len(g.starts()) != 1 {
		t.Fatalf("after reconcile: %s, %d starts", ep.Engine, len(g.starts()))
	}
	// Same settings as live: plain sharing, and the note is gone.
	bg = live
	bg.Label = LabelBackground
	g.src.set(bg)
	if ep := g.ready(LabelBackground); ep.Engine != epLive.Engine {
		t.Fatal("should share")
	}
	if ls := statusOf(g.r, LabelBackground); ls.Note != "" {
		t.Fatalf("note left after plain sharing: %q", ls.Note)
	}
}

// #562: when the second engine can't share the first (another model, other placement or
// template) and the card can't hold both, it isn't started: the label says why, naming the card.
func TestOneGPURefusesWhatDoesNotFit(t *testing.T) {
	g := newRig(t)
	oneCard(g)
	live := g.llm(LabelLive, g.model("qwen.gguf"))
	g.src.set(live)
	bg := g.llm(LabelBackground, g.model("other.gguf"))
	g.src.set(bg)

	epLive := g.ready(LabelLive)
	_, err := g.r.Resolve(context.Background(), LabelBackground)
	var nr *NotReadyError
	if !errors.As(err, &nr) || nr.State != StateDoesNotFit || !strings.Contains(nr.Reason, "RTX 3080 Ti (12288 MB) can't hold background next to live") {
		t.Fatalf("resolve background: %v", err)
	}
	if n := len(g.starts()); n != 1 {
		t.Fatalf("%d engine starts, want 1 (live's)", n)
	}
	if ls := statusOf(g.r, LabelBackground); ls.State != StateDoesNotFit || ls.Reason != nr.Reason {
		t.Fatalf("status %+v", ls)
	}
	// Reconciling doesn't start it either, and live is untouched.
	g.r.Reconcile(context.Background())
	if n := len(g.r.Instances()); n != 1 || g.ready(LabelLive).Engine != epLive.Engine {
		t.Fatalf("instances after reconcile: %+v", g.r.Instances())
	}
	// Live goes elsewhere (off): background now fits and starts.
	off := live
	off.Engine = ModeOff
	g.src.set(off)
	g.r.Reconcile(context.Background())
	if ep := g.ready(LabelBackground); ep.Engine == "" || ep.Engine == epLive.Engine {
		t.Fatalf("background after live went off: %+v", ep)
	}
}

// A CPU engine takes no GPU memory: no fit check.
func TestOneGPUSkipsCPU(t *testing.T) {
	g := newRig(t)
	asked := oneCard(g)
	c := g.llm(LabelLive, g.model("qwen.gguf"))
	c.GPUBackend = string(FlavourCPU)
	g.src.set(c)
	g.ready(LabelLive)
	if len(*asked) != 0 {
		t.Fatalf("fit asked for a CPU engine: %v", *asked)
	}
}

// #562 (3): an engine that ran out of GPU memory isn't retried (retrying the same placement
// gives the same OOM): it is left failed at once, with a reason that says so.
func TestOOMEngineIsNotRetried(t *testing.T) {
	g := newRig(t)
	g.r.RetryFailed = 20 * time.Millisecond
	g.r.Restart.MaxRestarts = 5
	t.Setenv("FAKE_ENGINE_OOM", "1")
	g.src.set(g.llm(LabelLive, g.model("qwen.gguf")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.r.Start(ctx)
	g.r.Resolve(context.Background(), LabelLive)
	var inst InstanceStatus
	deadline := time.Now().Add(30 * time.Second)
	for {
		if is := g.r.Instances(); len(is) == 1 && is[0].GaveUp {
			inst = is[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never gave up: %+v", g.r.Instances())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if inst.State != engines.Failed || inst.Retries != 0 || inst.Restarts != 0 || !strings.Contains(inst.GaveUpReason, "out of GPU memory") {
		t.Fatalf("gave up: %+v", inst)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(g.starts()); n != 1 {
		t.Fatalf("%d launches, want 1: an OOM was retried", n)
	}
	var nr *NotReadyError
	if _, err := g.r.Resolve(context.Background(), LabelLive); !errors.As(err, &nr) || nr.State != string(engines.Failed) ||
		!strings.Contains(nr.Reason, "out of GPU memory") || !strings.Contains(nr.Reason, "cudaMalloc failed") ||
		strings.Contains(nr.Reason, "gave up after") {
		t.Fatalf("resolve: %v", err)
	}
	// Retry (after freeing the card) still works.
	t.Setenv("FAKE_ENGINE_OOM", "")
	if err := g.r.Retry(context.Background(), inst.Name); err != nil {
		t.Fatal(err)
	}
	if ep := g.ready(LabelLive); ep.Engine != inst.Name {
		t.Fatalf("ready on %s, want %s", ep.Engine, inst.Name)
	}
}
