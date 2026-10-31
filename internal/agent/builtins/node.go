package builtins

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
)

// --- control_node: the speaker's own volume ---

// ControlNode is control_node (commands/control_node_command.py): this node's volume and mute,
// with its fast paths ("volume up", "set the volume to 5", "mute").
type ControlNode struct {
	Audio *sdkhost.Audio
	Store *nodesettings.Store // the fast-path switches; nil: all on
}

const controlNodeName = "control_node"

func (c *ControlNode) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def(controlNodeName)}
}

func (c *ControlNode) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != controlNodeName {
		return nil, false
	}
	out, ok := c.call(ctx, args)
	if ok, _ := out["success"].(bool); ok {
		// The exchange ends: a follow-up window would hear the reply's own "Volume 30 percent"
		// as a new volume command (the lab Pi, which has no voiceprints for jarvisd's gate,
		// stepped its volume down that way). Unlike N:.
		sig := node.TakeSignals(out)
		sig.ClearHistory = true
		out[node.SignalsKey] = sig
	}
	return out, ok
}

func (c *ControlNode) call(ctx context.Context, args map[string]any) (map[string]any, bool) {
	name := controlNodeName
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	action, _ := args["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "volume_up", "volume_down":
		delta := 10
		if action == "volume_down" {
			delta = -10
		}
		v, err := c.Audio.Adjust(ctx, delta)
		if err != nil {
			return failure("Could not read current volume.", map[string]any{"action": action}), true
		}
		return finish(map[string]any{"action": action, "volume_percent": v, "message": "Volume " + strconv.Itoa(v) + " percent."}, true, false), true
	case "set_volume":
		raw, ok := args["target_percent"]
		if !ok || raw == nil {
			return failure("target_percent is required for set_volume", map[string]any{"action": action, "parameters_received": args}), true
		}
		target, err := pyInt(raw)
		if err != nil {
			return failure("target_percent must be an integer, got "+pyReprArg(raw), map[string]any{"action": action, "parameters_received": args}), true
		}
		target = max(0, min(100, target))
		if !c.Audio.SetVolume(ctx, target) {
			return failure("Failed to set volume.", map[string]any{"action": action, "target_percent": target}), true
		}
		return finish(map[string]any{"action": action, "volume_percent": target, "message": "Volume set to " + strconv.Itoa(target) + " percent."}, true, false), true
	case "mute", "unmute":
		if !c.Audio.SetMuted(ctx, action == "mute") {
			return failure("Failed to "+action+".", map[string]any{"action": action}), true
		}
		msg := "Unmuted."
		if action == "mute" {
			msg = "Muted."
		}
		return finish(map[string]any{"action": action, "message": msg}, true, false), true
	}
	return failure("Unknown action: "+action, map[string]any{"action": action, "parameters_received": args}), true
}

// pyReprArg is Python's repr() of a JSON argument, for an error message.
func pyReprArg(v any) string {
	if s, ok := v.(string); ok {
		return "'" + s + "'"
	}
	return pyStr(v)
}

func (c *ControlNode) Direct(ctx context.Context, said string) (map[string]any, bool) {
	return directBy(ctx, c.Store, controlNodeName, controlNodePatterns, said, func(a map[string]any) map[string]any {
		out, _ := c.Call(ctx, controlNodeName, a, node.Request{})
		return out
	})
}

func (c *ControlNode) FastPaths(command string) []nodesettings.FastPath {
	if command != controlNodeName {
		return nil
	}
	return patternList(controlNodePatterns)
}

// ControlNodeMatch is the arguments Direct would run (for comparing with N:'s pre_route).
func ControlNodeMatch(said string) (Match, bool) {
	for _, p := range controlNodePatterns {
		if args, ok := p.match(said); ok {
			return Match{controlNodeName, args}, true
		}
	}
	return Match{}, false
}

var (
	volumeUpRe   = regexp.MustCompile(`(?i)\b(?:turn\s+(?:the\s+)?)?volume\s+up\b|\b(?:turn\s+it\s+up|louder|crank\s+it\s+up)\b`)
	volumeDownRe = regexp.MustCompile(`(?i)\b(?:turn\s+(?:the\s+)?)?volume\s+down\b|\b(?:turn\s+it\s+down|quieter|softer)\b`)
	volumeSetRe  = regexp.MustCompile(`(?i)\b(?:set|change|make|put)\s+(?:the\s+)?volume\s+(?:to\s+|at\s+)?(\d+)\s*(%)?`)
	volumeBareRe = regexp.MustCompile(`(?i)\bvolume\s+(\d+)\s*(%)?\b`)
	muteRe       = regexp.MustCompile(`(?i)^\s*(?:please\s+)?mute(?:\s+(?:the\s+)?(?:volume|sound|audio|speaker))?\s*\.?$`)
	unmuteRe     = regexp.MustCompile(`(?i)^\s*(?:please\s+)?unmute(?:\s+(?:the\s+)?(?:volume|sound|audio|speaker))?\s*\.?$`)
)

