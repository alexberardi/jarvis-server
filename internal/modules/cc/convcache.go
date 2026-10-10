package cc

import (
	"context"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// The conversation cache (docs/cc/01 §4, §11). Legacy kept a process-global dict with an
// absolute 10-minute TTL, no sweeper and no eviction on /conversation/end. Go (D40 01.Q2):
// a sliding idle TTL, a sweeper, eviction on /conversation/end and an entry cap. Each
// conversation has its own mutex, so turns of one conversation are serialized; the engine
// works on a copy of the history and commits it back only on success (fixes §8.3).
//
// Speaker identity is conversation state (D2/D3): it starts empty, is set by turns jarvisd
// identified itself, and dies with the conversation. Nothing is keyed per node.

const (
	convIdleTTL  = 10 * time.Minute
	convMaxCount = 512
	convSweep    = time.Minute
)

// chatMsg is one history message. transient marks the per-turn trailing system blocks
// (speaker, ambient, recently shown, stream override), which are stripped and rebuilt every
// turn: marked structurally instead of by content prefix (D8, 03.Q6).
type chatMsg struct {
	Role       string
	Content    string
	ToolCalls  []parse.ToolCall
	ToolCallID string
	Name       string
	transient  bool

	// id is the message's identity in its conversation (0 until committed; conversation.commit
	// assigns it). Background jobs (image descriptions, compaction) find and replace messages by
	// it, so they never clobber history a newer turn wrote (chat_images.go, compaction.go).
	id uint64
	// images are a chat turn's attached images (chat_images.go): sent to the live slot as
	// image_url parts until the description job replaces them with text (tools keep the bytes
	// through conversation.photos). Memory only: never written to disk, traces or logs.
	images []chatImage
	// summary marks the compaction summary message (compaction.go).
	summary bool
}

func sysMsg(s string) chatMsg       { return chatMsg{Role: "system", Content: s} }
func transientSys(s string) chatMsg { return chatMsg{Role: "system", Content: s, transient: true} }

// toLLM converts the history for the llm module (tool history forwarded as is).
func toLLM(msgs []chatMsg) []llm.Message {
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		lm := llm.Message{Role: m.Role, Content: llm.TextContent(m.Content), ToolCallID: m.ToolCallID, Name: m.Name}
		if len(m.images) > 0 {
			lm.Content = imageContent(m.images, m.Content)
		}
		for _, tc := range m.ToolCalls {
			lm.ToolCalls = append(lm.ToolCalls, llm.ToolCall{ID: tc.ID, Type: "function",
				Function: llm.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments}})
		}
		out = append(out, lm)
	}
	return out
}

// issuedCall is one client tool call handed to the node (dedupe, 02 §3.2 j.8).
type issuedCall struct {
	name, argsHash string
	at             time.Time
}

// wakeVerdict is the wake-clip verification result (01 §3.6), computed in process.
type wakeVerdict struct {
	Verified   bool
	Verdict    string // verified | unverified | clip_unreliable
	Transcript string
	Similarity float64
}

