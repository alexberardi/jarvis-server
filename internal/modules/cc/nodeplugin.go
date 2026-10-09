package cc

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Node plugin endpoints owned by the voice sub-phase: the LLM passthrough nodes use for
// chat_text() (D5: /api/v0/chat is dropped), the strict date context, and the public plugin
// notification API (docs/cc/13 §3.5). The attention broker interposition is 5c (D18): its
// hook is AttentionGate; with it nil or attention.enabled off these routes are the legacy
// delivery, byte for byte.

const sourceService = "jarvis-command-center"

// --- /node/llm/chat ---

func (m *Module) handleNodeLLMChat(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	var msgs []llm.Message
	v, present := b.m["messages"]
	if !present {
		b.fail("messages", "Field required")
	} else if l, isList := v.([]any); !isList {
		b.fail("messages", "Input should be a valid list")
	} else {
		for i, e := range l {
			em, isObj := e.(map[string]any)
			if !isObj {
				*b.errs = append(*b.errs, "body -> messages -> "+strconv.Itoa(i)+": Input should be a valid dictionary or object to extract fields from")
				continue
			}
			role, _ := em["role"].(string)
			if _, has := em["role"]; !has {
				*b.errs = append(*b.errs, "body -> messages -> "+strconv.Itoa(i)+" -> role: Field required")
			} else if role != "system" && role != "user" && role != "assistant" {
				*b.errs = append(*b.errs, "body -> messages -> "+strconv.Itoa(i)+" -> role: Input should be 'system', 'user' or 'assistant'")
			}
			content, isStr := em["content"].(string)
			if _, has := em["content"]; !has {
				*b.errs = append(*b.errs, "body -> messages -> "+strconv.Itoa(i)+" -> content: Field required")
			} else if !isStr {
				*b.errs = append(*b.errs, "body -> messages -> "+strconv.Itoa(i)+" -> content: Input should be a valid string")
			}
			msgs = append(msgs, llm.Message{Role: role, Content: llm.TextContent(content)})
		}
	}
	model := "live"
	if s, ok := b.str("model", false); ok {
		model = s
	}
	temp := 0.0
	if f, ok := b.number("temperature"); ok {
		temp = f
	}
	if !b.done(w) {
		return
	}
	if m.LLM == nil {
		detail(w, http.StatusServiceUnavailable, "LLM unavailable")
		return
	}
	resp, err := m.LLM.Chat(r.Context(), llm.ChatRequest{Label: model, Messages: msgs, Temperature: &temp})
	if err != nil {
		m.deps.Log.Warn("cc: node_llm_chat failed", "node", n.ID, "err", err)
		detail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"content": resp.Content})
}

// --- /generate/date-context ---

// handleDateContext returns the node's strict DateContext. D40 03.Q11: user_timezone and
// is_dst are always filled; an unknown zone falls back to UTC (D8) instead of a 500.
// The household's explicitly set zone wins over the node's ?timezone= (timezone.go).
func (m *Module) handleDateContext(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	tz := m.turnTimezone(r.Context(), n.HouseholdID, r.URL.Query().Get("timezone"))
	httpx.WriteJSON(w, http.StatusOK, dates.New(m.now(), tz).Object())
}

// --- inbox and push helpers (services/inbox_notification_service.py) ---

// pushTarget is _resolve_push_target: "user" without a user id falls back to the household.
func pushTarget(targetType string, userID *int64, hh string) (string, string) {
	if targetType == "user" {
		if userID == nil {
			return "household", hh
		}
		return "user", strconv.FormatInt(*userID, 10)
	}
	return "household", hh
}

// pushConfirmation is push_confirmation_to_inbox: a "confirmation" card (metadata
// {command_name, node_id, actions, draft}) and a high-priority push linking to it.
func (m *Module) pushConfirmation(ctx context.Context, hh string, userID *int64, nodeID, title, summary, body, command string,
	actions []any, draft any, targetType string) string {
	if m.Notify == nil {
		return ""
	}
	it, err := m.Notify.CreateInboxItem(ctx, nil, notifications.NewInboxItem{
		HouseholdID: hh, UserID: userID, Title: title, Summary: summary, Body: body, Category: "confirmation",
		SourceService: sourceService,
		Metadata:      map[string]any{"command_name": command, "node_id": nodeID, "actions": jsonValue(actions), "draft": jsonValue(draft)},
	})
	if err != nil {
		m.deps.Log.Error("cc: failed to push confirmation to inbox", "err", err)
		return ""
	}
	tt, tid := pushTarget(targetType, userID, hh)
	if _, err := m.Notify.Notify(ctx, nil, sourceService, notifications.Notification{
		TargetType: tt, TargetID: tid, Title: title, Body: summary, Priority: "high", Category: "confirmation",
		Data: map[string]any{"type": "confirmation", "inbox_item_id": it.ID},
	}); err != nil {
		m.deps.Log.Warn("cc: push notification error", "err", err)
	}
	return it.ID
}

