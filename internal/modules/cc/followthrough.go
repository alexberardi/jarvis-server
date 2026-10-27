package cc

import (
	"encoding/json"
	"fmt"
	"strings"

	tf "github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
)

// The follow-through guards (beyond legacy): one nudge each per engine run when a prose reply
// leaves the user's request half done. Found on a desktop agent's real test pass (2026-10-15):
//   - "Remember that my parking spot is B12." got "Got it, Jordan. Parking spot B12 noted." and
//     no call: the must-call guard's keyword half never armed, because the desktop agent's tools
//     all carry keywords and the server's memory tools carried none (serverToolKeywords);
//   - a memory request answered "noted" with no remember call this turn (after the must-call
//     retry was spent, didn't arm, or got another tool such as recall) is nudged to call it
//     (nagUnsaved); with no tool that stores it, to say so (nagNoMemoryTool);
//   - "Do you remember where my parking spot is?" got "Not in my memory" without a search
//     (nagRecallFirst);
//   - "Open Omerite and type hello there." opened the app, then told the user to type "hello
//     there" themselves (nagUnfinished);
//   - "Stop watching the example.com one" got "I've stopped watching it" with no cancel_watch
//     call, and the watch stayed on (#221, nagNotCancelled);
//   - a reply ending "Let me open it in the browser instead…" or "I'll run an errand to …"
//     ended the turn with nothing done (#575, nagPromised).

const (
	nagRecallFirst   = "[RECALL_FIRST]"
	nagUnfinished    = "[UNFINISHED_REQUEST]"
	nagNoMemoryTool  = "[NO_MEMORY_TOOL]"
	nagUnsaved       = "[NOT_SAVED]"
	nagNotCancelled  = "[NOT_CANCELLED]"
	nagDeviceNotDone = "[DEVICE_NOT_DONE]"
	nagPromised      = "[PROMISED_NOT_DONE]"
	nagScreenNotDone = "[SCREEN_NOT_DONE]"
)

const (
	recallFirstRetry = nagRecallFirst + " You said you don't have that, but you have not searched the user's " +
		"memories this turn. Call recall with what they asked about, then answer from its result."
	unfinishedTypeFormat = nagUnfinished + " The user also asked you to type something on the computer: %q " +
		"(what they said after \"type\"; it is words to type, not an app to open). " + desktopTypeTool +
		" has not run this turn. Call it now with only the words to type in text (and the app they opened in app). " +
		"Never tell the user to type it themselves."
	unsavedRetry = nagUnsaved + " Nothing was saved: remember has not run this turn, so your reply is not true yet. " +
		"Call remember now with what the user asked you to remember."
	notCancelledRetry = nagNotCancelled + " Nothing was stopped: cancel_watch has not run this turn, so your reply " +
		"is not true yet. Call cancel_watch now with the watch the user means (its watch_id from list_watches if " +
		"you have it), then tell the user its message."
	noMemoryToolRetry = nagNoMemoryTool + " None of your tools can save that, so nothing was saved. Answer " +
		"again: tell the user plainly, in one short sentence, that you can't remember it for them here. Do not " +
		"say it was noted or saved."
	deviceNotDoneRetry = nagDeviceNotDone + " No successful control_device call ran this turn, so no device was " +
		"changed and your reply is not true yet. control_device is the tool for smart-home devices (control_node " +
		"is only this speaker's own volume). Call control_device now with the device the user means (copy its " +
		"entity_id from the device list), or if its result above failed, tell the user plainly it didn't work."
	promisedRetry = nagPromised + " Your reply says you will do something, but it ends your turn: nothing will do it. " +
		"Call the tool for it now (run_errand for a task on the computer or the web that takes several steps), or " +
		"tell the user plainly, in one short sentence, that you can't."
)

// screenNotDoneRetry (VOICE-FLOWS.md Q32): success is what a tool did this turn, never the
// reply's word for it.
const screenNotDoneRetry = nagScreenNotDone + " Your reply says something was done on the computer, but no action " +
	"tool succeeded this turn, so nothing changed and your reply is not true. If the user asked for an action (even " +
	"one done before: \"again\", \"a bit more\"), call the tool for it now. Otherwise tell the user plainly what was " +
	"not done."

// readOnlyScreenTools look and change nothing: they don't make a claimed action true.
var readOnlyScreenTools = map[string]bool{"desktop_list_windows": true, "desktop_list_apps": true, "desktop_views": true,
	"desktop_read_screen": true, "desktop_controls": true, "browser_list_tabs": true, "browser_read_page": true}

// serverToolKeywords are the force guard's keywords for offered server tools, whose definitions
// carry none (a node's own tools carry theirs). Without them the guard's keyword half only knew
// the node's words: a Pi's happened to cover "remember", a desktop agent's didn't.
var serverToolKeywords = map[string][]string{
	"remember":       {"remember", "note", "don't forget", "keep in mind"},
	"forget":         {"forget"},
	"control_device": {"turn on", "turn off", "switch on", "switch off", "light", "lights", "lock", "unlock", "thermostat", "dim", "brighten"},
}

// offeredServerKeywords are serverToolKeywords for the server tools this conversation offers.
func offeredServerKeywords(conv *conversation) []string {
	var out []string
	for _, n := range []string{"remember", "forget", "control_device"} {
		if conv.serverNames[n] {
			out = append(out, serverToolKeywords[n]...)
		}
	}
	return out
}

