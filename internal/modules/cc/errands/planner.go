package errands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The planner (legacy services/errand_planner.py, docs/cc/09 §3.2): one LLM call maps a goal
// and a menu of building blocks (node commands ∪ gated server tools) to a reviewable step
// list. The planner never executes, and a step naming a command outside the menu is dropped,
// not run. Prompts are byte-exact (D22, 09.Q11).

// MenuEntry is one building block the planner may choose: a node command or a server tool.
// Args maps each argument name to its description, in the source order (prompt bytes).
type MenuEntry struct {
	Command     string
	Description string
	Args        *pyjson.Object
	IsRisky     bool
}

// commandMenu is the FALLBACK menu (COMMAND_MENU), used only when no real menu can be sourced.
// Its set_reminder arg names are legacy inventions (§8.11), kept as-is.
func commandMenu() []MenuEntry {
	e := func(cmd, desc string, kv ...any) MenuEntry {
		return MenuEntry{Command: cmd, Description: desc, Args: servertools.Obj(kv...)}
	}
	return []MenuEntry{
		e("get_weather", "Weather for the user's location on a given day.",
			"resolved_datetimes", `list of day keywords, e.g. ["today"]`),
		e("get_news", "Top news headlines by category.",
			"category", "one of general/business/technology/sports/entertainment/health"),
		e("get_calendar_events", "The user's calendar events for a day.",
			"resolved_datetimes", `list of day keywords, e.g. ["today"]`),
		e("set_reminder", "Create a reminder for the user.",
			"text", "what to remind about", "when", "natural-language time, e.g. 'tomorrow at 9am'"),
		e("get_device_status", "Read the state of a smart-home device.",
			"device_name", "the device's name"),
		e("control_device", "Act on a smart-home device.",
			"device_name", "the device's name", "action", "turn_on / turn_off / lock / unlock"),
	}
}

// menuDeny are commands (node or server) that make no sense as a headless background step
// (ERRAND_MENU_DENY). run_errand is load-bearing: without it an errand could plan an errand.
var menuDeny = map[string]bool{
	"routine": true, "chat": true, "answer_question": true, "tell_joke": true, "act_on_items": true,
	"send_link": true, "control_node": true,
	"run_errand": true, "request_validation": true, "identify_speaker": true,
	"resolve_relative_date": true, "get_command_examples": true,
}

// controlCommands are control-flow commands the planner may emit that are not capabilities;
// they always pass validation and are never risky.
var controlCommands = map[string]bool{cmdReplan: true}

const (
	cmdReplan  = "request_replan"
	cmdPhone   = "make_phone_call"
	cmdWaitFor = "wait_for"
)

// NodeMenu is build_errand_menu: a node's available commands (CommandDefinition dicts, as the
// conversation cached them or report_tools returned them) in the menu shape, minus the deny
// list.
func NodeMenu(available []*pyjson.Object) []MenuEntry {
	var menu []MenuEntry
	for _, c := range available {
		if c == nil {
			continue
		}
		name := parse.PyStrip(objStr(c, "command_name"))
		if name == "" || menuDeny[name] {
			continue
		}
		args := pyjson.NewObject()
		if pv, ok := c.Get("parameters"); ok {
			list, _ := pv.([]any)
			for _, p := range list {
				po, ok := p.(*pyjson.Object)
				if !ok {
					continue
				}
				pname := objStr(po, "name")
				if pname == "" {
					continue
				}
				args.Set(pname, orStr(po, "description", "type"))
			}
		}
		menu = append(menu, MenuEntry{
			Command: name, Description: objStr(c, "description"), Args: args, IsRisky: objTruthy(c, "is_risky"),
		})
	}
	return menu
}

// Setting keys the menu reads (the voice path's gates).
const (
	settingWebSearch     = "web_search.enabled"
	settingMemoryEnabled = "memory.enabled"
	settingRecallEnabled = "memory.recall_enabled"
)

