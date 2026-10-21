package llm

import (
	"context"
	"log/slog"
	"time"
)

// engineWarmTimeout bounds one label's warm-up call: a big model's first request loads it.
const engineWarmTimeout = 5 * time.Minute

// warmEngine runs when an engine becomes healthy (engine.Resolver.OnReady): a one-token call per
// chat label it serves, so an engine that loads lazily (mlx_lm.server reads its weights on the
// first request) is loaded before a user waits on it, then the listener runs with all the labels
// (command-center re-sends its recent prompt warm-ups). Decision 2026-10-20-prompt-diet.
func warmEngine(ctx context.Context, chat func(context.Context, ChatRequest) error, labels []string,
	listener func([]string), log *slog.Logger) {
	for _, l := range labels {
		if l != LabelLive && l != LabelBackground {
			continue
		}
		one, zero := 1, 0.0
		wctx, cancel := context.WithTimeout(ctx, engineWarmTimeout)
		start := time.Now()
		err := chat(wctx, ChatRequest{Label: l, Messages: []Message{{Role: "user", Content: TextContent("hi")}},
			MaxTokens: &one, Temperature: &zero, Priority: PriorityBackground})
		cancel()
		if err != nil {
			log.Warn("llm: engine warm-up failed", "label", l, "err", err)
			continue
		}
		log.Info("llm: engine warmed", "label", l, "ms", time.Since(start).Milliseconds())
	}
	if listener != nil {
		listener(labels)
	}
}
