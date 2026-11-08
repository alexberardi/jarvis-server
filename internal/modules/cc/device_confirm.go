package cc

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// A device command that worked is confirmed in a fixed sentence, without a second model call:
// on a Pi that call was ~0.6-0.8 s of every "turn on the lights" (decision
// 2026-11-07-device-confirmations). Anything else (a failure, a question in the same request,
// another tool in the round, an action or value the phrases don't cover) goes to the model.

var (
	questionCueRE = regexp.MustCompile(`(?i)\?|\b(what|when|where|who|why|how|which|is it|are there|tell me|do i|did i)\b`)
	numberRE      = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
	colorWordRE   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z -]{1,30}$`)
)

// deviceConfirmation is the spoken confirmation for a round of successful control_device
// calls, or false when the model should answer.
func deviceConfirmation(msgs []chatMsg, results []toolResult) (string, bool) {
	if len(results) == 0 || questionCueRE.MatchString(lastUserContent(msgs)) {
		return "", false
	}
	calls := lastToolCalls(msgs)
	if len(calls) != len(results) {
		return "", false
	}
	var said []string
	for _, r := range results {
		call, ok := calls[r.ToolCallID]
		if !ok || call.Function.Name != "control_device" {
			return "", false
		}
		o := outputObject(r.Output)
		if o == nil {
			return "", false
		}
		if ok, _ := o.Get("success"); ok != true {
			return "", false
		}
		var args map[string]any
		if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
			return "", false
		}
		str := func(k string) string {
			switch v := args[k].(type) {
			case string:
				return strings.TrimSpace(v)
			case float64: // "value": 40
				return strconv.FormatFloat(v, 'f', -1, 64)
			}
			return ""
		}
		line, ok := devicePhrase(str("action"), str("device_name"), str("value"), str("color"))
		if !ok {
			return "", false
		}
		said = append(said, line)
	}
	return strings.Join(said, " "), true
}

// devicePhrase is one action's confirmation ("Turned on the kitchen light.").
func devicePhrase(action, device, value, color string) (string, bool) {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(device, "the "), "The "))
	if name == "" {
		return "", false
	}
	the := "the " + name
	switch action {
	case "turn_on":
		return "Turned on " + the + ".", true
	case "turn_off":
		return "Turned off " + the + ".", true
	case "lock":
		return "Locked " + the + ".", true
	case "unlock":
		return "Unlocked " + the + ".", true
	case "play":
		return "Playing on " + the + ".", true
	case "pause":
		return "Paused " + the + ".", true
	case "volume_up":
		return "Turned " + the + " up.", true
	case "volume_down":
		return "Turned " + the + " down.", true
	case "next":
		return "Skipped ahead on " + the + ".", true
	case "previous":
		return "Went back on " + the + ".", true
	case "set_brightness":
		v := strings.TrimSuffix(value, "%")
		if !numberRE.MatchString(v) {
			return "", false
		}
		return "Set " + the + " to " + v + " percent.", true
	case "set_temperature":
		if !numberRE.MatchString(value) {
			return "", false
		}
		return "Set " + the + " to " + value + " degrees.", true
	case "set_mode":
		if !colorWordRE.MatchString(value) {
			return "", false
		}
		return "Set " + the + " to " + strings.ToLower(value) + ".", true
	case "set_color":
		if !colorWordRE.MatchString(color) { // "255,0,0" and the like: the model words it
			return "", false
		}
		return "Turned " + the + " " + strings.ToLower(color) + ".", true
	}
	return "", false
}

// lastToolCalls are the newest assistant message's tool calls by id.
func lastToolCalls(msgs []chatMsg) map[string]parse.ToolCall {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			out := map[string]parse.ToolCall{}
			for _, c := range msgs[i].ToolCalls {
				out[c.ID] = c
			}
			return out
		}
	}
	return nil
}
