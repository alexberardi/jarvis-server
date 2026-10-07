package cc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	lldates "github.com/alexberardi/jarvis-server/internal/modules/llm/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// One voice turn (ConversationHandler.process_voice_command_with_tools, docs/cc/01 §3.3 C):
// the only path left after D9. Both /voice/command and /voice/command/stream run it.

// errPrecondition is ConversationPreconditionError: no (or an expired) conversation.
var errPrecondition = errors.New("conversation not found or expired")

// wakeVerifyWait is the longest a wake turn waits for the wake-clip verdict (fails open).
const wakeVerifyWait = 1200 * time.Millisecond

// turnInput is VoiceCommandRequest (speaker_user_id is accepted and ignored, D2).
type turnInput struct {
	VoiceCommand      string
	ConversationID    string
	PreWakeSeconds    *float64
	Affect            map[string]any
	Source            string // turn_source ("" = not sent)
	WakeConfidence    *float64
	FollowUpIteration *int
	SelfPlayback      *bool
	SelfPlaybackKind  string
}

// turnState is the per-turn context legacy carried in turn_context.
type turnState struct {
	wakeVerified            *bool
	conversationWakeVerdict string
	doubtRound              *int
	doubtMaxRounds          *int
}

// turnOutcome is a finished turn.
type turnOutcome struct {
	res  engineResult
	conv *conversation
}

// processTurn runs one turn. It returns errPrecondition when the conversation is unknown.
func (m *Module) processTurn(ctx context.Context, n *nodeCtx, in turnInput) (turnOutcome, error) {
	conv := m.convs.get(in.ConversationID)
	if conv == nil {
		return turnOutcome{}, errPrecondition
	}
	conv.mu.Lock()
	defer conv.mu.Unlock()
	m.flushPendingTranscript(ctx, conv)

	// The turn's speaker: only jarvisd's own identification of this conversation's audio
	// (D2/D3). A confident id switches the conversation speaker; otherwise it keeps the one an
	// earlier turn of this conversation established.
	var turnSpeaker int64
	if conv.chatUserID != 0 {
		turnSpeaker = conv.chatUserID // mobile chat: the JWT user, never a voice match (D2)
	} else {
		turnSpeaker = m.applyTurnIdentity(ctx, conv)
	}

	if isSTTNoise(in.VoiceCommand) {
		m.deps.Log.Info("cc: not_for_me_prefilter", "conversation_id", conv.id, "transcript", in.VoiceCommand)
		return turnOutcome{res: engineResult{Stop: stopNotForMe}, conv: conv}, nil
	}

	var st turnState
	if m.resolveWakeVerified(ctx, conv, in, &st) {
		return turnOutcome{res: engineResult{Stop: stopNotForMe}, conv: conv}, nil
	}
	m.resolveFollowupDoubt(ctx, conv, in, &st)

	msgs := stripTransient(cloneMsgs(conv.messages))
	maxTurns := int(m.settings.Int(ctx, settingMaxTurns, settings.Scope{HouseholdID: conv.householdID}))
	msgs = trimHistory(msgs, maxTurns)

	speakerBlock := prompts.SpeakerBlock(conv.speakerNameOrDefault(), conv.memories)
	msgs = append(msgs, transientSys(speakerBlock))
	if block := prompts.AmbientBlock(conv.ambient); block != "" {
		msgs = append(msgs, transientSys(block))
	}
	if block := m.recentlyShownBlock(conv); block != "" {
		msgs = append(msgs, transientSys(block))
	}

	suffix := conv.provider.UserMessageSuffix(m.householdBool(ctx, settingIncludeThinking, conv.householdID))
	hints := []string{
		directionHint(in, turnSpeaker != 0),
		affectHint(in.Affect),
		turnHint(in, st, conv.memberNames),
		profileMatchHint(in.VoiceCommand, speakerBlock),
		m.agentContextHint(ctx, conv, in.VoiceCommand),
	}
	msgs = append(msgs, chatMsg{Role: "user", Content: prompts.UserMessage(in.VoiceCommand, hints, suffix)})

	maxIter := 3
	if conv.provider.SupportsNativeTools() {
		maxIter = 10
	}
	keys := lldates.Extract(in.VoiceCommand)
	conv.dateKeys = keys
	res, out := m.runEngine(ctx, engineInput{
		conv: conv, msgs: msgs, maxIter: maxIter, utterance: in.VoiceCommand, dateKeys: keys,
		doubleCheck: doubleCheckSentinel(in, st), turn: m.toolTurn(conv, in.VoiceCommand),
	})
	if res.Stop == stopServerToolComplete {
		var results []toolResult
		for _, r := range res.ServerResults {
			results = append(results, toolResult{ToolCallID: r.ToolCallID, Output: r.Content})
		}
		res, out = m.formatTextMode(ctx, conv, out, results)
	}
	if parse.ContainsNotForMe(res.Message) {
		m.deps.Log.Info("cc: not_for_me_sentinel", "conversation_id", conv.id, "node_id", conv.nodeID,
			"speaker_user_id", conv.speakerID, "prompt_provider", conv.provider.Name(), "turn_source", in.Source,
			"transcript", in.VoiceCommand)
		res = engineResult{Stop: stopNotForMe}
	}
	if res.Stop != stopError {
		conv.messages = out
	}
	res = applyExchangeComplete(res)
	res = rewriteTerminalFiller(res)
	if res.Stop != stopNotForMe && res.Stop != stopError {
		conv.answeredRounds++
	}
	m.noteTranscript(ctx, conv, in.VoiceCommand, res)
	return turnOutcome{res: res, conv: conv}, nil
}

