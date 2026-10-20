package errands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Planning: goal → draft row → plan card (§3.2), as a durable queue job so the card the voice
// ack promised survives a restart (D27; legacy lost the detached asyncio task).

// Job types.
const (
	JobPlan     = "cc.errand.plan"
	JobResume   = "cc.errand.resume"
	JobReplan   = "cc.errand.replan"
	JobDeadline = "cc.errand.deadline"
	JobExpire   = "cc.errand.expire"
)

// ErrDisabled is returned when errands.enabled is off for the household.
var ErrDisabled = errors.New("errands: disabled for this household")

// ErrUnavailable is returned when the runner has no queue or database.
var ErrUnavailable = errors.New("errands: unavailable")

// DraftRequest is one planning request (draft_errand_plan_detached's arguments).
type DraftRequest struct {
	HouseholdID string
	NodeID      string
	UserID      *int64
	Goal        string
	// Menu is the menu built from the live conversation; nil re-sources it from the node.
	Menu []MenuEntry
}

type planJob struct {
	HouseholdID string          `json:"household_id"`
	NodeID      string          `json:"node_id"`
	UserID      *int64          `json:"user_id"`
	Goal        string          `json:"goal"`
	Menu        json.RawMessage `json:"menu,omitempty"`
}

func planDedupPrefix(uid *int64) string {
	if uid == nil {
		return JobPlan + ":0:"
	}
	return JobPlan + ":" + strconv.FormatInt(*uid, 10) + ":"
}

func menuJSON(menu []MenuEntry) json.RawMessage {
	if len(menu) == 0 {
		return nil
	}
	list := make([]any, len(menu))
	for i, c := range menu {
		o := pyjson.NewObject()
		o.Set("command", c.Command)
		o.Set("description", c.Description)
		args := c.Args
		if args == nil {
			args = pyjson.NewObject()
		}
		o.Set("args", args)
		o.Set("is_risky", c.IsRisky)
		if c.RunWithoutConfirmation {
			o.Set("run_without_confirmation", true)
		}
		list[i] = o
	}
	return json.RawMessage(pyjson.Compact(list))
}

func menuFromJSON(raw json.RawMessage) []MenuEntry {
	if len(raw) == 0 {
		return nil
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		return nil
	}
	list, _ := v.([]any)
	var out []MenuEntry
	for _, e := range list {
		o, ok := e.(*pyjson.Object)
		if !ok {
			continue
		}
		args := objObj(o, "args")
		if args == nil {
			args = pyjson.NewObject()
		}
		out = append(out, MenuEntry{Command: objStr(o, "command"), Description: objStr(o, "description"),
			Args: args, IsRisky: objTruthy(o, "is_risky"), RunWithoutConfirmation: objTruthy(o, "run_without_confirmation")})
	}
	return out
}

// DraftErrand queues the planning of a goal (run_errand, and doc 08's schedule fire). The plan
// card — or an honest couldn't-plan card — follows from the job.
func (s *Service) DraftErrand(ctx context.Context, r DraftRequest) error {
	if !s.Available() {
		return ErrUnavailable
	}
	if !s.Enabled(ctx, r.HouseholdID) {
		return ErrDisabled
	}
	payload, err := json.Marshal(planJob{HouseholdID: r.HouseholdID, NodeID: r.NodeID, UserID: r.UserID,
		Goal: r.Goal, Menu: menuJSON(r.Menu)})
	if err != nil {
		return err
	}
	_, err = s.Queue.Enqueue(ctx, JobPlan, payload, queue.Options{DedupKey: planDedupPrefix(r.UserID) + hexID("")})
	return err
}

// resolveNodeMenu is _resolve_node_menu: the node's live commands (report_tools round trip)
// ∪ the enabled server tools; nil when nothing is available (the planner then uses its
// fallback menu).
func (s *Service) resolveNodeMenu(ctx context.Context, nodeID, hh string, uid *int64) []MenuEntry {
	var available []*pyjson.Object
	if s.Nodes != nil && nodeID != "" {
		if cmds, ok := s.Nodes.ReportCommands(ctx, nodeID, s.MenuTimeout); ok {
			available = cmds
		} else {
			s.log().Warn("errands: couldn't fetch node commands; server tools only", "node", nodeID)
		}
	}
	return s.FullMenu(ctx, available, hh, uid)
}

