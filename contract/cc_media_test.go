//go:build contract

package contract

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Command-center's media proxy and the node-mic voice-profile handoff (docs/cc/06 §2.1 M1–M5,
// A1–A2, V1, V6–V10). Nodes reach TTS and STT only through these routes (node X-API-Key);
// mobile uses the JWT twins A1/A2. jarvis-tts and jarvis-whisper's own contracts are in
// tts_test.go and whisper_test.go; here only what CC adds or changes is frozen: the re-emitted
// X-Audio-* headers (which must agree with jarvis-tts /audio/format), the pass-through bodies,
// CC's 400 validation envelope, and the enroll/verify round trip over MQTT.

// ccMediaTTSFormat reads jarvis-tts /audio/format (app credentials), the format CC's audio
// routes must announce.
func ccMediaTTSFormat(t *testing.T) (rate, channels, width int) {
	t.Helper()
	T(t).Need(t, TTS)
	rate, channels, width, _ = ttsFormat(t, SharedApp(t))
	return
}

func ccExpectPCMHeaders(r *RawResp, rate, channels, width int) {
	r.t.Helper()
	r.ExpectStatus(http.StatusOK).ExpectMediaType("audio/raw")
	r.ExpectChunked()
	// D40 01.Q8: the real engine rate, as decimal strings.
	r.ExpectHeaderVal("X-Audio-Sample-Rate", strconv.Itoa(rate))
	r.ExpectHeaderVal("X-Audio-Channels", strconv.Itoa(channels))
	r.ExpectHeaderVal("X-Audio-Sample-Width", strconv.Itoa(width))
}

func ccExpectPCMBody(r *RawResp, frame int) {
	r.t.Helper()
	if len(r.Body) == 0 || len(r.Body)%frame != 0 {
		r.Fatalf("PCM body: %d bytes is not a positive whole number of %d-byte frames", len(r.Body), frame)
	}
	if len(r.Body) >= 4 && string(r.Body[:4]) == "RIFF" {
		r.Fatalf("stream body carries a WAV header; it must be raw PCM")
	}
}

func ccExpectWAV(r *RawResp, rate, channels, width int) {
	r.t.Helper()
	r.ExpectStatus(http.StatusOK).ExpectMediaType("audio/wav")
	info, err := ParseWAV(r.Body)
	if err != nil {
		r.Fatalf("not a WAV: %v", err)
	}
	if info.Format != 1 || info.Channels != channels || info.SampleRate != rate || info.BitsPerSample != 8*width {
		r.Fatalf("WAV fmt %+v disagrees with /audio/format (%d Hz, %d ch, %d bytes)", info, rate, channels, width)
	}
	if info.DataLen == 0 || info.DataLen != info.DataAvailable || info.DataLen%(channels*width) != 0 {
		r.Fatalf("WAV data: declared %d, present %d, frame %d", info.DataLen, info.DataAvailable, channels*width)
	}
}

