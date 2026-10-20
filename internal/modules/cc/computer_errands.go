package cc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Computer tasks in errands (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 11): the errand
// runner's computer_task step drives computer.Loop, whose executor is the agent's
// computer_observe / computer_act / computer_stop over the node bus, on a computer of the
// household with computer control on.

const (
	computerObserveTimeout = 60 * time.Second
	computerActTimeout     = 3 * time.Minute // a batch of up to 12 actions, each with its waits
	computerStopTimeout    = 10 * time.Second
)

// computerTasks is the errand runner's computer_task wiring.
func (m *Module) computerTasks() *errands.ComputerTasks {
	store := &computer.Store{DB: m.deps.DB, Now: m.now}
	loop := &computer.Loop{
		LLM: m.LLM, Exec: computerExec{m: m, store: store}, Policy: m.policy, Store: store,
		Approvals: computerApprovals{m}, Settings: m.settings, Now: m.now,
		Models: computer.Models{
			Endpoint: func(ctx context.Context, label string) (llm.Endpoint, error) {
				if m.Endpoints == nil {
					return llm.Endpoint{}, errors.New("no llm endpoints")
				}
				return m.Endpoints(ctx, label)
			},
			VisionLabel: m.visionLabel,
		},
	}
	if m.autonomy != nil {
		loop.Audit = m.autonomy
	}
	return &errands.ComputerTasks{Loop: loop, Targets: m.computerTargets}
}

// computerTargets are the computers a computer task may run on (computer control on), or why
// there are none.
func (m *Module) computerTargets(ctx context.Context, hh string) ([]string, string) {
	cc, err := m.computerControl(ctx, hh)
	if err != nil {
		m.deps.Log.Warn("cc: computer control lookup", "household", hh, "err", err)
		return nil, "I couldn't check which computers I can use."
	}
	if ids := cc.targets(); len(ids) > 0 {
		return ids, ""
	}
	return nil, cc.offMessage()
}

// computerDefinitions are the computer task settings the loop and the errand step read.
func computerDefinitions() []settings.Definition {
	positive := func(v any) error {
		if n, ok := v.(int64); ok && n < 1 {
			return errors.New("must be at least 1")
		}
		return nil
	}
	return []settings.Definition{
		{Key: computer.SettingMaxRounds, Category: "computer", Type: settings.Int, Default: int64(40), Validate: positive,
			Description: "Most rounds (look, decide, act) one computer task may take before it gives up."},
		{Key: computer.SettingMaxMinutes, Category: "computer", Type: settings.Int, Default: int64(20), Validate: positive,
			Description: "Most minutes one computer task may run before it gives up."},
		{Key: computer.SettingModel, Category: "computer", Type: settings.String, Default: computer.ModelAuto,
			Description: "Which model drives computer tasks: auto (the largest vision model), live or background.",
			Validate: func(v any) error {
				switch v {
				case computer.ModelAuto, computer.ModelLive, computer.ModelBackground:
					return nil
				}
				return errors.New("must be auto, live or background")
			}},
		{Key: computer.SettingAutoApproveCents, Category: "computer", Type: settings.Int, Default: int64(0),
			Description: "Spend limit in cents: a computer task's checkout at most this much is approved without " +
				"asking (the progress card says so). 0 = always ask.",
			Validate: func(v any) error {
				if n, ok := v.(int64); ok && n < 0 {
					return errors.New("must be 0 or more")
				}
				return nil
			}},
	}
}

// computerApprovals mints the loop's approval tokens (piece 5).
type computerApprovals struct{ m *Module }

func (a computerApprovals) Mint(ctx context.Context, hh, nodeID, runID, digest string) (string, error) {
	return a.m.mintApproval(ctx, hh, nodeID, runID, digest, 0)
}

// computerExec is the loop's executor: the agent's computer tools on the run's computer.
type computerExec struct {
	m     *Module
	store *computer.Store
}

// node is the computer a run is on.
func (e computerExec) node(ctx context.Context, runID string) (*computer.Run, error) {
	run, err := e.store.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.NodeID == "" {
		return nil, errors.New("the task has no computer")
	}
	return run, nil
}

func toArgs(v any) *pyjson.Object {
	b, _ := json.Marshal(v)
	o, err := pyjson.Loads(string(b))
	if obj, ok := o.(*pyjson.Object); ok && err == nil {
		return obj
	}
	return pyjson.NewObject()
}

// nodeFailure is a tool output's failure, or nil when it succeeded.
func nodeFailure(out *pyjson.Object) error {
	if out == nil {
		return errors.New("no answer")
	}
	if v, ok := out.Get("success"); ok && v == false {
		msg := strings.TrimSpace(pyjson.Str(firstOf(out, "error", "message")))
		if msg == "" {
			msg = "the computer reported a failure"
		}
		if code := pyjson.Str(firstOf(out, "error_code")); code != "" {
			return &computerToolErr{code: code, msg: msg}
		}
		return errors.New(msg)
	}
	return nil
}

type computerToolErr struct{ code, msg string }

func (e *computerToolErr) Error() string { return e.msg }

func firstOf(o *pyjson.Object, keys ...string) any {
	for _, k := range keys {
		if v, ok := o.Get(k); ok && v != nil {
			return v
		}
	}
	return ""
}

