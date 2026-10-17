package engine

import (
	"context"
	"slices"
	"testing"
	"time"
)

// startsOf counts the fake engine launches that loaded model.
func (g *rig) startsOf(model string) int {
	n := 0
	for _, s := range g.starts() {
		if i := slices.Index(s.Args, "-m"); i >= 0 && i+1 < len(s.Args) && s.Args[i+1] == model {
			n++
		}
	}
	return n
}

// swapStalled swaps live's model to b and starts Resolve(live) in the background. The
// returned channel closes when that Resolve returns. With FAKE_ENGINE_IGNORE_TERM=1 set before live's engine started, the old engine
// ignores SIGTERM and takes the whole StopTimeout (2s) to go.
func (g *rig) swapStalled(b string) <-chan struct{} {
	g.t.Helper()
	g.src.set(g.llm(LabelLive, b))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = g.r.Resolve(context.Background(), LabelLive)
	}()
	g.t.Cleanup(func() { <-done })
	time.Sleep(200 * time.Millisecond)
	return done
}

// Loop item #112: another label resolves while live's old engine is stopping.
func TestResolveWhileAnotherLabelStops(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(g *rig)
	}{
		{"remote", func(g *rig) {
			g.src.set(LabelConfig{Label: LabelBackground, Kind: KindLlama, ModelKind: ModelLLM, Engine: ModeRemote,
				RemoteURL: "http://example.invalid/v1", RemoteModel: "m"})
		}},
		{"local", func(g *rig) {
			g.src.set(g.llm(LabelBackground, g.model("c.gguf")))
			g.ready(LabelBackground)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig(t)
			t.Setenv("FAKE_ENGINE_IGNORE_TERM", "1")
			a, b := g.model("a.gguf"), g.model("b.gguf")
			g.src.set(g.llm(LabelLive, a))
			g.ready(LabelLive)
			tc.set(g)
			done := g.swapStalled(b)

			t0 := time.Now()
			if _, err := g.r.Resolve(context.Background(), LabelBackground); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(t0); d > 500*time.Millisecond {
				t.Fatalf("background blocked %v behind live's engine stop", d)
			}
			// Status and Instances don't wait either.
			t0 = time.Now()
			g.r.Status(context.Background())
			g.r.Instances()
			if d := time.Since(t0); d > 500*time.Millisecond {
				t.Fatalf("status blocked %v", d)
			}
			select {
			case <-done:
				t.Fatal("live resolved before its old engine stopped")
			default:
			}
			<-done
		})
	}
}

// The replacement engine loads only once the old one has exited (to free its GPU memory),
// and a second Resolve of the same label during the stop doesn't start a duplicate.
func TestReplacementWaitsForOldEngine(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_ENGINE_IGNORE_TERM", "1")
	a, b := g.model("a.gguf"), g.model("b.gguf")
	g.src.set(g.llm(LabelLive, a))
	g.ready(LabelLive)
	done := g.swapStalled(b)
	second := make(chan struct{})
	go func() {
		defer close(second)
		_, _ = g.r.Resolve(context.Background(), LabelLive)
	}()
	g.r.Reconcile(context.Background()) // waits for the stop too
	<-done
	<-second
	g.ready(LabelLive) // b's engine is serving, so its launch is logged
	if n := g.startsOf(b); n != 1 {
		t.Fatalf("b started %d times", n)
	}
	if insts := g.r.Instances(); len(insts) != 1 || insts[0].Model != b {
		t.Fatalf("%+v", insts)
	}
}

func TestReplacementStartsOnlyAfterStop(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_ENGINE_IGNORE_TERM", "1")
	a, b := g.model("a.gguf"), g.model("b.gguf")
	g.src.set(g.llm(LabelLive, a))
	g.ready(LabelLive)
	done := g.swapStalled(b)
	time.Sleep(time.Second) // the old engine has ~0.8s left
	if n := g.startsOf(b); n != 0 {
		t.Fatalf("b started while a was stopping")
	}
	<-done
	g.ready(LabelLive)
	if n := g.startsOf(b); n != 1 {
		t.Fatalf("b started %d times", n)
	}
}

// Status reports; it never starts or stops an engine.
func TestStatusIsReadOnly(t *testing.T) {
	g := newRig(t)
	a, b := g.model("a.gguf"), g.model("b.gguf")
	g.src.set(g.llm(LabelLive, a))
	st := statusOf(g.r, LabelLive)
	if len(g.r.Instances()) != 0 || st.State != "stopped" {
		t.Fatalf("%+v %+v", st, g.r.Instances())
	}
	ep := g.ready(LabelLive)
	if st := statusOf(g.r, LabelLive); st.State != "ready" || st.Endpoint == nil || st.Endpoint.Engine != ep.Engine ||
		st.Endpoint.APIKey != "" {
		t.Fatalf("%+v", st)
	}
	g.src.set(g.llm(LabelLive, b))
	statusOf(g.r, LabelLive)
	if insts := g.r.Instances(); len(insts) != 1 || insts[0].Name != ep.Engine || g.startsOf(b) != 0 {
		t.Fatalf("status applied the new setting: %+v", insts)
	}
	// Labels that need no engine are reported from their settings alone.
	g.src.set(LabelConfig{Label: LabelBackground, Kind: KindLlama, ModelKind: ModelLLM, Engine: ModeRemote,
		RemoteURL: "http://example.invalid/v1/", RemoteModel: "m", RemoteAPIKey: "k"})
	if st := statusOf(g.r, LabelBackground); st.State != "remote" || st.Endpoint == nil ||
		st.Endpoint.BaseURL != "http://example.invalid/v1" || st.Endpoint.APIKey != "" || st.Config.RemoteAPIKey != "" {
		t.Fatalf("%+v", st)
	}
	if st := statusOf(g.r, LabelEmbeddings); st.State != "not_configured" {
		t.Fatalf("%+v", st)
	}
	// Once applied, a label whose engine can't run reports why.
	g.bins.mu.Lock()
	g.bins.have = map[Flavour]bool{}
	g.bins.mu.Unlock()
	g.src.set(g.llm(LabelLive, g.model("c.gguf")))
	g.r.Reconcile(context.Background())
	if st := statusOf(g.r, LabelLive); st.State != "fetching_engine" {
		t.Fatalf("%+v", st)
	}
}

func statusOf(r *Resolver, label string) LabelStatus {
	for _, s := range r.Status(context.Background()) {
		if s.Label == label {
			return s
		}
	}
	return LabelStatus{}
}
