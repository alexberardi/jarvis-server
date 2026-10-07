package cc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
)

// Wiring for errands and workflows (docs/cc/09; the runner lives in package errands): the
// service on the queue, its node and card adapters, the three voice tools, startup recovery
// and the D20 purge.

// registerErrands builds the errand runner (after settings and the Bus exist) and installs its
// queue jobs.
func (m *Module) registerErrands() {
	phoneCalls, schedules := m.ErrandPhone, m.ErrandSchedules
	if phoneCalls == nil {
		phoneCalls = errandPhone{m}
	}
	if schedules == nil {
		schedules = errandSchedules{m}
	}
	s := &errands.Service{
		DB: m.deps.DB, Log: m.deps.Log, LLM: m.LLM, Tools: m.tools, Settings: m.settings,
		Nodes: errandNodes{m}, Cards: errandCards{m}, Phone: phoneCalls, Schedules: schedules,
		Timezone: m.householdTimezone, Now: m.now,
	}
	if m.deps.Queue != nil {
		s.Register(m.deps.Queue)
	} else {
		s.Init()
	}
	m.errands = s
	// A due errand schedule (doc 08) drafts the plan; a linked call's terminal transition
	// (doc 11) resumes the run.
	m.rt.scheduleFire = func(ctx context.Context, sch ScheduledErrand) error {
		return s.DraftErrand(ctx, errands.DraftRequest{HouseholdID: sch.HouseholdID, NodeID: sch.NodeID,
			UserID: sch.UserID, Goal: sch.Intent})
	}
	if m.phone != nil && m.phone.Errands == nil {
		m.phone.Errands = phoneErrandHook{m}
	}
}

// errandSchedules is doc 08's schedule store as errands see it.
type errandSchedules struct{ m *Module }

func (a errandSchedules) CreateSchedule(ctx context.Context, s errands.NewSchedule) (string, error) {
	return a.m.CreateSchedule(ctx, NewSchedule(s))
}

func (a errandSchedules) PostListCard(ctx context.Context, hh string, userID *int64) (int, error) {
	n, _, err := a.m.postScheduleListCard(ctx, hh, userID)
	return n, err
}

// errandPhone is doc 11's phone service as errands see it.
type errandPhone struct{ m *Module }

func (a errandPhone) Enabled(ctx context.Context, hh string) bool {
	return a.m.phone != nil && a.m.phone.Enabled(ctx, hh)
}

func (a errandPhone) PlaceErrandCall(ctx context.Context, c errands.ErrandCall) (string, error) {
	if a.m.phone == nil {
		return "", errors.New("phone calls are not available")
	}
	step := int64(c.Step)
	id := a.m.phone.CreatePlan(ctx, phone.PlanRequest{Business: c.Business, Goal: c.Goal, HouseholdID: c.HouseholdID,
		UserID: c.UserID, ErrandID: c.WorkflowID, ErrandStep: &step, PriorContext: c.PriorContext})
	if id == "" {
		return "", errors.New("the call could not be drafted")
	}
	return id, nil
}

func (a errandPhone) CallStatus(ctx context.Context, sessionID string) (errands.CallOutcome, bool, error) {
	if a.m.phone == nil {
		return errands.CallOutcome{}, false, nil
	}
	snap, ok, err := a.m.phone.Snapshot(ctx, sessionID)
	if !ok || err != nil {
		return errands.CallOutcome{}, ok, err
	}
	return callOutcome(snap), true, nil
}

func (a errandPhone) DeclineErrandCalls(ctx context.Context, workflowID string) (bool, error) {
	if a.m.phone == nil {
		return false, nil
	}
	return a.m.phone.DeclineErrand(ctx, workflowID)
}

// phoneErrandHook resumes the errand a terminal call belongs to.
type phoneErrandHook struct{ m *Module }

func (h phoneErrandHook) CallTerminal(ctx context.Context, snap phone.CallSnapshot) {
	if h.m.errands == nil {
		return
	}
	if err := h.m.errands.OnCallTerminal(ctx, callOutcome(snap)); err != nil {
		h.m.deps.Log.Warn("cc: errand resume after call failed", "session", snap.SessionID, "err", err)
	}
}

func callOutcome(s phone.CallSnapshot) errands.CallOutcome {
	o := errands.CallOutcome{SessionID: s.SessionID, WorkflowID: s.ErrandID, State: s.State,
		ContactName: s.ContactName, ErrorMessage: s.ErrorMessage, OutcomeJSON: s.OutcomeJSON, ConfirmedAt: s.ConfirmedAt}
	if s.ErrandStep != nil {
		o.Step = int(*s.ErrandStep)
	}
	return o
}

// Errands is the errand runner (valid after Register): the phone module calls
// Errands().OnCallTerminal on a linked call's terminal transition (D16), doc 08's schedule fire
// calls Errands().DraftErrand, and doc 13's POST /callbacks dispatches command "errand" taps to
// Errands().Callbacks().
func (m *Module) Errands() *errands.Service { return m.errands }

