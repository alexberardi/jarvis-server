package cc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	lldates "github.com/alexberardi/jarvis-server/internal/modules/llm/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// /conversation/start and /conversation/end (docs/cc/01 §3.2).

// warmupTimeout bounds the warmup inference; its failure is never fatal.
const warmupTimeout = 120 * time.Second

// startRequest is ConversationStartRequest. adapter_settings and skip_warmup_inference are
// gone (D47/M4); old nodes that still send them are fine (unknown fields are ignored).
type startRequest struct {
	ConversationID string
	NodeContext    *pyjson.Object // client-supplied; only timezone, agents, recently_shown_items are read
	Commands       []*pyjson.Object
	ClientTools    []*pyjson.Object
	// chatUserID marks a mobile chat warmup (mobile_chat.go): the JWT user is the speaker.
	chatUserID int64
}

// readJSONBody reads a JSON object body twice over: as a validation map (pydantic-shaped 400s)
// and as an ordered pyjson object (key order is prompt bytes for tools). ok=false: written.
func readJSONBody(w http.ResponseWriter, r *http.Request) (*body, *pyjson.Object, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBody))
	if err != nil {
		detail(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return nil, nil, false
	}
	r.Body = io.NopCloser(strings.NewReader(string(raw)))
	b, _, ok := readBody(w, r, false)
	if !ok {
		return nil, nil, false
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		validationError(w, "body: JSON decode error")
		return nil, nil, false
	}
	obj, _ := v.(*pyjson.Object)
	if obj == nil {
		obj = pyjson.NewObject()
	}
	return b, obj, true
}

// objectList validates an optional list of objects (List[Dict] / List[CommandDefinition]) and
// returns the ordered values.
func objectList(b *body, ordered *pyjson.Object, name string) []*pyjson.Object {
	v, present := b.m[name]
	if !present || v == nil {
		return nil
	}
	l, isList := v.([]any)
	if !isList {
		b.fail(name, "Input should be a valid list")
		return nil
	}
	ov, _ := ordered.Get(name)
	ol, _ := ov.([]any)
	out := make([]*pyjson.Object, 0, len(l))
	for i := range l {
		if _, isObj := l[i].(map[string]any); !isObj {
			*b.errs = append(*b.errs, fmt.Sprintf("body -> %s -> %d: Input should be a valid dictionary", name, i))
			continue
		}
		if i < len(ol) {
			if o, ok := ol[i].(*pyjson.Object); ok {
				out = append(out, o)
			}
		}
	}
	return out
}

// validateCommands applies CommandDefinition's required fields.
func validateCommands(b *body, cmds []*pyjson.Object) {
	for i, c := range cmds {
		for _, f := range []string{"command_name", "description"} {
			v, ok := c.Get(f)
			if !ok {
				*b.errs = append(*b.errs, fmt.Sprintf("body -> available_commands -> %d -> %s: Field required", i, f))
			} else if _, isStr := v.(string); !isStr {
				*b.errs = append(*b.errs, fmt.Sprintf("body -> available_commands -> %d -> %s: Input should be a valid string", i, f))
			}
		}
		v, ok := c.Get("parameters")
		if !ok {
			*b.errs = append(*b.errs, fmt.Sprintf("body -> available_commands -> %d -> parameters: Field required", i))
		} else if _, isList := v.([]any); !isList {
			*b.errs = append(*b.errs, fmt.Sprintf("body -> available_commands -> %d -> parameters: Input should be a valid list", i))
		}
	}
}

func parseStartRequest(w http.ResponseWriter, r *http.Request) (startRequest, bool) {
	b, ordered, ok := readJSONBody(w, r)
	if !ok {
		return startRequest{}, false
	}
	var req startRequest
	req.ConversationID, _ = b.str("conversation_id", true)
	if _, ok := b.object("node_context", false); ok {
		v, _ := ordered.Get("node_context")
		req.NodeContext, _ = v.(*pyjson.Object)
	}
	req.Commands = objectList(b, ordered, "available_commands")
	validateCommands(b, req.Commands)
	req.ClientTools = objectList(b, ordered, "client_tools")
	if !b.done(w) {
		return startRequest{}, false
	}
	return req, true
}

// promptProvider resolves llm.prompt_provider. Unknown or unset is a hard error (D11).
func (m *Module) promptProvider(ctx context.Context) (prompts.Provider, error) {
	name := strings.TrimSpace(m.settings.String(ctx, settingPromptProvider, settings.Scope{}))
	if name == "" && m.DefaultPromptProvider != nil {
		name = m.DefaultPromptProvider(ctx)
	}
	return prompts.Lookup(name)
}

