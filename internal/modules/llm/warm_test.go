package llm

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// An engine that just became healthy gets a one-token call per chat label it serves, so a lazy
// engine (mlx_lm.server loads its weights on the first request) is loaded before a user waits on
// it, and then the listener (command-center's prompt re-warm) runs with the labels.
func TestWarmEngineLabels(t *testing.T) {
	var got []ChatRequest
	chat := func(_ context.Context, r ChatRequest) error {
		got = append(got, r)
		if r.Label == LabelBackground {
			return errors.New("busy") // a failed warm-up is logged, never fatal
		}
		return nil
	}
	var notified []string
	warmEngine(context.Background(), chat, []string{LabelBackground, "embeddings", LabelLive},
		func(l []string) { notified = l }, quietLog())

	var labels []string
	for _, r := range got {
		labels = append(labels, r.Label)
		if r.MaxTokens == nil || *r.MaxTokens != 1 || r.Priority != PriorityBackground || len(r.Messages) != 1 {
			t.Fatalf("warm-up request %+v", r)
		}
	}
	if !slices.Equal(labels, []string{LabelBackground, LabelLive}) {
		t.Fatalf("warmed %v, want only the chat labels", labels)
	}
	if !slices.Equal(notified, []string{LabelBackground, "embeddings", LabelLive}) {
		t.Fatalf("listener got %v", notified)
	}
}
