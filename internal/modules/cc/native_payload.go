package cc

import (
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// failureMessageParam is the optional argument every native tool gets, so the model can still
// write a spoken failure message per call, as the text path's "failure_message" key lets it
// (blueprint §6, Mode switch). normalizeNativeCalls strips it before the call runs.
const failureMessageParam = "failure_message"

const failureMessageDescription = "Optional: a brief spoken reply for the user if this call fails."

// nativePayload is the native Tools payload: prompts.NativeTools with the optional
// failure_message property added to each tool. The warmup and the engine both send it, so the
// prefix cache sees the same bytes. prompts.NativeTools and the cached tools are not changed.
func nativePayload(tools []prompts.Tool) []llm.Tool {
	native := prompts.NativeTools(tools)
	out := make([]prompts.Tool, len(native))
	for i, t := range native {
		out[i] = withFailureMessage(t)
	}
	return llmTools(out)
}

// withFailureMessage returns a copy of t whose function.parameters.properties ends with the
// failure_message property. It is never added to "required". A tool that already has the
// property, or whose parameters aren't an object schema, is returned as is.
func withFailureMessage(t prompts.Tool) prompts.Tool {
	fn, _ := t.Get("function")
	f, ok := fn.(*pyjson.Object)
	if !ok {
		return t
	}
	pv, _ := f.Get("parameters")
	params, ok := pv.(*pyjson.Object)
	if !ok {
		return t
	}
	if typ, _ := params.Get("type"); typ != "object" {
		return t
	}
	props := pyjson.NewObject()
	if v, has := params.Get("properties"); has {
		p, ok := v.(*pyjson.Object)
		if !ok {
			return t
		}
		if _, dup := p.Get(failureMessageParam); dup {
			return t
		}
		props = shallowCopy(p)
	}
	fm := pyjson.NewObject()
	fm.Set("type", "string")
	fm.Set("description", failureMessageDescription)
	props.Set(failureMessageParam, fm)

	np := shallowCopy(params)
	np.Set("properties", props)
	nf := shallowCopy(f)
	nf.Set("parameters", np)
	nt := shallowCopy(t)
	nt.Set("function", nf)
	return nt
}

func shallowCopy(o *pyjson.Object) *pyjson.Object {
	c := pyjson.NewObject()
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		c.Set(k, v)
	}
	return c
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
