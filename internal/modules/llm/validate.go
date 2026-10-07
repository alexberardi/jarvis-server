package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Request parsing with pydantic's outcomes: the legacy routes were FastAPI models with
// extra="allow", so unknown keys are ignored and a bad field is a 422 naming its loc. Bodies
// are decoded with Python's json.loads semantics (pyjson), which keeps json_schema's key order.

const maxBody = 64 << 20 // base64 images ride in chat bodies

// readJSONBody decodes the body as a JSON object, answering FastAPI's 422s otherwise.
func readJSONBody(w http.ResponseWriter, r *http.Request) (*pyjson.Object, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "Request body too large")
		} else {
			httpx.Error(w, http.StatusBadRequest, "Could not read request body")
		}
		return nil, false
	}
	if strings.TrimSpace(string(raw)) == "" {
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"body"}, Msg: "Field required"})
		return nil, false
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		pos := 0
		var de *pyjson.DecodeError
		if errors.As(err, &de) {
			pos = de.Pos
		}
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{
			"type": "json_invalid", "loc": []any{"body", pos}, "msg": "JSON decode error", "input": map[string]any{},
			"ctx": map[string]any{"error": msgOf(err)},
		}}})
		return nil, false
	}
	o, ok := v.(*pyjson.Object)
	if !ok {
		httpx.ValidationError(w, httpx.FieldError{Type: "model_attributes_type", Loc: []any{"body"},
			Msg: "Input should be a valid dictionary or object to extract fields from", Input: jsonable(v)})
		return nil, false
	}
	return o, true
}

func msgOf(err error) string {
	var de *pyjson.DecodeError
	if errors.As(err, &de) {
		return de.Msg
	}
	return err.Error()
}

// jsonable makes a decoded value safe for encoding/json (non-finite floats become null).
func jsonable(v any) any {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
	case *pyjson.Object, []any:
		return pyjson.ToJSON(x)
	}
	return v
}

// checker collects pydantic-style field errors.
type checker struct{ errs []httpx.FieldError }

func (c *checker) fail(typ, msg string, input any, loc []any) {
	c.errs = append(c.errs, httpx.FieldError{Type: typ, Loc: append([]any{}, loc...), Msg: msg, Input: jsonable(input)})
}

func at(loc []any, k any) []any { return append(append([]any{}, loc...), k) }

func (c *checker) obj(v any, loc []any, model string) (*pyjson.Object, bool) {
	o, ok := v.(*pyjson.Object)
	if !ok {
		c.fail("model_type", "Input should be a valid dictionary or instance of "+model, v, loc)
	}
	return o, ok
}

// field returns a key's value; present is false when absent.
func field(o *pyjson.Object, k string) (any, bool) { return o.Get(k) }

func (c *checker) str(o *pyjson.Object, k string, required bool, loc []any) (string, bool) {
	v, present := field(o, k)
	if !present || (v == nil && !required) {
		if required && !present {
			c.fail("missing", "Field required", o, at(loc, k))
		}
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		c.fail("string_type", "Input should be a valid string", v, at(loc, k))
	}
	return s, ok
}

func (c *checker) optFloat(o *pyjson.Object, k string, loc []any) *float64 {
	v, present := field(o, k)
	if !present || v == nil {
		return nil
	}
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case *big.Int:
		f, _ = new(big.Float).SetInt(x).Float64()
	case string:
		p, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			c.fail("float_parsing", "Input should be a valid number, unable to parse string as a number", v, at(loc, k))
			return nil
		}
		f = p
	default:
		c.fail("float_type", "Input should be a valid number", v, at(loc, k))
		return nil
	}
	return &f
}

func (c *checker) optInt(o *pyjson.Object, k string, loc []any) *int64 {
	v, present := field(o, k)
	if !present || v == nil {
		return nil
	}
	switch x := v.(type) {
	case *big.Int:
		if !x.IsInt64() {
			c.fail("int_parsing_size", "Unable to parse input string as an integer, exceed maximum size", v, at(loc, k))
			return nil
		}
		n := x.Int64()
		return &n
	case float64:
		if x != math.Trunc(x) || math.IsInf(x, 0) || math.IsNaN(x) {
			c.fail("int_from_float", "Input should be a valid integer, got a number with a fractional part", v, at(loc, k))
			return nil
		}
		n := int64(x)
		return &n
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			c.fail("int_parsing", "Input should be a valid integer, unable to parse string as an integer", v, at(loc, k))
			return nil
		}
		return &n
	}
	c.fail("int_type", "Input should be a valid integer", v, at(loc, k))
	return nil
}

