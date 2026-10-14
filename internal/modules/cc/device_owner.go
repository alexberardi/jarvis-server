package cc

import "context"

// A desktop agent's voice turns act as the member signed in to it (decision
// docs/decisions/2026-10-14-desktop-voice-speaks-as-its-owner.md), the way mobile chat acts as
// the JWT user: no voice match, so no speaker embedding either. Anyone talking to that computer
// is treated as its signed-in user, which is what a personal computer means; a household that
// wants otherwise signs the agent out.

// outcomeDeviceOwner is the (internal) speaker outcome of a desktop owner's command: not
// from the speaker pass (which did not run), so a voice turn never reads it as a match, but its
// user_id tells the agent who its own tools act for, as mobile STT reports the JWT user.
const outcomeDeviceOwner = "device_owner"

// deviceOwner is the member a node's voice turns act as without a voice match: a desktop
// agent's owner_user_id (who paired it or signed in on it, migration 00180) while that member
// is still in the node's household. 0 for a Pi, an agent nobody owns (made with the admin key)
// or an owner who left the household: those keep voice recognition, unknown until matched.
// Never a guess: the members come from auth on every node request.
func deviceOwner(n *nodeCtx) int64 {
	if n == nil || n.row == nil || !n.row.desktop() || !n.row.ownerUserID.Valid {
		return 0
	}
	id := n.row.ownerUserID.Int64
	if id <= 0 || !containsID(n.HouseholdMemberIDs, id) {
		return 0
	}
	return id
}

// signedOutComputer reports a desktop agent's voice conversation with nobody usable signed in
// (no owner, or one who left the household) and no speaker identified otherwise: per-user
// things refuse with "sign in again" (servertools.SignInAgain), not a voice-profile hint.
func (c *conversation) signedOutComputer() bool {
	return c.desktopVoice && c.ownerUserID == 0 && c.speakerID == 0
}

// setKnownSpeaker makes a user the conversation's speaker without a voice match (mobile chat's
// JWT user, a desktop agent's owner): name (display name, else username), User Profile
// memories when memory is on, and no "recognition is off" refusals.
func (m *Module) setKnownSpeaker(ctx context.Context, conv *conversation, userID int64) {
	conv.speakerID = userID
	conv.speakerName = m.userName(ctx, userID)
	conv.recognitionOff = false
	conv.memories = ""
	if m.Memory != nil && m.householdBool(ctx, settingMemoryEnabled, conv.householdID) {
		conv.memories = m.Memory.ProfileText(ctx, userID, conv.householdID)
	}
}

// ownerTurnSpeaker is a desktop owner conversation's turn speaker. The owner is re-checked
// against this request's node row and members: one removed from the household (or a node
// whose owner changed) mid-conversation drops the conversation back to voice recognition,
// unknown until matched, never to a guess.
func (m *Module) ownerTurnSpeaker(ctx context.Context, conv *conversation, n *nodeCtx) int64 {
	if n != nil && deviceOwner(n) != conv.ownerUserID {
		m.deps.Log.Info("cc: desktop owner no longer applies; speaker unknown", "conversation_id", conv.id,
			"node_id", conv.nodeID, "owner", conv.ownerUserID)
		conv.ownerUserID = 0
		conv.speakerID, conv.speakerName, conv.memories = 0, "", ""
		conv.recognitionOff = !(m.STT != nil && m.STT.RecognitionEnabled(ctx, conv.householdID))
		return m.applyTurnIdentity(ctx, conv)
	}
	// The transcription skipped the speaker pass; drop what it recorded (an outcome, no id).
	m.signals.takeIdentity(conv.id)
	m.deps.Log.Info("cc: identity-decision", "conversation_id", conv.id, "turn_speaker", conv.ownerUserID,
		"effective", conv.speakerID, "source", "desktop_owner", "has_memories", conv.memories != "")
	return conv.ownerUserID
}
