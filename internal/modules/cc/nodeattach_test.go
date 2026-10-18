package cc

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// The node side of inbox attachments (docs/decisions/2026-10-17-inbox-attachments.md): a node
// uploads a file, then names it in /node/inbox-item. The uploads are the node's, in its household.
func TestNodeInboxAttachments(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	h := ve.node.h()
	up := func(ct, body string, want int) map[string]any {
		t.Helper()
		hh := hdr{"Content-Type": ct}
		for k, v := range h {
			hh[k] = v
		}
		return ve.do("POST", "/api/v0/node/inbox-attachment", body, hh).want(want).json()
	}
	a := up("image/png", "PNG-BYTES", 201)
	if a["id"] != "att-1" {
		t.Fatalf("%v", a)
	}
	if u := ve.notify.uploads[0]; u.owner != "node:"+ve.node.id || u.household != voiceHH || u.contentType != "image/png" || u.body != "PNG-BYTES" {
		t.Fatalf("%+v", u)
	}
	if r := up("text/plain", "bad", 415); r["detail"] == nil {
		t.Fatalf("%v", r)
	}
	ve.do("POST", "/api/v0/node/inbox-attachment", "x", nil).want(400) // no node key: the legacy 400

	r := ve.do("POST", "/api/v0/node/inbox-item", map[string]any{"title": "A picture", "attachment_ids": []string{"att-1"}}, h).want(200).json()
	if r["sent"] != true {
		t.Fatalf("%v", r)
	}
	it := ve.notify.items[len(ve.notify.items)-1]
	if len(it.AttachmentIDs) != 1 || it.AttachmentIDs[0] != "att-1" || it.AttachmentOwner != "node:"+ve.node.id {
		t.Fatalf("%+v", it)
	}
	// A refused claim is the caller's error, not a silent "sent": false.
	ve.do("POST", "/api/v0/node/inbox-item", map[string]any{"title": "x", "attachment_ids": []string{"bad"}}, h).want(422)
	ve.do("POST", "/api/v0/node/inbox-item", map[string]any{"title": "x", "attachment_ids": "att-1"}, h).want(400)
}
