package cc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// approvalTTL is how long an approval token stays valid when the caller gives no ttl.
const approvalTTL = 10 * time.Minute

// mintApproval issues a single-use approval token for a computer task's risky actions
// (docs/agent/COMPUTER_CONTROL.md): the agent running run on nodeID verifies it with the digest
// of the approved actions before it acts. The token is 32 random bytes, base64url.
//
// nodeID is not in the blueprint's signature: cc_computer_runs does not exist yet, so the token
// carries the node that owns the run itself.
func (m *Module) mintApproval(ctx context.Context, hh, nodeID, runID, digest string, ttl time.Duration) (string, error) {
	if hh == "" || nodeID == "" || runID == "" || digest == "" {
		return "", errors.New("cc: approval needs a household, node, run and digest")
	}
	if ttl <= 0 {
		ttl = approvalTTL
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := m.now()
	_, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_computer_approvals
		(token, household_id, node_id, run_id, digest, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		token, hh, nodeID, runID, digest, dbTime(now), dbTime(now.Add(ttl)))
	if err != nil {
		return "", err
	}
	return token, nil
}

// handleVerifyApproval: POST /api/v0/computer/approvals/{token}/verify {digest} -> {valid}. Valid
// once, for the node (and household) the token was minted for, with the same digest, before it
// expires. Every refusal is the same {valid: false}, so a token can't be probed.
func (m *Module) handleVerifyApproval(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	digest, _ := b.str("digest", true)
	if !b.done(w) {
		return
	}
	now := dbTime(m.now())
	res, err := m.deps.DB.Write.ExecContext(r.Context(), `UPDATE cc_computer_approvals SET used_at = ?
		WHERE token = ? AND digest = ? AND node_id = ? AND household_id = ? AND used_at IS NULL AND expires_at > ?`,
		now, r.PathValue("token"), digest, n.ID, n.HouseholdID, now)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	nr, err := res.RowsAffected()
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"valid": nr == 1 && digest != ""})
}
