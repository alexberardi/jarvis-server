package errands

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Taught workflows in errands (plan P3 piece 31): a step replays a saved workflow on the
// household's computer. It is either the planner's run_workflow {name, params} (a schedule's
// "run workflow <name> with …" goes through it), or a computer_task carrying the workflow's id
// (taught_workflow_id, params), the step the run_workflow voice tool and the admin's Run button
// start an errand with. The errand's own workflow_id is the errand, never the taught workflow.

const (
	cmdRunWorkflow = "run_workflow"

	// ArgTaughtWorkflow and ArgParams are a computer_task step's replay arguments.
	ArgTaughtWorkflow = "taught_workflow_id"
	ArgParams         = "params"
)

// TaughtWorkflows are the household's saved workflows.
type TaughtWorkflows interface {
	// Visible are the workflows of hh that uid sees: their own and the shared ones (uid nil:
	// the shared ones).
	Visible(ctx context.Context, hh string, uid *int64) ([]computer.Workflow, error)
}

// isComputerStep reports a step that runs on the computer.
func isComputerStep(cmd string) bool {
	cmd = parse.PyStrip(cmd)
	return cmd == cmdComputer || cmd == cmdRunWorkflow
}

// runWorkflowEntry is run_workflow in the planner's menu, naming the workflows the speaker may
// run (nil when there are none).
func (s *Service) runWorkflowEntry(ctx context.Context, hh string, uid *int64) *MenuEntry {
	if s.Computer == nil || s.Computer.Taught == nil {
		return nil
	}
	ws, err := s.Computer.Taught.Visible(ctx, hh, uid)
	if err != nil {
		s.log().Warn("errands: list taught workflows", "household", hh, "err", err)
		return nil
	}
	if len(ws) == 0 {
		return nil
	}
	var names []string
	for _, w := range ws {
		n := strconv.Quote(w.Name)
		if len(w.Params) > 0 {
			var ps []string
			for _, p := range w.Params {
				ps = append(ps, p.Name)
			}
			n += " (params: " + strings.Join(ps, ", ") + ")"
		}
		names = append(names, n)
	}
	return &MenuEntry{Command: cmdRunWorkflow,
		Description: "Run a workflow the user taught Jarvis, on their computer, with new values for its params. " +
			"Saved workflows: " + strings.Join(names, "; ") + ". Args: name, params?",
		Args: servertools.Obj(
			"name", "the saved workflow's name, as listed",
			"params", `an object with a value for each of its params, e.g. {"month": "October 2026"}`,
		)}
}

// taughtStep resolves a replay step's workflow and param values. msg says why it can't run.
func (s *Service) taughtStep(ctx context.Context, rc *runCtx, args *pyjson.Object) (*computer.Workflow, map[string]string, string) {
	if s.Computer.Taught == nil {
		return nil, nil, "saved workflows are unavailable"
	}
	ws, err := s.Computer.Taught.Visible(ctx, rc.householdID, rc.userID)
	if err != nil {
		return nil, nil, "I couldn't load the saved workflows: " + err.Error()
	}
	var wf *computer.Workflow
	if id := parse.PyStrip(truthyStr(args, ArgTaughtWorkflow)); id != "" {
		for i := range ws {
			if ws[i].ID == id {
				wf = &ws[i]
			}
		}
		if wf == nil {
			return nil, nil, "That saved workflow is gone, or isn't shared with you."
		}
	} else {
		name := parse.PyStrip(truthyStr(args, "name"))
		switch m := computer.MatchWorkflows(ws, name); len(m) {
		case 0:
			return nil, nil, fmt.Sprintf("There's no saved workflow called %q.", name)
		case 1:
			wf = &m[0]
		default:
			return nil, nil, fmt.Sprintf("More than one saved workflow matches %q: %s.", name, workflowNames(m))
		}
	}
	params := ParamValues(argValue(args, ArgParams))
	if miss := wf.Missing(params); len(miss) > 0 {
		return nil, nil, fmt.Sprintf("I need the %s to run %q.", strings.Join(miss, " and "), wf.Name)
	}
	return wf, params, ""
}

// workflowNames lists workflows for the user: "“A”, “B” or “C”".
func workflowNames(ws []computer.Workflow) string {
	var ns []string
	for _, w := range ws {
		ns = append(ns, "“"+w.Name+"”")
	}
	if len(ns) == 1 {
		return ns[0]
	}
	return strings.Join(ns[:len(ns)-1], ", ") + " or " + ns[len(ns)-1]
}

func argValue(o *pyjson.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}

// ParamValues reads params as a model or a card sends them: an object (or its JSON text) of
// names to values, numbers written out.
func ParamValues(v any) map[string]string {
	out := map[string]string{}
	switch x := v.(type) {
	case *pyjson.Object:
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			if s := scalarText(val); s != "" {
				out[k] = s
			}
		}
	case map[string]any:
		for k, val := range x {
			if s := scalarText(val); s != "" {
				out[k] = s
			}
		}
	case string:
		if o, err := pyjson.Loads(x); err == nil {
			if obj, ok := o.(*pyjson.Object); ok {
				return ParamValues(obj)
			}
		}
	}
	return out
}

func scalarText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case *big.Int:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// ReplayStep is the computer_task step that replays a saved workflow with params.
func ReplayStep(wf computer.Workflow, params map[string]string) Step {
	args := pyjson.NewObject()
	args.Set(ArgTaughtWorkflow, wf.ID)
	p := pyjson.NewObject()
	for _, prm := range wf.Params {
		if v, ok := params[prm.Name]; ok {
			p.Set(prm.Name, v)
		}
	}
	args.Set(ArgParams, p)
	label := "Run “" + wf.Name + "”"
	return Step{Command: cmdComputer, Args: args, Label: label}
}

// StartRequest is an errand started with its steps already known (no planning, no plan card):
// the run_workflow tool's replay.
type StartRequest struct {
	HouseholdID string
	UserID      *int64
	NodeID      string
	Goal        string
	Steps       []Step
}

// Start launches an errand with r's steps at once and returns its id. Each step still runs
// under the household's rules: a computer task decides every action it takes.
func (s *Service) Start(ctx context.Context, r StartRequest) (string, error) {
	if !s.Available() {
		return "", ErrUnavailable
	}
	if !s.Enabled(ctx, r.HouseholdID) {
		return "", ErrDisabled
	}
	p := &planRow{ID: hexID("pl_"), HouseholdID: r.HouseholdID, UserID: r.UserID, NodeID: r.NodeID,
		Goal: r.Goal, Summary: r.Goal, Steps: r.Steps}
	if err := s.insertPlan(ctx, p); err != nil {
		return "", err
	}
	wfID := hexID("wf_")
	if _, err := s.launch(ctx, p, wfID, "errand", "", nil); err != nil {
		return "", err
	}
	s.log().Info("errands: started", "plan", p.ID, "workflow", wfID, "steps", len(r.Steps))
	return wfID, nil
}
