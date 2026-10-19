package node

import (
	"context"
	"net/http"
	"net/url"
)

// FactoryResetStatus reports a tracked factory reset's state ("in_progress", "success" or
// "failed", with errMsg for the last) to command-center. It authenticates with the reset
// token only, as the Python node does: the token is the proof that jarvisd asked for this
// reset, and by the final report the node's own key is wiped. A 401 is a token jarvisd never
// issued (or one that expired).
func (c *CC) FactoryResetStatus(ctx context.Context, taskID, token, state, errMsg string) error {
	body := map[string]any{"state": state}
	if errMsg != "" {
		body["error_message"] = errMsg
	}
	return doJSON(ctx, c.HTTP, http.MethodPost, joinURL(c.BaseURL, "/api/v0/nodes/factory-reset/"+url.PathEscape(taskID)+"/status"),
		map[string]string{"X-Reset-Token": token}, body, nil)
}

// VerifyReset is the legacy check for a factory-reset message without a task id: jarvisd
// answers 200 only for a token it issued to this node.
func (c *CC) VerifyReset(ctx context.Context, requestID string) error {
	return doJSON(ctx, c.HTTP, http.MethodPost, joinURL(c.BaseURL, "/api/v0/nodes/verify-reset"), nil,
		map[string]any{"node_id": c.Creds.NodeID, "request_id": requestID}, nil)
}
