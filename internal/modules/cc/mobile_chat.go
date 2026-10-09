package cc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Mobile chat (docs/cc/13 §3.1-3.2; legacy api/mobile_chat.py): the app and jarvis-web type
// to the same brain a voice node talks to. A chat conversation is warmed like a voice one
// (warmup.go) for the SELECTED node — its tools, room and voice mode — and every turn runs
// processTurn with turn_source "chat"; the node's client tools run headlessly on that node
// over MQTT (tool_call, answered on /device-control-results/{rid}) and the results continue
// the conversation (continueBlocking). The speaker is the authenticated user (JWT), never a
// voice match.
//
// Kept exactly (D32): the answer is replayed word by word as `delta` events with a 20 ms
// pause per word; the intermediate "Let me check…" delta vanishes when `done.full_text`
// replaces the bubble (D40 Q5). Changed: the warmup's report_tools fetch is skipped for an
// offline node (Q7), and node tools fail fast with a `status` event on an offline node (Q8).
// Each SSE event is one write followed by one flush (Q12): both clients drop a `data:` line
// split across network chunks.

const (
	chatMaxToolIterations = 5 // MAX_TOOL_ITERATIONS
	chatDefaultTimezone   = "America/New_York"
	chatMessageMax        = 5000
)

// chatWordPause is the fake-streaming pause per word (D32; a var for tests).
var chatWordPause = 20 * time.Millisecond

// chatToolWait bounds one headless node tool call (dispatch_node_command's 10 s; a var for
// tests).
var chatToolWait = 10 * time.Second

func (m *Module) registerMobileChat(mux *http.ServeMux) {
	const v0 = "/api/v0"
	mux.HandleFunc("POST "+v0+"/mobile/chat", m.user(m.handleMobileChat))
	mux.HandleFunc("POST "+v0+"/mobile/chat/warmup", m.user(m.handleMobileChatWarmup))
	mux.HandleFunc("GET "+v0+"/mobile/chat/capabilities", m.user(m.handleChatCapabilities))
}

// chatRequest is MobileChatRequest / WarmupRequest.
type chatRequest struct {
	Message          string
	NodeID           string
	HouseholdID      string
	ConversationID   string
	Timezone         string
	ClientTools      []*pyjson.Object
	Commands         []*pyjson.Object
	IncludeReasoning bool
	// rawImages is the request's images list, validated after the node gate
	// (chat_images.go); Images is the result.
	rawImages []any
	Images    []chatImage
}

// parseChatRequest reads the body; warmup has no message / conversation_id /
// include_reasoning.
func parseChatRequest(w http.ResponseWriter, r *http.Request, warmup bool) (chatRequest, bool) {
	limit := int64(httpx.MaxBody)
	if !warmup {
		limit = chatBodyMax // images (§6)
	}
	b, ordered, ok := readJSONBodyLimit(w, r, limit)
	if !ok {
		return chatRequest{}, false
	}
	var req chatRequest
	if !warmup {
		// Images, when present, make the message optional and allow it empty (§6). Their content
		// is checked after the node gate (422 images_invalid / images_unavailable).
		if v, present := b.m["images"]; present && v != nil {
			l, isList := v.([]any)
			if !isList {
				b.fail("images", "Input should be a valid list")
			}
			req.rawImages = l
		}
		withImages := len(req.rawImages) > 0
		if s, ok := b.str("message", !withImages); ok {
			minLen := 1
			if withImages {
				minLen = 0
			}
			b.strLen("message", s, minLen, chatMessageMax)
			req.Message = s
		}
	}
	req.NodeID, _ = b.str("node_id", true)
	req.HouseholdID, _ = b.str("household_id", true)
	req.Timezone = chatDefaultTimezone
	if v, present := b.m["timezone"]; present {
		if s, isStr := v.(string); isStr {
			req.Timezone = s
		} else {
			b.fail("timezone", "Input should be a valid string")
		}
	}
	req.ClientTools = objectList(b, ordered, "client_tools")
	req.Commands = objectList(b, ordered, "available_commands")
	if !warmup {
		if p, ok := b.optStrPtr("conversation_id"); ok && p != nil {
			req.ConversationID = *p
		}
		req.IncludeReasoning, _ = b.boolean("include_reasoning")
	}
	if !b.done(w) {
		return chatRequest{}, false
	}
	return req, true
}