// TestCCMediaTTS freezes M1 /media/tts/speak (a complete WAV) and M2 /media/tts/speak/stream
// (headerless chunked PCM with X-Audio-*), both node-authenticated.
func TestCCMediaTTS(t *testing.T) {
	tg := T(t)
	n := SharedCCNode(t)
	rate, channels, width := ccMediaTTSFormat(t)
	body := map[string]string{"text": "Contract media test."}

	t.Run("speak", func(t *testing.T) {
		ccExpectWAV(tg.SlowJSON(t, CommandCenter, "/api/v0/media/tts/speak", body, n.APIKeyH()), rate, channels, width)
	})

	t.Run("speak_stream", func(t *testing.T) {
		r := tg.SlowJSON(t, CommandCenter, "/api/v0/media/tts/speak/stream", body, n.APIKeyH())
		ccExpectPCMHeaders(r, rate, channels, width)
		// CC never forwards jarvis-tts's X-Audio-Provider (api/media.py builds its own headers).
		if r.HasHeader("X-Audio-Provider") {
			r.Fatalf("CC must not forward X-Audio-Provider")
		}
		ccExpectPCMBody(r, channels*width)
	})

	t.Run("empty_text", func(t *testing.T) {
		empty := map[string]string{"text": ""}
		if Jarvisd() {
			// D8: empty text (after clean_for_tts) is a 400 JSON error on both routes.
			tg.Post(t, CommandCenter, "/api/v0/media/tts/speak", empty, n.APIKeyH()).
				ExpectError(http.StatusBadRequest, "No text provided")
			tg.Post(t, CommandCenter, "/api/v0/media/tts/speak/stream", empty, n.APIKeyH()).
				ExpectError(http.StatusBadRequest, "No text provided")
			return
		}
		// LEGACY-BUG: jarvis-tts answers 200 {"error":"No text provided"} and CC relabels those
		// bytes as audio (docs/cc/06 §8 item 7): audio/wav on M1; on M2 audio/raw with the
		// default headers 22050/1/2 because the JSON reply carries no X-Audio-*.
		r := tg.SlowJSON(t, CommandCenter, "/api/v0/media/tts/speak", empty, n.APIKeyH())
		r.ExpectStatus(http.StatusOK).ExpectMediaType("audio/wav")
		if !strings.Contains(string(r.Body), `"No text provided"`) {
			r.Fatalf("want the relabelled JSON error body")
		}
		r = tg.SlowJSON(t, CommandCenter, "/api/v0/media/tts/speak/stream", empty, n.APIKeyH())
		r.ExpectStatus(http.StatusOK).ExpectMediaType("audio/raw")
		r.ExpectHeaderVal("X-Audio-Sample-Rate", "22050")
		if !strings.Contains(string(r.Body), `"No text provided"`) {
			r.Fatalf("want the relabelled JSON error body")
		}
	})

	t.Run("validation_and_auth", func(t *testing.T) {
		for _, p := range []string{"/api/v0/media/tts/speak", "/api/v0/media/tts/speak/stream"} {
			tg.Post(t, CommandCenter, p, map[string]any{}, n.APIKeyH()).
				Expect(http.StatusBadRequest, ccValidation("body -> text: Field required"))
			tg.Post(t, CommandCenter, p, body, H{"X-API-Key": n.ID + ":wrong-" + randHex(4)}).
				ExpectError(http.StatusUnauthorized, "Invalid node credentials")
		}
	})
}

// TestCCMediaTranscribe freezes M3 /media/whisper/transcribe: whisper's body passed through
// verbatim (sttResponse), the node's full upload accepted, CC's 400 for a missing file.
func TestCCMediaTranscribe(t *testing.T) {
	tg := T(t)
	tg.Need(t, Whisper)
	n := SharedCCNode(t)
	const path = "/api/v0/media/whisper/transcribe"

	t.Run("file_only", func(t *testing.T) {
		body, ct := MultipartBody(FormFile("file", "audio.wav", sttWAV()))
		tg.SlowDo(t, CommandCenter, http.MethodPost, path, ct, body, n.APIKeyH()).Expect(http.StatusOK, sttResponse)
	})
	t.Run("node_upload", func(t *testing.T) {
		// The node's real upload: command audio, wake+command speaker audio, conversation id
		// (keys the trace and wake verification), language/task (accepted, ignored; M13).
		body, ct := MultipartBody(
			FormFile("file", "audio.wav", sttWAV()),
			FormFile("speaker_audio", "speaker.wav", SineWAV(16000, 3.0, 330)),
			FormField("conversation_id", "contract-"+tg.RunID+"-stt-"+randHex(3)),
			FormField("language", "en"),
			FormField("task", "transcribe"),
		)
		tg.SlowDo(t, CommandCenter, http.MethodPost, path, ct, body, n.APIKeyH()).Expect(http.StatusOK, sttResponse)
	})
	t.Run("missing_file", func(t *testing.T) {
		body, ct := MultipartBody(FormField("language", "en"))
		tg.SlowDo(t, CommandCenter, http.MethodPost, path, ct, body, n.APIKeyH()).
			Expect(http.StatusBadRequest, ccValidation("body -> file: Field required"))
	})
	t.Run("auth", func(t *testing.T) {
		body, ct := MultipartBody(FormFile("file", "audio.wav", sttWAV()))
		tg.SlowDo(t, CommandCenter, http.MethodPost, path, ct, body, H{"X-API-Key": n.ID + ":wrong-" + randHex(4)}).
			ExpectError(http.StatusUnauthorized, "Invalid node credentials")
	})
}

