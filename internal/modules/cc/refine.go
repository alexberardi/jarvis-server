package cc

import (
	"context"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Parameter refinement (core/param_refinement.py, docs/cc/02 §3.6): the compressed text
// providers strip _refinable params from the prompt schema, so after the model picks a client
// tool a focused second call fills them in. Text path only, by explicit rule (D8 02.Q7); the
// cached schemas are never mutated, so the markers are always there to read.

// resolverHandledTools have dedicated resolvers and skip generic refinement.
var resolverHandledTools = map[string]bool{"control_device": true, "get_device_status": true}

func (m *Module) refineParams(ctx context.Context, conv *conversation, calls []parse.ToolCall, utterance string) []parse.ToolCall {
	for i, c := range calls {
		name := c.Function.Name
		if resolverHandledTools[name] {
			continue
		}
		tool := findTool(conv.tools, name)
		params := refinableParams(tool)
		if params == nil {
			continue
		}
		existing := argsObject(c.Function.Arguments)
		prompt := refinementPrompt(utterance, name, params, refinementExamples(tool, params), existing)
		resolved := m.refineCall(ctx, prompt)
		if resolved == nil {
			m.deps.Log.Warn("cc: param refinement returned nothing usable", "tool", name)
			continue
		}
		for _, k := range resolved.Keys() {
			if v, _ := resolved.Get(k); v != nil {
				existing.Set(k, v)
			}
		}
		calls[i].Function.Arguments = pyjson.Dumps(existing, true)
	}
	return calls
}

func findTool(tools []prompts.Tool, name string) *pyjson.Object {
	for _, t := range tools {
		if toolName(t) == name {
			if _, ok := t.Get("function"); ok {
				return t
			}
		}
	}
	return nil
}

// refinableParams is _get_refinable_params: the properties marked truthy _refinable.
func refinableParams(tool *pyjson.Object) *pyjson.Object {
	if tool == nil {
		return nil
	}
	fv, _ := tool.Get("function")
	fn, _ := fv.(*pyjson.Object)
	if fn == nil {
		return nil
	}
	pv, _ := fn.Get("parameters")
	params, _ := pv.(*pyjson.Object)
	if params == nil {
		return nil
	}
	propv, _ := params.Get("properties")
	props, _ := propv.(*pyjson.Object)
	if props == nil {
		return nil
	}
	out := pyjson.NewObject()
	for _, k := range props.Keys() {
		v, _ := props.Get(k)
		if ps, ok := v.(*pyjson.Object); ok {
			if r, _ := ps.Get("_refinable"); pyTruthy(r) {
				out.Set(k, ps)
			}
		}
	}
	if out.Len() == 0 {
		return nil
	}
	return out
}

type refinementExample struct {
	utterance string
	params    *pyjson.Object
}

// refinementExamples is _get_refinement_examples: the tool's examples, filtered to the
// refinable params (absent ones as null).
func refinementExamples(tool *pyjson.Object, params *pyjson.Object) []refinementExample {
	ev, _ := tool.Get("examples")
	exs, _ := ev.([]any)
	var out []refinementExample
	for _, e := range exs {
		eo, ok := e.(*pyjson.Object)
		if !ok {
			continue
		}
		u, _ := eo.Get("voice_command")
		us, _ := u.(string)
		if us == "" {
			continue
		}
		apv, _ := eo.Get("expected_parameters")
		all, _ := apv.(*pyjson.Object)
		filtered := pyjson.NewObject()
		for _, k := range params.Keys() {
			var v any
			if all != nil {
				v, _ = all.Get(k)
			}
			filtered.Set(k, v)
		}
		out = append(out, refinementExample{utterance: us, params: filtered})
	}
	return out
}

// refinementPrompt is _build_refinement_prompt (prompt bytes).
func refinementPrompt(utterance, tool string, params *pyjson.Object, examples []refinementExample, existing *pyjson.Object) string {
	var lines []string
	var keys []string
	for _, name := range params.Keys() {
		v, _ := params.Get(name)
		ps := v.(*pyjson.Object)
		desc := strOr(ps, "description", "")
		ptype := strOr(ps, "type", "string")
		if ev, ok := ps.Get("enum"); ok {
			if l, ok := ev.([]any); ok && len(l) > 0 {
				vals := make([]string, len(l))
				for i, x := range l {
					vals[i] = pyjson.Str(x)
				}
				lines = append(lines, "- "+name+" ("+ptype+"): one of ["+strings.Join(vals, ", ")+"]")
				keys = append(keys, `"`+name+`": ...`)
				continue
			}
		}
		lines = append(lines, "- "+name+" ("+ptype+"): "+desc)
		keys = append(keys, `"`+name+`": ...`)
	}
	prompt := "Given this voice command, determine the parameters for " + tool + ".\n\n" +
		"Parameters:\n" + strings.Join(lines, "\n") + "\n\n"
	if existing != nil && existing.Len() > 0 {
		prompt += "Already selected: " + pyjson.Dumps(existing, true) + "\n\n"
	}
	if len(examples) > 0 {
		prompt += "Examples:\n"
		for _, ex := range examples {
			prompt += `"` + ex.utterance + `" → ` + pyjson.Dumps(ex.params, true) + "\n"
		}
		prompt += "\n"
	}
	prompt += `Command: "` + utterance + "\"\n" +
		"Return ONLY JSON: {" + strings.Join(keys, ", ") + "}\n" +
		"Use null for parameters that don't apply."
	return prompt
}

// refineCall is _call_llm (shape D): temperature 0, max_tokens 256, fences stripped, an
// object or nothing.
func (m *Module) refineCall(ctx context.Context, prompt string) *pyjson.Object {
	maxTok := 256
	zero := 0.0
	off := 0
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{Label: llm.LabelLive, MaxTokens: &maxTok, Temperature: &zero,
		ReasoningBudget: &off, Messages: []llm.Message{{Role: "user", Content: llm.TextContent(prompt)}}})
	if err != nil || resp.Content == "" {
		return nil
	}
	content := parse.PyStrip(resp.Content)
	if strings.HasPrefix(content, "```") {
		var kept []string
		for _, line := range strings.Split(content, "\n") {
			if !strings.HasPrefix(parse.PyStrip(line), "```") {
				kept = append(kept, line)
			}
		}
		content = parse.PyStrip(strings.Join(kept, "\n"))
	}
	v, err := pyjson.Loads(content)
	if err != nil {
		return nil
	}
	o, _ := v.(*pyjson.Object)
	return o
}

func strOr(o *pyjson.Object, k, def string) string {
	v, ok := o.Get(k)
	if !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return pyjson.Str(v)
}

// pyTruthy is Python truthiness for decoded JSON values.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *pyjson.Object:
		return x.Len() > 0
	case float64:
		return x != 0
	}
	return pyjson.Repr(v) != "0"
}