// CreatePlan is create_errand_plan: plan, persist the DRAFT, post its card. A planning failure
// wraps ErrPlan (rephrase); anything else is infra.
func (s *Service) CreatePlan(ctx context.Context, r DraftRequest) (*planRow, error) {
	menu := r.Menu
	if menu == nil {
		menu = s.resolveNodeMenu(ctx, r.NodeID, r.HouseholdID, r.UserID)
	}
	plan, err := s.PlanErrand(ctx, r.Goal, menu)
	if err != nil {
		return nil, err
	}
	row := &planRow{ID: hexID("pl_"), HouseholdID: r.HouseholdID, UserID: r.UserID, NodeID: r.NodeID,
		Goal: r.Goal, Summary: plan.Summary, Steps: plan.Steps}
	// Checkpoint 1 (autonomy PLAN §1): with autonomy on, the household's rules decide the plan.
	var asked *policy.Event
	if s.autonomous(ctx, r.HouseholdID) {
		verdicts := s.decideSteps(ctx, r.HouseholdID, plan.Steps, policy.SourceErrandPlan)
		for _, v := range verdicts {
			if v.Effect == policy.Block {
				return s.blockPlan(ctx, row, verdicts)
			}
		}
		ok, reason := IsAutorunEligible(plan.Steps, verdicts)
		if ok {
			return s.autorunPlan(ctx, row)
		}
		asked = &policy.Event{Effect: policy.Ask, Outcome: policy.OutcomeAsked, Reason: reason}
		for i, v := range verdicts {
			if v.Effect != policy.Allow {
				asked.Tool, asked.Reason, asked.RuleID = strings.TrimSpace(plan.Steps[i].Command), v.Reason, ruleID(v)
				break
			}
		}
	}
	if err := s.insertPlan(ctx, row); err != nil {
		return nil, err
	}
	if asked != nil {
		asked.HouseholdID, asked.UserID, asked.Source, asked.SourceID = row.HouseholdID, row.UserID, policy.SourceErrandPlan, row.ID
		s.record(ctx, *asked)
	}
	if id := s.postPlanCard(ctx, row); id != "" && id != row.InboxItemID {
		row.InboxItemID = id
		s.setPlanInbox(ctx, row.ID, id)
	}
	s.enqueueExpire(ctx, row)
	s.log().Info("errands: plan drafted", "plan", row.ID, "household", r.HouseholdID, "node", r.NodeID, "steps", len(row.Steps))
	return row, nil
}

func ruleID(v policy.Verdict) *int64 {
	if v.Rule == nil {
		return nil
	}
	id := v.Rule.ID
	return &id
}