// conversation is one cached voice conversation.
type conversation struct {
	mu sync.Mutex // serializes turns

	id          string
	nodeID      string
	householdID string
	room        string
	memberIDs   []int64
	memberNames []string
	timezone    string
	agents      *pyjson.Object
	homeContext map[string]any

	provider prompts.Provider
	// messages[0] is the byte-stable system prompt.
	messages []chatMsg
	// tools are the offered tools (gated server tools, then the node's client tools), as raw
	// ordered objects; serverNames are the offered server-tool names (explicit plane routing).
	tools       []prompts.Tool
	serverNames map[string]bool
	commands    []*pyjson.Object // merged available commands (examples, flags, param types)
	forceTools  bool

	referenced []any // the RECENTLY SHOWN items (raw)

	// chatUserID is the mobile chat owner (13 §3.1): the JWT user, who is the speaker of every
	// turn. 0 for a node voice conversation.
	chatUserID int64
	// ambient is the household's situational bundle, frozen at warmup (03 §3.3 item 2); ""
	// when ambient_context.enabled or memory.enabled is off.
	ambient string

	// Speaker (D3): set by identified turns of this conversation only (chat: the owner).
	speakerID      int64
	speakerName    string
	memories       string
	recognitionOff bool

	answeredRounds int
	issued         []issuedCall
	dateKeys       []string // the last turn's extracted date keys (native continue reuses them)

	pendingTranscript *pendingTranscript

	// nextMsgID numbers committed messages (chatMsg.id).
	nextMsgID uint64
	// rev counts background edits of messages (descriptions, compaction): a commit prepared
	// before one (the continue stream) is rebased onto it (rebaseCommit).
	rev uint64
	// promptTokens is the last live call's prompt size (usage.prompt_tokens), 0 when unknown or
	// since a compaction: the compaction trigger (compaction.go).
	promptTokens int
	// photos are the latest photo message's bytes, kept for tools after its description lands
	// (chat_image_actions.go, CI8). Memory only; gone with the conversation.
	photos *keptPhotos
	// imageParams are the image parameters of every photo tool the warmup saw, offered or
	// hidden (chat_image_actions.go, §9): a client call to one gets the photos instead of the
	// model's numbers, or a refusal and never reaches the node. photoToolOffered: one of them
	// is in the offered tools.
	imageParams      map[string][]servertools.ImageParam
	photoToolOffered bool

	lastUsed time.Time
}

// commit makes msgs the conversation's history, numbering messages that have no id yet.
// Callers hold conv.mu.
func (c *conversation) commit(msgs []chatMsg) {
	for i := range msgs {
		if msgs[i].id == 0 {
			c.nextMsgID++
			msgs[i].id = c.nextMsgID
		}
	}
	c.messages = msgs
	c.keepPhotos(msgs)
}

// newMsgID numbers a message before it is committed (a chat turn's image message, so the
// description job can find it). Callers hold conv.mu.
func (c *conversation) newMsgID() uint64 {
	c.nextMsgID++
	return c.nextMsgID
}

// rebaseCommit applies background edits made since a commit was prepared: commit is base plus
// changes (dropped messages, new id-0 messages at the end); current is base after background
// edits (messages replaced in place by id, older ones swapped for a summary). The result keeps
// current's version of every message commit still has, current's new messages (a summary), and
// commit's new messages.
func rebaseCommit(base, current, commit []chatMsg) []chatMsg {
	inBase := map[uint64]bool{}
	for _, m := range base {
		inBase[m.id] = true
	}
	inCommit := map[uint64]bool{}
	for _, m := range commit {
		if m.id != 0 {
			inCommit[m.id] = true
		}
	}
	out := make([]chatMsg, 0, len(commit)+1)
	for _, m := range current {
		if inCommit[m.id] || !inBase[m.id] {
			out = append(out, m)
		}
	}
	for _, m := range commit {
		if m.id == 0 {
			out = append(out, m)
		}
	}
	return out
}

// convCache is the conversation store.
type convCache struct {
	mu    sync.Mutex
	m     map[string]*conversation
	now   func() time.Time
	ttl   time.Duration
	limit int
}

func newConvCache(now func() time.Time) *convCache {
	if now == nil {
		now = time.Now
	}
	return &convCache{m: map[string]*conversation{}, now: now, ttl: convIdleTTL, limit: convMaxCount}
}

// put stores a conversation (replacing one with the same id), evicting the least recently
// used entries beyond the cap.
func (c *convCache) put(conv *conversation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conv.lastUsed = c.now()
	c.m[conv.id] = conv
	for len(c.m) > c.limit {
		var oldest *conversation
		for _, e := range c.m {
			if oldest == nil || e.lastUsed.Before(oldest.lastUsed) {
				oldest = e
			}
		}
		delete(c.m, oldest.id)
	}
}

