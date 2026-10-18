package node

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Backend is what the dispatcher needs from jarvisd: verification and the result sinks.
// *CC implements it; tests fake it.
type Backend interface {
	Verify(ctx context.Context, rid string) (bool, error)
	PostToolReport(ctx context.Context, rid string, report any) error
	PostToolResult(ctx context.Context, rid string, output map[string]any) error
	PostAmbientResult(ctx context.Context, rid string, payload map[string]any) error
}

// Verb handles one more command verb (a Pi node's update_node_config, preview_led_pattern,
// measure_ambient_noise: internal/agent/pinode). It verifies and replies itself, through cc.
type Verb func(ctx context.Context, cc Backend, details map[string]any)

// Dispatcher turns command messages from jarvis/nodes/{id}/commands into tool work.
type Dispatcher struct {
	CC    Backend
	Tools Toolset
	Log   *slog.Logger
	// CallTimeout bounds one tool call (CC itself waits ~30 s for a result).
	CallTimeout time.Duration
	// Packages, if set, is report_tools' installed_packages (the app's Update / Revert / health
	// on a package card); nil reports none.
	Packages func() []map[string]any
	// Verbs, if set, handles the verbs it names beyond report_tools and tool_call.
	Verbs map[string]Verb
}

// command is one element of the published array: [{"command": verb, "details": {...}}].
type command struct {
	Command string         `json:"command"`
	Details map[string]any `json:"details"`
}

// Handle processes one MQTT payload. It blocks until every command in it is answered, so the
// MQTT callback should run it on its own goroutine.
func (d *Dispatcher) Handle(ctx context.Context, payload []byte) {
	var cmds []command
	if err := json.Unmarshal(payload, &cmds); err != nil {
		// Some publishers send a single object.
		var one command
		if json.Unmarshal(payload, &one) != nil || one.Command == "" {
			d.Log.Warn("ignoring malformed command payload", "bytes", len(payload))
			return
		}
		cmds = []command{one}
	}
	for _, c := range cmds {
		switch c.Command {
		case "report_tools":
			d.reportTools(ctx, c.Details)
		case "tool_call":
			d.toolCall(ctx, c.Details)
		default:
			if v, ok := d.Verbs[c.Command]; ok {
				v(ctx, d.CC, c.Details)
				continue
			}
			// A desktop node speaks two verbs. Others (tts, action, routine, ...) get no reply,
			// so CC times out exactly as with an offline node; guessing a sink would be worse.
			d.Log.Info("ignoring unsupported verb", "verb", c.Command)
		}
	}
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// Report builds the report_tools answer from what is available right now.
func (d *Dispatcher) Report(ctx context.Context) map[string]any {
	clientTools, commands := []any{}, []any{}
	for _, t := range d.Tools.Available(ctx) {
		clientTools = append(clientTools, t.ClientTool)
		commands = append(commands, t.Command)
	}
	pkgs := []map[string]any{}
	if d.Packages != nil {
		if p := d.Packages(); p != nil {
			pkgs = p
		}
	}
	return map[string]any{"client_tools": clientTools, "available_commands": commands, "installed_packages": pkgs}
}

func (d *Dispatcher) reportTools(ctx context.Context, det map[string]any) {
	rid := str(det, "reply_request_id")
	if rid == "" {
		d.Log.Warn("report_tools without reply_request_id")
		return
	}
	report := d.Report(ctx)
	if err := d.CC.PostToolReport(ctx, rid, report); err != nil {
		d.Log.Warn("report_tools: post failed", "err", err)
		return
	}
	d.Log.Info("reported tools", "count", len(report["client_tools"].([]any)))
}

func (d *Dispatcher) toolCall(ctx context.Context, det map[string]any) {
	rid := str(det, "reply_request_id")
	if rid == "" {
		d.Log.Warn("tool_call without reply_request_id")
		return
	}
	// The command must be one CC issued to this node. Broker ACLs already confine who can
	// publish here; verifying is cheap defence in depth for a node that can open things.
	verifyID := str(det, "request_id")
	if verifyID == "" {
		verifyID = rid
	}
	ok, err := d.CC.Verify(ctx, verifyID)
	if err != nil || !ok {
		d.Log.Warn("tool_call failed verification; dropped", "request_id", verifyID, "err", err)
		return
	}
	name := str(det, "command_name")
	args := DecodeArgs(det["arguments"])
	var output map[string]any
	if name == "" {
		output = Fail("missing command_name")
	} else {
		req := Request{ToolCallID: str(det, "tool_call_id"), VoiceCommand: str(det, "voice_command")}
		if f, ok := det["user_id"].(float64); ok {
			u := int64(f)
			req.UserID = &u
		}
		timeout := d.CallTimeout
		if timeout <= 0 {
			timeout = 25 * time.Second
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		out, handled := d.Tools.Call(cctx, name, args, req)
		cancel()
		if !handled {
			out = Fail("Unknown command: %s", name)
		}
		output = out
	}
	sig := TakeSignals(output)
	d.Log.Info("tool_call", "command", name, "args", LogArgs(args), "success", output["success"], "error_code", output["error_code"])
	if err := d.CC.PostToolResult(ctx, rid, output); err != nil {
		d.Log.Warn("tool_call: post failed", "err", err)
	}
	// Nothing is spoken on this path, so a deferred step (a media command's "start playing
	// now") runs as soon as the result is posted.
	if sig.OnResponseComplete != nil {
		if err := sig.OnResponseComplete(); err != nil {
			d.Log.Warn("tool_call: on_response_complete failed", "command", name, "err", err)
		}
	}
}
