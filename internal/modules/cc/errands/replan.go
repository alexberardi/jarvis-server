package errands

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Mid-run pause-and-replan (§3.6, kept by D14/D15) and the wait deadlines (D14, D40 09.Q6).

type replanJob struct {
	WorkflowID string `json:"workflow_id"`
	Step       int    `json:"step"`
	Reason     string `json:"reason"`
}

// addStepsOf reads a checkpoint step's pre-baked or stored continuation (args.add_steps).
func addStepsOf(st Step) []Step {
	v, _ := st.Args.Get("add_steps")
	list, _ := v.([]any)
	out := make([]Step, 0, len(list))
	for _, e := range list {
		out = append(out, stepFromValue(e))
	}
	return out
}

// runReplanJob is _resolve_and_decide_replan: resolve the continuation (cursor-aware: from the
// goal and the real results so far, unless the step pre-baked add_steps), persist it on the
// checkpoint, then gate it with WidensEnvelope — a widening change posts a delta card and waits
// for a tap; an in-envelope (or empty) change continues silently (D15). Any failure continues,
// so a run never parks on a broken replan.
func (s *Service) runReplanJob(ctx context.Context, job queue.Job) ([]byte, error) {
	var j replanJob
	if err := json.Unmarshal(job.Payload, &j); err != nil {
		return nil, queue.Permanent(err)
	}
	w, err := s.loadWorkflow(ctx, j.WorkflowID)
	if err != nil {
		return nil, err
	}
	if w == nil || w.State != "waiting" || w.WaitingOn != signalApproval || w.Cursor != j.Step+1 {
		return nil, nil // already resumed, stopped or timed out
	}
	if j.Step < 0 || j.Step >= len(w.Steps) {
		return nil, s.autoContinue(ctx, w.ID, j.Step)
	}
	add := addStepsOf(w.Steps[j.Step])
	if len(add) == 0 {
		menu := s.resolveNodeMenu(ctx, w.NodeID, w.HouseholdID, w.UserID)
		plan, err := s.ReplanFromProgress(ctx, w.Goal, w.Steps[:j.Step], w.Results, j.Reason, menu)
		switch {
		case err == nil:
			add = plan.Steps
		case ctx.Err() != nil:
			return nil, err // shutting down: retry after the restart
		case errors.Is(err, ErrPlan):
			s.log().Info("errands: replan produced no steps — continuing", "workflow", w.ID)
		default:
			s.log().Error("errands: replan failed; continuing", "workflow", w.ID, "err", err)
			return nil, s.autoContinue(ctx, w.ID, j.Step)
		}
	}
	if err := s.storeAddSteps(ctx, w, j.Step, add, j.Reason); err != nil {
		s.log().Error("errands: store replan add_steps", "workflow", w.ID, "err", err)
	}
	amended := append(append([]Step(nil), w.Steps...), add...)
	if len(add) > 0 && WidensEnvelope(w.Steps, amended) {
		fresh, err := s.loadWorkflow(ctx, w.ID)
		if err == nil && fresh != nil && fresh.State == "waiting" {
			s.postReplanCard(ctx, fresh, j.Reason, add)
		}
		return nil, nil
	}
	return nil, s.autoContinue(ctx, w.ID, j.Step)
}

// storeAddSteps is _store_replan_add_steps: the continuation goes onto the checkpoint's
// args.add_steps (and args.reason) so the card, Approve and auto-continue splice the same steps.
func (s *Service) storeAddSteps(ctx context.Context, w *workflowRow, i int, add []Step, reason string) error {
	steps := w.Steps
	args := copyObj(steps[i].Args)
	args.Set("add_steps", stepsToList(add))
	if reason != "" {
		if _, ok := args.Get("reason"); !ok {
			args.Set("reason", reason)
		}
	}
	steps[i].Args = args
	return s.saveSteps(ctx, w.ID, steps, false)
}

// applyReplan is _apply_workflow_replan: splice the checkpoint's add_steps right after it and
// bump the revision (the delta card's Approve becomes stale).
func (s *Service) applyReplan(ctx context.Context, workflowID string, at int) (bool, error) {
	w, err := s.loadWorkflow(ctx, workflowID)
	if err != nil || w == nil {
		return false, err
	}
	if at < 0 || at >= len(w.Steps) {
		return false, nil
	}
	add := addStepsOf(w.Steps[at])
	steps := make([]Step, 0, len(w.Steps)+len(add))
	steps = append(steps, w.Steps[:at+1]...)
	steps = append(steps, add...)
	steps = append(steps, w.Steps[at+1:]...)
	if err := s.saveSteps(ctx, w.ID, steps, true); err != nil {
		return false, err
	}
	return true, nil
}

// autoContinue is _auto_continue_replan: splice and resume with no tap.
func (s *Service) autoContinue(ctx context.Context, workflowID string, at int) error {
	if _, err := s.applyReplan(ctx, workflowID, at); err != nil {
		s.log().Error("errands: in-envelope splice failed", "workflow", workflowID, "err", err)
	}
	return s.enqueueResume(ctx, resumeJob{WorkflowID: workflowID, Step: at, Kind: signalApproval})
}

// --- deadlines ---

