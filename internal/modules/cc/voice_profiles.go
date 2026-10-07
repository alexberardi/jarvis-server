package cc

import (
	"errors"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The mobile voice-profile routes (docs/cc/06 §2.1 V1 and V6; legacy
// api/mobile_voice_profiles.py). Both act on the CALLER's own voiceprint in a household they
// are a member of: the user id comes from the JWT, never the request, so nobody can read or
// delete another member's profile. Enrollment itself is node-mic only (V7/V8/V10, media.go);
// the phone-mic routes V2–V5 are cut (D9).

func (m *Module) registerVoiceProfiles(mux *http.ServeMux) {
	const v0 = "/api/v0"
	mux.HandleFunc("GET "+v0+"/mobile/voice-profile/status", m.user(m.handleVoiceProfileStatus))
	mux.HandleFunc("DELETE "+v0+"/mobile/voice-profile", m.user(m.handleDeleteVoiceProfile))
}

// voiceProfileHousehold reads the required household_id query and checks membership.
func (m *Module) voiceProfileHousehold(w http.ResponseWriter, r *http.Request, u authn.User) (string, bool) {
	hh, present := r.URL.Query()["household_id"]
	if !present || len(hh) == 0 {
		validationError(w, "query -> household_id: Field required")
		return "", false
	}
	if err := m.requireRole(r.Context(), u.ID, hh[0], authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return "", false
	}
	if m.STT == nil {
		detail(w, http.StatusServiceUnavailable, "Speech-to-text unavailable")
		return "", false
	}
	return hh[0], true
}

// handleVoiceProfileStatus is V1: {"has_profile", "sample_count"} for the caller's voiceprint
// in the household, plus the additive "recognition_enabled" so the enrollment screen can say
// that speaker recognition is off (D35/M14: enrolling does not turn it on).
func (m *Module) handleVoiceProfileStatus(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, ok := m.voiceProfileHousehold(w, r, u)
	if !ok {
		return
	}
	ctx := r.Context()
	samples, err := m.STT.Samples(ctx, hh, u.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"has_profile": len(samples) > 0, "sample_count": len(samples),
		"recognition_enabled": m.STT.RecognitionEnabled(ctx, hh),
	})
}

// handleDeleteVoiceProfile is V6: delete the caller's voiceprint in the household (every
// sample). No profile is a 404 (D8; legacy relayed whisper's 404 as a 500).
func (m *Module) handleDeleteVoiceProfile(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, ok := m.voiceProfileHousehold(w, r, u)
	if !ok {
		return
	}
	err := m.STT.DeleteProfile(r.Context(), hh, u.ID)
	if errors.Is(err, stt.ErrNoProfile) {
		detail(w, http.StatusNotFound, "Voice profile not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: voice profile deleted via mobile", "user_id", u.ID, "household", hh)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "deleted", "user_id": u.ID, "household_id": hh})
}
