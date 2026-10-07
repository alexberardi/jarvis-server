package mqtt

import (
	"context"
	"log/slog"
)

// connLogHandler tidies one mochi log line. mochi's TCP listener logs every connection that
// ends with an error as Warn("", "error", err): no message, and a client that simply hung up
// ("read connection: EOF") reads like a problem on the Logs page. Such a record gets a
// message, and a plain hang-up drops to Debug. Everything else passes through unchanged (the
// auth hook already logs refused credentials with the client and reason).
type connLogHandler struct{ next slog.Handler }

func (h connLogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	// A Warn may become a Debug; let Handle decide when the level is close.
	return h.next.Enabled(ctx, l) || (l >= slog.LevelWarn && h.next.Enabled(ctx, slog.LevelDebug))
}

func (h connLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "" {
		hangUp := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "error" {
				hangUp = isHangUpText(a.Value.String())
			}
			return true
		})
		nr := slog.NewRecord(r.Time, r.Level, "mqtt connection ended with an error", r.PC)
		if hangUp {
			nr.Level, nr.Message = slog.LevelDebug, "mqtt client disconnected"
		}
		r.Attrs(func(a slog.Attr) bool { nr.AddAttrs(a); return true })
		r = nr
	}
	if !h.next.Enabled(ctx, r.Level) {
		return nil
	}
	return h.next.Handle(ctx, r)
}

func (h connLogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return connLogHandler{h.next.WithAttrs(as)}
}

func (h connLogHandler) WithGroup(name string) slog.Handler {
	return connLogHandler{h.next.WithGroup(name)}
}
