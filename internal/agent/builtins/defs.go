package builtins

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// defs are the ported commands' tool definitions exactly as the pinned Python commands
// rendered them (written by sidecar's TestDumpPythonBuiltins): command-center sees the same
// description, parameters, examples, rules and antipatterns from the Go port.
//
//go:embed defs/*.json
var defs embed.FS

// def is the named command's definition from defs.
func def(name string) node.ToolDef {
	b, err := defs.ReadFile("defs/" + name + ".json")
	if err != nil {
		panic(err)
	}
	var d struct {
		ClientTool map[string]any `json:"client_tool"`
		Command    map[string]any `json:"command"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		panic(err)
	}
	return node.ToolDef{Name: name, ClientTool: d.ClientTool, Command: d.Command}
}

// pattern is one of a command's fast paths (the SDK's FastPathPattern): a phrasing the node
// carries out with no model (VOICE-FLOWS.md funnel stage 1), each switchable in the app's
// Inspect fast-paths screen.
type pattern struct {
	id, description, example string
	// match is the command's arguments for what was said, or false.
	match func(said string) (map[string]any, bool)
}

// patternList is the patterns for the settings snapshot.
func patternList(ps []pattern) []nodesettings.FastPath {
	out := make([]nodesettings.FastPath, 0, len(ps))
	for _, p := range ps {
		out = append(out, nodesettings.FastPath{ID: p.id, Description: p.description, Example: p.example})
	}
	return out
}

// directBy is N:'s try_pre_route for one command: the first enabled pattern that matches runs
// the command, and its message is spoken. A failure or a result with nothing to say falls
// through to the model (false), as on N:.
func directBy(ctx context.Context, store *nodesettings.Store, command string, ps []pattern, said string,
	call func(args map[string]any) map[string]any) (map[string]any, bool) {
	if !node.Enabled(ctx, command) {
		return nil, false
	}
	for _, p := range ps {
		if store != nil && !store.FastPathEnabled(command, p.id) {
			continue
		}
		args, ok := p.match(said)
		if !ok {
			continue
		}
		out := call(args)
		msg, _ := out["message"].(string)
		if ok, _ := out["success"].(bool); !ok || msg == "" {
			return nil, false
		}
		sig := node.TakeSignals(out)
		sig.Speak = true
		out[node.SignalsKey] = sig
		return out, true
	}
	return nil, false
}

// finish is a command's output with its wait_for_input signal, as the sidecar returns
// context_data + success.
func finish(out map[string]any, success, waitForInput bool) map[string]any {
	out["success"] = success
	out[node.SignalsKey] = node.Signals{WaitForInput: waitForInput}
	return out
}

// failure is CommandResponse.error_response: context_data, error = error_details, no wait.
func failure(details string, data map[string]any) map[string]any {
	data["error"] = details
	return finish(data, false, false)
}

// validated is IJarvisCommand.execute's checks before run, against the command's declared
// parameters: a missing required one is the raised "Missing required params" (no
// context_data), a wrong type or a value outside the enum is CommandResponse.validation_error.
// nil when the arguments pass.
func validated(d node.ToolDef, args map[string]any) map[string]any {
	params, _ := d.Command["parameters"].([]any)
	var missing []string
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		if req, _ := p["required"].(bool); req && args[p["name"].(string)] == nil {
			missing = append(missing, p["name"].(string))
		}
	}
	if len(missing) > 0 {
		return map[string]any{"success": false, "error": "Missing required params: " + strings.Join(missing, ", ")}
	}
	var errs []any
	var msgs []string
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		name, _ := p["name"].(string)
		v := args[name]
		if v == nil {
			continue
		}
		typ, _ := p["type"].(string)
		enum, _ := p["enum_values"].([]any)
		var msg string
		switch {
		case !pyTypeOK(typ, v):
			msg = "Parameter '" + name + "' must be of type " + typ
		case len(enum) > 0 && !slices.ContainsFunc(enum, func(e any) bool { return e == pyStr(v) }):
			opts := make([]string, len(enum))
			for i, e := range enum {
				opts[i], _ = e.(string)
			}
			msg = "Invalid value '" + pyStr(v) + "' for '" + name + "'. Must be one of: " + strings.Join(opts, ", ")
		default:
			continue
		}
		var valid any
		if enum != nil {
			valid = enum
		}
		errs = append(errs, map[string]any{"param": name, "message": msg, "valid_values": valid})
		msgs = append(msgs, msg)
	}
	if len(errs) == 0 {
		return nil
	}
	return finish(map[string]any{"_validation_error": true, "errors": errs, "error": strings.Join(msgs, "\n")}, false, false)
}

// pyTypeOK is IJarvisParameter._validate_type on a JSON value: JSON has no int/float
// distinction, so a whole number is an int (and, as in Python, a bool is one too); datetime
// types never come out of JSON; an unknown type passes.
func pyTypeOK(typ string, v any) bool {
	switch typ {
	case "str", "string":
		_, ok := v.(string)
		return ok
	case "int", "integer":
		switch x := v.(type) {
		case bool, int, int64:
			return true
		case float64:
			return x == math.Trunc(x)
		case json.Number:
			_, err := x.Int64()
			return err == nil
		}
		return false
	case "float":
		switch v.(type) {
		case bool, int, int64, float64, json.Number:
			return true
		}
		return false
	case "bool", "boolean":
		_, ok := v.(bool)
		return ok
	case "list", "array":
		_, ok := v.([]any)
		return ok
	case "dict":
		_, ok := v.(map[string]any)
		return ok
	case "datetime", "date", "time", "timedelta":
		return false
	}
	if strings.HasPrefix(typ, "array<") || strings.HasPrefix(typ, "array[") || strings.HasSuffix(typ, "[]") {
		_, ok := v.([]any)
		return ok
	}
	return true
}

// pyStr is Python's str() of a JSON scalar.
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}

// browserDef is a ported command's data-browser settings as the Python command had them
// (defs/<name>.browser.json, written with the definitions).
func browserDef(name string) nodesettings.DataCommand {
	b, err := defs.ReadFile("defs/" + name + ".browser.json")
	if err != nil {
		panic(err)
	}
	var d nodesettings.DataCommand
	if err := json.Unmarshal(b, &d); err != nil {
		panic(err)
	}
	return d
}

// noCreate is Create for a command whose browser has no create (supports_create false).
func noCreate(command string) error {
	return &nodesettings.ErrRejected{Message: command + " doesn't create records from the app"}
}