// volumeTarget is _interpret_target: "50%" is 50; a bare 1-10 is the 0-10 scale (5 is 50).
func volumeTarget(m []string) (map[string]any, bool) {
	v, _ := strconv.Atoi(m[1])
	if m[2] == "" && v <= 10 {
		v *= 10
	}
	return map[string]any{"action": "set_volume", "target_percent": max(0, min(100, v))}, true
}

func action(a string) func([]string) (map[string]any, bool) {
	return func([]string) (map[string]any, bool) { return map[string]any{"action": a}, true }
}

var controlNodePatterns = []pattern{
	regexPattern("control_node.mute", "Bypass LLM for 'mute' / 'mute the speaker'", "mute", muteRe, action("mute")),
	regexPattern("control_node.unmute", "Bypass LLM for 'unmute' / 'unmute the speaker'", "unmute", unmuteRe, action("unmute")),
	regexPattern("control_node.volume_set", "Bypass LLM for 'set volume to N' / 'change volume to N%'", "set the volume to 50%",
		volumeSetRe, volumeTarget),
	regexPattern("control_node.volume_up", "Bypass LLM for 'volume up' / 'louder' / 'turn it up'", "volume up", volumeUpRe, action("volume_up")),
	regexPattern("control_node.volume_down", "Bypass LLM for 'volume down' / 'quieter' / 'turn it down'", "volume down",
		volumeDownRe, action("volume_down")),
	regexPattern("control_node.volume_bare", "Bypass LLM for bare 'volume N' / 'volume N%' (no verb)", "volume 7", volumeBareRe, volumeTarget),
}

// --- check_alerts: "what's up" ---

// CheckAlerts is check_alerts (commands/whats_up_command.py): the queued alerts, told by the
// live model, or cleared. "What's up" with alerts waiting and "clear my alerts" run with no
// other model turn.
type CheckAlerts struct {
	Alerts *sdkhost.Alerts
	CC     CC
	Store  *nodesettings.Store // the fast-path switches; nil: all on
}

const checkAlertsName = "check_alerts"

func (c *CheckAlerts) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def(checkAlertsName)}
}

// truthy is WhatsUpCommand._truthy: tool calling can deliver booleans as strings.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x == 1
	case int:
		return x == 1
	case string:
		return x == "1" || x == "true" || x == "True" || x == "yes"
	}
	return false
}

func (c *CheckAlerts) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != checkAlertsName {
		return nil, false
	}
	checked := args
	if d, ok := args["dismiss"]; ok { // validate_call reads dismiss as a boolean first
		checked = map[string]any{}
		for k, v := range args {
			checked[k] = v
		}
		checked["dismiss"] = truthy(d)
	}
	if out := validated(def(name), checked); out != nil {
		return out, true
	}
	if truthy(args["dismissed"]) || truthy(args["dismiss"]) {
		c.Alerts.Flush()
		return finish(map[string]any{"message": "Cleared."}, true, false), true
	}
	var given []map[string]any
	if s, _ := args["alerts_json"].(string); s != "" {
		_ = json.Unmarshal([]byte(s), &given)
	}
	if len(given) > 0 {
		return finish(map[string]any{"message": c.compose(ctx, given)}, true, false), true
	}
	pending := c.Alerts.Pending()
	if len(pending) == 0 {
		return finish(map[string]any{"message": "No pending alerts."}, true, false), true
	}
	list := make([]map[string]any, len(pending))
	ids := make([]string, len(pending))
	for i, a := range pending {
		list[i], ids[i] = a.Wire(), a.ID
	}
	msg := c.compose(ctx, list)
	c.Alerts.Remove(ids)
	return finish(map[string]any{"message": msg}, true, false), true
}

// compose is _compose_response: the live model tells the alerts, else their titles.
func (c *CheckAlerts) compose(ctx context.Context, alerts []map[string]any) string {
	b, _ := json.MarshalIndent(alerts, "", "  ")
	prompt := "/no_think\n" +
		"Deliver these updates conversationally, like a friend catching you up. " +
		"Be concise — one or two sentences per alert. " +
		"Group by topic if multiple alerts are from the same source.\n\n" +
		"Alerts:\n" + string(b) + "\n\n" +
		"Respond in 2-6 spoken sentences."
	if s, err := chatText(ctx, c.CC(), prompt); err == nil && s != "" {
		return strings.TrimSpace(thinkRe.ReplaceAllString(s, ""))
	}
	var parts []string
	for _, a := range alerts {
		if t, _ := a["title"].(string); t != "" {
			s, _ := a["summary"].(string)
			parts = append(parts, t+": "+s)
		}
	}
	if len(parts) == 0 {
		return "You have pending alerts."
	}
	return strings.Join(parts, ". ")
}

