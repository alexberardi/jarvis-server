package mqtt

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// mochi's TCP listener logs Warn("", "error", err) for every connection that ends badly; the
// A10 rehearsal's Logs page showed a row of blank WARNINGs for ordinary hang-ups.
func TestConnLogHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(connLogHandler{slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})}).
		With("component", "mqtt", "listener", "tcp")

	log.Warn("", "error", io.EOF)
	log.Warn("", "error", errors.New("read connection: EOF"))
	if buf.Len() != 0 {
		t.Fatalf("a hang-up was logged at info level:\n%s", buf.String())
	}
	log.Warn("", "error", errors.New("bad username or password"))
	out := buf.String()
	if !strings.Contains(out, `level=WARN msg="mqtt connection ended with an error"`) ||
		!strings.Contains(out, `error="bad username or password"`) || !strings.Contains(out, "listener=tcp") {
		t.Fatalf("other errors keep Warn with a message and their attributes:\n%s", out)
	}
	buf.Reset()
	log.Info("attached listener", "id", "t1")
	if !strings.Contains(buf.String(), `msg="attached listener"`) {
		t.Fatalf("other lines pass through:\n%s", buf.String())
	}

	// With debug on, a hang-up is visible as a debug line.
	buf.Reset()
	dbg := slog.New(connLogHandler{slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})})
	dbg.Warn("", "error", io.EOF)
	if !strings.Contains(buf.String(), `level=DEBUG msg="mqtt client disconnected"`) {
		t.Fatalf("debug: %s", buf.String())
	}
}