// TestCCMobileAudio freezes A1 /mobile/stt and A2 /mobile/tts (JWT + member of household_id).
func TestCCMobileAudio(t *testing.T) {
	tg := T(t)
	tg.Need(t, CommandCenter, Whisper)
	u := SharedUser(t)
	rate, channels, width := ccMediaTTSFormat(t)

	t.Run("stt", func(t *testing.T) {
		body, ct := MultipartBody(FormFile("file", "audio.wav", sttWAV()), FormField("household_id", u.HouseholdID), FormField("language", "en"))
		// The speaker pass is skipped and raw.speaker is overwritten from the JWT.
		tg.SlowDo(t, CommandCenter, http.MethodPost, "/api/v0/mobile/stt", ct, body, u.H()).Expect(http.StatusOK, Obj{
			"text": String,
			"raw": Obj{
				"text":     String,
				"segments": ArrayOf(Obj{"t0_ms": Int, "t1_ms": Int, "text": String}),
				"speaker":  Obj{"user_id": Eq(u.ID), "confidence": Eq(1.0), "source": Eq("jwt")},
				"affect":   Null,
			},
		})
		body, ct = MultipartBody(FormFile("file", "audio.wav", sttWAV()))
		tg.SlowDo(t, CommandCenter, http.MethodPost, "/api/v0/mobile/stt", ct, body, u.H()).
			Expect(http.StatusBadRequest, ccValidation("body -> household_id: Field required"))
		other := NewUser(t)
		body, ct = MultipartBody(FormFile("file", "audio.wav", sttWAV()), FormField("household_id", u.HouseholdID))
		tg.SlowDo(t, CommandCenter, http.MethodPost, "/api/v0/mobile/stt", ct, body, other.H()).
			ExpectError(http.StatusForbidden, "User is not a member of this household")
	})

	t.Run("tts", func(t *testing.T) {
		ccExpectWAV(tg.SlowJSON(t, CommandCenter, "/api/v0/mobile/tts",
			map[string]string{"text": "Contract mobile test.", "household_id": u.HouseholdID}, u.H()), rate, channels, width)
		tg.Post(t, CommandCenter, "/api/v0/mobile/tts", map[string]string{"text": "x"}, u.H()).
			Expect(http.StatusBadRequest, ccValidation("body -> household_id: Field required"))
		other := NewUser(t)
		tg.Post(t, CommandCenter, "/api/v0/mobile/tts", map[string]string{"text": "x", "household_id": u.HouseholdID}, other.H()).
			ExpectError(http.StatusForbidden, "User is not a member of this household")
		tg.Post(t, CommandCenter, "/api/v0/mobile/tts", map[string]string{"text": "x", "household_id": u.HouseholdID}).
			ExpectError(http.StatusUnauthorized, "Missing or invalid Authorization header")
	})
}

