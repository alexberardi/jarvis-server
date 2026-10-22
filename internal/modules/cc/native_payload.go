package cc

import (
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// failureMessageParam is the optional argument every native tool gets, so the model can still
// write a spoken failure message per call, as the text path's "failure_message" key lets it
// (blueprint §6, Mode switch). normalizeNativeCalls strips it before the call runs.
const failureMessageParam = "failure_message"

// nativePayload is the native Tools payload: prompts.NativeTools as is. The warmup and the
// engine both send it, so the prefix cache sees the same bytes.
//
// It no longer adds the optional failure_message property to every tool (decision
// 2026-10-20-prompt-diet): that was ~110 chars per tool (~1k tokens on a desktop node) on every
// turn, and with native tool calling the model sees a failed result and speaks for it on the
// next turn anyway. takeFailureMessage still strips one a model sends.
//
// utterance is the turn's words ("" for the warmup): enable_computer_control is left out unless
// they say "control" (C11, askedForControl). Offered on every turn, the 9B called it for "turn
// on the computer" in 10 of 10 runs (#435), and its refusal doesn't undo the call. Most turns
// don't say it, so the warmup's bytes are theirs.
func nativePayload(tools []prompts.Tool, utterance string) []llm.Tool {
	if !askedForControl(utterance) {
		tools = slices.DeleteFunc(slices.Clone(tools), func(t prompts.Tool) bool { return toolName(t) == toolEnableComputerControl })
	}
	native := prompts.NativeTools(tools)
	for _, t := range native {
		if toolName(t) == notForMeTool {
			return llmTools(native)
		}
	}
	return llmTools(append(native, notForMeToolDef()))
}

// notForMeTool is the native form of the <not_for_me/> sentinel (decision
// 2026-10-20-prompt-diet): the model calls it, without arguments, when the speech wasn't meant
// for it. The engine takes the sentinel's silent path; it never reaches a node.
const notForMeTool = "not_for_me"

const notForMeToolJSON = `{"type": "function", "function": {"name": "not_for_me", ` +
	`"description": "Only for speech clearly meant for someone else, or background noise: say nothing. Never for a command or question.", ` +
	`"parameters": {"type": "object", "properties": {}}}}`

func notForMeToolDef() prompts.Tool {
	v, err := pyjson.Loads(notForMeToolJSON)
	if err != nil {
		panic("notForMeToolJSON: " + err.Error())
	}
	return v.(*pyjson.Object)
}

// splitNotForMe separates native not_for_me calls from the rest. only is true when every call
// was not_for_me (and there was at least one): the turn is the sentinel's.
func splitNotForMe(calls []parse.ToolCall) (rest []parse.ToolCall, only bool) {
	n := 0
	for _, c := range calls {
		if c.Function.Name == notForMeTool {
			n++
			continue
		}
		rest = append(rest, c)
	}
	return rest, n > 0 && len(rest) == 0
}

// takeFailureMessage removes a string failure_message from a native call's arguments. It
// returns the arguments to run (key order kept) and the message. Arguments that aren't a JSON
// object, or that have no string failure_message, are returned unchanged.
func takeFailureMessage(args string) (string, string) {
	v, err := pyjson.Loads(args)
	obj, ok := v.(*pyjson.Object)
	if err != nil || !ok {
		return args, ""
	}
	fv, _ := obj.Get(failureMessageParam)
	msg, ok := fv.(string)
	if !ok {
		return args, ""
	}
	rest := pyjson.NewObject()
	for _, k := range obj.Keys() {
		if k != failureMessageParam {
			val, _ := obj.Get(k)
			rest.Set(k, val)
		}
	}
	return pyjson.Dumps(rest, true), msg
}

// askedForControl is whether the user's words ask for computer control by name: "enable
// computer control", "turn control on". "Turn on the computer" doesn't (C11).
func askedForControl(utterance string) bool {
	return strings.Contains(strings.ToLower(utterance), "control")
}
