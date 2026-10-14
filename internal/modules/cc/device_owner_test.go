package cc

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
)

// Decision 2026-10-14 (desktop voice speaks as its owner): a desktop agent's voice turns act as
// the member signed in to it, with no voice match and no speaker embedding.

// makeDesktop turns the env's node into a desktop agent owned by owner (0: nobody).
func (ve *voiceEnv) makeDesktop(owner int64) {
	ve.t.Helper()
	var o any
	if owner != 0 {
		o = owner
	}
	if _, err := ve.d.Write.Exec(`UPDATE cc_nodes SET node_kind = 'desktop', owner_user_id = ? WHERE node_id = ?`, o, ve.node.id); err != nil {
		ve.t.Fatal(err)
	}
}

// removeMember takes a user out of a household, as auth would after "remove member".
func (a *fakeAuth) removeMember(id int64, household string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.roles[household], id)
}

// askWhoAmI runs a turn whose model calls identify_speaker and returns the tool's answer.
func (ve *voiceEnv) askWhoAmI(cid string) map[string]any {
	ve.t.Helper()
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_w", "type": "function",
		"function": map[string]any{"name": "identify_speaker", "arguments": `{}`}}}})
	ve.eng.say("ok")
	ve.turn("/api/v0/voice/command", cid, "who am i", nil).want(200)
	var tool map[string]any
	for _, m := range messagesOf(ve.eng.last()) {
		if m["role"] == "tool" {
			_ = json.Unmarshal([]byte(m["content"].(string)), &tool)
		}
	}
	if tool == nil {
		ve.t.Fatal("identify_speaker never ran")
	}
	return tool
}

func offeredTools(req map[string]any) []string {
	var out []string
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		f, _ := t.(map[string]any)["function"].(map[string]any)
		out = append(out, f["name"].(string))
	}
	return out
}

