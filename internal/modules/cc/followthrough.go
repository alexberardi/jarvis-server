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
//     call, and the watch stayed on (#221, nagNotCancelled).

const (
	nagRecallFirst   = "[RECALL_FIRST]"
	nagUnfinished    = "[UNFINISHED_REQUEST]"
	nagNoMemoryTool  = "[NO_MEMORY_TOOL]"
	nagUnsaved       = "[NOT_SAVED]"
	nagNotCancelled  = "[NOT_CANCELLED]"
	nagDeviceNotDone = "[DEVICE_NOT_DONE]"
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
)

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
	case conv.hasTool("control_device") && countNags(msgs, nagDeviceNotDone) < 1 &&
		tf.IsDeviceChangeRequest(utterance) && tf.ClaimsDeviceChanged(reply) && !deviceDoneThisTurn(msgs):
		return deviceNotDoneRetry
	case !ran && conv.serverNames["recall"] && countNags(msgs, nagRecallFirst) < 1 &&
		tf.AsksOwnFact(utterance) && tf.DeniesStoredFact(reply):
		return recallFirstRetry
	case recordingClaimNag(conv, reply, msgs) != "":
		return notStoppedRetry
	case recordingNotRunNag(conv, utterance, msgs) != "":
		return recordingNotRunNag(conv, utterance, msgs)
	case switchNotRunNag(conv, utterance, msgs) != "":
		return switchNotRunRetry
	case conv.hasTool(desktopTypeTool) && countNags(msgs, nagUnfinished) < 1 && !calledThisTurn(msgs, desktopTypeTool):
		if words := tf.TypeAfterOpenWords(utterance); words != "" {
			return fmt.Sprintf(unfinishedTypeFormat, words)
		}
	}
	return ""
}

// deviceDoneThisTurn reports a successful control_device result since the turn's user message
// (#512: a reply may only say a device changed when its tool call confirmed it). A node's
// result message carries no Name, so results are matched to calls by id.
func deviceDoneThisTurn(msgs []chatMsg) bool {
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
		if name != "control_device" {
			continue
		}
		var o map[string]any
		// A node's own control_device answers with whatever its client wrote, so success is
		// read leniently ("true", 1) as the rest of the module reads node results.
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
		isNag(m, nagNotCancelled) || isNag(m, nagDeviceNotDone) || isNag(m, nagStopRecording) || isNag(m, nagNotStopped) ||
		isNag(m, nagRecordingNotRun) || isNag(m, nagSwitchNotRun) || isNag(m, nagAnswerAfterTool)
}