type deadlineJob struct {
	WorkflowID string `json:"workflow_id"`
	Step       int    `json:"step"`
	Kind       string `json:"kind"` // approval | phone_call
	SessionID  string `json:"session_id,omitempty"`
}

func (s *Service) enqueueDeadline(ctx context.Context, j deadlineJob, after time.Duration) {
	s.enqueueDeadlineAt(ctx, j, s.now().Add(after))
}

func (s *Service) enqueueDeadlineAt(ctx context.Context, j deadlineJob, at time.Time) {
	b, _ := json.Marshal(j)
	if _, err := s.Queue.Enqueue(ctx, JobDeadline, b, queue.Options{RunAt: at}); err != nil {
		s.log().Error("errands: schedule wait deadline", "workflow", j.WorkflowID, "err", err)
	}
}

const callTimeoutText = "the call wasn't completed in time"

// runDeadlineJob ends a wait that outlived its deadline. An approval wait fails the run with a
// card (D14). A phone wait is the safety net for a missed terminal hook, and otherwise fails
// 60 min after the user confirmed the call (D40 09.Q6: legacy counted from the draft). Either
// way the errand's undialled calls are declined.
func (s *Service) runDeadlineJob(ctx context.Context, job queue.Job) ([]byte, error) {
	var j deadlineJob
	if err := json.Unmarshal(job.Payload, &j); err != nil {
		return nil, queue.Permanent(err)
	}
	w, err := s.loadWorkflow(ctx, j.WorkflowID)
	if err != nil {
		return nil, err
	}
	if w == nil || w.State != "waiting" || w.Cursor != j.Step+1 || w.WaitingOn != j.Kind {
		return nil, nil // resolved in time
	}
	label := ""
	if j.Step >= 0 && j.Step < len(w.Steps) {
		label = w.Steps[j.Step].Label
	}
	switch j.Kind {
	case signalApproval:
		s.declineCalls(ctx, w.ID)
		_, err := s.deliver(ctx, resumeJob{WorkflowID: w.ID, Step: j.Step, Kind: signalApproval,
			Fail: &failOutcome{Command: cmdReplan, Label: firstNonEmpty(label, "Replan"),
				Error: "the change wasn't approved in time"}})
		return nil, err
	case signalPhone:
		var o CallOutcome
		found := false
		if s.Phone != nil && j.SessionID != "" {
			o, found, err = s.Phone.CallStatus(ctx, j.SessionID)
			if err != nil {
				return nil, err
			}
		}
		if found && o.Terminal() {
			o.WorkflowID, o.Step = w.ID, j.Step
			_, err := s.deliver(ctx, resumeJob{WorkflowID: w.ID, Step: j.Step, Kind: signalPhone, Call: &o})
			return nil, err
		}
		if found {
			next := s.now().Add(s.PhoneDeadline) // not confirmed yet: its own draft expiry ends it
			if !o.ConfirmedAt.IsZero() {
				next = o.ConfirmedAt.Add(s.PhoneDeadline)
			}
			if next.After(s.now()) {
				s.enqueueDeadlineAt(ctx, j, next)
				return nil, nil
			}
		}
		s.declineCalls(ctx, w.ID)
		_, err := s.deliver(ctx, resumeJob{WorkflowID: w.ID, Step: j.Step, Kind: signalPhone, Call: &CallOutcome{
			SessionID: j.SessionID, WorkflowID: w.ID, Step: j.Step, State: "failed",
			ContactName: o.ContactName, ErrorMessage: callTimeoutText,
		}})
		return nil, err
	}
	return nil, nil
}

// declineCalls is _decline_pending_workflow_calls (best-effort).
func (s *Service) declineCalls(ctx context.Context, workflowID string) bool {
	if s.Phone == nil {
		return false
	}
	declined, err := s.Phone.DeclineErrandCalls(ctx, workflowID)
	if err != nil {
		s.log().Error("errands: decline pending calls", "workflow", workflowID, "err", err)
		return false
	}
	return declined
}

// Register installs the errand job handlers on the queue (before it starts). Planning and the
// replan share the background LLM; one worker each keeps a 6000-token think budget from
// stacking up (PLAN §3.2).
func (s *Service) Register(q *queue.Queue) {
	s.defaults()
	s.Queue = q
	long := 15 * time.Minute
	q.Register(JobPlan, queue.Handler{Run: s.runPlanJob, Concurrency: 1, MaxAttempts: 2, Lease: long})
	q.Register(JobReplan, queue.Handler{Run: s.runReplanJob, Concurrency: 1, MaxAttempts: 2, Lease: long})
	// A drive is not retried after it started (a step may have acted); a lost lease leaves the
	// run "running", which startup recovery fails with a card.
	q.Register(JobResume, queue.Handler{Run: s.runResumeJob, Concurrency: 4, MaxAttempts: 3, Lease: time.Hour})
	q.Register(JobDeadline, queue.Handler{Run: s.runDeadlineJob, Concurrency: 1, Lease: time.Hour})
	q.Register(JobExpire, queue.Handler{Run: s.runExpireJob, Concurrency: 1})
}

// Init applies the defaults without a queue (tests, or a build with no queue).
func (s *Service) Init() { s.defaults() }
