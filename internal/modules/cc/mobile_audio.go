package cc

import (
	"encoding/json"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Mobile audio (docs/cc/06 §2.1 A1/A2; legacy api/mobile_audio.py): push-to-talk STT and
// spoken replies for the phone, with the user's JWT instead of node auth. The caller must be a
// member of the household named in the request.

func (m *Module) registerMobileAudio(mux *http.ServeMux) {
	const v0 = "/api/v0"
	mux.HandleFunc("POST "+v0+"/mobile/stt", m.user(m.handleMobileSTT))
	mux.HandleFunc("POST "+v0+"/mobile/tts", m.user(m.handleMobileTTS))
}

// handleMobileSTT is A1: multipart file + household_id (+ language, ignored, M13). The phone
// knows who is speaking, so the speaker pass is skipped and raw.speaker is the JWT user:
// {"text", "raw": {<transcription>, "speaker": {"user_id", "confidence": 1.0, "source": "jwt"}}}.
func (m *Module) handleMobileSTT(w http.ResponseWriter, r *http.Request, u authn.User) {
	if !parseUpload(w, r) {
		return
	}
	file, hasFile, err := formFile(r, "file")
	hh, hasHH := r.MultipartForm.Value["household_id"]
	var missing []string
	if !hasFile {
		missing = append(missing, "body -> file: Field required")
	}
	if !hasHH || len(hh) == 0 {
		missing = append(missing, "body -> household_id: Field required")
	}
	if len(missing) > 0 {
		validationError(w, missing...)
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if err := m.requireRole(r.Context(), u.ID, hh[0], authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	if m.STT == nil {
		detail(w, http.StatusServiceUnavailable, "STT unavailable")
		return
	}
	res, err := m.STT.Transcribe(r.Context(), file, stt.TranscribeOptions{})
	if err != nil {
		m.sttError(w, err)
		return
	}
	raw := map[string]any{}
	b, err := json.Marshal(res)
	if err == nil {
		err = json.Unmarshal(b, &raw)
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	raw["speaker"] = map[string]any{"user_id": u.ID, "confidence": 1.0, "source": "jwt"}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"text": res.Text, "raw": raw})
}

// handleMobileTTS is A2: JSON {"text", "household_id"} → a complete RIFF WAV, as M1.
func (m *Module) handleMobileTTS(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	text, _ := b.str("text", true)
	hh, _ := b.str("household_id", true)
	if !b.done(w) {
		return
	}
	if err := m.requireRole(r.Context(), u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	m.speakWAV(w, r, text)
}