// enabledServerTools is enabled_errand_server_tools: the voice path's gates, except that
// make_phone_call needs phone calls actually enabled and run_errand is excluded.
func (s *Service) enabledServerTools(ctx context.Context, hh string, speaker *int64) []string {
	var names []string
	if s.Phone != nil && s.Phone.Enabled(ctx, hh) {
		names = append(names, cmdPhone)
	}
	sc := settings.Scope{HouseholdID: hh}
	if s.Settings != nil && s.Settings.Bool(ctx, settingWebSearch, sc) {
		names = append(names, "deep_research", "quick_search")
	}
	memOn := s.Settings == nil || s.Settings.Bool(ctx, settingMemoryEnabled, sc)
	if speaker != nil && memOn {
		names = append(names, "remember", "forget")
		if s.Settings == nil || s.Settings.Bool(ctx, settingRecallEnabled, sc) {
			names = append(names, "recall")
		}
	}
	out := names[:0]
	for _, n := range names {
		if !menuDeny[n] {
			out = append(out, n)
		}
	}
	return out
}

// serverMenu is build_server_tool_menu: the gated server tools as menu entries, from each
// registered tool's definition. An unregistered name is skipped.
func (s *Service) serverMenu(ctx context.Context, hh string, speaker *int64) []MenuEntry {
	var menu []MenuEntry
	if s.Tools == nil {
		return nil
	}
	for _, name := range s.enabledServerTools(ctx, hh, speaker) {
		t, ok := s.Tools.Get(name)
		if !ok {
			continue
		}
		fn := objObj(t.Definition(), "function")
		params := objObj(fn, "parameters")
		props := objObj(params, "properties")
		args := pyjson.NewObject()
		if props != nil {
			for _, k := range props.Keys() {
				if k == "" {
					continue
				}
				v, _ := props.Get(k)
				spec, _ := v.(*pyjson.Object)
				args.Set(k, orStr(spec, "description", "type"))
			}
		}
		cmd := objStr(fn, "name")
		if cmd == "" {
			cmd = name
		}
		menu = append(menu, MenuEntry{Command: cmd, Description: objStr(fn, "description"), Args: args, IsRisky: toolRisky(t)})
	}
	return menu
}

// RiskyTool is implemented by server tools that spend money, contact a stranger or act
// irreversibly (legacy IServerTool.is_risky). make_phone_call is risky even when its port
// doesn't implement it (legacy make_phone_call_tool.is_risky = True).
type RiskyTool interface{ IsRisky() bool }

func toolRisky(t servertools.ServerTool) bool {
	if r, ok := t.(RiskyTool); ok {
		return r.IsRisky()
	}
	return t.Name() == cmdPhone
}

// FullMenu is build_full_errand_menu: node commands ∪ enabled server tools; on a name
// collision the server tool wins (the executor would run it).
func (s *Service) FullMenu(ctx context.Context, available []*pyjson.Object, hh string, speaker *int64) []MenuEntry {
	server := s.serverMenu(ctx, hh, speaker)
	names := map[string]bool{}
	for _, c := range server {
		names[c.Command] = true
	}
	var out []MenuEntry
	for _, c := range NodeMenu(available) {
		if !names[c.Command] {
			out = append(out, c)
		}
	}
	return append(out, server...)
}

// --- prompts (byte-exact) ---

const fabricationGuardrail = "Do NOT invent specific data you weren't given — a made-up email address, " +
	"phone number, or mailing address. You MAY use the user's own words to name " +
	"who to contact (e.g. call the business, or 'the pharmacy' they mentioned); " +
	"the user reviews and confirms every call before it is placed, so keep the " +
	"step. Only leave a step out if performing it would require inventing a " +
	"specific contact detail the user never provided.\n\n"

const decompositionRule = "Create a SEPARATE step for EACH distinct action the user asks for, and keep " +
	"the order they gave. If they say 'first do X, then Y' — or list several " +
	"things — that is MULTIPLE steps, one per action. Do not merge two actions " +
	"into one step and do not drop an action. Only include actions the user " +
	"actually asked for.\n\n"

const closingDirective = "Think step by step: list each action the user wants and its order, then output " +
	"ONLY the final JSON object and nothing after it."

const replanCheckpointRule = "CHECKPOINT (rare): if — and ONLY if — a later action genuinely depends on the " +
	"RESULT of an earlier step that cannot be known yet (e.g. 'check the weather and " +
	"IF it's going to rain remind me to bring an umbrella', or 'call the pharmacy and " +
	"then do the right follow-up based on what they say'), end the plan at that point " +
	"with a single checkpoint step " +
	`{"command": "request_replan", "args": {"reason": "<what to decide once the ` +
	`earlier steps have run>"}, "label": "<short label>"} and DO NOT try to guess the ` +
	"steps that come after it — they will be planned once the earlier results are " +
	"known. Most goals do NOT need a checkpoint; only use one when the next actions " +
	"truly cannot be chosen until the earlier steps have run.\n\n"