// householdBool reads a per-household bool setting.
func (m *Module) householdBool(ctx context.Context, key, hh string) bool {
	return m.settings.Bool(ctx, key, settings.Scope{HouseholdID: hh})
}

func (m *Module) handleConversationStart(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	req, ok := parseStartRequest(w, r)
	if !ok {
		return
	}
	start := m.now()
	tr, ctx := startVoiceTrace(r.Context())
	endWarm := tr.measure("warmup_conversation_with_tools", "cc", nil)
	conv, err := m.warmup(ctx, n, req)
	endWarm(err)
	m.recordVoiceTrace(n, tr, req.ConversationID, "warmup", "", "", start, err)
	if err != nil {
		m.deps.Log.Error("cc: start conversation failed", "conversation_id", req.ConversationID, "err", err)
		detail(w, http.StatusInternalServerError, "Failed to start conversation: "+err.Error())
		return
	}
	var home any
	if conv.homeContext != nil {
		home = conv.homeContext
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "success", "conversation_id": req.ConversationID, "home_context": home,
	})
}

// warmup is ConversationHandler.warmup_conversation_with_tools: tools (gated server tools +
// the node's client tools), command flags, the byte-exact system prompt, the cache entry and
// the warmup inference.
func (m *Module) warmup(ctx context.Context, n *nodeCtx, req startRequest) (*conversation, error) {
	provider, err := m.promptProvider(ctx)
	if err != nil {
		return nil, err
	}
	hh := n.HouseholdID
	conv := &conversation{
		id: req.ConversationID, nodeID: n.ID, householdID: hh,
		memberIDs: append([]int64(nil), n.HouseholdMemberIDs...),
		provider:  provider, forceTools: provider.ForceToolCalls(), chatUserID: req.chatUserID,
	}
	room, voiceMode := prompts.DefaultRoom, prompts.DefaultVoiceMode
	if n.row != nil {
		if n.row.room != "" {
			room = n.row.room
		}
		if n.row.voiceMode.Valid && n.row.voiceMode.String != "" {
			voiceMode = n.row.voiceMode.String
		}
	}
	conv.room = room

	if loc := strings.TrimSpace(m.settings.String(ctx, settingHouseholdLocation, settings.Scope{HouseholdID: hh})); loc != "" {
		conv.homeContext = map[string]any{"location": loc}
	}
	conv.memberNames = m.memberNames(ctx, conv.memberIDs)

	// Client-supplied context: timezone, agents (read-only), recently shown items. The node's
	// speaker_user_id / speaker_confidence are ignored (D2): a conversation starts unknown.
	if nc := req.NodeContext; nc != nil {
		if tz, ok := nc.Get("timezone"); ok {
			conv.timezone, _ = tz.(string)
			if req.chatUserID == 0 { // a phone's zone says nothing about where the node is
				m.recordNodeTimezone(ctx, n.ID, conv.timezone)
			}
		}
		if a, ok := nc.Get("agents"); ok {
			conv.agents, _ = a.(*pyjson.Object)
		}
		if items, ok := nc.Get("recently_shown_items"); ok {
			if l, ok := items.([]any); ok && len(l) > 0 {
				conv.referenced = l
			}
		}
	}

	// Per-household gates: memory fails open, web search closed (01 §7.10). The tool list is
	// fixed at warmup (prefix cache), and no speaker exists yet (D3), so the memory tools are
	// offered when the household can identify speakers at all; they refuse an unknown speaker
	// at execute time (D21).
	recognition := m.STT != nil && m.STT.RecognitionEnabled(ctx, hh)
	conv.recognitionOff = !recognition
	if req.chatUserID != 0 {
		m.setChatSpeaker(ctx, conv) // mobile chat: the speaker is known from the JWT
	}
	// An explicitly set household zone wins over the reported one (timezone.go).
	conv.timezone = m.turnTimezone(ctx, hh, conv.timezone)
	conv.ambient = m.ambientBundle(ctx, hh, conv.timezone)
	gates := prompts.ToolGates{
		WebSearch:     m.householdBool(ctx, settingWebSearch, hh),
		SpeakerKnown:  recognition || conv.chatUserID != 0,
		MemoryEnabled: m.householdBool(ctx, settingMemoryEnabled, hh),
		RecallEnabled: m.householdBool(ctx, settingRecallEnabled, hh),
	}
	persona := parse.PyStrip(m.settings.String(ctx, settingPersona, settings.Scope{HouseholdID: hh}))

	serverNames := m.gateErrandTools(ctx, hh, prompts.ServerToolNames(provider.SupportsNativeTools(), m.tools.Names(), gates))
	serverDefs := m.tools.Definitions(serverNames)
	conv.serverNames = map[string]bool{}
	for _, d := range serverDefs {
		conv.serverNames[toolName(d)] = true
	}
	conv.tools = append(append([]prompts.Tool(nil), serverDefs...), req.ClientTools...)
	conv.commands = mergeCommands(req.ClientTools, req.Commands)

	pctx := prompts.Context{
		Room: room, VoiceMode: voiceMode, HouseholdPersona: persona,
		DateKeys: m.dateVocabulary(), Agents: conv.agents, RoomHierarchy: m.roomHierarchy(ctx, hh),
	}
	system, _ := prompts.AssembleSystemPrompt(provider, pctx, conv.tools, commandFlags(conv.commands), prompts.Characterization{})
	conv.messages = []chatMsg{sysMsg(system)}

	m.convs.put(conv)

	// The warmup inference primes the engine's prefix cache with exactly the bytes the first
	// turn sends (01 §7.1-2). Native: same tools payload, max_tokens=1 (D40 01.Q10).
	if m.LLM != nil {
		wctx, cancel := context.WithTimeout(ctx, warmupTimeout)
		defer cancel()
		one := 1
		zero := 0.0
		creq := llm.ChatRequest{Label: llm.LabelLive, Messages: toLLM(conv.messages), MaxTokens: &one, Temperature: &zero}
		if provider.SupportsNativeTools() {
			creq.Tools = llmTools(prompts.NativeTools(provider, conv.tools))
			creq.ToolChoice = json.RawMessage(`"auto"`)
		}
		creq.ReasoningBudget = m.thinkingBudget(ctx, hh)
		tr := traceFrom(ctx)
		llmStart := tr.since()
		resp, err := m.LLM.Chat(wctx, creq)
		var meta map[string]any
		if err == nil {
			meta = map[string]any{"prompt_tokens": resp.Usage.PromptTokens}
		}
		tr.span("warmup_inference", "llm_proxy", llmStart, tr.since(), err, meta)
		if err != nil {
			m.deps.Log.Warn("cc: warmup inference failed (non-fatal)", "conversation_id", conv.id, "err", err)
		}
	}
	m.deps.Log.Info("cc: conversation started", "conversation_id", conv.id, "node", n.ID,
		"provider", provider.Name(), "server_tools", len(serverDefs), "client_tools", len(req.ClientTools))
	return conv, nil
}

