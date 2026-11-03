package node

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/logging"
)

// LogSink ships the agent's log records to jarvisd's Logs page (the jarvis-logs service's POST
// /api/v0/node/logs/batch), so the household sees why a wake was dropped without fetching
// agent.log from the computer. Until Connect it answers logging.ErrNotReady, and the shipper
// holds the records.
type LogSink struct{ to atomic.Pointer[logTarget] }

type logTarget struct {
	url   string
	creds Creds
}

var logHTTP = &http.Client{Timeout: 5 * time.Second}

// Connect points the sink at the jarvis-logs service cc's discovery listed (none: unchanged).
func (s *LogSink) Connect(cc *CC) {
	if u, err := cc.Services.Need(SvcLogs); err == nil {
		s.to.Store(&logTarget{url: joinURL(u, "/api/v0/node/logs/batch"), creds: cc.Creds})
	}
}

func (s *LogSink) Write(ctx context.Context, batch []logging.Record) error {
	t := s.to.Load()
	if t == nil {
		return logging.ErrNotReady
	}
	logs := make([]map[string]any, len(batch))
	for i, r := range batch {
		c := make(map[string]any, len(r.Attrs)+1)
		for k, v := range r.Attrs {
			c[k] = logValue(v)
		}
		if r.Source != "jarvisd" { // logging's name for a record without a "module" attribute
			c["module"] = r.Source
		}
		logs[i] = map[string]any{"timestamp": r.Time.UTC().Format(time.RFC3339Nano), "service": "jarvis-agent",
			"level": logLevel(r.Level), "message": r.Message, "context": c}
	}
	return doJSON(ctx, logHTTP, http.MethodPost, t.url,
		map[string]string{"X-Node-Id": t.creds.NodeID, "X-Node-Key": t.creds.NodeKey}, map[string]any{"logs": logs}, nil)
}

// logValue is v as JSON can show it: an error or a Stringer (a duration) as its text, a group
// (a LogValuer such as Creds) as an object.
func logValue(v any) any {
	switch v := v.(type) {
	case error:
		return v.Error()
	case fmt.Stringer:
		return v.String()
	case []slog.Attr:
		m := make(map[string]any, len(v))
		for _, a := range v {
			m[a.Key] = logValue(a.Value.Resolve().Any())
		}
		return m
	}
	return v
}

// logLevel is the Logs page's level name.
func logLevel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARNING"
	case l >= slog.LevelInfo:
		return "INFO"
	}
	return "DEBUG"
}
