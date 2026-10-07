package stt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

var appH = map[string]string{"X-Jarvis-App-Id": "jarvis-cc", "X-Jarvis-App-Key": "k"}

type env struct {
	m   *Module
	h   http.Handler
	d   *db.DB
	w   *fakeWhisper
	ctx context.Context
}

func setup(t *testing.T, opts ...func(*Module)) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "stt", Migrations()); err != nil {
		t.Fatal(err)
	}
	fw := newFakeWhisper(t)
	m := &Module{Auth: fakeAuth{}, Engine: fw.resolver(), Speaker: toneEmbedder{}}
	for _, o := range opts {
		o(m)
	}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return &env{m: m, h: mux, d: d, w: fw, ctx: ctx}
}

func (e *env) do(t *testing.T, method, path string, body io.Reader, ct string, hdr ...map[string]string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	for _, h := range hdr {
		for k, v := range h {
			r.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Body.Len() == 0 {
		return rec.Code, nil
	}
	return rec.Code, decodeJSON(t, rec.Body.Bytes())
}

func (e *env) post(t *testing.T, path string, hdr map[string]string, parts ...part) (int, map[string]any) {
	t.Helper()
	b, ct := multipartBody(parts...)
	return e.do(t, http.MethodPost, path, b, ct, appH, hdr)
}

func (e *env) set(t *testing.T, key string, v any, household string) {
	t.Helper()
	if err := e.m.settings.Set(e.ctx, key, v, settings.Scope{HouseholdID: household}); err != nil {
		t.Fatal(err)
	}
}

// enroll posts one take and fails unless it is stored.
func (e *env) enroll(t *testing.T, hh string, uid int64, wav []byte) map[string]any {
	t.Helper()
	c, body := e.post(t, fmt.Sprintf("/voice-profiles/enroll?user_id=%d&household_id=%s", uid, hh), nil, file("file", wav))
	if c != 200 {
		t.Fatalf("enroll %d: %d %v", uid, c, body)
	}
	return body
}

func ctxH(hh, members string) map[string]string {
	return map[string]string{"X-Context-Household-Id": hh, "X-Context-Household-Member-Ids": members}
}

func speakerOf(t *testing.T, body map[string]any) (any, float64) {
	t.Helper()
	sp, ok := body["speaker"].(map[string]any)
	if !ok {
		t.Fatalf("no speaker in %v", body)
	}
	return sp["user_id"], sp["confidence"].(float64)
}

// --- transcription ---

func TestTranscribeForwardsLegacyParamsAt16k(t *testing.T) {
	e := setup(t)
	// 44.1 kHz in: whisper must get 16 kHz.
	c, body := e.post(t, "/transcribe", nil, file("file", sineWAV(44100, 1.0, 440)),
		field("conversation_id", "c1"), field("language", "fr"), field("task", "translate"))
	if c != 200 {
		t.Fatalf("%d %v", c, body)
	}
	form, rate, n := e.w.last()
	if rate != 16000 || n < 15990 || n > 16010 {
		t.Fatalf("whisper got %d Hz, %d samples", rate, n)
	}
	want := map[string]string{"response_format": "verbose_json", "language": "en", "temperature": "0", "temperature_inc": "0.2", "beam_size": "2"}
	for k, v := range want {
		if form[k] != v {
			t.Errorf("form %s = %q, want %q (M13: request language ignored)", k, form[k], v)
		}
	}
	if _, ok := form["prompt"]; ok {
		t.Error("empty prompt forwarded")
	}
	if body["text"] != "turn off  the lights" && body["text"] != "turn off the lights" {
		t.Errorf("text %q", body["text"])
	}
	segs := body["segments"].([]any)
	if len(segs) != 2 {
		t.Fatalf("segments %v", segs)
	}
	s1 := segs[1].(map[string]any)
	if s1["t0_ms"] != 840.0 || s1["t1_ms"] != 1840.0 || s1["text"] != " the lights" {
		t.Errorf("segment %v", s1)
	}
	if v, ok := body["affect"]; !ok || v != nil {
		t.Errorf("affect must be present and null (D38): %v", body)
	}
	if uid, conf := speakerOf(t, body); uid != nil || conf != 0 {
		t.Errorf("speaker with recognition off: %v %v", uid, conf)
	}
}

func TestTranscribeQueryOverridesAndSettings(t *testing.T) {
	e := setup(t)
	e.set(t, "whisper.default_beam_size", int64(4), "")
	if c, _ := e.post(t, "/transcribe", nil, file("file", sineWAV(16000, 1, 440))); c != 200 {
		t.Fatal(c)
	}
	if form, _, _ := e.w.last(); form["beam_size"] != "4" {
		t.Errorf("setting beam size not used: %v", form)
	}
	c, _ := e.post(t, "/transcribe?beam_size=7&temperature=0.4&temperature_inc=0.1&prompt=Jarvis&preprocess=true", nil,
		file("file", sineWAV(16000, 1, 440)))
	if c != 200 {
		t.Fatal(c)
	}
	form, _, _ := e.w.last()
	if form["beam_size"] != "7" || form["temperature"] != "0.4" || form["temperature_inc"] != "0.1" || form["prompt"] != "Jarvis" {
		t.Errorf("overrides: %v", form)
	}
}

func TestTranscribeValidationAndAuth(t *testing.T) {
	e := setup(t)
	b, ct := multipartBody(field("language", "en"))
	c, body := e.do(t, "POST", "/transcribe", b, ct, appH)
	if c != 422 || !hasLoc(body, "body", "file") {
		t.Errorf("missing file: %d %v", c, body)
	}
	// No body at all is the same 422.
	if c, body := e.do(t, "POST", "/transcribe", nil, "", appH); c != 422 || !hasLoc(body, "body", "file") {
		t.Errorf("no body: %d %v", c, body)
	}
	for q, loc := range map[string]string{"beam_size=0": "beam_size", "beam_size=17": "beam_size", "temperature=2": "temperature",
		"temperature_inc=x": "temperature_inc", "speaker_recognition=maybe": "speaker_recognition", "preprocess=2": "preprocess"} {
		if c, body := e.post(t, "/transcribe?"+q, nil, file("file", sineWAV(16000, 1, 440))); c != 422 || !hasLoc(body, "query", loc) {
			t.Errorf("%s: %d %v", q, c, body)
		}
	}
	b, ct = multipartBody(file("file", sineWAV(16000, 1, 440)))
	if c, body := e.do(t, "POST", "/transcribe", b, ct); c != 401 || body["detail"] != "Missing app credentials" {
		t.Errorf("no creds: %d %v", c, body)
	}
	b, ct = multipartBody(file("file", sineWAV(16000, 1, 440)))
	if c, body := e.do(t, "POST", "/transcribe", b, ct, map[string]string{"X-Jarvis-App-Id": "jarvis-cc", "X-Jarvis-App-Key": "bad"}); c != 401 || body["detail"] != "Invalid app credentials" {
		t.Errorf("bad creds: %d %v", c, body)
	}
	// Not a WAV.
	if c, body := e.post(t, "/transcribe", nil, file("file", []byte("not audio"))); c != 400 {
		t.Errorf("garbage audio: %d %v", c, body)
	}
}

func TestTranscribeUploadCap(t *testing.T) {
	e := setup(t, func(m *Module) { m.MaxUploadBytes = 64 << 10 })
	if c, body := e.post(t, "/transcribe", nil, file("file", sineWAV(16000, 3, 440))); c != 413 || body["detail"] != "Audio upload too large" {
		t.Errorf("%d %v", c, body)
	}
}

func TestTranscribeEngineErrors(t *testing.T) {
	e := setup(t, func(m *Module) {
		m.Engine = ResolveFunc(func(context.Context, string) (string, error) { return "", errors.New("not configured") })
	})
	if c, body := e.post(t, "/transcribe", nil, file("file", sineWAV(16000, 1, 440))); c != 503 {
		t.Errorf("unresolvable engine: %d %v", c, body)
	}
	e = setup(t)
	e.w.status, e.w.response = 500, `{"error":"failed to process audio"}`
	c, body := e.post(t, "/transcribe", nil, file("file", sineWAV(16000, 1, 440)))
	if c != 500 || !strings.Contains(body["error"].(string), "failed to process audio") || body["stderr"] != "failed to process audio" {
		t.Errorf("engine 500: %d %v", c, body)
	}
}

// --- speaker identification ---

func TestSpeakerRecognitionScopedToMembers(t *testing.T) {
	e := setup(t)
	e.enroll(t, "hh", 1, sineWAV(16000, 4, 220))
	e.enroll(t, "hh", 2, sineWAV(16000, 4, 440))

	// D35: off by default, enrolling does not turn it on.
	_, body := e.post(t, "/transcribe", ctxH("hh", "1,2"), file("file", sineWAV(16000, 2, 220)))
	if uid, conf := speakerOf(t, body); uid != nil || conf != 0 {
		t.Fatalf("recognition off: %v %v", uid, conf)
	}
	e.set(t, "voice.recognition_enabled", true, "hh")

	_, body = e.post(t, "/transcribe", ctxH("hh", "1,2"), file("file", sineWAV(16000, 2, 220)))
	if uid, conf := speakerOf(t, body); uid != 1.0 || conf < 0.9 {
		t.Errorf("member 1: %v %v", uid, conf)
	}
	// 48 kHz speaker audio is resampled before embedding.
	_, body = e.post(t, "/transcribe", ctxH("hh", "1,2"), file("file", sineWAV(48000, 2, 440)))
	if uid, _ := speakerOf(t, body); uid != 2.0 {
		t.Errorf("member 2 at 48 kHz: %v", uid)
	}
	// speaker_audio wins over file for the speaker pass (invariant 4).
	_, body = e.post(t, "/transcribe", ctxH("hh", "1,2"), file("file", sineWAV(16000, 1, 440)), file("speaker_audio", sineWAV(16000, 2, 220)))
	if uid, _ := speakerOf(t, body); uid != 1.0 {
		t.Errorf("speaker_audio: %v", uid)
	}
	// Only listed members are scored (invariant 1); none listed = none scored.
	_, body = e.post(t, "/transcribe", ctxH("hh", "2"), file("file", sineWAV(16000, 2, 220)))
	if uid, conf := speakerOf(t, body); uid != nil || conf <= 0 || conf > 0.5 {
		t.Errorf("non-member voice: %v %v (abstain keeps the score)", uid, conf)
	}
	_, body = e.post(t, "/transcribe", ctxH("hh", ""), file("file", sineWAV(16000, 2, 220)))
	if uid, conf := speakerOf(t, body); uid != nil || conf != 0 {
		t.Errorf("no members: %v %v", uid, conf)
	}
	// Another household's voiceprints never match (D36).
	e.set(t, "voice.recognition_enabled", true, "other")
	_, body = e.post(t, "/transcribe", ctxH("other", "1,2"), file("file", sineWAV(16000, 2, 220)))
	if uid, _ := speakerOf(t, body); uid != nil {
		t.Errorf("cross-household: %v", uid)
	}
	// speaker_recognition=false skips the pass.
	_, body = e.post(t, "/transcribe?speaker_recognition=false", ctxH("hh", "1,2"), file("file", sineWAV(16000, 2, 220)))
	if uid, conf := speakerOf(t, body); uid != nil || conf != 0 {
		t.Errorf("skipped: %v %v", uid, conf)
	}
}

func TestSpeakerMarginGateAndThreshold(t *testing.T) {
	e := setup(t)
	e.set(t, "voice.recognition_enabled", true, "hh")
	// Two members with nearly the same voice: above threshold but ambiguous (D33, D21).
	e.enroll(t, "hh", 1, wavOf(mix(sine(16000, 4, 220), sine(16000, 4, 330), 0.52), 16000))
	e.enroll(t, "hh", 2, wavOf(mix(sine(16000, 4, 220), sine(16000, 4, 330), 0.48), 16000))
	clip := wavOf(mix(sine(16000, 2, 220), sine(16000, 2, 330), 0.5), 16000)

	sp, err := e.m.Identify(e.ctx, SpeakerScope{HouseholdID: "hh", AllMembers: true}, mustDecode(t, clip))
	if err != nil || sp.UserID != nil || sp.Outcome != OutcomeAmbiguous || sp.Confidence < 0.43 {
		t.Fatalf("ambiguous: %+v %v", sp, err)
	}
	e.set(t, "voice.min_speaker_margin", 0.0, "hh")
	sp, _ = e.m.Identify(e.ctx, SpeakerScope{HouseholdID: "hh", AllMembers: true}, mustDecode(t, clip))
	if sp.UserID == nil || sp.Outcome != OutcomeMatched {
		t.Errorf("margin gate disabled: %+v", sp)
	}
	// One threshold for every clip length (D33).
	e.set(t, "voice.similarity_threshold", 0.9999, "hh")
	sp, _ = e.m.Identify(e.ctx, SpeakerScope{HouseholdID: "hh", MemberIDs: []int64{1}}, mustDecode(t, clip))
	if sp.UserID != nil || sp.Outcome != OutcomeNoMatch || sp.Confidence == 0 {
		t.Errorf("below threshold: %+v", sp)
	}
	// Recognition off is an error in process (M14 messaging).
	if _, err := e.m.Identify(e.ctx, SpeakerScope{HouseholdID: "nope", AllMembers: true}, mustDecode(t, clip)); !errors.Is(err, ErrRecognitionOff) {
		t.Errorf("off: %v", err)
	}
}

func mix(a, b []float32, wa float64) []float32 {
	out := make([]float32, len(a))
	for i := range a {
		out[i] = float32(wa*float64(a[i]) + (1-wa)*float64(b[i]))
	}
	return out
}

func mustDecode(t *testing.T, wav []byte) []float32 {
	t.Helper()
	s, err := DecodeWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInProcessTranscribe(t *testing.T) {
	e := setup(t)
	e.enroll(t, "hh", 7, sineWAV(16000, 4, 330))
	e.set(t, "voice.recognition_enabled", true, "hh")
	res, err := e.m.Transcribe(e.ctx, sineWAV(22050, 2, 330), TranscribeOptions{
		Speaker: &SpeakerScope{HouseholdID: "hh", AllMembers: true}, BeamSize: 3,
	})
	if err != nil || res.Speaker.UserID == nil || *res.Speaker.UserID != 7 || res.Speaker.Outcome != OutcomeMatched || res.Affect != nil {
		t.Fatalf("%+v %v", res, err)
	}
	if form, rate, _ := e.w.last(); form["beam_size"] != "3" || rate != 16000 {
		t.Errorf("%v %d", form, rate)
	}
	res, _ = e.m.Transcribe(e.ctx, sineWAV(16000, 1, 330), TranscribeOptions{})
	if res.Speaker.Outcome != OutcomeSkipped || res.Speaker.UserID != nil {
		t.Errorf("no scope: %+v", res.Speaker)
	}
}

// --- enrollment and profiles ---

func TestEnrollAllocationAndOverwrite(t *testing.T) {
	e := setup(t)
	q := "/voice-profiles/enroll?user_id=5&household_id=hh"
	for i, want := range []float64{0, 1} {
		body := e.enroll(t, "hh", 5, sineWAV(16000, 3, 220))
		if body["status"] != "enrolled" || body["sample_index"] != want || body["total_samples"] != float64(i+1) ||
			body["household_id"] != "hh" || body["user_id"] != 5.0 {
			t.Fatalf("take %d: %v", i, body)
		}
	}
	c, body := e.post(t, q+"&sample_index=0", nil, file("file", sineWAV(16000, 3, 220)))
	if c != 200 || body["sample_index"] != 0.0 || body["total_samples"] != 2.0 {
		t.Errorf("overwrite: %d %v", c, body)
	}
	c, body = e.post(t, q+"&sample_index=1000", nil, file("file", sineWAV(16000, 3, 220)))
	if c != 400 || body["detail"] != "sample_index must be in [0, 999]" {
		t.Errorf("1000: %d %v", c, body)
	}
	c, body = e.post(t, q+"&sample_index=-1", nil, file("file", sineWAV(16000, 3, 220)))
	if c != 400 {
		t.Errorf("-1: %d %v", c, body)
	}
	c, body = e.post(t, "/voice-profiles/enroll?user_id=5", nil, file("file", sineWAV(16000, 3, 220)))
	if c != 422 || !hasLoc(body, "query", "household_id") {
		t.Errorf("missing household: %d %v", c, body)
	}
	c, body = e.post(t, "/voice-profiles/enroll?user_id=x&household_id=hh", nil)
	if c != 422 || !hasLoc(body, "query", "user_id") || !hasLoc(body, "body", "file") {
		t.Errorf("bad user + no file: %d %v", c, body)
	}
}

func TestEnrollQualityGate(t *testing.T) {
	e := setup(t)
	q := "/voice-profiles/enroll?user_id=5&household_id=hh"
	lowQuality := func(name string, wav []byte, reason string) {
		t.Helper()
		c, body := e.post(t, q, nil, file("file", wav))
		if c != 422 || body["success"] != false || body["error"] != "low_quality" || body["reason"] != reason {
			t.Errorf("%s: %d %v", name, c, body)
		}
	}
	lowQuality("silence", sineWAV(16000, 8, 0), "too_little_speech")
	// 2 s of voice in an 8 s take (the node records 8 s).
	take := append(sine(16000, 2, 220), make([]float32, 16000*6)...)
	lowQuality("2 s of speech", wavOf(take, 16000), "too_little_speech")
	if c, body := e.do(t, "GET", "/voice-profiles/check?user_id=5&household_id=hh", nil, "", appH); c != 200 || body["exists"] != false {
		t.Fatalf("rejected takes must not be stored: %v", body)
	}
	// 4 s of speech in 8 s with room noise passes.
	take = append(sine(16000, 4, 220), noise(16000*4, 0.003)...)
	e.enroll(t, "hh", 5, wavOf(take, 16000))
	// Someone else entirely, against the user's other takes.
	lowQuality("inconsistent", sineWAV(16000, 4, 880), "inconsistent")
	// …unless it overwrites the only other take.
	if c, body := e.post(t, q+"&sample_index=0", nil, file("file", sineWAV(16000, 4, 880))); c != 200 {
		t.Errorf("overwrite of the only take: %d %v", c, body)
	}
	e.set(t, "voice.enroll_min_speech_seconds", 1.0, "hh")
	if c, body := e.post(t, q, nil, file("file", sineWAV(16000, 1.5, 880))); c != 200 {
		t.Errorf("lowered gate: %d %v", c, body)
	}
}

func noise(n int, amp float64) []float32 {
	s := make([]float32, n)
	x := uint32(1)
	for i := range s {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		s[i] = float32(amp * (float64(x)/math.MaxUint32*2 - 1))
	}
	return s
}

func TestVoiceProfileLifecycle(t *testing.T) {
	e := setup(t)
	get := func(path string) (int, map[string]any) { return e.do(t, "GET", path, nil, "", appH) }
	del := func(path string) (int, map[string]any) { return e.do(t, "DELETE", path, nil, "", appH) }

	if c, body := get("/voice-profiles?household_id=hh"); c != 200 || len(body["profiles"].([]any)) != 0 {
		t.Fatalf("empty list: %v", body)
	}
	c, body := e.post(t, "/voice-profiles/verify?user_id=5&household_id=hh", nil, file("file", sineWAV(16000, 2, 220)))
	if c != 404 || body["detail"] != "No voice profile enrolled for user 5" {
		t.Errorf("verify without profile: %d %v", c, body)
	}
	e.enroll(t, "hh", 5, sineWAV(16000, 3, 220))
	e.enroll(t, "hh", 5, sineWAV(16000, 3, 220))
	e.enroll(t, "hh2", 5, sineWAV(16000, 3, 220))
	e.enroll(t, "hh", 6, sineWAV(16000, 3, 440))

	_, body = get("/voice-profiles/check?user_id=5&household_id=hh")
	if body["exists"] != true || body["sample_count"] != 2.0 || body["user_id"] != 5.0 {
		t.Errorf("check: %v", body)
	}
	_, body = get("/voice-profiles/5/samples?household_id=hh")
	samples := body["samples"].([]any)
	if len(samples) != 2 || samples[1].(map[string]any)["filename"] != "sample_001.wav" || samples[1].(map[string]any)["size_bytes"].(float64) <= 0 {
		t.Errorf("samples: %v", body)
	}
	_, body = get("/voice-profiles?household_id=hh")
	profiles := body["profiles"].([]any)
	if len(profiles) != 2 {
		t.Fatalf("list: %v", body)
	}
	found := false
	for _, p := range profiles {
		p := p.(map[string]any)
		if p["filename"] == HashUserID(5) && p["samples"] == 2.0 {
			found = true
		}
	}
	if !found || HashUserID(5) != "ef2d127de37b942b" { // sha256("5")[:16]
		t.Errorf("hashed listing: %v (%s)", profiles, HashUserID(5))
	}

	// Verify uses voice.similarity_threshold, not recognition_enabled (it's the wizard's self-test).
	_, body = e.post(t, "/voice-profiles/verify?user_id=5&household_id=hh", nil, file("file", sineWAV(16000, 2, 220)))
	if body["matched"] != true || body["user_id"] != 5.0 || body["confidence"].(float64) < 0.9 {
		t.Errorf("verify self: %v", body)
	}
	_, body = e.post(t, "/voice-profiles/verify?user_id=5&household_id=hh", nil, file("file", sineWAV(16000, 2, 880)))
	if body["matched"] != false {
		t.Errorf("verify other voice: %v", body)
	}
	if conf := body["confidence"].(float64); conf != math.Round(conf*1e4)/1e4 {
		t.Errorf("confidence not 4 dp: %v", conf)
	}

	if c, body := del("/voice-profiles/5/samples/1?household_id=hh"); c != 200 || body["remaining_samples"] != 1.0 || body["sample_index"] != 1.0 {
		t.Errorf("delete sample: %d %v", c, body)
	}
	if c, body := del("/voice-profiles/5/samples/7?household_id=hh"); c != 404 || body["detail"] != "Sample 7 not found for user 5" {
		t.Errorf("missing sample: %d %v", c, body)
	}
	if c, body := del("/voice-profiles/5?household_id=hh"); c != 200 || body["household_id"] != "hh" || body["status"] != "deleted" {
		t.Errorf("delete profile: %d %v", c, body)
	}
	if c, body := del("/voice-profiles/5?household_id=hh"); c != 404 || body["detail"] != "Voice profile not found" {
		t.Errorf("second delete: %d %v", c, body)
	}
	e.enroll(t, "hh", 5, sineWAV(16000, 3, 220))
	_, body = del("/voice-profiles/user/5")
	if hs := body["households"].([]any); len(hs) != 2 || hs[0] != "hh" || hs[1] != "hh2" {
		t.Errorf("purge: %v", body)
	}
	_, body = del("/voice-profiles/user/5")
	if hs := body["households"].([]any); len(hs) != 0 {
		t.Errorf("idempotent purge: %v", body)
	}
	if c, body := get("/voice-profiles/check?household_id=hh"); c != 422 || !hasLoc(body, "query", "user_id") {
		t.Errorf("check validation: %d %v", c, body)
	}
	if c, body := get("/voice-profiles/x/samples?household_id=hh"); c != 422 || !hasLoc(body, "path", "user_id") {
		t.Errorf("path validation: %d %v", c, body)
	}
	if c, _ := e.do(t, "GET", "/voice-profiles/check?user_id=5&household_id=hh", nil, ""); c != 401 {
		t.Errorf("unauthenticated: %d", c)
	}
}

func TestPurgeUserInTransaction(t *testing.T) {
	e := setup(t)
	e.enroll(t, "hh", 5, sineWAV(16000, 3, 220))
	e.enroll(t, "hh2", 5, sineWAV(16000, 3, 220))
	e.enroll(t, "hh", 6, sineWAV(16000, 3, 440))
	if err := e.d.Tx(e.ctx, func(tx *sql.Tx) error { return e.m.PurgeUser(e.ctx, tx, 5) }); err != nil {
		t.Fatal(err)
	}
	var n5, n6 int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM stt_voiceprints WHERE user_id = 5`).Scan(&n5)
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM stt_voiceprints WHERE user_id = 6`).Scan(&n6)
	if n5 != 0 || n6 != 1 {
		t.Errorf("after PurgeUser: user5=%d user6=%d", n5, n6)
	}
	if err := e.d.Tx(e.ctx, func(tx *sql.Tx) error { return e.m.PurgeHousehold(e.ctx, tx, "hh") }); err != nil {
		t.Fatal(err)
	}
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM stt_voiceprints`).Scan(&n6)
	if n6 != 0 {
		t.Errorf("after PurgeHousehold: %d", n6)
	}
}

// D34: voiceprints carry their model id; another model's are invisible and the first take
// under the new model clears them. No audio column exists.
func TestVoiceprintsAreModelTagged(t *testing.T) {
	e := setup(t)
	e.enroll(t, "hh", 5, sineWAV(16000, 3, 220))
	e.enroll(t, "hh", 5, sineWAV(16000, 3, 220))
	e.m.Speaker = toneEmbedder{id: "tone-v2"}
	_, body := e.do(t, "GET", "/voice-profiles/check?user_id=5&household_id=hh", nil, "", appH)
	if body["exists"] != false {
		t.Fatalf("old model's voiceprints still count: %v", body)
	}
	if c, _ := e.post(t, "/voice-profiles/verify?user_id=5&household_id=hh", nil, file("file", sineWAV(16000, 2, 220))); c != 404 {
		t.Errorf("verify against a stale model: %d", c)
	}
	body = e.enroll(t, "hh", 5, sineWAV(16000, 3, 220))
	if body["sample_index"] != 0.0 || body["total_samples"] != 1.0 {
		t.Errorf("first take under the new model: %v", body)
	}
	var stale int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM stt_voiceprints WHERE model_id = 'tone-v1'`).Scan(&stale)
	if stale != 0 {
		t.Errorf("stale voiceprints kept: %d", stale)
	}
	rows, _ := e.d.Read.Query(`SELECT name FROM pragma_table_info('stt_voiceprints')`)
	defer rows.Close()
	for rows.Next() {
		var col string
		rows.Scan(&col)
		if strings.Contains(col, "wav") || strings.Contains(col, "audio") {
			t.Errorf("audio column %s (D34)", col)
		}
	}
}

func TestSpeakerModelMissing(t *testing.T) {
	e := setup(t, func(m *Module) {
		m.Speaker = nil
		m.Models = ModelPathFunc(func(context.Context, string) (string, bool) { return "", false })
	})
	c, body := e.post(t, "/voice-profiles/enroll?user_id=1&household_id=hh", nil, file("file", sineWAV(16000, 4, 220)))
	if c != 503 {
		t.Errorf("enroll without a model: %d %v", c, body)
	}
	e.set(t, "voice.recognition_enabled", true, "hh")
	c, body = e.post(t, "/transcribe", ctxH("hh", "1"), file("file", sineWAV(16000, 1, 220)))
	if uid, conf := speakerOf(t, body); c != 200 || uid != nil || conf != 0 {
		t.Errorf("transcribe must survive a missing speaker model: %d %v", c, body)
	}
}

func TestHealthAndPing(t *testing.T) {
	e := setup(t)
	c, body := e.do(t, "GET", "/health", nil, "")
	sp, _ := body["speaker"].(map[string]any)
	if c != 200 || body["status"] != "healthy" || body["version"] != "dev" || sp["recognition_enabled"] != false || sp["encoder"] != "tone-v1" {
		t.Errorf("health: %v", body)
	}
	if c, body := e.do(t, "GET", "/ping", nil, ""); c != 200 || body["message"] != "pong" {
		t.Errorf("ping: %v", body)
	}
}

func hasLoc(body map[string]any, loc ...string) bool {
	d, _ := body["detail"].([]any)
	for _, x := range d {
		l, _ := x.(map[string]any)["loc"].([]any)
		if len(l) != len(loc) {
			continue
		}
		ok := true
		for i := range l {
			if fmt.Sprint(l[i]) != loc[i] {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}
