package builtins

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

var routineSpec = node.Spec{
	Name:        "routine",
	Description: "Execute a multi-step voice routine (e.g. good morning, good night). Runs sub-commands and composes a natural spoken response.",
	Params: []node.Param{{Name: "routine_name", Type: "string", Required: true,
		Description: "Name of the routine to execute (e.g. good_morning, good_night)."}},
	Keywords: []string{"routine", "good morning", "good night", "bedtime", "start my day", "morning routine", "briefing",
		"daily briefing", "morning briefing", "nightly briefing", "evening briefing", "nightly update", "catch me up", "daily update"},
	Examples: []node.Example{
		{VoiceCommand: "Good morning", Params: map[string]any{"routine_name": "good_morning"}, Primary: true},
		{VoiceCommand: "Good night", Params: map[string]any{"routine_name": "good_night"}},
		{VoiceCommand: "Give me my morning briefing", Params: map[string]any{"routine_name": "morning_briefing"}},
		{VoiceCommand: "Give me my nightly briefing", Params: map[string]any{"routine_name": "nightly_briefing"}},
	},
}

// RoutineTimeout bounds a whole routine: jarvisd waits about a minute for a run's result.
const RoutineTimeout = 55 * time.Second

var lengthInstructions = map[string]string{
	"short":  "Respond in 2-4 spoken sentences, conversational tone.",
	"medium": "Respond in 6-10 spoken sentences, flowing narrative tone. Cover each topic with a sentence or two.",
	"long":   "Respond in a detailed paragraph style, about 60 seconds of speech. Cover each topic thoroughly.",
}

var typeLengths = map[string]string{"routine": "short", "briefing": "medium"}

var relativeDays = map[string]int{"today": 0, "tomorrow": 1, "yesterday": -1, "day_after_tomorrow": 2}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

// Routine is the routine command (commands/routine_command.py): each step a call to the node's
// own toolset, then one spoken reply composed by the live model from the steps' results. A
// definition comes with jarvisd's run (D24), else from the routines pulled into Store, else
// from the built-in defaults.
type Routine struct {
	Store *nodesettings.Store // nil: the defaults only
	// Pantry are the routines installed packages ship (routine.json), by slug; nil: none.
	Pantry func() map[string]map[string]any
	// Tools runs the steps: the node's whole toolset, set once it is built.
	Tools node.Toolset
	CC    CC
	Now   func() time.Time
	Log   *slog.Logger
}

func (t *Routine) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{routineSpec.Render()}
}

// definition is the named routine: the store's copy, then a package's, then the default (N:'s
// precedence).
func (t *Routine) definition(slug string) map[string]any {
	if t.Store != nil {
		if o, ok := t.Store.Data(nodesettings.RoutineCommand, slug, t.now()); ok {
			var d map[string]any
			if b, err := json.Marshal(o); err == nil && json.Unmarshal(b, &d) == nil {
				return d
			}
		}
	}
	if d := t.pantry()[slug]; d != nil {
		return d
	}
	return DefaultRoutines()[slug]
}

func (t *Routine) pantry() map[string]map[string]any {
	if t.Pantry == nil {
		return nil
	}
	return t.Pantry()
}

func (t *Routine) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Routine) Call(ctx context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if name != routineSpec.Name {
		return nil, false
	}
	slug, _ := args["routine_name"].(string)
	def, _ := args["routine_definition"].(map[string]any) // the verb's; never the model's
	if steps, _ := def["steps"].([]any); len(steps) == 0 {
		def = t.definition(slug)
	}
	if def == nil {
		return node.Fail("Unknown routine: %s", slug), true
	}
	steps, _ := def["steps"].([]any)
	if len(steps) == 0 {
		return node.Fail("Routine '%s' has no steps configured.", slug), true
	}
	results, errs := map[string]any{}, map[string]string{}
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		cmd, _ := step["command"].(string)
		label, _ := step["label"].(string)
		if label == "" {
			label = cmd
		}
		stepArgs, _ := step["args"].(map[string]any)
		stepArgs = maps.Clone(stepArgs) // the definition stays as it was
		if stepArgs == nil {
			stepArgs = map[string]any{}
		}
		if dates, ok := stepArgs["resolved_datetimes"].([]any); ok {
			stepArgs["resolved_datetimes"] = t.resolveDates(dates)
		}
		if cmd == routineSpec.Name {
			errs[label] = "A routine can't run a routine"
			continue
		}
		out, handled := t.Tools.Call(ctx, cmd, stepArgs, req)
		if !handled {
			t.log().Warn("routine: step skipped, command not found", "command", cmd, "routine", slug)
			errs[label] = "Command '" + cmd + "' not available"
			continue
		}
		node.TakeSignals(out)
		if ok, _ := out["success"].(bool); !ok {
			msg, _ := out["error"].(string)
			errs[label] = cmp.Or(msg, "Unknown error")
			t.log().Warn("routine: step failed", "label", label, "error", msg)
			continue
		}
		data := maps.Clone(out)
		delete(data, "success")
		results[label] = data
	}
	if len(results) == 0 {
		return map[string]any{"success": false, "error": "All routine steps failed.", "errors": errs, "passed": 0, "failed": len(errs)}, true
	}
	instruction, _ := def["response_instruction"].(string)
	length, _ := def["response_length"].(string)
	if length == "" {
		typ, _ := def["type"].(string)
		length = cmp.Or(typeLengths[typ], "short")
	}
	return map[string]any{"success": true, "message": t.compose(ctx, results, errs, cmp.Or(instruction, "Summarize the results conversationally."), length),
		"passed": len(results), "failed": len(errs)}, true
}

func (t *Routine) log() *slog.Logger {
	if t.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return t.Log
}