var triggerPhrases = []string{"what's up", "whats up", "what's new", "whats new", "any alerts", "any updates", "anything new",
	"what did i miss", "any notifications", "check alerts", "check notifications"}

var (
	dismissRe         = regexp.MustCompile(`\b(clear|dismiss|cancel|remove|delete)\b(?:\s+(?:all|my|the|those|these|that|any|old|pending|of|please))*\s+(alerts?|notifications?)\b`)
	dismissNotAlertRe = regexp.MustCompile(`^\s+(?:sound|sounds|setting|settings|tone|tones|volume)`)
	dismissNegationRe = regexp.MustCompile(`\b(?:don'?t|do not|didn'?t|never|won'?t|not|gonna)\s+(?:clear|dismiss|cancel|remove|delete)\b`)
)

// dismissMatch is _DISMISS_RE.search: Go has no lookahead, so a match followed by a non-alert
// noun ("notification sounds") is skipped by hand.
func dismissMatch(text string) bool {
	for _, loc := range dismissRe.FindAllStringIndex(text, -1) {
		if !dismissNotAlertRe.MatchString(text[loc[1]:]) {
			return true
		}
	}
	return false
}

// greetingMatch is pre_route's greeting test: a trigger phrase in a short utterance, or a short
// utterance inside one.
func greetingMatch(text string) bool {
	for _, p := range triggerPhrases {
		if strings.Contains(text, p) {
			return len(text)-len(p) <= 10
		}
	}
	if len(text) < 4 {
		return false
	}
	for _, p := range triggerPhrases {
		if strings.Contains(p, text) {
			return true
		}
	}
	return false
}

// AlertsMatch is which fast path claims what was said: "dismiss", "greeting" or "" (for
// comparing with N:'s pre_route; greeting's alerts-waiting test is left out).
func AlertsMatch(said string) string {
	text := strings.ToLower(strings.TrimSpace(said))
	switch {
	case text == "":
		return ""
	case dismissMatch(text) && !dismissNegationRe.MatchString(text):
		return "dismiss"
	case greetingMatch(text):
		return "greeting"
	}
	return ""
}

func (c *CheckAlerts) Direct(ctx context.Context, said string) (map[string]any, bool) {
	if !node.Enabled(ctx, checkAlertsName) {
		return nil, false
	}
	on := func(id string) bool { return c.Store == nil || c.Store.FastPathEnabled(checkAlertsName, id) }
	var args map[string]any
	switch AlertsMatch(said) {
	case "dismiss":
		if !on("check_alerts.dismiss") {
			return nil, false
		}
		args = map[string]any{"dismissed": true}
	case "greeting":
		if !on("check_alerts.greeting") || c.Alerts.Count() == 0 {
			return nil, false // nothing waiting: casual conversation for the model
		}
		args = map[string]any{}
	default:
		return nil, false
	}
	out, _ := c.Call(ctx, checkAlertsName, args, node.Request{})
	sig := node.TakeSignals(out)
	sig.Speak = true
	out[node.SignalsKey] = sig
	return out, true
}

func (c *CheckAlerts) FastPaths(command string) []nodesettings.FastPath {
	if command != checkAlertsName {
		return nil
	}
	return []nodesettings.FastPath{
		{ID: "check_alerts.greeting", Description: "Bypass the LLM when greeting phrases ('what's up', 'any alerts') are spoken with pending alerts in the queue", Example: "what's up"},
		{ID: "check_alerts.dismiss", Description: "Bypass the LLM when dismiss phrases ('clear alerts', 'dismiss notifications') are spoken — silently flushes the queue", Example: "clear alerts"},
	}
}

// --- act_on_items ---

// ActOnItems is act_on_items (commands/act_on_items_command.py). On N: its calls were
// dispatched to the command that showed the items (the RECENTLY SHOWN list); the Go node
// doesn't keep that list yet, so run's fail-soft answer is all there is, as with the Python
// command on a Go node.
type ActOnItems struct{}

func (ActOnItems) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def("act_on_items")}
}

func (ActOnItems) Call(_ context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != "act_on_items" {
		return nil, false
	}
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	out := map[string]any{"message": "I don't have anything shown to act on right now.", "success": true}
	out[node.SignalsKey] = node.Signals{} // final_response: no wait, no clear
	return out, true
}
