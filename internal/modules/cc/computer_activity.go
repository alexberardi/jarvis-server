package cc

import (
	"context"
	"regexp"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// What a desktop agent says Jarvis is doing with its computer, on every voice turn
// (computer_activity, #288). The user's test (2026-10-19): "record a macro called test2"
// started the recording, then the follow-up "I'm done." got "Got it. I've recorded the macro
// called test2" with no stop_recording call, and a new wake "Hey Jarvis, I'm done." was dropped
// as not for Jarvis; the recording went on. Now, while the node records:
//   - the prompt says a recording is running on this computer (computerActivityHint);
//   - a stop phrase (computeruse.IsRecordingStopPhrase, the agent's own list) skips the follow-up gate
//     and the not-for-me verdict, the model is nudged to call stop_recording (nagStopRecording),
//     and if it still doesn't, jarvisd calls it itself (stopRecordingFallback);
//   - a reply claiming it stopped or saved without stop_recording is nudged (nagNotStopped),
//     then replaced (unclaimStop).
// The agent stops the task itself on a stop phrase and says so (stopped): stop_recording then
// doesn't ask it again, its own upload carries the trace (stopRecordingTool).

// computerActivity is a turn's computer_activity.
type computerActivity struct {
	Kind    string // "recording", "controlling" or "paused"
	Task    string
	Stopped bool // the command was a stop phrase and the agent already stopped the task
}

// parseComputerActivity reads computer_activity ({kind, task, stopped}); an unknown kind is
// none.
func parseComputerActivity(m map[string]any) *computerActivity {
	a := &computerActivity{}
	a.Kind, _ = m["kind"].(string)
	a.Task, _ = m["task"].(string)
	a.Stopped, _ = m["stopped"].(bool)
	switch a.Kind {
	case "recording", "controlling", "paused":
		return a
	}
	return nil
}

func (a *computerActivity) recording() bool { return a != nil && a.Kind == "recording" }

// stopAsked: a task runs (or ran until this command) and the command says stop.
func (a *computerActivity) stopAsked(utterance string) bool {
	if a == nil {
		return false
	}
	if a.recording() {
		return a.Stopped || computeruse.IsRecordingStopPhrase(utterance)
	}
	return a.Stopped || computeruse.IsStopPhrase(utterance)
}

// stoppedHere is the kind of task the agent stopped on this command ("" none): stop_recording's
// servertools.Turn.ComputerStopped.
func (a *computerActivity) stoppedHere() string {
	if a == nil || !a.Stopped {
		return ""
	}
	return a.Kind
}

// computerActivityHint is the turn's line about the computer ("" when it's idle).
func computerActivityHint(a *computerActivity, utterance string) string {
	switch {
	case a == nil:
		return ""
	case a.recording() && a.stopAsked(utterance):
		return "[a recording is running on this computer, and the user just said they're done: call stop_recording " +
			"now (it is meant for you), then tell them its message.]"
	case a.recording():
		return "[a recording is running on this computer: Jarvis is watching what the user does, to learn a macro. " +
			"When they say they're done ('I'm done', 'that's it', 'stop recording'), call stop_recording. Never say " +
			"the recording stopped or was saved unless stop_recording ran.]"
	case a.stopAsked(utterance) && a.Stopped:
		return "[the user just stopped Jarvis's task on this computer (" + a.Task + "); it has stopped. Tell them so in a " +
			"few words.]"
	default:
		return "[Jarvis is using this computer right now: " + a.Task + ".]"
	}
}

const (
	nagStopRecording   = "[STOP_RECORDING]"
	nagNotStopped      = "[NOT_STOPPED]"
	nagRecordingNotRun = "[RECORDING_NOT_RUN]"

	startNotRunRetry = nagRecordingNotRun + " Nothing is recording: start_recording has not run this turn, so your " +
		"reply is not true yet. Call start_recording now."
	stopNotRunRetry = nagRecordingNotRun + " Nothing was stopped: stop_recording has not run this turn, so your reply " +
		"is not true yet. Call stop_recording now."

	stopRecordingRetry = nagStopRecording + " A recording is running on this computer and the user said they're done: " +
		"that is meant for you. stop_recording has not run this turn. Call it now."
	notStoppedRetry = nagNotStopped + " Nothing was stopped or saved: stop_recording has not run this turn and the " +
		"recording is still going, so your reply is not true. If the user said they're done, call stop_recording now; " +
		"otherwise answer again without saying it stopped, finished or was saved."

	// stillRecordingReply replaces a reply that claimed a stop that didn't happen.
	stillRecordingReply = "I'm still recording. Say \"stop recording\" when you're done."
	// stoppedReply answers a stop the agent made itself when the model had nothing to say.
	stoppedReply = "Okay, I stopped."
)

// claimsStopRE is a reply saying the recording ended: "I've stopped the recording", "I've
// recorded the macro", "the recording is saved". It names the recording (or macro): "I stopped
// the timer" or "your note is saved" during a recording are other tools' answers.
var claimsStopRE = regexp.MustCompile(`(?i)\b(stopped (the |your )?recording|` +
	`(saved|finished|ended|done with) (the |your |that |this )?(recording|macro|workflow)|` +
	`(have|'ve|’ve) recorded (the |your |that |this )?(macro|recording|workflow|it)|recorded (the|your) macro|` +
	`(finished|done|ended|end) recording|` +
	`(recording|macro) (is |has |was )?(been )?(done|finished|complete|over|saved|stopped|ended))\b`)

// recordingClaimNag is the follow-through nudge for a reply claiming the running recording
// stopped when stop_recording didn't run, or "".
func recordingClaimNag(conv *conversation, reply string, msgs []chatMsg) string {
	if !conv.computer.recording() || conv.computer.Stopped || !conv.serverNames[toolStopRecording] ||
		calledThisTurn(msgs, toolStopRecording) || countNags(msgs, nagNotStopped) >= 1 || !claimsStopRE.MatchString(reply) {
		return ""
	}
	return notStoppedRetry
}

// recordStartRE is a request to record a macro ("record a macro called X", "watch me do this",
// "learn how I do this"); not "record a reminder". recordStopRE is "stop recording".
var (
	recordStartRE = regexp.MustCompile(`(?i)\brecord (this |a |the |my |that )?(new )?(macro|workflow)\b|` +
		`\bwatch (me|how i)\b|\blearn how i\b|\blet me show you how\b`)
	recordStopRE = regexp.MustCompile(`(?i)\bstop recording\b`)
)

// recordingNotRunNag is the follow-through nudge, with nothing recording, for a request to
// start or stop recording answered in prose without the tool (#435: "I'm recording a macro
// called test2.", "Alright, recording's stopped."), or "". A running recording has its own
// (mustStopRecording, recordingClaimNag).
func recordingNotRunNag(conv *conversation, utterance string, msgs []chatMsg) string {
	if conv.computer.recording() || countNags(msgs, nagRecordingNotRun) >= 1 {
		return ""
	}
	switch {
	case conv.serverNames[toolStopRecording] && recordStopRE.MatchString(utterance) && !calledThisTurn(msgs, toolStopRecording):
		return stopNotRunRetry
	case conv.serverNames[toolStartRecording] && recordStartRE.MatchString(utterance) && !calledThisTurn(msgs, toolStartRecording):
		return startNotRunRetry
	}
	return ""
}

// mustStopRecording is the tool a turn has to call: stop_recording on a stop phrase while the
// node records (and offers it), else "".
func mustStopRecording(conv *conversation, utterance string) string {
	if conv.computer.recording() && conv.computer.stopAsked(utterance) && conv.serverNames[toolStopRecording] {
		return toolStopRecording
	}
	return ""
}

// afterComputerTurn holds a finished turn to #288's promises: a stop phrase while recording
// ends with stop_recording having run (jarvisd calls it when the model didn't), one the agent
// already carried out is never dropped as not for Jarvis, and no reply claims a stop that
// didn't happen. res is unchanged when none applies.
func (m *Module) afterComputerTurn(ctx context.Context, conv *conversation, utterance string, res engineResult, out []chatMsg) (engineResult, []chatMsg) {
	a := conv.computer
	if a == nil || (res.Stop != stopComplete && res.Stop != stopNotForMe) || calledThisTurn(out, toolStopRecording) {
		return res, out
	}
	reply := func(msg string) (engineResult, []chatMsg) {
		for len(out) > 0 && out[len(out)-1].Role == "assistant" && len(out[len(out)-1].ToolCalls) == 0 {
			out = out[:len(out)-1]
		}
		out = append(out, chatMsg{Role: "assistant", Content: msg})
		return engineResult{Stop: stopComplete, Message: msg}, out
	}
	switch {
	case mustStopRecording(conv, utterance) != "":
		m.deps.Log.Info("cc: stop phrase while recording, model didn't stop it; stopping", "conversation_id", conv.id,
			"node_id", conv.nodeID, "transcript", utterance)
		v, err := (&stopRecordingTool{m: m}).Execute(ctx, servertools.Call{Name: toolStopRecording, Args: pyjson.NewObject()},
			m.toolTurn(conv, utterance))
		msg := "I couldn't stop the recording. Use Stop recording in the Jarvis menu."
		if o, ok := v.(*pyjson.Object); ok && err == nil {
			if s, _ := o.Get("message"); s != nil {
				msg, _ = s.(string)
			}
		}
		return reply(msg)
	case res.Stop == stopNotForMe && a.Stopped && a.stopAsked(utterance):
		return reply(stoppedReply)
	case res.Stop == stopComplete && a.recording() && !a.Stopped && countNags(out, nagNotStopped) >= 1 &&
		claimsStopRE.MatchString(res.Message):
		m.deps.Log.Info("cc: reply claimed the recording stopped; it didn't", "conversation_id", conv.id, "reply", res.Message)
		return reply(stillRecordingReply)
	}
	return res, out
}