func menuText(menu []MenuEntry) string {
	lines := make([]string, len(menu))
	for i, c := range menu {
		args := c.Args
		if args == nil {
			args = pyjson.NewObject()
		}
		lines[i] = "- " + c.Command + ": " + c.Description + " | args: " + pyjson.Dumps(args, true)
	}
	return strings.Join(lines, "\n")
}

// BuildPrompt is _build_prompt.
func BuildPrompt(goal string, menu []MenuEntry) string {
	return "You are Jarvis's errand planner. Turn the user's goal into an ordered " +
		"list of steps, choosing ONLY from the available commands below. Fill each " +
		"step's args from that command's arg spec, using the user's own words, and " +
		"give each step a short human-readable label.\n\n" +
		decompositionRule +
		replanCheckpointRule +
		"Available commands:\n" + menuText(menu) + "\n\n" +
		"User's goal: " + goal + "\n\n" +
		fabricationGuardrail +
		`Return a JSON object: {"summary": "<one-line plain-English plan>", ` +
		`"steps": [{"command": "<name>", "args": {...}, "label": "<short label>"}]}. ` +
		"No markdown.\n\n" +
		closingDirective
}

// BuildRefinePrompt is _build_refine_prompt.
func BuildRefinePrompt(summary string, current []Step, instruction string, menu []MenuEntry) string {
	return "You are Jarvis's errand planner revising an EXISTING plan. Apply the " +
		"user's change to the current plan, keeping the parts they didn't ask to " +
		"change and choosing ONLY from the available commands.\n\n" +
		decompositionRule +
		"Current plan: " + summary + "\n" +
		"Current steps: " + stepsDumps(current) + "\n\n" +
		"Available commands:\n" + menuText(menu) + "\n\n" +
		"The user wants this change: " + instruction + "\n\n" +
		fabricationGuardrail +
		`Return the revised JSON object: {"summary": "<one-line plan>", ` +
		`"steps": [{"command": "<name>", "args": {...}, "label": "<short label>"}]}. ` +
		"No markdown.\n\n" +
		closingDirective
}

// BuildReplanPrompt is _build_replan_prompt.
func BuildReplanPrompt(goal string, done []Step, progress, reason string, menu []MenuEntry) string {
	return "You are Jarvis's errand planner CONTINUING an errand that is already underway. " +
		"Some steps have run — here is what ACTUALLY happened. Decide the NEXT steps " +
		"needed to finish the goal BASED ON those results, choosing ONLY from the " +
		"available commands. Return ONLY the steps still to run (do not repeat the ones " +
		"already done).\n\n" +
		decompositionRule +
		"Goal: " + goal + "\n" +
		"Steps already run: " + stepsDumps(done) + "\n" +
		"What happened:\n" + progress + "\n\n" +
		"Why we paused to re-plan: " + reason + "\n\n" +
		"Available commands:\n" + menuText(menu) + "\n\n" +
		fabricationGuardrail +
		"If the results mean nothing further is needed, return an EMPTY steps list. " +
		`Return a JSON object: {"summary": "<one-line plan for the remaining work>", ` +
		`"steps": [{"command": "<name>", "args": {...}, "label": "<short label>"}]}. ` +
		"No markdown.\n\n" +
		closingDirective
}

// --- the LLM call ---

// ErrPlan is a planning failure the user can fix by rephrasing (legacy ValueError): an empty
// or unparseable response, or no usable step.
var ErrPlan = errors.New("errands: no usable plan")

// defaultPlannerMaxTokens is errands.planner_max_tokens' default. Legacy used 6000, measured on
// Qwen3.5-9B (~1900 think tokens); Qwen3-8B thought past 6000 on "check the weather, then set a
// timer if it's sunny" and never planned (jarvis-dev, 2026-10-07).
const defaultPlannerMaxTokens = 12000

func (s *Service) plannerMaxTokens(ctx context.Context) int {
	if s.Settings != nil {
		if n := s.Settings.Int(ctx, SettingPlannerMaxTokens, settings.Scope{}); n > 0 {
			return int(n)
		}
	}
	return defaultPlannerMaxTokens
}

