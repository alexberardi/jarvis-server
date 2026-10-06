//go:build contract

package contract

import (
	"net/http"
	"testing"
	"time"
)

// The jarvis-log-client batch endpoints. Services post {"logs": [...]} with app credentials to
// /api/v0/logs/batch; nodes post the same body with X-Node-Id/X-Node-Key to
// /api/v0/node/logs/batch (the node must have access to service "jarvis-logs"). The client
// treats 200 and 204 as success and gives up after repeated 401/403.

func logBatch(msg string) map[string]any {
	return map[string]any{"logs": []map[string]any{
		{"service": "contract-suite", "level": "INFO", "message": msg, "context": map[string]any{"contract": true}},
		// An explicit timestamp must be recent: legacy logs forwards to Loki, which rejects
		// samples older than its retention window and the whole batch then fails with 502
		// "Failed to push logs to Loki". That is Loki's behaviour, not a contract, so it is
		// not frozen.
		{"service": "contract-suite", "level": "DEBUG", "message": msg + " (2)", "timestamp": time.Now().UTC().Format(time.RFC3339Nano)},
	}}
}

func TestLogsBatchApp(t *testing.T) {
	tg := T(t)
	tg.Need(t, Logs)
	app := SharedApp(t)

	t.Run("accepted", func(t *testing.T) {
		tg.Post(t, Logs, "/api/v0/logs/batch", logBatch("contract app batch"), app.H()).
			ExpectStatus(http.StatusNoContent).ExpectEmpty()
	})
	t.Run("empty batch", func(t *testing.T) {
		tg.Post(t, Logs, "/api/v0/logs/batch", map[string]any{"logs": []any{}}, app.H()).
			ExpectStatus(http.StatusNoContent).ExpectEmpty()
	})
	t.Run("single entry", func(t *testing.T) {
		tg.Post(t, Logs, "/api/v0/logs", map[string]any{"service": "contract-suite", "level": "INFO", "message": "contract single"}, app.H()).
			ExpectStatus(http.StatusNoContent).ExpectEmpty()
	})
	t.Run("bad level", func(t *testing.T) {
		tg.Post(t, Logs, "/api/v0/logs/batch",
			map[string]any{"logs": []map[string]any{{"service": "contract-suite", "level": "TRACE", "message": "x"}}}, app.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "logs", 0, "level"))
	})
	t.Run("missing credentials", func(t *testing.T) {
		tg.Post(t, Logs, "/api/v0/logs/batch", logBatch("x")).
			ExpectError(http.StatusUnauthorized, "Missing app credentials")
	})
	t.Run("invalid credentials", func(t *testing.T) {
		tg.Post(t, Logs, "/api/v0/logs/batch", logBatch("x"), H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong"}).
			ExpectError(http.StatusUnauthorized, "Invalid app credentials")
	})
}

func TestLogsBatchNode(t *testing.T) {
	tg := T(t)
	tg.Need(t, Logs)
	node := SharedNode(t)

	t.Run("accepted", func(t *testing.T) {
		tg.Post(t, Logs, "/api/v0/node/logs/batch", logBatch("contract node batch"), node.LogsH()).
			ExpectStatus(http.StatusNoContent).ExpectEmpty()
	})
	t.Run("missing credentials", func(t *testing.T) {
		tg.Post(t, Logs, "/api/v0/node/logs/batch", logBatch("x")).
			ExpectError(http.StatusUnauthorized, "Missing node credentials")
	})
	t.Run("invalid credentials", func(t *testing.T) {
		// LEGACY-BUG: bad node credentials are 403 here (with validate-node's reason as the
		// detail) where every other service answers 401. The log client treats both alike.
		tg.Post(t, Logs, "/api/v0/node/logs/batch", logBatch("x"), H{"X-Node-Id": node.ID, "X-Node-Key": "wrong"}).
			ExpectError(http.StatusForbidden, "Invalid node credentials")
	})
	t.Run("node without jarvis-logs access", func(t *testing.T) {
		app := SharedApp(t)
		n := NewNode(t, SharedUser(t).HouseholdID, app.ID)
		tg.Post(t, Logs, "/api/v0/node/logs/batch", logBatch("x"), n.LogsH()).
			ExpectError(http.StatusForbidden, "Node is not authorized to access service 'jarvis-logs'")
	})
}