// blockPlan stores a plan a household rule blocks as cancelled and says so on a card.
func (s *Service) blockPlan(ctx context.Context, p *planRow, verdicts []policy.Verdict) (*planRow, error) {
	if err := s.insertPlan(ctx, p); err != nil {
		return nil, err
	}
	if _, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_errand_plans SET state = 'cancelled', error = 'blocked by a household rule',
		updated_at = ? WHERE id = ?`, dbTime(s.now()), p.ID); err != nil {
		return nil, err
	}
	p.State = "cancelled"
	summary := firstNonEmpty(p.Summary, p.Goal)
	var lines []string
	for i, v := range verdicts {
		if v.Effect != policy.Block {
			continue
		}
		st := p.Steps[i]
		cmd := strings.TrimSpace(st.Command)
		line := "- " + firstNonEmpty(st.Label, cmd) + " (" + cmd + ")"
		if v.Rule != nil && v.Rule.Note != "" {
			line += ": " + v.Rule.Note
		}
		lines = append(lines, line)
		s.record(ctx, policy.Event{HouseholdID: p.HouseholdID, UserID: p.UserID, Source: policy.SourceErrandPlan, SourceID: p.ID,
			Tool: cmd, Effect: policy.Block, Outcome: policy.OutcomeBlocked, RuleID: ruleID(v), Reason: v.Reason})
	}
	if s.Cards != nil {
		s.Cards.Post(ctx, Card{
			HouseholdID: p.HouseholdID, UserID: p.UserID, Title: "🚫 Errand blocked: " + summary,
			Summary: "Your household's rules block this errand.", Category: planCategory,
			Body: "I didn't run this errand because your household's rules block:\n\n" + strings.Join(lines, "\n") +
				"\n\nA household admin can change the rules.",
			Metadata: map[string]any{"household_id": p.HouseholdID, "plan_id": p.ID, "state": "blocked",
				"steps": stepsOrEmpty(p.Steps)},
			Push: true, TargetType: targetType(p.UserID),
		})
	}
	s.log().Info("errands: plan blocked by a household rule", "plan", p.ID, "household", p.HouseholdID)
	return p, nil
}

// autorunPlan launches a plan the household's rules allow at once, reporting on a "Ran on its
// own" card (through the attention gate, which may withhold it; the run goes ahead either way).
func (s *Service) autorunPlan(ctx context.Context, p *planRow) (*planRow, error) {
	if err := s.insertPlan(ctx, p); err != nil {
		return nil, err
	}
	wfID := hexID("wf_")
	w := &workflowRow{ID: wfID, Kind: KindAutorun, HouseholdID: p.HouseholdID, UserID: p.UserID, NodeID: p.NodeID,
		Goal: firstNonEmpty(p.Goal, p.Summary), Title: firstNonEmpty(p.Summary, p.Goal), Steps: p.Steps, Revision: 1}
	rc := s.ranCard(ctx, w, nil, "running")
	rc.PlanID = p.ID
	cardID := ""
	if s.Cards != nil {
		card := s.ranCardOf(w, rc, "")
		card.Gated, card.DedupeKey = true, "autorun:"+wfID
		cardID = s.Cards.Post(ctx, card)
	}
	cmds := make([]any, 0, len(p.Steps))
	for _, st := range p.Steps {
		cmds = append(cmds, strings.TrimSpace(st.Command))
	}
	raced, err := s.launch(ctx, p, wfID, KindAutorun, cardID, &policy.Event{HouseholdID: p.HouseholdID, UserID: p.UserID,
		Source: policy.SourceErrandPlan, SourceID: p.ID, Effect: policy.Allow, Outcome: policy.OutcomeLaunched,
		Reason: "every step is allowed by the household's rules", Detail: map[string]any{"workflow_id": wfID, "steps": cmds}})
	if cardID != "" {
		p.InboxItemID = cardID
		s.setPlanInbox(ctx, p.ID, cardID)
	}
	if err != nil {
		w.InboxItemID = cardID
		rc.State = "failed"
		s.putRanCard(ctx, w, rc, "I couldn't start that errand.")
		return nil, err
	}
	if raced {
		return p, nil
	}
	p.State, p.WorkflowID = "launched", wfID
	s.log().Info("errands: plan autorun", "plan", p.ID, "workflow", wfID, "household", p.HouseholdID, "steps", len(p.Steps))
	return p, nil
}

type expireJob struct {
	PlanID      string `json:"plan_id"`
	HouseholdID string `json:"household_id"`
}

func (s *Service) enqueueExpire(ctx context.Context, p *planRow) {
	b, _ := json.Marshal(expireJob{PlanID: p.ID, HouseholdID: p.HouseholdID})
	if _, err := s.Queue.Enqueue(ctx, JobExpire, b, queue.Options{
		DedupKey: JobExpire + ":" + p.ID, RunAt: s.now().Add(s.DraftTTL),
	}); err != nil && !errors.Is(err, queue.ErrDuplicate) {
		s.log().Warn("errands: schedule draft expiry", "plan", p.ID, "err", err)
	}
}

// runPlanJob drafts one plan. Never-vanish: any failure posts a couldn't-plan card.
func (s *Service) runPlanJob(ctx context.Context, job queue.Job) ([]byte, error) {
	var r planJob
	if err := json.Unmarshal(job.Payload, &r); err != nil {
		return nil, queue.Permanent(err)
	}
	_, err := s.CreatePlan(ctx, DraftRequest{HouseholdID: r.HouseholdID, NodeID: r.NodeID, UserID: r.UserID,
		Goal: r.Goal, Menu: menuFromJSON(r.Menu)})
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return nil, err // shutting down: the queue retries it after the restart
	case errors.Is(err, ErrPlan):
		s.log().Info("errands: no usable plan", "goal", r.Goal, "err", err)
		s.postCouldntPlanCard(context.WithoutCancel(ctx), r.HouseholdID, r.UserID,
			fmt.Sprintf(`I couldn't turn "%s" into a plan I can run.`, r.Goal),
			fmt.Sprintf(`I couldn't turn "%s" into a plan I can run — try rephrasing it.`, r.Goal))
	default:
		s.log().Error("errands: draft failed", "goal", r.Goal, "err", err)
		s.postCouldntPlanCard(context.WithoutCancel(ctx), r.HouseholdID, r.UserID,
			"I hit a snag planning that errand.",
			"I hit a snag putting that errand together — try again in a bit.")
	}
	return nil, nil
}

// runExpireJob is the 24 h draft TTL (D40 09.Q6): a still-draft plan becomes expired and its
// card loses its buttons.
func (s *Service) runExpireJob(ctx context.Context, job queue.Job) ([]byte, error) {
	var r expireJob
	if err := json.Unmarshal(job.Payload, &r); err != nil {
		return nil, queue.Permanent(err)
	}
	res, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_errand_plans SET state = 'expired', updated_at = ?
		WHERE id = ? AND household_id = ? AND state = 'draft'`, dbTime(s.now()), r.PlanID, r.HouseholdID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	p, err := s.getPlan(ctx, r.PlanID, r.HouseholdID)
	if err == nil && p != nil {
		s.expirePlanCard(ctx, p)
	}
	return nil, nil
}
