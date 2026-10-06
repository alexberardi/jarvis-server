//go:build contract

package contract

import (
	"fmt"
	"net/http"
	"os"
	"testing"
)

// Command-center node fixtures (docs/cc/05 §3.1). CC rejects a node that is valid in auth but
// missing from its own nodes table ("Node not configured locally"), so CC node routes need a
// CC row. Admin create (POST /api/v0/admin/nodes with X-API-Key: ADMIN_API_KEY) registers the
// node in jarvis-auth itself (app-to-app /internal/nodes/register, services ["jarvis-logs"],
// CC auto-granted) and inserts the CC row, returning the node_key once. It cannot adopt an
// existing auth node: auth answers 400 for a duplicate node_id.
//
// Cleanup is DELETE /api/v0/admin/nodes/{id} with the owning user's JWT (power_user in the
// household; the solo-household owner qualifies). That publishes factory-reset, deactivates
// the node in auth, and hard-deletes the CC row and its settings requests, snapshots and (by
// FK cascade) callback jobs. A deactivate through auth's admin API follows as a backstop.

const EnvCCAdminKey = "JARVIS_CONTRACT_CC_ADMIN_KEY" // command-center ADMIN_API_KEY

// CCNode is a node that exists in both jarvis-auth and command-center.
type CCNode struct {
	Node
	Owner *User
	Room  string
}

// CCAdminH is command-center's admin header (the same X-API-Key header as node auth).
func CCAdminH() H { return H{"X-API-Key": os.Getenv(EnvCCAdminKey)} }

// NeedCCAdmin skips unless command-center and its admin key are configured.
func (tg *Target) NeedCCAdmin(t testing.TB) {
	t.Helper()
	tg.NeedAdmin(t)
	tg.Need(t, CommandCenter)
	if os.Getenv(EnvCCAdminKey) == "" {
		t.Skipf("contract: %s is not set (command-center ADMIN_API_KEY, for CC node fixtures)", EnvCCAdminKey)
	}
}

func (tg *Target) createCCNode(owner *User) (*CCNode, error) {
	id := fmt.Sprintf("contract-%s-ccnode-%s", tg.RunID, randHex(3))
	r, err := tg.do(CommandCenter, http.MethodPost, "/api/v0/admin/nodes", map[string]any{
		"node_id": id, "household_id": owner.HouseholdID, "name": "contract cc node", "room": "contract",
	}, CCAdminH())
	if err != nil {
		return nil, err
	}
	var out struct {
		NodeID  string `json:"node_id"`
		Room    string `json:"room"`
		NodeKey string `json:"node_key"`
	}
	if err := r.expect(http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &CCNode{
		Node:  Node{ID: out.NodeID, Key: out.NodeKey, HouseholdID: owner.HouseholdID, Services: []string{"jarvis-logs"}},
		Owner: owner,
		Room:  out.Room,
	}, nil
}

// deleteCCNode is the CC hard delete. A 404 (already gone) is fine.
func (tg *Target) deleteCCNode(n *CCNode) error {
	del := func() (*Resp, error) {
		return tg.do(CommandCenter, http.MethodDelete, "/api/v0/admin/nodes/"+n.ID, nil, n.Owner.H())
	}
	r, err := del()
	if err != nil {
		return err
	}
	if r.Status == http.StatusUnauthorized {
		if err := tg.login(n.Owner); err != nil {
			return fmt.Errorf("login before CC node delete: %w", err)
		}
		if r, err = del(); err != nil {
			return err
		}
	}
	if r.Status != http.StatusNotFound {
		if err := r.expect(http.StatusOK, nil); err != nil {
			return err
		}
	}
	return tg.deactivateNode(&n.Node)
}

// NewCCNode creates a CC node in owner's household, deleted when the test ends.
func NewCCNode(t testing.TB, owner *User) *CCNode {
	t.Helper()
	tg := T(t)
	tg.NeedCCAdmin(t)
	n, err := tg.createCCNode(owner)
	if err != nil {
		t.Fatalf("fixture CC node: %v", err)
	}
	t.Cleanup(func() {
		if err := tg.deleteCCNode(n); err != nil {
			t.Errorf("cleanup CC node %s: %v", n.ID, err)
		}
	})
	return n
}

var sharedCC struct {
	node *CCNode
}

// SharedCCNode is a CC node in SharedUser's household. Don't delete or re-key it.
func SharedCCNode(t testing.TB) *CCNode {
	t.Helper()
	tg := T(t)
	tg.NeedCCAdmin(t)
	u := SharedUser(t)
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if sharedCC.node != nil {
		return sharedCC.node
	}
	n, err := tg.createCCNode(u)
	if err != nil {
		t.Fatalf("shared CC node: %v", err)
	}
	sharedCC.node = n
	addSharedCleanup(func() error { return tg.deleteCCNode(n) })
	return n
}