// TestCCNodeVoiceEnrollment walks the node-mic enrollment handoff end to end with a fake node:
// mobile V7/V8 start → MQTT enroll_voice / verify_voice on the node's commands topic → the
// node's M4/M5 upload through CC → V9 result → mobile V10 poll (202 pending, then the result
// once). V1 status and V6 delete close it. Own user and node, so no profile ever lands on a
// shared fixture.
func TestCCNodeVoiceEnrollment(t *testing.T) {
	tg := T(t)
	tg.Need(t, Whisper)
	tg.NeedMQTT(t)
	u := NewUser(t)
	n := NewCCNode(t, u)
	hh := u.HouseholdID
	hhq := url.QueryEscape(hh)
	c := DialMQTTNode(t, n)
	c.Subscribe(t, "jarvis/nodes/"+n.ID+"/#")
	// A fresh admin-created node has never been seen; V7/V8 refuse an offline node.
	tg.Post(t, CommandCenter, "/api/v0/admin/nodes/heartbeat", nil, n.APIKeyH()).ExpectStatus(http.StatusOK)

	t.Cleanup(func() {
		tg.do(CommandCenter, http.MethodDelete, "/api/v0/mobile/voice-profile?household_id="+hhq, nil, u.H())
		// LEGACY-BUG: whisper leaves an empty voice_profiles/<household>/ directory (see
		// TestWhisperVoiceProfiles); opt-in host cleanup.
		removeEmptyProfileDir(t, hh)
	})

	start := func(kind string, body map[string]any) string {
		t.Helper()
		body["node_id"] = n.ID
		rid := tg.Post(t, CommandCenter, "/api/v0/mobile/voice-profile/start-node-"+kind, body, u.H()).
			Expect(http.StatusOK, Obj{"request_id": UUID}).Object()["request_id"].(string)
		return rid
	}
	expectVerb := func(verb, rid string, secs float64, prompt string) {
		t.Helper()
		ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(n.ID, verb)), commandShape(verb, Obj{
			"request_id": Eq(rid), "user_id": Eq(u.ID), "household_id": Eq(hh),
			"prompt_text": Eq(prompt), "duration_secs": Eq(secs),
		}))
	}
	poll := func(rid string) *Resp {
		return tg.Get(t, CommandCenter, "/api/v0/mobile/voice-profile-results/"+rid, u.H())
	}
	status := func(count int) {
		t.Helper()
		shape := Obj{"has_profile": Eq(count > 0), "sample_count": Eq(count)}
		if Jarvisd() {
			// Additive: the enrollment screen says when recognition is off (D35/M14).
			shape["recognition_enabled"] = Bool
		}
		tg.Get(t, CommandCenter, "/api/v0/mobile/voice-profile/status?household_id="+hhq, u.H()).Expect(http.StatusOK, shape)
	}
	enrollURL := fmt.Sprintf("/api/v0/media/whisper/voice-profiles/enroll?user_id=%d", u.ID)
	verifyURL := fmt.Sprintf("/api/v0/media/whisper/voice-profiles/verify?user_id=%d&household_id=%s", u.ID, hhq)
	upload := func(path string, secs float64) *RawResp {
		body, ct := MultipartBody(FormFile("file", "take.wav", SineWAV(16000, secs, 220)))
		return tg.SlowDo(t, CommandCenter, http.MethodPost, path, ct, body, n.APIKeyH())
	}

	status(0)

	t.Run("verify_without_profile", func(t *testing.T) {
		rid := start("verification", map[string]any{})
		expectVerb("verify_voice", rid, 5.0, "")
		r := upload(verifyURL, 2.0)
		if Jarvisd() {
			// D8: the store's not-found is a 404, not a relayed 500.
			r.ExpectError(http.StatusNotFound, "No voice profile enrolled")
		} else {
			// LEGACY-BUG: whisper's 404 surfaces as an unhandled 500 in CC (docs/cc/06 §2.1 M5).
			r.ExpectStatus(http.StatusInternalServerError)
			// uvicorn drops the keep-alive connection after an unhandled error.
			tg.HTTP.CloseIdleConnections()
		}
		tg.Post(t, CommandCenter, "/api/v0/mobile/voice-profile-results/"+rid,
			map[string]any{"success": false, "error": "verify_failed: contract"}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		poll(rid).Expect(http.StatusOK, Obj{"success": Eq(false), "error": Eq("verify_failed: contract")})
	})

	t.Run("enroll", func(t *testing.T) {
		rid := start("enrollment", map[string]any{"prompt_text": "Say hello", "duration_secs": 6.5})
		expectVerb("enroll_voice", rid, 6.5, "Say hello")
		poll(rid).ExpectError(http.StatusAccepted, "pending")

		// LEGACY-BUG: V10 has no ownership check; any signed-in user may poll (and would
		// consume) another user's result. D4: jarvisd allows only the starting user.
		stranger := NewUser(t)
		if Jarvisd() {
			tg.Get(t, CommandCenter, "/api/v0/mobile/voice-profile-results/"+rid, stranger.H()).
				ExpectError(http.StatusForbidden, "Not authorized")
		} else {
			tg.Get(t, CommandCenter, "/api/v0/mobile/voice-profile-results/"+rid, stranger.H()).
				ExpectError(http.StatusAccepted, "pending")
		}

		// M4: the node never sends household_id or sample_index; CC uses the node's household
		// and whisper auto-allocates the index.
		res := upload(enrollURL, 3.0).Expect(http.StatusOK, Obj{
			"status": Eq("enrolled"), "user_id": Eq(u.ID), "household_id": Eq(hh),
			"sample_index": Eq(0), "total_samples": Eq(1),
		}).Object()

		// V9: the node's enroll-success body; V10 returns it verbatim, once.
		result := map[string]any{"success": true, "user_id": u.ID, "response": res, "duration_secs": 6.5}
		tg.Post(t, CommandCenter, "/api/v0/mobile/voice-profile-results/"+rid, result, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		poll(rid).Expect(http.StatusOK, Obj{
			"success": Eq(true), "user_id": Eq(u.ID), "duration_secs": Eq(6.5),
			"response": Obj{"status": Eq("enrolled"), "user_id": Eq(u.ID), "household_id": Eq(hh),
				"sample_index": Eq(0), "total_samples": Eq(1)},
		})
		poll(rid).ExpectError(http.StatusAccepted, "pending")
		status(1)

		// An upload with no pending start for this node and user.
		r := upload(enrollURL, 3.0)
		if Jarvisd() {
			// D4 (06.Q5): M4 must match a pending V7 start for this node and user.
			r.ExpectError(http.StatusForbidden, "No pending voice enrollment for this node and user")
			status(1)
		} else {
			// LEGACY-BUG: any node may enroll any user id into its household, unprompted.
			r.Expect(http.StatusOK, Obj{
				"status": Eq("enrolled"), "user_id": Eq(u.ID), "household_id": Eq(hh),
				"sample_index": Eq(1), "total_samples": Eq(2),
			})
			status(2)
		}
	})

	t.Run("verify", func(t *testing.T) {
		rid := start("verification", map[string]any{"duration_secs": 4})
		expectVerb("verify_voice", rid, 4.0, "")
		res := upload(verifyURL, 2.0).Expect(http.StatusOK, Obj{"matched": Bool, "confidence": Num, "user_id": Eq(u.ID)}).Object()
		tg.Post(t, CommandCenter, "/api/v0/mobile/voice-profile-results/"+rid,
			map[string]any{"success": true, "matched": res["matched"], "confidence": res["confidence"]}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		poll(rid).Expect(http.StatusOK, Obj{"success": Eq(true), "matched": Bool, "confidence": Num})
	})

	t.Run("start_errors", func(t *testing.T) {
		for _, kind := range []string{"enrollment", "verification"} {
			p := "/api/v0/mobile/voice-profile/start-node-" + kind
			tg.Post(t, CommandCenter, p, map[string]any{"node_id": "contract-nosuch-" + randHex(3)}, u.H()).
				ExpectError(http.StatusNotFound, "Node not found")
			stranger := NewUser(t)
			tg.Post(t, CommandCenter, p, map[string]any{"node_id": n.ID}, stranger.H()).
				ExpectError(http.StatusForbidden, "User is not a member of this household")
			tg.Post(t, CommandCenter, p, map[string]any{}, u.H()).
				Expect(http.StatusBadRequest, ccValidation("body -> node_id: Field required"))
		}
	})

	t.Run("delete", func(t *testing.T) {
		del := "/api/v0/mobile/voice-profile?household_id=" + hhq
		tg.Do(t, CommandCenter, http.MethodDelete, del, nil, u.H()).
			Expect(http.StatusOK, Obj{"status": Eq("deleted"), "user_id": Eq(u.ID), "household_id": Eq(hh)})
		status(0)
		r := tg.Do(t, CommandCenter, http.MethodDelete, del, nil, u.H())
		if Jarvisd() {
			// D8: no profile is a 404.
			r.ExpectError(http.StatusNotFound, "Voice profile not found")
		} else {
			// LEGACY-BUG: whisper's 404 becomes an unhandled 500, so mobile's "Re-Record All"
			// aborts when the profile is already gone (docs/cc/06 §2.1 V6).
			r.ExpectStatus(http.StatusInternalServerError)
			tg.HTTP.CloseIdleConnections()
		}
	})

	if left := c.Drain(300 * time.Millisecond); len(left) > 0 {
		for _, m := range left {
			t.Errorf("unexpected node message: %s %s", m.Topic, m.Payload)
		}
	}
}