func (c *checker) optBool(o *pyjson.Object, k string, loc []any) *bool {
	v, present := field(o, k)
	if !present || v == nil {
		return nil
	}
	t, f := true, false
	switch x := v.(type) {
	case bool:
		return &x
	case *big.Int:
		switch x.String() {
		case "0":
			return &f
		case "1":
			return &t
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "0", "off", "f", "false", "n", "no":
			return &f
		case "1", "on", "t", "true", "y", "yes":
			return &t
		}
	}
	c.fail("bool_parsing", "Input should be a valid boolean, unable to interpret input", v, at(loc, k))
	return nil
}

// optObject accepts null/absent or a dict and returns it compacted (order kept).
func (c *checker) optObject(o *pyjson.Object, k string, loc []any) (json.RawMessage, bool) {
	v, present := field(o, k)
	if !present || v == nil {
		return nil, true
	}
	if _, ok := v.(*pyjson.Object); !ok {
		c.fail("dict_type", "Input should be a valid dictionary", v, at(loc, k))
		return nil, false
	}
	return pyjson.ToJSON(v), true
}

func intPtr(p *int64) *int {
	if p == nil {
		return nil
	}
	n := int(*p)
	return &n
}

// messages parses List[Message].
func (c *checker) messages(v any, present bool, parent *pyjson.Object, loc []any) []Message {
	if !present {
		c.fail("missing", "Field required", parent, loc)
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		c.fail("list_type", "Input should be a valid list", v, loc)
		return nil
	}
	out := make([]Message, 0, len(list))
	for i, item := range list {
		ml := at(loc, i)
		o, ok := c.obj(item, ml, "Message")
		if !ok {
			continue
		}
		var m Message
		m.Role, _ = c.str(o, "role", true, ml)
		m.Content = c.content(o, ml)
		if tcs, present := field(o, "tool_calls"); present && tcs != nil {
			m.ToolCalls = c.toolCalls(tcs, at(ml, "tool_calls"))
		}
		m.ToolCallID, _ = c.str(o, "tool_call_id", false, ml)
		if n, ok := field(o, "name"); ok {
			m.Name, _ = n.(string)
		}
		out = append(out, m)
	}
	return out
}

func (c *checker) content(o *pyjson.Object, loc []any) *Content {
	v, present := field(o, "content")
	cl := at(loc, "content")
	if !present || v == nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		return TextContent(x)
	case []any:
		parts := make([]Part, 0, len(x))
		for i, item := range x {
			pl := at(cl, i)
			po, ok := item.(*pyjson.Object)
			if !ok {
				c.fail("model_attributes_type", "Input should be a valid dictionary or object to extract fields from", item, pl)
				continue
			}
			t, has := field(po, "type")
			ts, _ := t.(string)
			switch {
			case !has:
				c.fail("union_tag_not_found", "Unable to extract tag using discriminator 'type'", item, pl)
			case ts == "text":
				s, _ := c.str(po, "text", true, at(pl, "text"))
				parts = append(parts, Part{Type: "text", Text: s})
			case ts == "image_url":
				iv, has := field(po, "image_url")
				il := at(pl, "image_url")
				if !has {
					c.fail("missing", "Field required", po, il)
					continue
				}
				io, ok := c.obj(iv, il, "ImageUrl")
				if !ok {
					continue
				}
				u, _ := c.str(io, "url", true, il)
				d, _ := c.str(io, "detail", false, il)
				parts = append(parts, Part{Type: "image_url", ImageURL: &ImageURL{URL: u, Detail: d}})
			default:
				c.fail("union_tag_invalid", fmt.Sprintf("Input tag '%s' found using 'type' does not match any of the expected tags: 'text', 'image_url'", pyjson.Str(t)), item, pl)
			}
		}
		return &Content{Parts: parts}
	}
	c.fail("string_type", "Input should be a valid string", v, at(cl, "str"))
	return nil
}

func (c *checker) toolCalls(v any, loc []any) []ToolCall {
	list, ok := v.([]any)
	if !ok {
		c.fail("list_type", "Input should be a valid list", v, loc)
		return nil
	}
	var out []ToolCall
	for i, item := range list {
		l := at(loc, i)
		o, ok := c.obj(item, l, "ToolCall")
		if !ok {
			continue
		}
		var tc ToolCall
		tc.ID, _ = c.str(o, "id", true, l)
		tc.Type = "function"
		if t, ok := field(o, "type"); ok && t != "function" {
			c.fail("literal_error", "Input should be 'function'", t, at(l, "type"))
		}
		fv, has := field(o, "function")
		if !has {
			c.fail("missing", "Field required", o, at(l, "function"))
			continue
		}
		fo, ok := c.obj(fv, at(l, "function"), "FunctionCall")
		if !ok {
			continue
		}
		tc.Function.Name, _ = c.str(fo, "name", true, at(l, "function"))
		tc.Function.Arguments, _ = c.str(fo, "arguments", true, at(l, "function"))
		out = append(out, tc)
	}
	return out
}