// followThroughNag is the nudge for a prose reply (outside <think>) that leaves the request half
// done, or "". Each fires at most once per engine run.
func followThroughNag(conv *conversation, utterance, reply string, msgs []chatMsg) string {
	if utterance == "" {
		return ""
	}
	ran := toolRanThisTurn(msgs)
	switch {
	case conv.serverNames["remember"] && !calledThisTurn(msgs, "remember") && countNags(msgs, nagUnsaved) < 1 &&
		tf.IsRememberRequest(utterance) && tf.ClaimsRemembered(reply):
		return unsavedRetry
	case !ran && !conv.serverNames["remember"] && countNags(msgs, nagNoMemoryTool) < 1 &&
		tf.IsRememberRequest(utterance) && tf.ClaimsRemembered(reply):
		return noMemoryToolRetry
	case conv.serverNames["cancel_watch"] && !calledThisTurn(msgs, "cancel_watch") && countNags(msgs, nagNotCancelled) < 1 &&
		tf.IsStopWatchingRequest(utterance) && tf.ClaimsStoppedWatching(reply):
		return notCancelledRetry
	case !ran && conv.serverNames["recall"] && countNags(msgs, nagRecallFirst) < 1 &&
		tf.AsksOwnFact(utterance) && tf.DeniesStoredFact(reply):
		return recallFirstRetry
	case recordingClaimNag(conv, reply, msgs) != "":
		return notStoppedRetry
	case recordingNotRunNag(conv, utterance, msgs) != "":
		return recordingNotRunNag(conv, utterance, msgs)
	case switchNotRunNag(conv, utterance, msgs) != "":
		return switchNotRunRetry
	// After the guards that own a target of their own (recordings, computer control), so they
	// win on a phrasing the device filter doesn't exclude (review of #512).
	case conv.hasTool("control_device") && countNags(msgs, nagDeviceNotDone) < 1 &&
		tf.IsDeviceChangeRequest(utterance) && tf.ClaimsDeviceChanged(reply) && !deviceDoneThisTurn(msgs):
		return deviceNotDoneRetry
	case conv.hasTool(desktopTypeTool) && countNags(msgs, nagUnfinished) < 1 && !calledThisTurn(msgs, desktopTypeTool) &&
		tf.TypeAfterOpenWords(utterance) != "":
		return fmt.Sprintf(unfinishedTypeFormat, tf.TypeAfterOpenWords(utterance))
	// Near last: the guards above own their targets and say more.
	case conv.hasScreenTools() && countNags(msgs, nagScreenNotDone) < 1 && tf.ClaimsScreenAction(reply) &&
		!succeededThisTurn(msgs, func(name string) bool { return !readOnlyScreenTools[name] }):
		return screenNotDoneRetry
	// Last: "Let me open it in the browser instead…" or "I'll run an errand…" ended the turn (#575).
	case countNags(msgs, nagPromised) < 1 && tf.PromisesAction(reply):
		return promisedRetry
	}
	return ""
}

// deviceDoneThisTurn reports a successful control_device result since the turn's user message
// (#512: a reply may only say a device changed when its tool call confirmed it). A node's
// result message carries no Name, so results are matched to calls by id.
func deviceDoneThisTurn(msgs []chatMsg) bool {
	return succeededThisTurn(msgs, func(name string) bool { return name == "control_device" })
}

// succeededThisTurn reports a successful result, since the turn's user message, of a tool whose
// name counts.
func succeededThisTurn(msgs []chatMsg, counts func(name string) bool) bool {
	start := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && !strings.HasPrefix(msgs[i].Content, nagDoubleCheck) {
			start = i
			break
		}
	}
	names := map[string]string{}
	for _, m := range msgs[start:] {
		for _, c := range m.ToolCalls {
			names[c.ID] = c.Function.Name
		}
		if m.Role != "tool" {
			continue
		}
		name := m.Name
		if name == "" {
			name = names[m.ToolCallID]
		}
		if !counts(name) {
			continue
		}
		var o map[string]any
		// A node answers with whatever its client wrote, so success is read leniently ("true", 1) as the rest of the module reads node results.
		dec := json.NewDecoder(strings.NewReader(m.Content))
		dec.UseNumber()
		if dec.Decode(&o) == nil {
			if s, ok := laxBool(o["success"]); ok && s {
				return true
			}
		}
	}
	return false
}

// calledThisTurn reports whether an assistant message since the turn's user message called
// the tool.
func calledThisTurn(msgs []chatMsg, name string) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "user" && !strings.HasPrefix(m.Content, nagDoubleCheck) {
			return false
		}
		for _, c := range m.ToolCalls {
			if c.Function.Name == name {
				return true
			}
		}
	}
	return false
}

// isFollowThroughNag reports one of this file's nudges (scrubbed like the other nags).
func isFollowThroughNag(m chatMsg) bool {
	return isNag(m, nagRecallFirst) || isNag(m, nagUnfinished) || isNag(m, nagNoMemoryTool) || isNag(m, nagUnsaved) ||
		isNag(m, nagNotCancelled) || isNag(m, nagDeviceNotDone) || isNag(m, nagScreenNotDone) || isNag(m, nagStopRecording) || isNag(m, nagNotStopped) ||
		isNag(m, nagRecordingNotRun) || isNag(m, nagSwitchNotRun) || isNag(m, nagAnswerAfterTool) ||
		isNag(m, nagPromised)
}
