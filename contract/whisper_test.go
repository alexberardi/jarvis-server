//go:build contract

package contract

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// jarvis-whisper-api: STT and the voice-profile store (docs/cc/06 §2.1 M3–M5, V1–V6). Nodes
// and mobile reach these only through command-center's media proxy, which adds app-to-app
// credentials plus the X-Context-* headers; these tests call whisper the same way. The input is
// a generated 16 kHz sine WAV, so the transcript text is not asserted, only its shape.

// sttResponse is POST /transcribe's body. D38: `affect` stays in the response and is always
// null once the affect pass is gone; it is null on the target today (voice.emotion_enabled off).
// `speaker` is always present: {user_id: null, confidence: 0.0} when recognition is off.
var sttResponse = Obj{
	"text":     String,
	"segments": ArrayOf(Obj{"t0_ms": Int, "t1_ms": Int, "text": String}),
	"speaker":  Obj{"user_id": NullOr(Int), "confidence": Num},
	"affect":   Null,
}

func sttWAV() []byte { return SineWAV(16000, 1.0, 440) }

func TestWhisperTranscribe(t *testing.T) {
	tg := T(t)
	tg.Need(t, Whisper)
	app := SharedApp(t)
	ctx := H{ // what CC's whisper client sends (docs/cc/06 §3.1)
		"X-Context-Household-Id":         "contract-household",
		"X-Context-Node-Id":              "contract-node",
		"X-Context-Household-Member-Ids": "",
	}

	t.Run("file only", func(t *testing.T) {
		body, ct := MultipartBody(FormFile("file", "audio.wav", sttWAV()))
		tg.SlowDo(t, Whisper, http.MethodPost, "/transcribe", ct, body, app.H(), ctx).
			Expect(http.StatusOK, sttResponse)
	})

	t.Run("node upload", func(t *testing.T) {
		// The node's M3 upload as CC forwards it: file + speaker_audio (wake + command) +
		// conversation_id, language and task as form fields. Whisper declares none of the form
		// fields except the two files and ignores the rest.
		body, ct := MultipartBody(
			FormFile("file", "audio.wav", sttWAV()),
			FormFile("speaker_audio", "speaker.wav", SineWAV(16000, 2.0, 330)),
			FormField("conversation_id", "contract-"+randHex(4)),
			FormField("language", "en"),
			FormField("task", "transcribe"),
		)
		tg.SlowDo(t, Whisper, http.MethodPost, "/transcribe", ct, body, app.H(), ctx).
			Expect(http.StatusOK, sttResponse)
	})

	t.Run("speaker_recognition=false", func(t *testing.T) {
		// Mobile push-to-talk and wake verification skip the speaker pass.
		body, ct := MultipartBody(FormFile("file", "audio.wav", sttWAV()))
		tg.SlowDo(t, Whisper, http.MethodPost, "/transcribe?speaker_recognition=false", ct, body, app.H()).
			Expect(http.StatusOK, Obj{
				"text":     String,
				"segments": Array,
				"speaker":  Obj{"user_id": Null, "confidence": Eq(0)},
				"affect":   Null,
			})
	})

	t.Run("missing file", func(t *testing.T) {
		body, ct := MultipartBody(FormField("language", "en"))
		tg.SlowDo(t, Whisper, http.MethodPost, "/transcribe", ct, body, app.H()).
			Expect(http.StatusUnprocessableEntity, ValidationError("body", "file"))
	})

	t.Run("app auth", func(t *testing.T) {
		body, ct := MultipartBody(FormFile("file", "audio.wav", sttWAV()))
		tg.SlowDo(t, Whisper, http.MethodPost, "/transcribe", ct, body).
			ExpectStatus(http.StatusUnauthorized).ExpectDetail("Missing app credentials")
		tg.SlowDo(t, Whisper, http.MethodPost, "/transcribe", ct, body,
			H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong-" + randHex(8)}).
			ExpectStatus(http.StatusUnauthorized).ExpectDetail("Invalid app credentials")
	})
}

// TestWhisperVoiceProfiles walks one throwaway profile for the run's contract user through
// every voice-profile route CC's whisper client calls. The profile lives under the contract
// user's own household and is purged at the end (and again on cleanup).
func TestWhisperVoiceProfiles(t *testing.T) {
	tg := T(t)
	tg.Need(t, Whisper)
	app := SharedApp(t)
	u := NewUser(t) // own user, so a failure never leaves a profile on a shared fixture
	hh := url.QueryEscape(u.HouseholdID)
	uid := u.ID
	q := fmt.Sprintf("user_id=%d&household_id=%s", uid, hh)

	purge := func() *Resp {
		return tg.Do(t, Whisper, http.MethodDelete, fmt.Sprintf("/voice-profiles/user/%d", uid), nil, app.H())
	}
	t.Cleanup(func() {
		if r := purge(); r.Status != http.StatusOK {
			t.Errorf("cleanup voice profiles for user %d: %d %s", uid, r.Status, r.Body)
		}
	})

	enroll := func(extra string) *RawResp {
		body, ct := MultipartBody(FormFile("file", "enroll.wav", SineWAV(16000, 3.0, 220)))
		return tg.SlowDo(t, Whisper, http.MethodPost, "/voice-profiles/enroll?"+q+extra, ct, body, app.H())
	}
	verify := func() *RawResp {
		body, ct := MultipartBody(FormFile("file", "verify.wav", SineWAV(16000, 2.0, 220)))
		return tg.SlowDo(t, Whisper, http.MethodPost, "/voice-profiles/verify?"+q, ct, body, app.H())
	}
	check := func(exists bool, n int) {
		t.Helper()
		tg.Get(t, Whisper, "/voice-profiles/check?"+q, app.H()).Expect(http.StatusOK, Obj{
			"exists": Eq(exists), "user_id": Eq(uid), "sample_count": Eq(n),
		})
	}
	enrolled := func(idx, total int) Matcher {
		return Obj{
			"status": Eq("enrolled"), "user_id": Eq(uid), "household_id": Eq(u.HouseholdID),
			"sample_index": Eq(idx), "total_samples": Eq(total),
		}
	}

	check(false, 0)
	tg.Get(t, Whisper, "/voice-profiles?household_id="+hh, app.H()).
		Expect(http.StatusOK, Obj{"household_id": Eq(u.HouseholdID), "profiles": Eq([]any{})})
	verify().ExpectError(http.StatusNotFound, fmt.Sprintf("No voice profile enrolled for user %d", uid))

	// Enroll auto-allocates the next free index (M4: the node never sends sample_index).
	enroll("").Expect(http.StatusOK, enrolled(0, 1))
	enroll("").Expect(http.StatusOK, enrolled(1, 2))
	// An explicit index overwrites that slot (V2); 0–999 only.
	enroll("&sample_index=0").Expect(http.StatusOK, enrolled(0, 2))
	enroll("&sample_index=1000").ExpectError(http.StatusBadRequest, "sample_index must be in [0, 999]")
	check(true, 2)

	sample := Obj{"index": Int, "filename": Regexp(`^sample_\d{3}\.wav$`), "size_bytes": Int}
	tg.Get(t, Whisper, fmt.Sprintf("/voice-profiles/%d/samples?household_id=%s", uid, hh), app.H()).
		Expect(http.StatusOK, Obj{"household_id": Eq(u.HouseholdID), "user_id": Eq(uid), "samples": NonEmptyArrayOf(sample)})
	// Profiles are listed by a one-way hash of the user id, never the id itself.
	tg.Get(t, Whisper, "/voice-profiles?household_id="+hh, app.H()).
		Expect(http.StatusOK, Obj{
			"household_id": Eq(u.HouseholdID),
			"profiles":     NonEmptyArrayOf(Obj{"filename": Regexp(`^[0-9a-f]{16}$`), "samples": Int}),
		})

	// M5/V5: confidence is rounded to 4 dp; the match itself is not frozen (it is a sine).
	verify().Expect(http.StatusOK, Obj{"matched": Bool, "confidence": Num, "user_id": Eq(uid)})

	// V4: delete one sample.
	tg.Do(t, Whisper, http.MethodDelete, fmt.Sprintf("/voice-profiles/%d/samples/1?household_id=%s", uid, hh), nil, app.H()).
		Expect(http.StatusOK, Obj{"status": Eq("deleted"), "user_id": Eq(uid), "sample_index": Eq(1), "remaining_samples": Eq(1)})
	tg.Do(t, Whisper, http.MethodDelete, fmt.Sprintf("/voice-profiles/%d/samples/7?household_id=%s", uid, hh), nil, app.H()).
		ExpectError(http.StatusNotFound, fmt.Sprintf("Sample 7 not found for user %d", uid))

	// V6: delete the whole profile in this household; a second delete is 404.
	del := fmt.Sprintf("/voice-profiles/%d?household_id=%s", uid, hh)
	tg.Do(t, Whisper, http.MethodDelete, del, nil, app.H()).
		Expect(http.StatusOK, Obj{"status": Eq("deleted"), "user_id": Eq(uid), "household_id": Eq(u.HouseholdID)})
	tg.Do(t, Whisper, http.MethodDelete, del, nil, app.H()).ExpectError(http.StatusNotFound, "Voice profile not found")
	check(false, 0)

	// Account purge (CC api/me.py): every household, idempotent.
	enroll("").Expect(http.StatusOK, enrolled(0, 1))
	purge().Expect(http.StatusOK, Obj{"status": Eq("deleted"), "user_id": Eq(uid), "households": Eq([]any{u.HouseholdID})})
	purge().Expect(http.StatusOK, Obj{"status": Eq("deleted"), "user_id": Eq(uid), "households": Eq([]any{})})
	check(false, 0)

	// Missing query parameters are FastAPI 422s.
	body, ct := MultipartBody(FormFile("file", "enroll.wav", sttWAV()))
	tg.SlowDo(t, Whisper, http.MethodPost, fmt.Sprintf("/voice-profiles/enroll?user_id=%d", uid), ct, body, app.H()).
		Expect(http.StatusUnprocessableEntity, ValidationError("query", "household_id"))
	tg.Get(t, Whisper, "/voice-profiles/check?household_id="+hh, app.H()).
		Expect(http.StatusUnprocessableEntity, ValidationError("query", "user_id"))

	// App auth on the profile routes.
	tg.Get(t, Whisper, "/voice-profiles/check?"+q).ExpectError(http.StatusUnauthorized, "Missing app credentials")
	tg.Do(t, Whisper, http.MethodDelete, del, nil, H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong"}).
		ExpectError(http.StatusUnauthorized, "Invalid app credentials")
}