func (c *checker) tools(o *pyjson.Object, loc []any) []Tool {
	v, present := field(o, "tools")
	if !present || v == nil {
		return nil
	}
	tl := at(loc, "tools")
	list, ok := v.([]any)
	if !ok {
		c.fail("list_type", "Input should be a valid list", v, tl)
		return nil
	}
	var out []Tool
	for i, item := range list {
		l := at(tl, i)
		to, ok := c.obj(item, l, "ToolDefinition")
		if !ok {
			continue
		}
		if t, ok := field(to, "type"); ok && t != "function" {
			c.fail("literal_error", "Input should be 'function'", t, at(l, "type"))
		}
		fv, has := field(to, "function")
		if !has {
			c.fail("missing", "Field required", to, at(l, "function"))
			continue
		}
		fl := at(l, "function")
		fo, ok := c.obj(fv, fl, "FunctionDefinition")
		if !ok {
			continue
		}
		name, _ := c.str(fo, "name", true, fl)
		desc, _ := c.str(fo, "description", false, fl)
		params, _ := c.optObject(fo, "parameters", fl)
		// Forward the definition as sent (type defaults to "function"), extra keys included.
		if _, has := to.Get("type"); !has {
			cp := pyjson.NewObject()
			cp.Set("type", "function")
			for _, k := range to.Keys() {
				v, _ := to.Get(k)
				cp.Set(k, v)
			}
			to = cp
		}
		out = append(out, Tool{Raw: pyjson.ToJSON(to), Type: "function", Function: ToolFunction{Name: name, Description: desc, Parameters: params}})
	}
	return out
}

func (c *checker) responseFormat(o *pyjson.Object, loc []any) *ResponseFormat {
	v, present := field(o, "response_format")
	if !present || v == nil {
		return nil
	}
	rl := at(loc, "response_format")
	ro, ok := c.obj(v, rl, "ResponseFormat")
	if !ok {
		return nil
	}
	t, _ := c.str(ro, "type", true, rl)
	schema, _ := c.optObject(ro, "json_schema", rl)
	return &ResponseFormat{Type: t, JSONSchema: schema}
}

// chatInput is a parsed ChatCompletionRequest.
type chatInput struct {
	Model              string
	Stream             bool
	IncludeDateContext bool
	Req                ChatRequest
}

// parseChat validates a ChatCompletionRequest body.
func parseChat(o *pyjson.Object) (chatInput, []httpx.FieldError) {
	c := &checker{}
	body := []any{"body"}
	var in chatInput
	in.Model, _ = c.str(o, "model", true, body)
	mv, present := field(o, "messages")
	msgs := c.messages(mv, present, o, at(body, "messages"))
	r := ChatRequest{Messages: msgs}
	r.Temperature = c.optFloat(o, "temperature", body)
	r.TopP = c.optFloat(o, "top_p", body)
	r.MaxTokens = intPtr(c.optInt(o, "max_tokens", body))
	r.Seed = c.optInt(o, "seed", body)
	if s := c.optBool(o, "stream", body); s != nil {
		in.Stream = *s
	}
	r.ResponseFormat = c.responseFormat(o, body)
	r.Tools = c.tools(o, body)
	if tc, ok := field(o, "tool_choice"); ok && tc != nil {
		switch tc.(type) {
		case string, *pyjson.Object:
			r.ToolChoice = pyjson.ToJSON(tc)
		default:
			c.fail("string_type", "Input should be a valid string", tc, at(at(body, "tool_choice"), "str"))
		}
	}
	if d := c.optBool(o, "include_date_context", body); d != nil {
		in.IncludeDateContext = *d
	}
	if av, ok := field(o, "adapter_settings"); ok && av != nil {
		al := at(body, "adapter_settings")
		if ao, ok := c.obj(av, al, "AdapterSettings"); ok { // LoRA is cut: validated, then ignored
			c.str(ao, "hash", true, al)
			c.optFloat(ao, "scale", al)
			c.optBool(ao, "enabled", al)
		}
	}
	r.ReasoningBudget = intPtr(c.optInt(o, "reasoning_budget", body))
	r.Label = echoLabel(in.Model)
	r.WantDateKeys = in.IncludeDateContext
	in.Req = r
	return in, c.errs
}

// enqueueInput is a parsed EnqueueRequest.
type enqueueInput struct {
	JobID, JobType, CreatedAt, IdempotencyKey, TraceID string
	TTLSeconds                                         int64
	Metadata                                           json.RawMessage
	Request                                            *pyjson.Object
	Callback                                           Callback
}