// chatNode is the pre-stream gate: household membership (403), then the node must belong to
// that household (404, legacy _validate_node_in_household). Cross-household is blocked by
// both: a user can only chat to nodes of a household they are a member of.
func (m *Module) chatNode(ctx context.Context, u authn.User, req chatRequest) (*nodeRow, error) {
	if err := m.requireRole(ctx, u.ID, req.HouseholdID, authn.RoleMember); err != nil {
		return nil, err
	}
	row, err := m.nodeByID(ctx, req.NodeID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil || !row.householdID.Valid || row.householdID.String != req.HouseholdID {
		return nil, m.chatNodeNotFound(ctx, req)
	}
	return row, nil
}

// chatNodeNotFound is legacy's 404. Chat needs a node, in legacy too (its tools, room and
// speaker context; the app disables the input until one is picked), so a fresh install with
// none can't chat (A10 F16): for a household with no nodes the detail says what to do.
func (m *Module) chatNodeNotFound(ctx context.Context, req chatRequest) error {
	msg := fmt.Sprintf("Node %s not found in household %s", req.NodeID, req.HouseholdID)
	var n int
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_nodes WHERE household_id = ?`, req.HouseholdID).Scan(&n)
	if err == nil && n == 0 {
		msg += ": this household has no Jarvis node yet. Chat runs through a node (its commands, room and " +
			"speaker), so add one first (in the mobile app: Nodes, Add a node), then pick it for chat."
	}
	return fail(http.StatusNotFound, msg)
}

func newMobileConversationID() string { return "mobile-" + randHex(6) }

func (m *Module) handleMobileChatWarmup(w http.ResponseWriter, r *http.Request, u authn.User) {
	req, ok := parseChatRequest(w, r, true)
	if !ok {
		return
	}
	ctx := r.Context()
	row, err := m.chatNode(ctx, u, req)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if m.LLM == nil {
		detail(w, http.StatusServiceUnavailable, "Voice pipeline unavailable")
		return
	}
	cid := newMobileConversationID()
	conv, err := m.chatWarmup(ctx, u, row, req, cid)
	if err != nil {
		m.deps.Log.Error("cc: mobile chat warmup failed", "conversation_id", cid, "err", err)
		m.internalError(w, err)
		return
	}
	loaded := len(conv.tools) - len(conv.serverNames) // client tools only
	m.deps.Log.Info("cc: mobile warmup complete", "conversation_id", cid, "tools", loaded)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"conversation_id": cid, "tools_loaded": loaded})
}

// chatWarmup is _do_warmup: the node context (room, voice mode, devices, room hierarchy), the
// tools (the node's own report first — only when it is online, Q7 — then what the client
// sent), then the shared voice warmup with the user as the known speaker.
func (m *Module) chatWarmup(ctx context.Context, u authn.User, row *nodeRow, req chatRequest, cid string) (*conversation, error) {
	nc := servertools.Obj("timezone", req.Timezone)
	if agents := m.chatDeviceAgents(ctx, req.HouseholdID); agents != nil {
		nc.Set("agents", agents)
	}
	clientTools, commands := req.ClientTools, req.Commands
	if row.reachable(m.now()) {
		if ct, ac, ok := m.fetchNodeTools(ctx, row.nodeID); ok {
			clientTools, commands = ct, ac
		}
	} else {
		m.deps.Log.Info("cc: mobile chat node offline; using the client's tools", "node", row.nodeID)
	}
	n := &nodeCtx{ID: row.nodeID, HouseholdID: req.HouseholdID, HouseholdMemberIDs: []int64{u.ID}, row: row}
	return m.warmup(ctx, n, startRequest{ConversationID: cid, NodeContext: nc, Commands: commands,
		ClientTools: clientTools, chatUserID: u.ID})
}

// setChatSpeaker makes the chat owner the conversation's speaker: name, User Profile memories
// (when memory is on) and no "recognition is off" refusals — the JWT identifies them.
func (m *Module) setChatSpeaker(ctx context.Context, conv *conversation) {
	conv.speakerID = conv.chatUserID
	conv.speakerName = m.userName(ctx, conv.chatUserID)
	conv.recognitionOff = false
	conv.memories = ""
	if m.Memory != nil && m.householdBool(ctx, settingMemoryEnabled, conv.householdID) {
		conv.memories = m.Memory.ProfileText(ctx, conv.chatUserID, conv.householdID)
	}
}

// fetchNodeTools is _resolve_tools' MQTT step: report_tools to the node, kept in key order
// (tool bytes are prompt bytes). ok=false when the node didn't answer or reported no client
// tools.
func (m *Module) fetchNodeTools(ctx context.Context, nodeID string) (clientTools, commands []*pyjson.Object, ok bool) {
	if !m.bus.Available() {
		return nil, nil, false
	}
	raw, err := m.reportTools(ctx, nodeID, nodeToolsWait)
	if err != nil {
		m.deps.Log.Warn("cc: MQTT tool fetch failed", "node", nodeID, "err", err)
		return nil, nil, false
	}
	v, err := pyjson.Loads(string(raw))
	report, _ := v.(*pyjson.Object)
	if err != nil || report == nil {
		return nil, nil, false
	}
	objs := func(key string) []*pyjson.Object {
		lv, _ := report.Get(key)
		l, _ := lv.([]any)
		var out []*pyjson.Object
		for _, e := range l {
			if o, ok := e.(*pyjson.Object); ok {
				out = append(out, o)
			}
		}
		return out
	}
	clientTools = objs("client_tools")
	if len(clientTools) == 0 {
		return nil, nil, false
	}
	return clientTools, objs("available_commands"), true
}

// chatDeviceAgents stands in for the node's DeviceDiscoveryAgent context (doc 07 §3: mobile
// chat injects every active DB device): {"home_assistant": {"device_controls": {domain:
// [{entity_id, name, area, state:"unknown"}]}}}, or nil when the household has none. The key
// is home_assistant even for direct devices (07 §8.10).
func (m *Module) chatDeviceAgents(ctx context.Context, hh string) *pyjson.Object {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT d.entity_id, d.name, COALESCE(NULLIF(d.domain, ''), 'switch'),
		COALESCE(r.name, '') FROM cc_devices d LEFT JOIN cc_rooms r ON r.id = d.room_id
		WHERE d.household_id = ? AND d.is_active = 1 ORDER BY d.rowid`, hh)
	if err != nil {
		m.deps.Log.Warn("cc: device context for mobile chat unavailable", "err", err)
		return nil
	}
	defer rows.Close()
	controls := pyjson.NewObject()
	for rows.Next() {
		var entity, name, domain, area string
		if err := rows.Scan(&entity, &name, &domain, &area); err != nil {
			m.deps.Log.Warn("cc: device context for mobile chat unavailable", "err", err)
			return nil
		}
		list, _ := controls.Get(domain)
		l, _ := list.([]any)
		controls.Set(domain, append(l, servertools.Obj("entity_id", entity, "name", name, "area", area, "state", "unknown")))
	}
	if controls.Len() == 0 {
		return nil
	}
	return servertools.Obj("home_assistant", servertools.Obj("device_controls", controls))
}

