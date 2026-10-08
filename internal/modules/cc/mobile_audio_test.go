package cc

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

func mobileSTTBody(t *testing.T, household string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "audio.wav")
	_, _ = fw.Write(stt.EncodeWAV(make([]float32, 16000)))
	if household != "" {
		_ = mw.WriteField("household_id", household)
	}
	_ = mw.WriteField("language", "en")
	_ = mw.Close()
	return buf.String(), mw.FormDataContentType()
}

// A1 /mobile/stt and A2 /mobile/tts (docs/cc/06 §2.1).
func TestMobileAudio(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.auth.addUser(9, "hh-other", "member")
	alex, outsider := bearer("tok-7"), bearer("tok-9")
	ve.stt.recognition = true
	speaker := int64(8)
	ve.stt.speaker = &speaker // would be matched by a speaker pass, which A1 must skip

	with := func(h hdr, ct string) hdr {
		out := hdr{"Content-Type": ct}
		for k, v := range h {
			out[k] = v
		}
		return out
	}

	body, ct := mobileSTTBody(t, voiceHH)
	r := ve.do("POST", "/api/v0/mobile/stt", body, with(alex, ct)).want(200).json()
	raw, _ := r["raw"].(map[string]any)
	sp, _ := raw["speaker"].(map[string]any)
	if r["text"] != raw["text"] || sp["user_id"] != 7.0 || sp["confidence"] != 1.0 || sp["source"] != "jwt" || len(sp) != 3 {
		t.Fatalf("stt %v", r)
	}
	if _, ok := raw["segments"]; !ok {
		t.Fatalf("raw lacks the transcription: %v", raw)
	}
	body, ct = mobileSTTBody(t, "")
	if d := ve.do("POST", "/api/v0/mobile/stt", body, with(alex, ct)).want(400).json()["details"]; fmt.Sprint(d) != "[body -> household_id: Field required]" {
		t.Fatalf("validation %v", d)
	}
	body, ct = mobileSTTBody(t, voiceHH)
	ve.do("POST", "/api/v0/mobile/stt", body, with(outsider, ct)).detail(403, "User is not a member of this household")
	ve.do("POST", "/api/v0/mobile/stt", body, hdr{"Content-Type": ct}).want(401)

	res := ve.do("POST", "/api/v0/mobile/tts", map[string]any{"text": "**Hello** there", "household_id": voiceHH}, alex).want(200)
	if res.header.Get("Content-Type") != "audio/wav" || !bytes.HasPrefix(res.body, []byte("RIFF")) || !bytes.Contains(res.body, []byte("<Hello there>")) {
		t.Fatalf("tts %q %q", res.header.Get("Content-Type"), res.body)
	}
	if d := ve.do("POST", "/api/v0/mobile/tts", map[string]any{"text": "x"}, alex).want(400).json()["details"]; fmt.Sprint(d) != "[body -> household_id: Field required]" {
		t.Fatalf("validation %v", d)
	}
	ve.do("POST", "/api/v0/mobile/tts", map[string]any{"text": "x", "household_id": voiceHH}, outsider).detail(403, "User is not a member of this household")
	ve.do("POST", "/api/v0/mobile/tts", map[string]any{"text": "", "household_id": voiceHH}, alex).detail(400, "No text provided")
}