// resolveDates turns today, tomorrow, ... into YYYY-MM-DD; anything else passes through.
func (t *Routine) resolveDates(vals []any) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
		if s, ok := v.(string); ok {
			if off, ok := relativeDays[strings.ToLower(strings.TrimSpace(s))]; ok {
				out[i] = t.now().AddDate(0, 0, off).Format(time.DateOnly)
			}
		}
	}
	return out
}

// compose is _compose_response: the live model's spoken summary of the results, else the
// results' own messages.
func (t *Routine) compose(ctx context.Context, results map[string]any, errs map[string]string, instruction, length string) string {
	res, _ := json.MarshalIndent(results, "", "  ")
	prompt := "/no_think\n" + instruction + "\n\nHere are the results from each step:\n" + string(res) + "\n\n" +
		cmp.Or(lengthInstructions[length], lengthInstructions["short"]) +
		" Do not mention steps that failed — only include information from successful steps."
	if len(errs) > 0 {
		e, _ := json.Marshal(errs)
		prompt += "\n\nThese steps had errors (do not mention them): " + string(e)
	}
	if s, err := chatText(ctx, t.CC(), prompt); err != nil {
		t.log().Warn("routine: composition failed; using the steps' messages", "err", err)
	} else if s = strings.TrimSpace(thinkRe.ReplaceAllString(s, "")); s != "" {
		return s
	}
	var parts []string
	for _, label := range slices.Sorted(maps.Keys(results)) {
		if m, _ := results[label].(map[string]any)["message"].(string); m != "" {
			parts = append(parts, m)
		}
	}
	if len(parts) == 0 {
		return "Routine completed."
	}
	return strings.Join(parts, " ")
}

// Direct is pre_route (VOICE-FLOWS.md funnel stage 1): a transcript that matches a routine's
// trigger phrase runs that routine on the node, with no model choosing it, and its reply is
// spoken. Routines are tried in order: the defaults, then the rest as stored.
func (t *Routine) Direct(ctx context.Context, said string) (map[string]any, bool) {
	if !node.Enabled(ctx, routineSpec.Name) {
		return nil, false
	}
	text := normalizeSaid(said)
	if text == "" {
		return nil, false
	}
	for _, slug := range t.slugs() {
		if t.Store != nil && !t.Store.FastPathEnabled(routineSpec.Name, patternID(slug)) {
			continue // the household turned this routine's phrases off
		}
		phrases, _ := t.definition(slug)["trigger_phrases"].([]any)
		if !matchesTrigger(text, phrases) {
			continue
		}
		t.log().Info("routine: run by its trigger phrase", "routine", slug, "said", said)
		out, _ := t.Call(ctx, routineSpec.Name, map[string]any{"routine_name": slug},
			node.Request{UserID: node.Speaker(ctx), VoiceCommand: said})
		out[node.SignalsKey] = node.Signals{Speak: true}
		return out, true
	}
	return nil, false
}

// patternID is a routine's fast-path pattern id, as N:'s RoutineCommand names it.
func patternID(slug string) string { return "routine." + slug }

// FastPaths are the routine command's fast-path patterns, one per routine with trigger phrases
// (fast_path_patterns), so the app can turn one routine's phrases off when they collide with
// another command's.
func (t *Routine) FastPaths(command string) []nodesettings.FastPath {
	if command != routineSpec.Name {
		return nil
	}
	var out []nodesettings.FastPath
	for _, slug := range t.slugs() {
		phrases, _ := t.definition(slug)["trigger_phrases"].([]any)
		if len(phrases) == 0 {
			continue
		}
		first, _ := phrases[0].(string)
		out = append(out, nodesettings.FastPath{ID: patternID(slug),
			Description: "Run the '" + slug + "' routine without the LLM", Example: first})
	}
	return out
}

// slugs are the routines this node knows: the defaults first, then the packages', then the
// stored ones.
func (t *Routine) slugs() []string {
	out := slices.Clone(nodesettings.DefaultRoutines)
	for _, slug := range slices.Sorted(maps.Keys(t.pantry())) {
		if !slices.Contains(out, slug) {
			out = append(out, slug)
		}
	}
	if t.Store != nil {
		for _, r := range t.Store.Records(nodesettings.RoutineCommand, t.now()) {
			if !slices.Contains(out, r.Key) {
				out = append(out, r.Key)
			}
		}
	}
	return out
}

// normalizeSaid lowercases a transcript and drops the punctuation speech-to-text adds
// ("Good night!").
func normalizeSaid(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(".,!?;:\"", r) {
			return -1
		}
		return r
	}, strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// matchesTrigger is RoutineCommand._matches: the phrase exactly, the phrase inside what was
// said, what was said inside the phrase, or 80% of either side's words shared. Unlike N:, the
// last two need two words said: a lone "good" (a follow-up's "Good.") isn't "good morning".
func matchesTrigger(text string, phrases []any) bool {
	words := strings.Fields(text)
	loose := len(words) >= 2
	for _, p := range phrases {
		s, _ := p.(string)
		phrase := normalizeSaid(s)
		if phrase == "" {
			continue
		}
		if text == phrase || strings.Contains(text, phrase) {
			return true
		}
		if !loose {
			continue
		}
		if strings.Contains(phrase, text) {
			return true
		}
		pw := strings.Fields(phrase)
		shared := 0
		for _, w := range uniq(pw) {
			if slices.Contains(words, w) {
				shared++
			}
		}
		if float64(shared)/float64(len(uniq(pw))) >= 0.8 || float64(shared)/float64(len(uniq(words))) >= 0.8 {
			return true
		}
	}
	return false
}

// uniq is a word list as a set (Python's set(text.split())).
func uniq(w []string) []string {
	w = slices.Clone(w)
	slices.Sort(w)
	return slices.Compact(w)
}