// thinkingBudget maps model.include_thinking onto the live call (D8 01.Q10: per call, never a
// shared provider field): on → unrestricted, off → the label default.
func (m *Module) thinkingBudget(ctx context.Context, hh string) *int {
	if m.householdBool(ctx, settingIncludeThinking, hh) {
		b := -1
		return &b
	}
	return nil
}

func (m *Module) dateVocabulary() []string {
	if m.dateKeys != nil {
		return m.dateKeys
	}
	return lldates.Vocabulary
}

func (m *Module) handleConversationEnd(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	id, _ := b.str("conversation_id", true)
	if !b.done(w) {
		return
	}
	m.convs.evict(id)
	m.signals.drop(id)
	m.deps.Log.Info("cc: conversation ended", "conversation_id", id, "node", n.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "conversation_id": id})
}

// memberNames resolves the household members' display names once per conversation (the
// "addressed to another member" hint). Best effort.
func (m *Module) memberNames(ctx context.Context, ids []int64) []string {
	if m.Names == nil || len(ids) == 0 {
		return nil
	}
	names, err := m.Names.UserNames(ctx, ids)
	if err != nil {
		m.deps.Log.Warn("cc: member names unavailable", "err", err)
		return nil
	}
	var out []string
	for _, id := range ids {
		if n := names[id]; n != "" {
			out = append(out, n)
		}
	}
	return out
}

// userName resolves one user's display name ("" when unknown).
func (m *Module) userName(ctx context.Context, id int64) string {
	if m.Names == nil || id == 0 {
		return ""
	}
	names, err := m.Names.UserNames(ctx, []int64{id})
	if err != nil {
		return ""
	}
	return names[id]
}