func (e computerExec) Observe(ctx context.Context, o computer.Observe) (computer.Observation, error) {
	run, err := e.node(ctx, o.RunID)
	if err != nil {
		return computer.Observation{}, err
	}
	args := map[string]any{"run_id": o.RunID, "target": o.Target, "snapshot": o.Snapshot, "screenshot": o.Screenshot}
	nodes := errandNodes{e.m}
	out, imgs, err := nodes.RunToolImages(ctx, run.NodeID, computeruse.ToolObserve, toArgs(args), run.UserID, computerObserveTimeout)
	if err != nil {
		return computer.Observation{}, err
	}
	var te *computerToolErr
	if ferr := nodeFailure(out); errors.As(ferr, &te) && te.code == "invalid_argument" && o.Target.Site != "" &&
		strings.Contains(te.msg, "no window") {
		// The task's first look: open its window on the site.
		args["url"] = "https://" + o.Target.Site
		out, imgs, err = nodes.RunToolImages(ctx, run.NodeID, computeruse.ToolObserve, toArgs(args), run.UserID, computerObserveTimeout)
		if err != nil {
			return computer.Observation{}, err
		}
	}
	if err := nodeFailure(out); err != nil {
		return computer.Observation{}, err
	}
	var parsed struct {
		URL      string `json:"url"`
		Title    string `json:"title"`
		App      string `json:"app"`
		Snapshot *struct {
			Gen      int                       `json:"gen"`
			Elements []computeruse.SnapElement `json:"elements"`
		} `json:"snapshot"`
		Capture *struct {
			CaptureID string `json:"capture_id"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"capture"`
	}
	if err := json.Unmarshal([]byte(pyjson.Dumps(out, false)), &parsed); err != nil {
		return computer.Observation{}, fmt.Errorf("unreadable observation: %w", err)
	}
	obs := computer.Observation{URL: parsed.URL, Title: parsed.Title, App: parsed.App}
	if parsed.Snapshot != nil {
		obs.Gen = parsed.Snapshot.Gen
		for _, el := range parsed.Snapshot.Elements {
			obs.Elements = append(obs.Elements, computer.Element{Ref: el.Ref, Role: el.Role, Name: el.Name, Value: el.Value,
				Type: el.Type, Autocomplete: el.Autocomplete, State: el.State, Secure: el.Element().Secure})
		}
	}
	if len(imgs) > 0 && parsed.Capture != nil {
		obs.Image = &computer.Image{MIME: imgs[0].MIME, Data: imgs[0].Data, CaptureID: parsed.Capture.CaptureID,
			Width: parsed.Capture.Width, Height: parsed.Capture.Height}
	}
	return obs, nil
}

func (e computerExec) Act(ctx context.Context, b computer.Batch) (computer.ActResult, error) {
	run, err := e.node(ctx, b.RunID)
	if err != nil {
		return computer.ActResult{}, err
	}
	args := map[string]any{"run_id": b.RunID, "target": b.Target, "actions": b.Actions,
		"blocked": map[string]any{"sites": []string{}, "apps": []string{}}}
	if b.Resume {
		args["resume"] = true
	}
	out, _, err := errandNodes{e.m}.RunToolImages(ctx, run.NodeID, computeruse.ToolAct, toArgs(args), run.UserID, computerActTimeout)
	if err != nil {
		return computer.ActResult{}, err
	}
	var parsed struct {
		Results []computer.StepResult `json:"results"`
		Paused  bool                  `json:"paused"`
	}
	if err := json.Unmarshal([]byte(pyjson.Dumps(out, false)), &parsed); err != nil {
		return computer.ActResult{}, fmt.Errorf("unreadable result: %w", err)
	}
	if len(parsed.Results) == 0 {
		if err := nodeFailure(out); err != nil {
			return computer.ActResult{}, err
		}
	}
	return computer.ActResult{Results: parsed.Results, Paused: parsed.Paused}, nil
}

func (e computerExec) Files(ctx context.Context, f computer.Files) (computer.FileInfo, error) {
	run, err := e.node(ctx, f.RunID)
	if err != nil {
		return computer.FileInfo{}, err
	}
	out := errandNodes{e.m}.RunTool(ctx, run.NodeID, computeruse.ToolFiles,
		toArgs(map[string]any{"run_id": f.RunID, "op": f.Op, "path": f.Path}), run.UserID, "", computerObserveTimeout)
	if err := nodeFailure(out); err != nil {
		return computer.FileInfo{}, err
	}
	var parsed struct {
		Path    string `json:"path"`
		Bytes   int64  `json:"bytes"`
		Text    string `json:"text"`
		Entries []struct {
			Name  string `json:"name"`
			Dir   bool   `json:"dir"`
			Bytes int64  `json:"bytes"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(pyjson.Dumps(out, false)), &parsed); err != nil {
		return computer.FileInfo{}, fmt.Errorf("unreadable files answer: %w", err)
	}
	info := computer.FileInfo{Path: parsed.Path, Bytes: parsed.Bytes, Text: parsed.Text}
	for _, en := range parsed.Entries {
		info.Entries = append(info.Entries, computer.FileEntry{Name: en.Name, Dir: en.Dir, Bytes: en.Bytes})
	}
	return info, nil
}

func (e computerExec) Stop(ctx context.Context, runID string) {
	run, err := e.node(ctx, runID)
	if err != nil {
		return
	}
	out := errandNodes{e.m}.RunTool(ctx, run.NodeID, computeruse.ToolStop, toArgs(map[string]any{"run_id": runID}),
		run.UserID, "", computerStopTimeout)
	if err := nodeFailure(out); err != nil {
		e.m.deps.Log.Warn("cc: computer_stop", "run", runID, "node", run.NodeID, "err", err)
	}
}