// --- the SSE stream ---

// sseWriter writes `data: <json>\n\n` events, each as one write plus one flush (Q12).
// json is Python's json.dumps (", " / ": " separators, ASCII-escaped).
type sseWriter struct {
	w    io.Writer
	rc   *http.ResponseController
	dead bool
}

func (s *sseWriter) send(ev *pyjson.Object) {
	if s.dead {
		return
	}
	if _, err := io.WriteString(s.w, "data: "+pyjson.Dumps(ev, true)+"\n\n"); err != nil {
		s.dead = true
		return
	}
	_ = s.rc.Flush()
}

func (m *Module) handleMobileChat(w http.ResponseWriter, r *http.Request, u authn.User) {
	req, ok := parseChatRequest(w, r, false)
	if !ok {
		return
	}
	ctx := r.Context()
	row, err := m.chatNode(ctx, u, req)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if m.LLM == nil {
		detail(w, http.StatusServiceUnavailable, "Voice pipeline unavailable")
		return
	}
	if len(req.rawImages) > 0 {
		if !m.slotVision(ctx, llm.LabelLive) {
			detailCode(w, http.StatusUnprocessableEntity, "Images are not available: the live model has no image input", codeImagesUnavailable)
			return
		}
		imgs, err := parseChatImages(req.rawImages)
		if err != nil {
			detailCode(w, http.StatusUnprocessableEntity, err.Error(), codeImagesInvalid)
			return
		}
		req.Images, req.rawImages = imgs, nil
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sse := &sseWriter{w: w, rc: http.NewResponseController(w)}
	_ = sse.rc.Flush()
	m.chatStream(ctx, sse, u, row, req)
}

// chatState is what the tool loop collects for the final `done`.
type chatState struct {
	actions       []any          // pending_actions: the last tool output carrying actions wins
	actionContext *pyjson.Object // {command_name, context}
	actionPreview any            // context.preview || context.message || None
	reasoning     string         // the latest non-empty reasoning
}

// chatStream is _chat_stream. Every stream ends with exactly one `done` or `error`. The LLM
// steps run on a context that outlives a client hang-up so the conversation history still
// commits (§11); node waits stop when the client is gone.
func (m *Module) chatStream(ctx context.Context, sse *sseWriter, u authn.User, row *nodeRow, req chatRequest) {
	tr := newReqTrace()
	llmCtx := withTrace(context.WithoutCancel(ctx), tr) // the pipeline's spans land on tr
	cid := req.ConversationID
	if cid == "" {
		cid = newMobileConversationID()
	}
	trace := Trace{ConversationID: cid, RequestType: "mobile_chat", Source: "mobile", NodeID: row.nodeID,
		HouseholdID: req.HouseholdID, UserID: u.ID, UserCommand: withImageMarkers(req.Message, len(req.Images))}
	finish := func(answer, errMsg string) {
		trace.AssistantMessage, trace.TotalDurationMS, trace.Spans = answer, tr.totalMS(), tr.spans()
		if errMsg != "" {
			trace.Status, trace.ErrorMessage = "error", errMsg
		}
		m.recordTraceAsync(trace)
	}
	fatal := func(msg string) {
		tr.status = "error"
		sse.send(servertools.Obj("type", "error", "message", msg, "conversation_id", cid, "trace_summary", tr.summary()))
		finish("", msg)
	}

	// Warm check. A live conversation that isn't this user's chat with this node (another
	// user's, another household's, or a voice conversation) is never joined: the turn gets a
	// fresh id instead, which `done` hands back. An expired id warms up under the same id.
	conv := m.convs.get(cid)
	if conv != nil && (conv.chatUserID != u.ID || conv.nodeID != row.nodeID || conv.householdID != req.HouseholdID) {
		m.deps.Log.Warn("cc: mobile chat refused a foreign conversation id", "conversation_id", cid, "user_id", u.ID)
		cid = newMobileConversationID()
		trace.ConversationID = cid
		conv = nil
	}
	if conv == nil {
		sse.send(servertools.Obj("type", "status", "message", "Starting conversation..."))
		end := tr.measure("warmup", "cc", nil)
		_, err := m.chatWarmup(llmCtx, u, row, req, cid)
		end(err)
		if err != nil {
			m.deps.Log.Error("cc: mobile chat warmup failed", "conversation_id", cid, "err", err)
			fatal(err.Error())
			return
		}
	}

	// Instant acknowledgment: keyword pools, no LLM, before the pipeline starts.
	sse.send(servertools.Obj("type", "acknowledgment", "text", acknowledgment(req.Message, rand.IntN)))

	end := tr.measure("process_command", "cc", nil)
	out, err := m.processTurn(llmCtx, nil, turnInput{VoiceCommand: req.Message, ConversationID: cid, Source: "chat", Images: req.Images})
	end(err)
	if err != nil {
		fatal(chatErrorText(cid, err))
		return
	}
	if out.imageMsgID != 0 {
		// After the reply (CI5): the images stayed in the message for the whole turn,
		// tool-loop continues included; now a description replaces them.
		defer m.scheduleDescribe(cid, out.imageMsgID)
	}
	res := out.res

	var st chatState
	for iter := 0; iter < chatMaxToolIterations; iter++ {
		if res.Reasoning != "" {
			st.reasoning = res.Reasoning
		}
		switch res.Stop {
		case stopComplete, stopServerToolComplete:
			answer := stripToolData(res.Message)
			m.replayWords(ctx, sse, answer)
			done := servertools.Obj("type", "done", "conversation_id", cid, "full_text", answer,
				"stop_reason", stopComplete, "trace_summary", tr.summary())
			if len(st.actions) > 0 {
				done.Set("actions", st.actions)
				done.Set("action_context", st.actionContext)
				if pyTruthy(st.actionPreview) {
					done.Set("action_preview", st.actionPreview)
				}
			}
			if req.IncludeReasoning && st.reasoning != "" {
				done.Set("reasoning", st.reasoning)
			}
			sse.send(done)
			finish(answer, "")
			return

		case stopValidation:
			v := res.Validation
			if v == nil {
				v = &validationRequest{}
			}
			opts := v.Options
			if opts == nil {
				opts = []any{}
			}
			sse.send(servertools.Obj("type", "done", "conversation_id", cid, "full_text", v.Question,
				"stop_reason", stopValidation,
				"validation", servertools.Obj("question", v.Question, "parameter_name", v.ParameterName, "options", opts),
				"trace_summary", tr.summary()))
			finish(v.Question, "")
			return

		case stopToolCalls:
			if len(res.ToolCalls) == 0 {
				break
			}
			if intermediate := parse.PyStrip(res.Message); intermediate != "" {
				sse.send(servertools.Obj("type", "delta", "text", intermediate+" "))
			}
			results := m.runChatTools(ctx, sse, tr, u, row.nodeID, req.Message, res.ToolCalls, &st)
			if ctx.Err() != nil {
				// The client is gone: nobody will read the answer. The exchange stays pending
				// and the next turn flushes it (transcripts.go).
				finish("", "client disconnected")
				return
			}
			end := tr.measure("continue_conversation", "cc", nil)
			res, _, err = m.continueBlocking(llmCtx, cid, results)
			end(err)
			if err != nil {
				fatal(chatErrorText(cid, err))
				return
			}
			continue

		case stopError:
			msg := res.Err
			if msg == "" {
				msg = "An error occurred"
			}
			fatal(msg)
			return

		default: // not_for_me
			done := servertools.Obj("type", "done", "conversation_id", cid, "full_text", res.Message,
				"stop_reason", res.Stop, "trace_summary", tr.summary())
			if req.IncludeReasoning && st.reasoning != "" {
				done.Set("reasoning", st.reasoning)
			}
			sse.send(done)
			finish(res.Message, "")
			return
		}
		break // only a tool_calls result without calls gets here: legacy broke out of the loop
	}
	fatal("Too many tool iterations")
}

// chatErrorText is str(e) for the failures legacy raised inside the stream.
func chatErrorText(cid string, err error) string {
	if errors.Is(err, errPrecondition) {
		return fmt.Sprintf("Conversation %s not found or expired", cid)
	}
	return err.Error()
}

// stripToolData drops a leading "[Tool data: …]\n\n" history prefix from a displayed answer.
func stripToolData(s string) string {
	if !strings.Contains(s, "[Tool data:") {
		return s
	}
	if i := strings.Index(s, "]\n\n"); i >= 0 {
		return parse.PyStrip(s[i+3:])
	}
	return s
}

// replayWords is the fake streaming kept by D32: one delta per space-separated word (each but
// the last with its trailing space), pausing chatWordPause after each when there are more
// than 3 words.
func (m *Module) replayWords(ctx context.Context, sse *sseWriter, answer string) {
	if answer == "" {
		return
	}
	words := strings.Split(answer, " ")
	for i, word := range words {
		text := word
		if i < len(words)-1 {
			text += " "
		}
		sse.send(servertools.Obj("type", "delta", "text", text))
		if len(words) > 3 && !sse.dead && ctx.Err() == nil {
			time.Sleep(chatWordPause)
		}
	}
}

// runChatTools routes each client tool call to the selected node, sequentially (13 §7.5),
// harvesting actions into st. An offline node fails every call fast with a `status` event
// instead of waiting out each timeout (D40 Q8); the LLM still narrates the failure.
func (m *Module) runChatTools(ctx context.Context, sse *sseWriter, tr *reqTrace, u authn.User, nodeID, message string,
	calls []parse.ToolCall, st *chatState) []toolResult {
	online := false
	if row, err := m.nodeByID(ctx, nodeID); err == nil {
		online = row.reachable(m.now())
	}
	if online {
		sse.send(servertools.Obj("type", "status", "message", "Running command on node..."))
	} else {
		sse.send(servertools.Obj("type", "status", "message", "Node is offline"))
	}
	results := make([]toolResult, 0, len(calls))
	for _, c := range calls {
		id := c.ID
		if id == "" {
			id = uuid4()
		}
		var output *pyjson.Object
		if !online {
			output = servertools.Obj("success", false, "error", "the node is offline", "offline", true)
		} else {
			end := tr.measure("mqtt_tool_"+c.Function.Name, "node", map[string]any{"command": c.Function.Name})
			output = m.chatToolCall(ctx, nodeID, id, c, u.ID, message)
			end(nil)
		}
		results = append(results, toolResult{ToolCallID: id, Output: output})
		harvestActions(st, c.Function.Name, output)
	}
	return results
}

// chatToolCall is dispatch_node_command for one call: the tool_call verb with the typed
// message as voice_command (commands read it from RequestInformation), and the node's POST
// to /device-control-results/{rid}, which only that node can fill (D4; no `trusted`). A
// timeout or a missing broker is a synthetic failure output, never an error.
func (m *Module) chatToolCall(ctx context.Context, nodeID, toolCallID string, c parse.ToolCall, userID int64, message string) *pyjson.Object {
	if !m.bus.Available() {
		return servertools.Obj("success", false, "error", "could not dispatch to node: "+ErrNoBroker.Error())
	}
	rid := uuid4()
	var args any = c.Function.Arguments // a JSON string; the node decodes it
	if c.Function.Arguments == "" {
		args = map[string]any{}
	}
	details := map[string]any{"command_name": c.Function.Name, "arguments": args, "tool_call_id": toolCallID,
		"reply_request_id": rid, "user_id": userID}
	if message != "" {
		details["voice_command"] = message
	}
	m.bus.Expect(rid, nodeID)
	defer m.bus.Drop(rid)
	m.bus.CommandWithID(nodeID, "tool_call", details, rid)
	wctx, cancel := context.WithTimeout(ctx, chatToolWait)
	defer cancel()
	raw, err := m.bus.Await(wctx, rid)
	if err != nil {
		return servertools.Obj("success", false, "error", "the node didn't respond in time", "timeout", true)
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		return servertools.Obj("success", false, "error", "malformed node result")
	}
	output := v
	if o, ok := v.(*pyjson.Object); ok {
		if out, ok := o.Get("output"); ok {
			output = out
		}
	}
	if o, ok := output.(*pyjson.Object); ok {
		return o
	}
	return servertools.Obj("success", true, "result", output)
}

// harvestActions is the action-button extraction (mobile_chat.py:424-444): actions from the
// output or its context; the last output carrying them wins.
func harvestActions(st *chatState, command string, output *pyjson.Object) {
	actions, _ := output.Get("actions")
	if !pyTruthy(actions) {
		if cv, ok := output.Get("context"); ok {
			if co, ok := cv.(*pyjson.Object); ok {
				actions, _ = co.Get("actions")
			} else {
				actions = nil
			}
		}
	}
	list, isList := actions.([]any)
	if !isList || len(list) == 0 {
		return
	}
	st.actions = list
	var raw any = output
	if cv, ok := output.Get("context"); ok {
		raw = cv
	}
	st.actionContext = servertools.Obj("command_name", command, "context", raw)
	if ro, ok := raw.(*pyjson.Object); ok {
		st.actionPreview = nil
		if p, _ := ro.Get("preview"); pyTruthy(p) {
			st.actionPreview = p
		} else if msg, _ := ro.Get("message"); pyTruthy(msg) {
			st.actionPreview = msg
		}
	}
}