// roomHierarchy is the household's rooms, included only when any room has a parent.
func (m *Module) roomHierarchy(ctx context.Context, hh string) []prompts.Room {
	if hh == "" {
		return nil
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT id, name, COALESCE(parent_room_id, '') FROM cc_rooms WHERE household_id = ? ORDER BY rowid`, hh)
	if err != nil {
		m.deps.Log.Warn("cc: room hierarchy unavailable", "err", err)
		return nil
	}
	defer rows.Close()
	var rooms []prompts.Room
	nested := false
	for rows.Next() {
		var rm prompts.Room
		if err := rows.Scan(&rm.ID, &rm.Name, &rm.ParentRoomID); err != nil {
			return nil
		}
		if rm.ParentRoomID != "" {
			nested = true
		}
		rooms = append(rooms, rm)
	}
	if !nested {
		return nil
	}
	return rooms
}

// --- tools and commands ---

func toolName(t *pyjson.Object) string {
	if t == nil {
		return ""
	}
	if fv, ok := t.Get("function"); ok {
		if fn, ok := fv.(*pyjson.Object); ok {
			n, _ := fn.Get("name")
			s, _ := n.(string)
			return s
		}
	}
	n, _ := t.Get("name")
	s, _ := n.(string)
	return s
}

// mergeCommands is warmup_service.build_examples_map + merge_available_commands: one entry
// per client tool with examples (in tool order), then the available commands that had no such
// entry; a command matching an entry contributes its examples and parameters to it.
func mergeCommands(clientTools, commands []*pyjson.Object) []*pyjson.Object {
	byName := map[string]*pyjson.Object{}
	var out []*pyjson.Object
	for _, t := range clientTools {
		name := toolName(t)
		if name == "" {
			continue
		}
		ev, _ := t.Get("examples")
		exs, _ := ev.([]any)
		if len(exs) == 0 {
			continue
		}
		var kept []any
		for _, e := range exs {
			eo, ok := e.(*pyjson.Object)
			if !ok {
				continue
			}
			vc, _ := eo.Get("voice_command")
			if s, _ := vc.(string); s == "" {
				continue
			}
			params, ok := eo.Get("expected_parameters")
			if !ok {
				params = pyjson.NewObject()
			}
			primary, ok := eo.Get("is_primary")
			if !ok {
				primary = false
			}
			kept = append(kept, servertools.Obj("voice_command", vc, "expected_parameters", params, "is_primary", primary))
		}
		if kept == nil {
			kept = []any{}
		}
		kw, _ := t.Get("keywords")
		ap, _ := t.Get("antipatterns")
		ad, _ := t.Get("allow_direct_answer")
		entry := servertools.Obj("command_name", name, "examples", kept, "keywords", kw, "antipatterns", ap, "allow_direct_answer", ad)
		if _, dup := byName[name]; !dup {
			out = append(out, entry)
		} else {
			for i, e := range out {
				if toolName2(e) == name {
					out[i] = entry
				}
			}
		}
		byName[name] = entry
	}
	for _, c := range commands {
		name, _ := c.Get("command_name")
		ns, _ := name.(string)
		if entry, ok := byName[ns]; ok {
			ev, _ := entry.Get("examples")
			exs, _ := ev.([]any)
			if more, ok := c.Get("examples"); ok {
				if l, ok := more.([]any); ok {
					exs = append(exs, l...)
				}
			}
			entry.Set("examples", exs)
			if p, ok := c.Get("parameters"); ok {
				entry.Set("parameters", p)
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

func toolName2(cmd *pyjson.Object) string {
	v, _ := cmd.Get("command_name")
	s, _ := v.(string)
	return s
}

// commandFlags is build_available_command_flags over the merged commands.
func commandFlags(cmds []*pyjson.Object) []prompts.CommandFlag {
	var out []prompts.CommandFlag
	for _, c := range cmds {
		name := toolName2(c)
		if name == "" {
			continue
		}
		v, _ := c.Get("allow_direct_answer")
		allow, _ := v.(bool)
		out = append(out, prompts.CommandFlag{CommandName: name, AllowDirectAnswer: allow})
	}
	return out
}

// llmTools turns ordered tool objects into llm tools whose bytes go upstream unchanged, but
// for the "format": "date-time" marker (withoutDateTimeFormat).
func llmTools(tools []prompts.Tool) []llm.Tool {
	out := make([]llm.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, llm.Tool{Raw: json.RawMessage(prompts.CompactASCII(withoutDateTimeFormat(t))), Type: "function"})
	}
	return out
}

// withoutDateTimeFormat returns a copy of a tool schema without its "format": "date-time"
// keys (A10b). The SDK puts that marker on date parameters and CC reads it, from the cached
// schemas, to know where to resolve date keys; the model is told to write keys ("today",
// "this_weekend"). Sent to llama-server, the marker became a grammar admitting only ISO
// timestamps (Qwen 3.5's XML tool calls constrain every argument to its schema), so the model
// could not write a key and guessed a date instead. Other formats are kept; v is not changed.
func withoutDateTimeFormat(v any) any {
	switch x := v.(type) {
	case *pyjson.Object:
		o := pyjson.NewObject()
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			if k == "format" && val == "date-time" {
				continue
			}
			o.Set(k, withoutDateTimeFormat(val))
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = withoutDateTimeFormat(e)
		}
		return out
	}
	return v
}
