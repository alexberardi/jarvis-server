package computer

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// MaxKeyframeAttachments is how many of a trace's keyframes computer_record_stop sends; the rest
// stay on this computer.
const MaxKeyframeAttachments = 4

// RecordTools are the recorder's node tools: computer_record_start {mode},
// computer_record_stop {recording_id} and computer_record_delete {recording_id}. jarvisd's
// start_recording / stop_recording tools and its retention sweep call them; the model never
// sees them (computeruse.IsServerOnlyTool).
type RecordTools struct{ R *Recorder }

var recordSpecs = []node.Spec{
	{Name: computeruse.ToolRecordStart, Description: "Start recording what the user does, to learn a workflow from it.",
		Params: []node.Param{{Name: "mode", Type: "string", Enum: []string{ModeBrowser, ModeDesktop},
			Description: "browser (the default): the browser extension's actions; desktop: the screen"}}},
	{Name: computeruse.ToolRecordStop, Description: "Stop the recording and return its trace.",
		Params: []node.Param{{Name: "recording_id", Type: "string", Description: "The recording to stop; omit for the one going on"}}},
	{Name: computeruse.ToolRecordDelete, Description: "Delete this computer's copy of a recording.",
		Params: []node.Param{{Name: "recording_id", Type: "string", Required: true}}},
}

func (t *RecordTools) Available(context.Context) []node.ToolDef {
	out := make([]node.ToolDef, len(recordSpecs))
	for i, s := range recordSpecs {
		out[i] = s.Render()
	}
	return out
}

func (t *RecordTools) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	id, _ := args["recording_id"].(string)
	switch name {
	case computeruse.ToolRecordStart:
		mode, _ := args["mode"].(string)
		if mode == "" {
			mode = ModeBrowser
		}
		id, err := t.R.Start(ctx, mode)
		if err != nil {
			return recordFail(err), true
		}
		return map[string]any{"success": true, "recording_id": id, "mode": mode, "max_minutes": int(MaxRecording.Minutes())}, true
	case computeruse.ToolRecordStop:
		tr, err := t.R.Stop(id)
		if err != nil {
			return recordFail(err), true
		}
		return TraceOutput(tr), true
	case computeruse.ToolRecordDelete:
		if err := t.R.Delete(id); err != nil {
			return node.Fail("%v", err), true
		}
		return map[string]any{"success": true}, true
	}
	return nil, false
}

// TraceOutput is a stopped recording as computer_record_stop answers it (and the upload
// carries): the trace, and at most MaxKeyframeAttachments keyframes as image_attachments.
func TraceOutput(tr Trace) map[string]any {
	out := map[string]any{"success": true, "recording_id": tr.ID, "mode": tr.Mode, "reason": tr.Reason,
		"started_at": tr.StartedAt, "stopped_at": tr.StoppedAt, "trace": tr.Events, "events": len(tr.Events),
		"keyframes": len(tr.Keyframes), "truncated": tr.Truncated}
	var atts []any
	for _, k := range SampleKeyframes(tr.Keyframes, MaxKeyframeAttachments) {
		b, err := os.ReadFile(k.Path)
		if err != nil {
			continue
		}
		atts = append(atts, map[string]any{"mime": "image/jpeg", "data": base64.StdEncoding.EncodeToString(b),
			"label": "keyframe at event " + strconv.Itoa(k.Seq)})
	}
	if len(atts) > 0 {
		out["image_attachments"] = atts
	}
	return out
}

// recordFail is a failure output with a code jarvisd tells apart.
func recordFail(err error) map[string]any {
	o := node.Fail("%v", err)
	switch {
	case errors.Is(err, ErrNotRecording):
		o["error_code"] = "not_recording"
	case errors.Is(err, ErrBusy):
		o["error_code"] = "busy"
	case errors.Is(err, ErrDesktopRecording):
		o["error_code"] = "unsupported"
	}
	return o
}