func parseEnqueue(o *pyjson.Object) (enqueueInput, []httpx.FieldError) {
	c := &checker{}
	body := []any{"body"}
	var in enqueueInput
	in.JobID, _ = c.str(o, "job_id", true, body)
	in.JobType, _ = c.str(o, "job_type", true, body)
	in.CreatedAt, _ = c.str(o, "created_at", true, body)
	c.str(o, "priority", false, body)
	in.TraceID, _ = c.str(o, "trace_id", false, body)
	in.IdempotencyKey, _ = c.str(o, "idempotency_key", true, body)
	c.str(o, "job_type_version", false, body)
	in.TTLSeconds = 86400
	if v, ok := field(o, "ttl_seconds"); ok {
		if v == nil {
			c.fail("int_type", "Input should be a valid integer", v, at(body, "ttl_seconds"))
		} else if n := c.optInt(o, "ttl_seconds", body); n != nil {
			in.TTLSeconds = *n
		}
	}
	in.Metadata, _ = c.optObject(o, "metadata", body)
	if rv, ok := field(o, "request"); !ok {
		c.fail("missing", "Field required", o, at(body, "request"))
	} else if ro, ok := rv.(*pyjson.Object); ok {
		in.Request = ro
	} else {
		c.fail("dict_type", "Input should be a valid dictionary", rv, at(body, "request"))
	}
	cl := at(body, "callback")
	if cv, ok := field(o, "callback"); !ok {
		c.fail("missing", "Field required", o, cl)
	} else if co, ok := c.obj(cv, cl, "CallbackInfo"); ok {
		in.Callback.URL, _ = c.str(co, "url", true, cl)
		in.Callback.AuthType, _ = c.str(co, "auth_type", false, cl)
		in.Callback.Token, _ = c.str(co, "token", false, cl)
	}
	return in, c.errs
}

// parseQueueRequest is QueueRequest(**request): it becomes the job's chat request. A failure is
// the enqueue route's 400 invalid_request with pydantic's error text.
func parseQueueRequest(o *pyjson.Object) (ChatRequest, error) {
	c := &checker{}
	var root []any
	model, _ := c.str(o, "model", true, root)
	_ = model
	mv, present := field(o, "messages")
	msgs := c.messages(mv, present, o, at(root, "messages"))
	r := ChatRequest{Label: LabelBackground, Messages: msgs}
	r.ResponseFormat = c.responseFormat(o, root)
	r.ReasoningBudget = intPtr(c.optInt(o, "reasoning_budget", root))
	var sampling *pyjson.Object
	if sv, ok := field(o, "sampling"); ok && sv != nil {
		sampling, _ = c.obj(sv, at(root, "sampling"), "SamplingSettings")
	}
	for _, k := range []string{"artifacts", "timeouts"} {
		if v, ok := field(o, k); ok && v != nil {
			c.obj(v, at(root, k), k)
		}
	}
	// Legacy worker: sampling first, then the request's own temperature/max_tokens.
	if sampling != nil {
		sl := at(root, "sampling")
		if _, has := field(sampling, "temperature"); has {
			r.Temperature = c.optFloat(sampling, "temperature", sl)
		} else {
			r.Temperature = c.optFloat(o, "temperature", root)
		}
		r.TopP = c.optFloat(sampling, "top_p", sl)
		r.MaxTokens = intPtr(c.optInt(sampling, "max_tokens", sl))
		r.Seed = c.optInt(sampling, "seed", sl)
	} else {
		r.Temperature = c.optFloat(o, "temperature", root)
	}
	if r.MaxTokens == nil || *r.MaxTokens == 0 {
		if mt := intPtr(c.optInt(o, "max_tokens", root)); mt != nil {
			r.MaxTokens = mt
		}
	}
	if len(c.errs) > 0 {
		return r, fmt.Errorf("%s", pydanticText("QueueRequest", c.errs))
	}
	return r, nil
}

// pydanticText approximates str(ValidationError).
func pydanticText(model string, errs []httpx.FieldError) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d validation error", len(errs))
	if len(errs) != 1 {
		b.WriteString("s")
	}
	b.WriteString(" for " + model)
	for _, e := range errs {
		parts := make([]string, len(e.Loc))
		for i, l := range e.Loc {
			parts[i] = fmt.Sprint(l)
		}
		fmt.Fprintf(&b, "\n%s\n  %s [type=%s]", strings.Join(parts, "."), e.Msg, e.Type)
	}
	return b.String()
}

// parseCreatedAt is _parse_created_at: ISO 8601 (Z allowed; naive is local time, as Python's
// .timestamp() read it), else epoch seconds, else now.
func parseCreatedAt(s string, now time.Time) time.Time {
	ts := strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999Z07:00", "2006-01-02 15:04:05.999999Z07:00",
		"2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999", "2006-01-02 15:04:05.999999", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, ts, time.Local); err == nil {
			return t
		}
	}
	if f, err := strconv.ParseFloat(ts, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		sec, frac := math.Modf(f)
		return time.Unix(int64(sec), int64(frac*1e9))
	}
	return now
}
