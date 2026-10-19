package cc

import (
	"context"
	"encoding/base64"
	"testing"
	"time"
)

func approvalPath(token string) string { return "/api/v0/computer/approvals/" + token + "/verify" }

func (e *env) verifyApproval(n testNode, token, digest string) any {
	e.t.Helper()
	return e.do("POST", approvalPath(token), map[string]any{"digest": digest}, n.h()).want(200).json()["valid"]
}

func (e *env) mint(hh, nodeID, digest string, ttl time.Duration) string {
	e.t.Helper()
	tok, err := e.m.mintApproval(context.Background(), hh, nodeID, "cr_1", digest, ttl)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func TestApprovalSingleUse(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	n := e.createNode("node-1", "hh-1")
	tok := e.mint("hh-1", "node-1", "sha256:abc", 0)
	if raw, err := base64.RawURLEncoding.DecodeString(tok); err != nil || len(raw) != 32 {
		t.Fatalf("token %q: %d bytes, %v", tok, len(raw), err)
	}
	if other := e.mint("hh-1", "node-1", "sha256:abc", 0); other == tok {
		t.Fatal("two mints gave the same token")
	}
	if v := e.verifyApproval(n, tok, "sha256:abc"); v != true {
		t.Fatalf("first verify: %v", v)
	}
	if v := e.verifyApproval(n, tok, "sha256:abc"); v != false {
		t.Fatalf("second verify: %v", v)
	}
	if v := e.verifyApproval(n, "nope", "sha256:abc"); v != false {
		t.Fatalf("unknown token: %v", v)
	}
	// Node auth only; the digest is required.
	e.do("POST", approvalPath(tok), map[string]any{"digest": "sha256:abc"}, nil).want(400)
	e.do("POST", approvalPath(tok), map[string]any{"digest": "sha256:abc"}, testNode{id: "node-1", key: "wrong"}.h()).want(401)
	e.do("POST", approvalPath(tok), map[string]any{}, n.h()).want(400)
	if _, err := e.m.mintApproval(context.Background(), "hh-1", "node-1", "cr_1", "", 0); err == nil {
		t.Fatal("minted without a digest")
	}
}

func TestApprovalWrongDigest(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	n := e.createNode("node-1", "hh-1")
	tok := e.mint("hh-1", "node-1", "sha256:abc", 0)
	if v := e.verifyApproval(n, tok, "sha256:abd"); v != false {
		t.Fatalf("wrong digest: %v", v)
	}
	if v := e.verifyApproval(n, tok, ""); v != false {
		t.Fatalf("empty digest: %v", v)
	}
	// A wrong digest does not use the token up.
	if v := e.verifyApproval(n, tok, "sha256:abc"); v != true {
		t.Fatalf("right digest after a wrong one: %v", v)
	}
}

func TestApprovalOtherNode(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	owner := e.createNode("node-1", "hh-1")
	sibling := e.createNode("node-2", "hh-1")
	stranger := e.createNode("node-3", "hh-2")
	tok := e.mint("hh-1", "node-1", "sha256:abc", 0)
	if v := e.verifyApproval(sibling, tok, "sha256:abc"); v != false {
		t.Fatalf("same household, other node: %v", v)
	}
	if v := e.verifyApproval(stranger, tok, "sha256:abc"); v != false {
		t.Fatalf("other household's node: %v", v)
	}
	// A token minted for the node under another household is not the node's either.
	foreign := e.mint("hh-2", "node-1", "sha256:abc", 0)
	if v := e.verifyApproval(owner, foreign, "sha256:abc"); v != false {
		t.Fatalf("other household's token: %v", v)
	}
	if v := e.verifyApproval(owner, tok, "sha256:abc"); v != true {
		t.Fatalf("owner after the others: %v", v)
	}
}

func TestApprovalExpired(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	n := e.createNode("node-1", "hh-1")
	tok := e.mint("hh-1", "node-1", "sha256:abc", 0) // default: 10 minutes
	short := e.mint("hh-1", "node-1", "sha256:abc", time.Minute)
	e.advance(9*time.Minute + 59*time.Second)
	if v := e.verifyApproval(n, short, "sha256:abc"); v != false {
		t.Fatalf("1-minute token after 9m59s: %v", v)
	}
	e.advance(time.Second)
	if v := e.verifyApproval(n, tok, "sha256:abc"); v != false {
		t.Fatalf("10-minute token after 10m: %v", v)
	}
	fresh := e.mint("hh-1", "node-1", "sha256:abc", 0)
	e.advance(9 * time.Minute)
	if v := e.verifyApproval(n, fresh, "sha256:abc"); v != true {
		t.Fatalf("token within its ttl: %v", v)
	}
}