func TestDeviceOwner(t *testing.T) {
	desk := func(kind, mode string, owner int64, members ...int64) *nodeCtx {
		row := &nodeRow{nodeID: "n"}
		if kind != "" {
			row.nodeKind = sql.NullString{String: kind, Valid: true}
		}
		if mode != "" {
			row.installMode = sql.NullString{String: mode, Valid: true}
		}
		if owner != 0 {
			row.ownerUserID = sql.NullInt64{Int64: owner, Valid: true}
		}
		return &nodeCtx{ID: "n", HouseholdMemberIDs: members, row: row}
	}
	for _, c := range []struct {
		name string
		n    *nodeCtx
		want int64
	}{
		{"desktop, owner a member", desk("desktop", "", 7, 7, 8), 7},
		{"desktop by install mode", desk("", "desktop-agent", 8, 7, 8), 8},
		{"owner left the household", desk("desktop", "", 7, 8), 0},
		{"no owner (admin key)", desk("desktop", "", 0, 7, 8), 0},
		{"a Pi with an owner", desk("pi", "native", 7, 7), 0},
		{"no node kind yet", desk("", "", 7, 7), 0},
		{"no row", &nodeCtx{HouseholdMemberIDs: []int64{7}}, 0},
		{"nil", nil, 0},
	} {
		if got := deviceOwner(c.n); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

// The owner is the speaker from the first turn: name in the speaker block, identify_speaker
// answers with it, memory tools are offered (recognition is off) and store as the owner, and
// the transcription skips the speaker pass (no embedding).
func TestDesktopOwnerIsTheSpeaker(t *testing.T) {
	me := newMemEnv(t) // recognition off (the default): only the owner makes the speaker known
	me.makeDesktop(7)
	me.start("d1", "[]")
	out := me.transcribe("d1", true)
	if opts := me.stt.opts[len(me.stt.opts)-1]; opts.Speaker != nil {
		t.Fatalf("a desktop owner's command ran the speaker pass: %+v", opts.Speaker)
	}
	// The agent hands the transcription's user id to its own tools (node.Request.UserID), as
	// mobile STT reports the JWT user.
	if sp, _ := out["speaker"].(map[string]any); sp["user_id"] != 7.0 || sp["confidence"] != 1.0 {
		t.Fatalf("transcription speaker %v", out["speaker"])
	}

	if tool := me.askWhoAmI("d1"); tool["speaker_name"] != "alex" {
		t.Fatalf("identify_speaker %v", tool)
	}
	req := me.eng.last()
	if msgs := messagesOf(req); msgs[1]["content"] != "You are speaking with alex." {
		t.Fatalf("speaker block %v", msgs[1])
	}
	if names := strings.Join(offeredTools(req), ","); !strings.Contains(names, "remember") {
		t.Fatalf("memory tools not offered to a known owner: %s", names)
	}

	me.transcribe("d1", true)
	me.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_r", "type": "function",
		"function": map[string]any{"name": "remember", "arguments": `{"content": "parking spot is B12"}`}}}})
	me.eng.say("Got it.")
	me.turn("/api/v0/voice/command", "d1", "remember that my parking spot is B12", nil).want(200)
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE user_id = 7 AND household_id = ? AND content = 'parking spot is B12'`, voiceHH) != 1 {
		t.Fatal("remember did not store as the owner")
	}
	// Transcripts are attributed to the owner, like an identified voice (D19).
	for _, r := range me.transcripts() {
		if r["user_id"] != int64(7) {
			t.Fatalf("transcript %v", r)
		}
	}
}

// The owner wins over a voice match and over a dev speaker hint: the turn never consults them.
func TestDesktopOwnerIgnoresVoiceMatch(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B, func(m *Module) { m.DevSpeakerHints = true })
	ve.stt.recognition = true
	sam := int64(8)
	ve.stt.speaker = &sam
	ve.makeDesktop(7)
	ve.start("d2", "[]")
	ve.transcribe("d2", false)
	ve.eng.say("Hi.")
	ve.turn("/api/v0/voice/command", "d2", "hello", map[string]any{"speaker_user_id": 8}).want(200)
	if msgs := messagesOf(ve.eng.last()); msgs[1]["content"] != "You are speaking with alex." {
		t.Fatalf("speaker block %v", msgs[1])
	}
}

// No owner, or an owner who left the household: an unknown speaker until a voice match, and
// per-user things say to sign in again on this computer.
func TestDesktopOwnerFallsBackToUnknown(t *testing.T) {
	for _, c := range []struct {
		name  string
		owner int64
		setup func(ve *voiceEnv)
	}{
		{"no owner", 0, func(*voiceEnv) {}},
		{"owner removed", 7, func(ve *voiceEnv) { ve.auth.removeMember(7, voiceHH) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
			ve.makeDesktop(c.owner)
			c.setup(ve)
			ve.start("d3", "[]")
			out := ve.transcribe("d3", false)
			if opts := ve.stt.opts[len(ve.stt.opts)-1]; opts.Speaker == nil {
				t.Fatal("without an owner the speaker pass must run")
			}
			if sp, _ := out["speaker"].(map[string]any); sp["user_id"] != nil {
				t.Fatalf("transcription speaker %v", sp)
			}
			// On a computer the fix is signing in again, not a voice profile (and not the
			// recognition setting either).
			tool := ve.askWhoAmI("d3")
			if tool["speaker_name"] != nil || tool["message"] != servertools.SignInAgain {
				t.Fatalf("identify_speaker %v", tool)
			}
			req := ve.eng.last()
			if msgs := messagesOf(req); msgs[1]["content"] != prompts.SignedOutComputerBlock {
				t.Fatalf("speaker block %v", msgs[1])
			}
			// The memory tools are offered (recognition is off) so that they can say so.
			if names := strings.Join(offeredTools(req), ","); !strings.Contains(names, "remember") {
				t.Fatalf("memory tools not offered on a signed-out computer: %s", names)
			}
		})
	}
}

// Removed mid-conversation: the next turn is unknown, never the old owner.
func TestDesktopOwnerRemovedMidConversation(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.makeDesktop(7)
	ve.start("d4", "[]")
	if tool := ve.askWhoAmI("d4"); tool["speaker_name"] != "alex" {
		t.Fatalf("before: %v", tool)
	}
	ve.auth.removeMember(7, voiceHH)
	ve.transcribe("d4", false)
	if opts := ve.stt.opts[len(ve.stt.opts)-1]; opts.Speaker == nil {
		t.Fatal("a removed owner's node must run the speaker pass again")
	}
	if tool := ve.askWhoAmI("d4"); tool["speaker_name"] != nil {
		t.Fatalf("after removal: %v", tool)
	}
	if conv := ve.m.convs.get("d4"); conv.speakerID != 0 || conv.ownerUserID != 0 || conv.memories != "" {
		t.Fatalf("conversation kept the owner: %+v", conv.speakerID)
	}
	if tool := ve.askWhoAmI("d4"); tool["message"] != servertools.SignInAgain {
		t.Fatalf("after removal the refusal says %v", tool["message"])
	}
}

// remember on a signed-out computer refuses with the sign-in sentence and stores nothing; on a
// Pi the same unknown speaker keeps the D21 / M14 wording.
func TestSignedOutComputerRefusals(t *testing.T) {
	remember := func(me *memEnv, cid string) map[string]any {
		t.Helper()
		me.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_r", "type": "function",
			"function": map[string]any{"name": "remember", "arguments": `{"content": "parking spot is B12"}`}}}})
		me.eng.say("ok")
		me.turn("/api/v0/voice/command", cid, "remember that my parking spot is B12", nil).want(200)
		var tool map[string]any
		for _, m := range messagesOf(me.eng.last()) {
			if m["role"] == "tool" {
				_ = json.Unmarshal([]byte(m["content"].(string)), &tool)
			}
		}
		return tool
	}

	me := newMemEnv(t)
	me.stt.recognition = true // a Pi offers memory only when the household can recognise voices
	me.start("pi", "[]")
	me.transcribe("pi", false)
	if tool := remember(me, "pi"); tool["message"] != "I'm not sure who's speaking." {
		t.Fatalf("pi: %v", tool)
	}
	if msgs := messagesOf(me.eng.last()); msgs[1]["content"] != prompts.UnknownSpeakerBlock {
		t.Fatalf("pi speaker block %v", msgs[1])
	}

	me.makeDesktop(0)
	me.start("desk", "[]")
	me.transcribe("desk", false)
	if tool := remember(me, "desk"); tool["error"] != "no_speaker" || tool["message"] != servertools.SignInAgain {
		t.Fatalf("desktop: %v", tool)
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE household_id = ?`, voiceHH) != 0 {
		t.Fatal("a refused remember stored something")
	}
}

// Mobile (and web) chat keep their JWT user even when the target node is someone else's
// desktop agent.
func TestChatToSomeonesDesktopKeepsTheJWTUser(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	ce.makeDesktop(8) // sam's computer
	cid := ce.warm("tok-7")
	conv := ce.m.convs.get(cid)
	if conv.speakerID != 7 || conv.speakerName != "alex" || conv.ownerUserID != 0 {
		t.Fatalf("chat speaker %d %q owner %d", conv.speakerID, conv.speakerName, conv.ownerUserID)
	}
	ce.eng.say("Hi Alex.")
	ce.chat("tok-7", map[string]any{"message": "hello", "conversation_id": cid})
	if msgs := messagesOf(ce.eng.last()); msgs[1]["content"] != "You are speaking with alex." {
		t.Fatalf("speaker block %v", msgs[1])
	}
}