// speakerNameOrDefault is build_speaker_block's name argument: "default" (unknown) unless a
// speaker is identified and named.
func (c *conversation) speakerNameOrDefault() string {
	if c.speakerID != 0 && c.speakerName != "" {
		return c.speakerName
	}
	return "default"
}

// applyTurnIdentity consumes the turn's identification. It returns the user id identified in
// THIS turn (0 when none), which alone drives speaker_known for the direction hint (06 §7.7).
func (m *Module) applyTurnIdentity(ctx context.Context, conv *conversation) int64 {
	id := m.signals.takeIdentity(conv.id)
	if id == nil {
		return 0
	}
	conv.recognitionOff = id.RecognitionOff
	if id.UserID == 0 || !containsID(conv.memberIDs, id.UserID) {
		return 0
	}
	if id.UserID != conv.speakerID {
		conv.speakerID = id.UserID
		conv.speakerName = m.userName(ctx, id.UserID)
		conv.memories = ""
		if m.Memory != nil && m.householdBool(ctx, settingMemoryEnabled, conv.householdID) {
			conv.memories = m.Memory.ProfileText(ctx, id.UserID, conv.householdID)
		}
	}
	m.noteVoicePresence(ctx, conv.householdID, id.UserID, conv.nodeID, conv.speakerName) // 5c, D40 10.Q7
	m.deps.Log.Info("cc: identity-decision", "conversation_id", conv.id, "turn_speaker", id.UserID,
		"effective", conv.speakerID, "source", "stt", "has_memories", conv.memories != "")
	return id.UserID
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// toolTurn is the context server tools run in (D21: the speaker or its absence).
func (m *Module) toolTurn(conv *conversation, utterance string) servertools.Turn {
	return servertools.Turn{
		ConversationID: conv.id, HouseholdID: conv.householdID, NodeID: conv.nodeID, Timezone: conv.timezone,
		Utterance: utterance, Agents: conv.agents, MemberIDs: conv.memberIDs,
		Speaker: servertools.Speaker{UserID: conv.speakerID, Name: conv.speakerName, RecognitionOff: conv.recognitionOff},
	}
}

// recentlyShownBlock renders the stashed referenceable items.
func (m *Module) recentlyShownBlock(conv *conversation) string {
	if len(conv.referenced) == 0 {
		return ""
	}
	items := make([]*prompts.ReferencedItem, 0, len(conv.referenced))
	for _, it := range conv.referenced {
		o, ok := it.(*pyjson.Object)
		if !ok {
			items = append(items, nil)
			continue
		}
		ri := &prompts.ReferencedItem{}
		if v, ok := o.Get("ref_id"); ok {
			ri.RefID = pyjson.Str(v)
		}
		if v, ok := o.Get("label"); ok {
			ri.Label = pyjson.Str(v)
		}
		if acts, ok := o.Get("actions"); ok {
			if l, ok := acts.([]any); ok {
				for _, a := range l {
					if s, ok := a.(string); ok {
						ri.Actions = append(ri.Actions, s)
					}
				}
			}
		}
		items = append(items, ri)
	}
	return prompts.RecentlyShownBlock(items)
}

// stashReferenced remembers the referenceable_items tool results carried (only when some did).
func stashReferenced(conv *conversation, results []toolResult) {
	var items []any
	for _, tr := range results {
		out := tr.Output
		if s, ok := out.(string); ok {
			v, err := pyjson.Loads(s)
			if err != nil {
				continue
			}
			out = v
		}
		o, ok := out.(*pyjson.Object)
		if !ok {
			continue
		}
		if ri, ok := o.Get("referenceable_items"); ok {
			if l, ok := ri.([]any); ok && len(l) > 0 {
				items = append(items, l...)
			}
		}
	}
	if len(items) > 0 {
		conv.referenced = items
	}
}

// --- wake verification and doubt (01 §3.6) ---

// wakeMode is voice.wake_verification_mode (household + node); unknown values mean off.
func (m *Module) wakeMode(ctx context.Context, hh, node string) string {
	v := strings.ToLower(strings.TrimSpace(m.settings.String(ctx, settingWakeMode, settings.Scope{HouseholdID: hh, NodeID: node})))
	if v == "bias" || v == "enforce" {
		return v
	}
	return "off"
}

// resolveWakeVerified folds the wake-clip verdict into the turn. true = enforce mode wants the
// turn silently dropped.
func (m *Module) resolveWakeVerified(ctx context.Context, conv *conversation, in turnInput, st *turnState) bool {
	if in.Source != "wake" {
		return false
	}
	mode := m.wakeMode(ctx, conv.householdID, conv.nodeID)
	if mode == "off" {
		return false
	}
	v := m.signals.wakeVerdict(ctx, conv.id, wakeVerifyWait)
	if v == nil || v.Verified || v.Verdict != "unverified" {
		return false
	}
	m.deps.Log.Info("cc: wake_unverified", "verdict", v.Verdict, "mode", mode, "node_id", conv.nodeID,
		"conversation_id", conv.id, "clip_transcript", v.Transcript, "command", in.VoiceCommand)
	if mode == "enforce" {
		return true
	}
	f := false
	st.wakeVerified = &f
	return false
}

// resolveFollowupDoubt marks follow-up turns of a conversation whose wake was unverified.
func (m *Module) resolveFollowupDoubt(ctx context.Context, conv *conversation, in turnInput, st *turnState) {
	if in.Source != "follow_up" {
		return
	}
	v := m.signals.storedWake(conv.id)
	if v == nil || v.Verdict != "unverified" {
		return
	}
	st.conversationWakeVerdict = "unverified"
	round := conv.answeredRounds
	st.doubtRound = &round
	max := int(m.settings.Int(ctx, settingDoubtMaxRounds, settings.Scope{HouseholdID: conv.householdID, NodeID: conv.nodeID}))
	if max <= 0 {
		max = 2
	}
	st.doubtMaxRounds = &max
}

// --- result post-processing ---

// applyExchangeComplete is exchange_complete.apply_to_result: strip the marker from the
// message and set end_of_exchange.
func applyExchangeComplete(r engineResult) engineResult {
	if parse.ContainsExchangeComplete(r.Message) {
		r.Message = parse.StripExchangeComplete(r.Message)
		r.EndOfExchange = true
	}
	return r
}

// rewriteTerminalFiller is _rewrite_terminal_filler.
func rewriteTerminalFiller(r engineResult) engineResult {
	switch r.Stop {
	case stopNotForMe, stopToolCalls, stopValidation, stopError, stopServerToolComplete:
		return r
	}
	if isGenericFiller(r.Message) {
		r.Message = fillerReplacement
	}
	return r
}