// stripThink keeps only what follows the last </think>.
func stripThink(text string) string {
	if i := strings.LastIndex(text, "</think>"); i >= 0 {
		return text[i+len("</think>"):]
	}
	return text
}

// stripFences is _strip_fences: drop ``` fence lines, then cut to the outer {…}.
func stripFences(text string) string {
	text = parse.PyStrip(text)
	if strings.HasPrefix(text, "```") {
		var kept []string
		for _, ln := range strings.Split(text, "\n") {
			if !strings.HasPrefix(parse.PyStrip(ln), "```") {
				kept = append(kept, ln)
			}
		}
		text = parse.PyStrip(strings.Join(kept, "\n"))
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start != -1 && end != -1 && end > start {
		return text[start : end+1]
	}
	return text
}

// runPlanner is _run_planner: one background-label call (temperature 0, 6000 max tokens,
// thinking left to the label: LD8), parsed to a JSON object. Planning failures wrap ErrPlan;
// an LLM error is returned as-is (infra).
func (s *Service) runPlanner(ctx context.Context, prompt string) (*pyjson.Object, error) {
	if s.LLM == nil {
		return nil, errors.New("errands: no LLM")
	}
	temp, maxTok := 0.0, s.plannerMaxTokens(ctx)
	start := time.Now()
	req := llm.ChatRequest{
		Label: "background", Temperature: &temp, MaxTokens: &maxTok,
		Messages: []llm.Message{{Role: "user", Content: llm.TextContent(prompt)}},
	}
	resp, err := s.LLM.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	s.log().Info("errands: planner call", "completion_tokens", resp.Usage.CompletionTokens,
		"prompt_tokens", resp.Usage.PromptTokens, "max_tokens", maxTok, "finish", resp.FinishReason,
		"elapsed", time.Since(start).Round(100*time.Millisecond))
	if resp.Content == "" && resp.FinishReason == "length" {
		// The model thought through the whole budget without answering (Qwen3-8B does on
		// simple two-step goals; legacy measured ~1900 think tokens on Qwen3.5-9B). Retry
		// once without thinking: a plan that may merge steps beats no plan.
		s.log().Warn("errands: planner thought past max_tokens; retrying without thinking")
		off := 0
		req.ReasoningBudget = &off
		if resp, err = s.LLM.Chat(ctx, req); err != nil {
			return nil, err
		}
	}
	raw := resp.Content
	if raw == "" {
		s.log().Warn("errands: planner EMPTY response", "finish", resp.FinishReason)
		return nil, fmt.Errorf("%w: planner returned an empty response", ErrPlan)
	}
	v, err := pyjson.Loads(stripFences(stripThink(raw)))
	if err != nil {
		s.log().Warn("errands: planner UNPARSEABLE JSON", "head", truncRunes(raw, 400))
		return nil, fmt.Errorf("%w: planner returned invalid JSON: %v", ErrPlan, err)
	}
	obj, ok := v.(*pyjson.Object)
	if !ok {
		// Legacy data.get on a non-dict raised AttributeError: an infra-style failure.
		return nil, errors.New("errands: planner returned a non-object")
	}
	return obj, nil
}

// Plan is a planner result.
type Plan struct {
	Summary string
	Steps   []Step
}

// planFromData is _plan_from_data: keep steps whose command is allowed, stamp is_risky.
func planFromData(data *pyjson.Object, allowed map[string]bool, fallbackSummary string, risky map[string]bool, requireNonEmpty bool) (Plan, error) {
	var steps []Step
	sv, _ := data.Get("steps")
	list, _ := sv.([]any)
	for _, raw := range list {
		so, ok := raw.(*pyjson.Object)
		if !ok {
			continue
		}
		cmd := parse.PyStrip(objStr(so, "command"))
		if !allowed[cmd] {
			continue
		}
		args := pyjson.NewObject()
		if av, ok := so.Get("args"); ok {
			if ao, ok := av.(*pyjson.Object); ok {
				args = copyObj(ao)
			}
		}
		label := truthyStr(so, "label")
		if label == "" {
			label = cmd
		}
		steps = append(steps, Step{Command: cmd, Args: args, Label: label, IsRisky: risky[cmd]})
	}
	if len(steps) == 0 && requireNonEmpty {
		return Plan{}, fmt.Errorf("%w: planner produced no usable steps", ErrPlan)
	}
	summary := truthyStr(data, "summary")
	if summary == "" {
		summary = fallbackSummary
	}
	return Plan{Summary: parse.PyStrip(summary), Steps: steps}, nil
}

func allowedOf(menu []MenuEntry) (map[string]bool, map[string]bool) {
	allowed := map[string]bool{}
	risky := map[string]bool{}
	for _, c := range menu {
		allowed[c.Command] = true
		risky[c.Command] = c.IsRisky
	}
	for c := range controlCommands {
		allowed[c] = true
	}
	return allowed, risky
}

// PlanErrand is plan_errand. An empty menu falls back to the static menu.
func (s *Service) PlanErrand(ctx context.Context, goal string, menu []MenuEntry) (Plan, error) {
	if len(menu) == 0 {
		menu = commandMenu()
	}
	data, err := s.runPlanner(ctx, BuildPrompt(goal, menu))
	if err != nil {
		return Plan{}, err
	}
	allowed, risky := allowedOf(menu)
	return planFromData(data, allowed, goal, risky, true)
}

// RefinePlan is refine_errand_plan: the allowed set also keeps the plan's own commands, so a
// degraded menu can't strip steps the user didn't ask to change.
func (s *Service) RefinePlan(ctx context.Context, summary string, current []Step, instruction string, menu []MenuEntry) (Plan, error) {
	if len(menu) == 0 {
		menu = commandMenu()
	}
	data, err := s.runPlanner(ctx, BuildRefinePrompt(summary, current, instruction, menu))
	if err != nil {
		return Plan{}, err
	}
	allowed, risky := allowedOf(menu)
	for _, st := range current {
		if st.Command != "" {
			allowed[st.Command] = true
		}
	}
	return planFromData(data, allowed, summary, risky, true)
}

// ReplanFromProgress is replan_from_progress: the continuation after a checkpoint, which may be
// empty ("nothing more to do").
func (s *Service) ReplanFromProgress(ctx context.Context, goal string, done []Step, results []Result, reason string, menu []MenuEntry) (Plan, error) {
	if len(menu) == 0 {
		menu = commandMenu()
	}
	progress := summarizeProgress(done, results)
	data, err := s.runPlanner(ctx, BuildReplanPrompt(goal, done, progress, reason, menu))
	if err != nil {
		return Plan{}, err
	}
	allowed, risky := allowedOf(menu)
	fallback := truthyStr(data, "summary")
	if fallback == "" {
		fallback = "continue"
	}
	return planFromData(data, allowed, fallback, risky, false)
}

// compactResultData is _compact_result_data (cap 320): the step's real result for the replan.
func compactResultData(data *pyjson.Object, limit int) string {
	if data == nil {
		return ""
	}
	skip := map[string]bool{"success": true, "error": true, "actions": true, "status": true, "message": true}
	var parts []string
	total := 0
	for _, k := range data.Keys() {
		if skip[k] {
			continue
		}
		v, _ := data.Get(k)
		var val string
		if isScalar(v) {
			val = pyjson.Str(v)
		} else {
			val = pyjson.Dumps(v, true)
		}
		p := k + "=" + val
		parts = append(parts, p)
		total += runeLen(p)
		if total > limit {
			break
		}
	}
	return truncRunes(strings.Join(parts, "; "), limit)
}

// summarizeProgress is _summarize_progress. It pairs done[i] with results[i] (legacy §8.10).
func summarizeProgress(done []Step, results []Result) string {
	var lines []string
	for i, st := range done {
		label := st.Label
		if label == "" {
			label = st.Command
		}
		if label == "" {
			label = fmt.Sprintf("step %d", i+1)
		}
		var r Result
		if i < len(results) {
			r = results[i]
		}
		detail := firstNonEmpty(r.Message, r.Summary, compactResultData(r.Data, 320), r.Error)
		if detail == "" {
			if r.Success {
				detail = "done"
			} else {
				detail = "no result"
			}
		}
		lines = append(lines, "- "+label+": "+detail)
	}
	if len(lines) == 0 {
		return "(nothing has run yet)"
	}
	return strings.Join(lines, "\n")
}
