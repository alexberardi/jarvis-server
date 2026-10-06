package mqtt

import (
	"log/slog"
	"strings"
	"sync"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Kind is what a connected client is.
type Kind int

const (
	// Node is a voice node. It is confined to its own topics (see CanSubscribe, CanPublish).
	Node Kind = iota + 1
	// Server is a trusted server-side client (e.g. an out-of-process CC). Unrestricted.
	Server
	// Anonymous is a client without credentials, admitted only with Options.AllowAnonymous.
	// Unrestricted, matching the legacy shared-credential Mosquitto setup.
	Anonymous
)

// Principal is the identity a client authenticated as.
type Principal struct {
	Kind   Kind
	NodeID string // set for Node
}

// Authenticator checks broker credentials (the ones CC issues via /node/mqtt-credentials).
// It must be safe for concurrent use.
type Authenticator interface {
	Authenticate(username, password string) (Principal, bool)
}

// NodeClientIDPrefix is the node's MQTT client ID prefix: nodes connect as
// "jarvis-node-{node_id}" (doc 05 §2.4). A node principal must use exactly that ID, so one
// node can't take over another node's persistent session.
const NodeClientIDPrefix = "jarvis-node-"

// commandDataOps are the command-data ops a node answers (doc 05 §2.4 row 20).
var commandDataOps = map[string]bool{
	"commands": true, "schema": true, "list": true, "get": true,
	"create": true, "update": true, "delete": true,
}

// CanSubscribe reports whether a node may subscribe to filter, or receive a message on
// that topic (D4): only its own subtree jarvis/nodes/{id}/# and jarvis/auth/+/ready.
func CanSubscribe(nodeID, filter string) bool {
	own := "jarvis/nodes/" + nodeID
	if filter == own || strings.HasPrefix(filter, own+"/") {
		return true
	}
	l := strings.Split(filter, "/")
	return len(l) == 4 && l[0] == "jarvis" && l[1] == "auth" && l[2] != "" && l[2] != "#" && l[3] == "ready"
}

// CanPublish reports whether a node may publish to topic (D4): only responses under its own
// subtree, i.e. the two request/response shapes in doc 05 §2.4 rows 20 and 21:
//
//	jarvis/nodes/{id}/command-data/{op}/response/{correlation_id}
//	jarvis/nodes/{id}/context/query/response/{correlation_id}
func CanPublish(nodeID, topic string) bool {
	rest, ok := strings.CutPrefix(topic, "jarvis/nodes/"+nodeID+"/")
	if !ok {
		return false
	}
	l := strings.Split(rest, "/")
	if len(l) != 4 || l[2] != "response" || l[3] == "" || strings.ContainsAny(l[3], "+#") {
		return false
	}
	return (l[0] == "command-data" && commandDataOps[l[1]]) || (l[0] == "context" && l[1] == "query")
}

// validNodeID rejects IDs that would let a node's subtree overlap another's or a wildcard.
func validNodeID(id string) bool {
	return id != "" && !strings.ContainsAny(id, "/+#") && !strings.HasPrefix(id, "$")
}

type principalEntry struct {
	p  Principal
	cl *mochi.Client // the connection that authenticated it
}

// authHook is the mochi hook enforcing authentication and the D4 ACLs. Principals are kept
// per client ID and survive a disconnect for persistent sessions, because mochi ACL-checks
// every delivery, including ones queued for an offline client.
type authHook struct {
	mochi.HookBase
	auth      Authenticator
	allowAnon bool
	log       *slog.Logger

	mu         sync.RWMutex
	principals map[string]principalEntry // client ID -> principal
}

func newAuthHook(auth Authenticator, allowAnon bool, log *slog.Logger) *authHook {
	return &authHook{auth: auth, allowAnon: allowAnon, log: log, principals: map[string]principalEntry{}}
}

func (h *authHook) ID() string { return "jarvis-auth" }

func (h *authHook) Provides(b byte) bool {
	switch b {
	case mochi.OnConnectAuthenticate, mochi.OnACLCheck, mochi.OnPublish, mochi.OnDisconnect, mochi.OnClientExpired:
		return true
	}
	return false
}

func (h *authHook) OnConnectAuthenticate(cl *mochi.Client, pk packets.Packet) bool {
	user := string(pk.Connect.Username)
	var p Principal
	switch {
	case user == "" && len(pk.Connect.Password) == 0:
		if !h.allowAnon {
			h.reject(cl, user, "anonymous connections are disabled")
			return false
		}
		p = Principal{Kind: Anonymous}
	case h.auth == nil:
		h.reject(cl, user, "no authenticator")
		return false
	default:
		var ok bool
		if p, ok = h.auth.Authenticate(user, string(pk.Connect.Password)); !ok {
			h.reject(cl, user, "bad credentials")
			return false
		}
	}
	if p.Kind == Node {
		if !validNodeID(p.NodeID) {
			h.reject(cl, user, "invalid node id")
			return false
		}
		if cl.ID != NodeClientIDPrefix+p.NodeID {
			h.reject(cl, user, "client id does not match node")
			return false
		}
	}
	h.mu.Lock()
	h.principals[cl.ID] = principalEntry{p: p, cl: cl}
	h.mu.Unlock()
	return true
}

func (h *authHook) reject(cl *mochi.Client, user, reason string) {
	h.log.Warn("mqtt connection rejected", "reason", reason, "client", cl.ID, "username", user, "remote", cl.Net.Remote)
}

func (h *authHook) principal(clientID string) (Principal, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	e, ok := h.principals[clientID]
	return e.p, ok
}

func (h *authHook) OnACLCheck(cl *mochi.Client, topic string, write bool) bool {
	if cl.Net.Inline {
		return true
	}
	p, ok := h.principal(cl.ID)
	if !ok {
		return false
	}
	if p.Kind != Node {
		return true
	}
	if write {
		return CanPublish(p.NodeID, topic)
	}
	return CanSubscribe(p.NodeID, topic)
}

// OnPublish strips the retain flag from node publishes: a retained response would be
// replayed to the next Request that reuses its correlation ID.
func (h *authHook) OnPublish(cl *mochi.Client, pk packets.Packet) (packets.Packet, error) {
	if pk.FixedHeader.Retain && !cl.Net.Inline {
		if p, ok := h.principal(cl.ID); ok && p.Kind == Node {
			pk.FixedHeader.Retain = false
		}
	}
	return pk, nil
}

func (h *authHook) OnDisconnect(cl *mochi.Client, _ error, expire bool) {
	if expire {
		h.forget(cl)
	}
}

func (h *authHook) OnClientExpired(cl *mochi.Client) { h.forget(cl) }

// forget drops cl's principal unless a newer connection with the same client ID owns it.
func (h *authHook) forget(cl *mochi.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.principals[cl.ID]; ok && e.cl == cl {
		delete(h.principals, cl.ID)
	}
}