// postInboxItem is post_inbox_item_sync.
func (m *Module) postInboxItem(ctx context.Context, hh string, userID *int64, title, summary, body, category string,
	metadata map[string]any, push bool, targetType string) string {
	if m.Notify == nil {
		return ""
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	it, err := m.Notify.CreateInboxItem(ctx, nil, notifications.NewInboxItem{
		HouseholdID: hh, UserID: userID, Title: title, Summary: summary, Body: body, Category: category,
		SourceService: sourceService, Metadata: metadata,
	})
	if err != nil {
		m.deps.Log.Error("cc: failed to post inbox item", "category", category, "err", err)
		return ""
	}
	if push {
		tt, tid := pushTarget(targetType, userID, hh)
		if _, err := m.Notify.Notify(ctx, nil, sourceService, notifications.Notification{
			TargetType: tt, TargetID: tid, Title: title, Body: summary, Priority: "high", Category: category,
			Data: map[string]any{"type": category, "inbox_item_id": it.ID},
		}); err != nil {
			m.deps.Log.Warn("cc: push notification error", "err", err)
		}
	}
	return it.ID
}

// sendLinkPush is send_link_push_sync: a user-scoped "link" card, then a user push.
func (m *Module) sendLinkPush(ctx context.Context, hh string, userID int64, url, title, body string) bool {
	if m.Notify == nil {
		return false
	}
	uid := userID
	it, err := m.Notify.CreateInboxItem(ctx, nil, notifications.NewInboxItem{
		HouseholdID: hh, UserID: &uid, Title: title, Summary: body, Body: url, Category: "link",
		SourceService: sourceService, Metadata: map[string]any{"url": url, "type": "open_url"},
	})
	if err != nil {
		m.deps.Log.Error("cc: send_link inbox write failed", "err", err)
		return false
	}
	if _, err := m.Notify.Notify(ctx, nil, sourceService, notifications.Notification{
		TargetType: "user", TargetID: strconv.FormatInt(userID, 10), Title: title, Body: body, Category: "link", Priority: "high",
		Data: map[string]any{"type": "open_url", "url": url, "household_id": hh, "inbox_item_id": it.ID},
	}); err != nil {
		m.deps.Log.Warn("cc: send_link push error", "err", err)
	}
	return true
}

// attentionGate runs the 5c broker when it exists and the household turned it on.
func (m *Module) attentionGate(ctx context.Context, req AttentionRequest) (AttentionDecision, bool) {
	if m.Attention == nil || !m.settings.Bool(ctx, settingAttention, settings.Scope{HouseholdID: req.HouseholdID}) {
		return AttentionDecision{}, false
	}
	return m.Attention.Gate(ctx, req)
}

func (m *Module) attentionOutcome(ctx context.Context, d AttentionDecision, gated, delivered bool, inboxID string) {
	if !gated || m.Attention == nil {
		return
	}
	outcome := "failed"
	if delivered {
		outcome = "delivered"
	}
	m.Attention.Outcome(ctx, d.DeliveryID, outcome, inboxID)
}

func targetTypeField(b *body) string {
	t := "household"
	if s, ok := b.str("target_type", false); ok {
		if s != "user" && s != "household" {
			b.fail("target_type", "Input should be 'user' or 'household'")
		}
		t = s
	}
	return t
}

func optUserID(b *body) *int64 {
	if v, ok := b.integer("user_id", false); ok {
		return &v
	}
	return nil
}

// --- /node/push-notification ---

func (m *Module) handleNodePush(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	title, _ := b.str("title", true)
	text, _ := b.str("body", true)
	if s, ok := b.str("priority", false); ok {
		_ = s // D40 13.Q3 (honour priority/category) waits on the mobile check; legacy ignores it
	}
	category := "alert"
	if s, ok := b.str("category", false); ok {
		category = s
	}
	userID := optUserID(b)
	targetType := targetTypeField(b)
	dedupe, _ := b.str("dedupe_key", false)
	force, _ := b.boolean("force")
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh := n.HouseholdID
	d, gated := m.attentionGate(ctx, AttentionRequest{HouseholdID: hh, OriginNodeID: n.ID, Source: category, Category: category,
		Title: title, Summary: text, RequestedRung: "push", DedupeKey: dedupe, TargetUserID: userID, Payload: b.m, Force: force})
	if gated && !d.Deliver {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": false, "inbox_item_id": nil, "withheld_by": d.WithheldBy})
		return
	}
	var id string
	if gated && d.Rung == "inbox" {
		id = m.postInboxItem(ctx, hh, userID, title, text, text, category, map[string]any{"node_id": n.ID}, false, targetType)
	} else {
		id = m.pushConfirmation(ctx, hh, userID, n.ID, title, text, text, "reminder", []any{}, nil, targetType)
	}
	m.attentionOutcome(ctx, d, gated, id != "", id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": id != "", "inbox_item_id": nullIfBlank(id), "withheld_by": nil})
}

// --- /node/send-link ---

func (m *Module) handleNodeSendLink(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	userID, _ := b.integer("user_id", true)
	url, _ := b.str("url", true)
	tp, _ := b.optStrPtr("title")
	bp, _ := b.optStrPtr("body")
	if !b.done(w) {
		return
	}
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": false, "withheld_by": nil})
		return
	}
	title, text := "Link from Jarvis", "Tap to open"
	if tp != nil && strings.TrimSpace(*tp) != "" {
		title = strings.TrimSpace(*tp)
	}
	if bp != nil && strings.TrimSpace(*bp) != "" {
		text = strings.TrimSpace(*bp)
	}
	ctx := r.Context()
	hh := n.HouseholdID
	uid := userID
	d, gated := m.attentionGate(ctx, AttentionRequest{HouseholdID: hh, OriginNodeID: n.ID, Source: "send_link", Category: "link",
		Title: title, Summary: text, RequestedRung: "push", DedupeKey: url, TargetUserID: &uid, Payload: b.m})
	if gated && !d.Deliver {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": false, "withheld_by": d.WithheldBy})
		return
	}
	if gated && d.Rung == "inbox" {
		// M11: a demoted link keeps metadata {url, type: open_url}.
		id := m.postInboxItem(ctx, hh, &uid, title, text, text+"\n\n["+title+"]("+url+")", "link",
			map[string]any{"node_id": n.ID, "url": url, "type": "open_url"}, false, "user")
		m.attentionOutcome(ctx, d, gated, id != "", id)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": id != "", "withheld_by": nil})
		return
	}
	ok2 := m.sendLinkPush(ctx, hh, userID, url, title, text)
	m.attentionOutcome(ctx, d, gated, ok2, "")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent": ok2, "withheld_by": nil})
}

// --- /node/inbox-item ---

func (m *Module) handleNodeInboxItem(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	title, _ := b.str("title", true)
	summary, _ := b.str("summary", false)
	text, _ := b.str("body", false)
	category := "general"
	if s, ok := b.str("category", false); ok {
		category = s
	}
	metadata, _ := b.object("metadata", false)
	userID := optUserID(b)
	push, _ := b.boolean("create_push_notification")
	targetType := targetTypeField(b)
	dedupe, _ := b.str("dedupe_key", false)
	force, _ := b.boolean("force")
	if !b.done(w) {
		return
	}
	title = strings.TrimSpace(title)
	hh := n.HouseholdID
	if title == "" || hh == "" {
		if hh == "" {
			m.deps.Log.Warn("cc: cannot post node inbox item: node has no household", "node", n.ID)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": nil, "sent": false, "withheld_by": nil})
		return
	}
	md := map[string]any{}
	for k, v := range metadata {
		md[k] = v
	}
	if _, has := md["node_id"]; !has { // setdefault: a caller-supplied node_id wins
		md["node_id"] = n.ID
	}
	if category == "" {
		category = "general"
	}
	if dedupe == "" {
		if s, ok := md["dedupe_key"].(string); ok {
			dedupe = s
		}
	}
	rung := "inbox"
	if push {
		rung = "push"
	}
	payload := map[string]any{}
	for k, v := range b.m {
		if k != "metadata" {
			payload[k] = v
		}
	}
	ctx := r.Context()
	d, gated := m.attentionGate(ctx, AttentionRequest{HouseholdID: hh, OriginNodeID: n.ID, Source: category, Category: category,
		Title: title, Summary: summary, RequestedRung: rung, DedupeKey: dedupe, TargetUserID: userID, Payload: payload, Force: force})
	if gated && !d.Deliver {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": nil, "sent": false, "withheld_by": d.WithheldBy})
		return
	}
	if gated {
		push = d.Rung == "push"
	}
	id := m.postInboxItem(ctx, hh, userID, title, summary, text, category, md, push, targetType)
	m.attentionOutcome(ctx, d, gated, id != "", id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": nullIfBlank(id), "sent": id != "", "withheld_by": nil})
}
