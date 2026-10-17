package cc

import (
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// TestDesktopChatAndActionsOwnerOrAdmin: chat (warmup and chat) and /nodes/{id}/actions on a
// desktop agent are the owner's (while still a member) or a household admin's; an ownerless
// desktop is admin only. A Pi stays open to every member of its household.
func TestDesktopChatAndActionsOwnerOrAdmin(t *testing.T) {
	old := actionWait
	actionWait = 20 * time.Millisecond
	t.Cleanup(func() { actionWait = old })
	ce := newChatEnv(t, prompts.Qwen3_8B)
	// tok-7 and tok-8 are members of voiceHH (newVoiceEnv).
	admin := ce.auth.addUser(20, voiceHH, authn.RoleAdmin)
	power := ce.auth.addUser(21, voiceHH, authn.RolePowerUser)
	set := func(kind string, owner any) {
		t.Helper()
		if _, err := ce.d.Write.Exec(`UPDATE cc_nodes SET node_kind = ?, owner_user_id = ? WHERE node_id = ?`, kind, owner, ce.node.id); err != nil {
			t.Fatal(err)
		}
	}
	warmup := func(tok string) *resp {
		return ce.do("POST", "/api/v0/mobile/chat/warmup", map[string]any{"node_id": ce.node.id, "household_id": voiceHH}, bearer(tok))
	}
	chat := func(tok string) *resp {
		return ce.do("POST", "/api/v0/mobile/chat", map[string]any{"message": "hi", "node_id": ce.node.id, "household_id": voiceHH}, bearer(tok))
	}
	action := func(tok string) *resp {
		return ce.do("POST", "/api/v0/nodes/"+ce.node.id+"/actions", map[string]any{"command_name": "cmd", "action_name": "x"}, bearer(tok))
	}
	allowed := func(tok string) {
		t.Helper()
		warmup(tok).want(200)
		action(tok).want(200)
	}
	denied := func(tok string) {
		t.Helper()
		warmup(tok).detail(403, errDesktopChatRole)
		chat(tok).detail(403, errDesktopChatRole)
		action(tok).detail(403, errDesktopActionRole)
	}

	// Desktop owned by member 7.
	set("desktop", 7)
	allowed("tok-7")
	ce.chat("tok-7", map[string]any{"message": "hi"})
	denied("tok-8")
	denied(power)
	allowed(admin)

	// Ownerless desktop (admin key, or paired before owners were recorded): admin only.
	set("desktop", nil)
	denied("tok-7")
	allowed(admin)

	// The owner left the household: no longer a member, so the household check answers.
	set("desktop", 30)
	ce.auth.addUser(30, "hh-elsewhere", authn.RoleMember)
	warmup("tok-30").detail(403, "User is not a member of this household")
	action("tok-30").detail(403, "User is not a member of this household")
	denied("tok-8")
	allowed(admin)

	// A Pi owned by member 7 stays open to member 8.
	set("pi", 7)
	allowed("tok-8")
}
