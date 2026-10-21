package cc

import (
	"context"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// Identical warm-ups in flight share one model call (decision 2026-10-20-prompt-diet): five
// conversations opening at once sent five cold copies of the same prefix to a one-at-a-time
// engine, and a voice turn queued behind them for 35 s.
func TestWarmFlightSharesIdenticalWarmups(t *testing.T) {
	var w warmFlight
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex
	fn := func(context.Context) error {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return nil
	}
	req := llm.ChatRequest{Label: llm.LabelLive, Messages: []llm.Message{{Role: "system", Content: llm.TextContent("prefix")}}}
	other := llm.ChatRequest{Label: llm.LabelLive, Messages: []llm.Message{{Role: "system", Content: llm.TextContent("another")}}}

	var wg sync.WaitGroup
	started := make(chan struct{}, 6)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			if err := w.do(context.Background(), req, fn); err != nil {
				t.Error(err)
			}
		}()
	}
	for i := 0; i < 5; i++ {
		<-started
	}
	// A different prefix is its own call.
	wg.Add(1)
	go func() { defer wg.Done(); _ = w.do(context.Background(), other, fn) }()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return calls == 2 })
	close(release)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("%d model calls, want 2 (one per distinct prefix)", calls)
	}
}

// When the live engine (re)starts, the recent warm-up prefixes are sent again, so the first turn
// after a restart or a model change is warm.
func TestEngineReadyRewarmsRecentPrefix(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("rw1", searchTools)
	reqs := ve.eng.requests()
	if len(reqs) == 0 {
		t.Fatal("no warm-up at conversation start")
	}
	first := jsonOf(reqs[0]["messages"])

	ve.m.EngineReady([]string{llm.LabelBackground}) // not live: nothing to re-warm
	ve.m.EngineReady([]string{llm.LabelLive})
	waitFor(t, func() bool { return len(ve.eng.requests()) > len(reqs) })
	again := ve.eng.requests()[len(reqs)]
	if jsonOf(again["messages"]) != first || again["max_tokens"] != float64(1) {
		t.Fatalf("re-warm sent %s", jsonOf(again))
	}
}