// registerErrandTools registers run_errand / schedule_errand / list_scheduled_errands. The
// runner is resolved at execute time (it is built after the tools).
func (m *Module) registerErrandTools() {
	svc := func() *errands.Service {
		if m.errands == nil || !m.errands.Available() {
			return nil
		}
		return m.errands
	}
	m.tools.Register(&errands.RunErrandTool{Svc: svc, Commands: m.conversationCommands})
	m.tools.Register(&errands.ScheduleErrandTool{Svc: svc})
	m.tools.Register(&errands.ListScheduledErrandsTool{Svc: svc})
}

// conversationCommands are a live conversation's merged available commands (run_errand's
// node menu).
func (m *Module) conversationCommands(id string) []*pyjson.Object {
	if m.convs == nil {
		return nil
	}
	if conv := m.convs.get(id); conv != nil {
		return conv.commands
	}
	return nil
}

// gateErrandTools drops the errand tools from a conversation's offer when errands.enabled is
// off for the household (D40 09.Q12).
func (m *Module) gateErrandTools(ctx context.Context, hh string, names []string) []string {
	if m.errands == nil || m.errands.Enabled(ctx, hh) {
		return names
	}
	gated := map[string]bool{}
	for _, n := range errands.ToolNames {
		gated[n] = true
	}
	out := names[:0:0]
	for _, n := range names {
		if !gated[n] {
			out = append(out, n)
		}
	}
	return out
}

// startErrands is startup recovery: runs caught mid-step by the restart fail with a card.
func (m *Module) startErrands(ctx context.Context) {
	if m.errands != nil {
		m.errands.Recover(ctx)
	}
}

// purgeErrands is the D20 hook.
func (m *Module) purgeErrands(ctx context.Context, tx *sql.Tx, userID int64) error {
	return errands.PurgeUser(ctx, tx, userID, m.deps.Queue != nil)
}

// errandNodes runs headless node steps over the Bus.
type errandNodes struct{ m *Module }

// RunTool is dispatch_node_command: the tool_call verb (no `trusted`, D4) and the node's POST
// to /device-control-results/{reply_request_id}.
func (n errandNodes) RunTool(ctx context.Context, nodeID, command string, args *pyjson.Object, userID *int64, voice string, timeout time.Duration) *pyjson.Object {
	if n.m.bus == nil || !n.m.bus.Available() {
		return servertools.Obj("success", false, "error", "could not dispatch to node: MQTT is unavailable")
	}
	if args == nil {
		args = pyjson.NewObject()
	}
	key := uuid4()
	details := map[string]any{"command_name": command, "arguments": args, "tool_call_id": key, "reply_request_id": key}
	if userID != nil {
		details["user_id"] = *userID
	}
	if voice != "" {
		details["voice_command"] = voice
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, res, err := n.m.bus.CommandAwait(wctx, nodeID, "tool_call", details, key)
	if err != nil {
		return servertools.Obj("success", false, "error", "the node didn't respond in time", "timeout", true)
	}
	v, err := pyjson.Loads(string(res))
	if err != nil {
		return servertools.Obj("success", true, "result", string(res))
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

// ReportCommands is _request_tools_from_node: report_tools and the node's available_commands.
func (n errandNodes) ReportCommands(ctx context.Context, nodeID string, timeout time.Duration) ([]*pyjson.Object, bool) {
	if n.m.bus == nil || !n.m.bus.Available() {
		return nil, false
	}
	res, err := n.m.reportTools(ctx, nodeID, timeout)
	if err != nil {
		return nil, false
	}
	v, err := pyjson.Loads(string(res))
	if err != nil {
		return nil, false
	}
	o, _ := v.(*pyjson.Object)
	if o == nil {
		return nil, false
	}
	av, _ := o.Get("available_commands")
	list, _ := av.([]any)
	var out []*pyjson.Object
	for _, e := range list {
		if c, ok := e.(*pyjson.Object); ok {
			out = append(out, c)
		}
	}
	return out, true
}

// errandCards posts through the notify service (D31).
type errandCards struct{ m *Module }

func (c errandCards) Post(ctx context.Context, card errands.Card) string {
	return c.m.postInboxItem(ctx, card.HouseholdID, card.UserID, card.Title, card.Summary, card.Body, card.Category,
		card.Metadata, card.Push, card.TargetType)
}

// inboxUpdater is the notifications module's in-place update (update_inbox_item_sync).
type inboxUpdater interface {
	UpdateInboxItem(ctx context.Context, tx *sql.Tx, id, householdID string, u notifications.InboxUpdate) (notifications.InboxItem, error)
}

func (c errandCards) Update(ctx context.Context, id string, card errands.Card) bool {
	up, ok := c.m.Notify.(inboxUpdater)
	if !ok {
		return false
	}
	md := card.Metadata
	if md == nil {
		md = map[string]any{}
	}
	if _, err := up.UpdateInboxItem(ctx, nil, id, card.HouseholdID, notifications.InboxUpdate{
		Title: &card.Title, Summary: &card.Summary, Body: &card.Body, Category: &card.Category, Metadata: md,
	}); err != nil {
		c.m.deps.Log.Warn("cc: inbox update failed", "item", id, "err", err)
		return false
	}
	return true
}