// peek returns a live conversation without sliding its idle TTL (background jobs must not
// keep a conversation alive).
func (c *convCache) peek(id string) *conversation {
	c.mu.Lock()
	defer c.mu.Unlock()
	conv := c.m[id]
	if conv == nil || c.now().Sub(conv.lastUsed) > c.ttl {
		return nil
	}
	return conv
}

// get returns a live conversation and slides its idle TTL; expired entries are dropped.
func (c *convCache) get(id string) *conversation {
	c.mu.Lock()
	defer c.mu.Unlock()
	conv := c.m[id]
	if conv == nil {
		return nil
	}
	now := c.now()
	if now.Sub(conv.lastUsed) > c.ttl {
		delete(c.m, id)
		return nil
	}
	conv.lastUsed = now
	return conv
}

// evict removes a conversation (/conversation/end).
func (c *convCache) evict(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[id]
	delete(c.m, id)
	return ok
}

// sweep drops idle conversations; it returns how many went.
func (c *convCache) sweep() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	n := 0
	for id, conv := range c.m {
		if now.Sub(conv.lastUsed) > c.ttl {
			delete(c.m, id)
			n++
		}
	}
	return n
}

// purgeUser forgets a deleted user's identity in every live conversation (D20/M15).
func (c *convCache) purgeUser(userID int64) {
	c.mu.Lock()
	convs := make([]*conversation, 0, len(c.m))
	for _, conv := range c.m {
		convs = append(convs, conv)
	}
	c.mu.Unlock()
	for _, conv := range convs {
		conv.mu.Lock()
		if conv.speakerID == userID {
			conv.speakerID, conv.speakerName, conv.memories = 0, "", ""
		}
		conv.mu.Unlock()
	}
}

func (c *convCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// runSweeper sweeps until ctx ends.
func (c *convCache) runSweeper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.sweep()
		}
	}
}

// --- history helpers ---

// stripTransient drops the per-turn trailing system blocks.
func stripTransient(msgs []chatMsg) []chatMsg {
	out := msgs[:0:0]
	for _, m := range msgs {
		if !m.transient {
			out = append(out, m)
		}
	}
	return out
}

// expectsContinuation: a tool result, or an assistant message that issued tool calls,
// leaves its exchange open (conversation_cache._expects_continuation).
func expectsContinuation(m chatMsg) bool {
	return m.Role == "tool" || (m.Role == "assistant" && len(m.ToolCalls) > 0)
}

// trimHistory is trim_history_to_max_turns: keep the leading system prefix and the newest
// maxTurns exchanges, dropping whole turns from the front. maxTurns <= 0 disables it.
func trimHistory(msgs []chatMsg, maxTurns int) []chatMsg {
	if maxTurns <= 0 {
		return msgs
	}
	prefix := 0
	for prefix < len(msgs) && msgs[prefix].Role == "system" {
		prefix++
	}
	var turns [][]chatMsg
	for _, m := range msgs[prefix:] {
		switch {
		case m.Role == "user" && (len(turns) == 0 || !expectsContinuation(turns[len(turns)-1][len(turns[len(turns)-1])-1])):
			turns = append(turns, []chatMsg{m})
		case len(turns) > 0:
			turns[len(turns)-1] = append(turns[len(turns)-1], m)
		default:
			turns = append(turns, []chatMsg{m})
		}
	}
	if len(turns) <= maxTurns {
		return msgs
	}
	out := append([]chatMsg(nil), msgs[:prefix]...)
	for _, t := range turns[len(turns)-maxTurns:] {
		out = append(out, t...)
	}
	return out
}

// withoutRole drops messages of a role (the text path drops role=tool).
func withoutRole(msgs []chatMsg, role string) []chatMsg {
	out := make([]chatMsg, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != role {
			out = append(out, m)
		}
	}
	return out
}

// lastUserContent is the content of the last user message ("" when none).
func lastUserContent(msgs []chatMsg) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

func cloneMsgs(msgs []chatMsg) []chatMsg { return append([]chatMsg(nil), msgs...) }
