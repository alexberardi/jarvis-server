package voice

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"
)

// Voice-profile takes on the node's microphone: the enroll_voice and verify_voice verbs the
// mobile app's "record on this node" publishes (V7/V8). Ported from the Python node
// (N:scripts/mqtt_tts_listener.py handle_enroll_voice / handle_verify_voice): say the cue,
// wait the preroll, record a fixed length, upload it (M4/M5) and post the outcome to V9.

const (
	takeCue = "Read the prompt on your screen, starting now."
	// takePreroll is the Python node's preroll_secs: time to start reading after the cue.
	takePreroll = 1500 * time.Millisecond
	// maxTakeSecs bounds a duration_secs from the command (jarvisd sends 8 and 5).
	maxTakeSecs = 30.0
)

// Recorder records a take through the voice pipeline (*Mic).
type Recorder interface {
	Record(ctx context.Context, t Take) ([]int16, error)
}

// ProfileServer is command-center's voice-profile routes (*HTTPServer).
type ProfileServer interface {
	EnrollVoice(ctx context.Context, userID int64, wav []byte) (map[string]any, error)
	VerifyVoice(ctx context.Context, userID int64, householdID string, wav []byte) (VerifyResult, error)
	PostVoiceProfileResult(ctx context.Context, rid string, result map[string]any) error
}

// VerifyResult is M5's answer.
type VerifyResult struct {
	Matched    bool    `json:"matched"`
	Confidence float64 `json:"confidence"`
}

// VoiceProfiles answers enroll_voice and verify_voice. A desktop agent doesn't: its speaker is
// the signed-in user (#143).
type VoiceProfiles struct {
	Mic Recorder
	Log *slog.Logger
}

// profileTake is a command's ids and length, or ok false when an id is missing (the Python
// node skips such a command without a word: there is nowhere to send an error).
type profileTake struct {
	userID      int64
	householdID string
	rid         string
	secs        float64
}

func parseTake(det map[string]any, defaultSecs float64) (profileTake, bool) {
	uid, _ := det["user_id"].(float64)
	hh, _ := det["household_id"].(string)
	rid, _ := det["request_id"].(string)
	if uid == 0 || hh == "" || rid == "" {
		return profileTake{}, false
	}
	secs, _ := det["duration_secs"].(float64)
	if secs <= 0 || math.IsNaN(secs) {
		secs = defaultSecs
	}
	return profileTake{userID: int64(uid), householdID: hh, rid: rid, secs: min(secs, maxTakeSecs)}, true
}

// record makes the take, or returns the V9 failure to post.
func (v *VoiceProfiles) record(ctx context.Context, verb string, t profileTake) ([]int16, map[string]any) {
	v.Log.Info(verb+": recording", "request_id", t.rid, "user_id", t.userID, "secs", t.secs)
	pcm, err := v.Mic.Record(ctx, Take{Cue: takeCue, Preroll: takePreroll, Secs: t.secs})
	if errors.Is(err, ErrMicUnavailable) {
		return nil, fail("audio_bus_unavailable")
	}
	if err != nil {
		return nil, fail("record_failed: " + err.Error())
	}
	return pcm, nil
}

func fail(msg string) map[string]any { return map[string]any{"success": false, "error": msg} }

// timeout bounds one take from the command to the posted result: waiting out a turn in
// progress, the cue, the recording and the upload.
func timeout(secs float64) time.Duration {
	return 2*time.Minute + time.Duration(secs*float64(time.Second))
}

// Enroll is enroll_voice: a take uploaded to M4 as the user's voiceprint sample.
func (v *VoiceProfiles) Enroll(ctx context.Context, srv ProfileServer, det map[string]any) {
	t, ok := parseTake(det, 8)
	if !ok {
		v.Log.Warn("enroll_voice without user_id, household_id or request_id; ignored")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(t.secs))
	defer cancel()
	pcm, res := v.record(ctx, "enroll_voice", t)
	if res == nil {
		resp, err := srv.EnrollVoice(ctx, t.userID, wavBytes(pcm))
		if err != nil {
			// jarvisd's V9 relays an upload_failed after a 422 as "low_quality": keep the prefix.
			res = fail("upload_failed: " + err.Error())
		} else {
			res = map[string]any{"success": true, "user_id": t.userID, "response": resp,
				"duration_secs": float64(len(pcm)) / CaptureRate}
		}
	}
	v.post(ctx, srv, "enroll_voice", t.rid, res)
}

// Verify is verify_voice: a take scored against the user's voiceprint (M5).
func (v *VoiceProfiles) Verify(ctx context.Context, srv ProfileServer, det map[string]any) {
	t, ok := parseTake(det, 5)
	if !ok {
		v.Log.Warn("verify_voice without user_id, household_id or request_id; ignored")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(t.secs))
	defer cancel()
	pcm, res := v.record(ctx, "verify_voice", t)
	if res == nil {
		r, err := srv.VerifyVoice(ctx, t.userID, t.householdID, wavBytes(pcm))
		if err != nil {
			res = fail("verify_failed: " + err.Error())
		} else {
			res = map[string]any{"success": true, "matched": r.Matched, "confidence": r.Confidence}
		}
	}
	v.post(ctx, srv, "verify_voice", t.rid, res)
}

func (v *VoiceProfiles) post(ctx context.Context, srv ProfileServer, verb, rid string, res map[string]any) {
	v.Log.Info(verb+": done", "request_id", rid, "success", res["success"], "error", res["error"], "matched", res["matched"])
	if err := srv.PostVoiceProfileResult(ctx, rid, res); err != nil {
		v.Log.Warn(verb+": posting the result failed", "request_id", rid, "err", err)
	}
}
