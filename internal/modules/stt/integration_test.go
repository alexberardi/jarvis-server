package stt

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/voice/sherpa"
)

// Integration tests with the real ERes2Net extractor (sherpa-onnx via purego). They skip
// unless JARVIS_SHERPA_MODELS points at a directory with
// 3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx and speakers/<speaker>/*.wav (the layout
// of scripts/fetch-test-models.sh plus the voice spike's LibriSpeech clips).

const eres2net = "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx"

func realSpeaker(t *testing.T) (*env, string) {
	t.Helper()
	dir := os.Getenv("JARVIS_SHERPA_MODELS")
	if dir == "" {
		t.Skip("JARVIS_SHERPA_MODELS not set")
	}
	model := filepath.Join(dir, eres2net)
	if _, err := os.Stat(model); err != nil {
		t.Skip(err)
	}
	e := setup(t, func(m *Module) {
		m.Speaker = nil
		m.Models = ModelPathFunc(func(_ context.Context, kind string) (string, bool) { return model, kind == "speaker" })
		// Not t.TempDir(): Windows keeps loaded DLLs locked (see internal/voice/sherpa).
		m.LibDir = filepath.Join(os.TempDir(), "jarvis-sherpa-test")
	})
	return e, dir
}

func speakerClips(t *testing.T, dir, spk string) [][]byte {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "speakers", spk, "*.wav"))
	sort.Strings(files)
	if len(files) < 12 {
		t.Skipf("speaker %s: %d clips, want 12+", spk, len(files))
	}
	out := make([][]byte, len(files))
	for i, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = b
	}
	return out
}

// take concatenates clips into one enrollment take (LibriSpeech clips are 3 s each; a node
// take is 8 s).
func take(t *testing.T, clips ...[]byte) []byte {
	t.Helper()
	var s []float32
	for _, c := range clips {
		s = append(s, mustDecode(t, c)...)
	}
	return wavOf(s, SampleRate)
}

func TestRealSpeakerEnrollIdentifyVerify(t *testing.T) {
	e, dir := realSpeaker(t)
	spks, _ := os.ReadDir(filepath.Join(dir, "speakers"))
	if len(spks) < 3 {
		t.Skip("need 3 speakers")
	}
	a, b, c := spks[0].Name(), spks[1].Name(), spks[2].Name()
	ca, cb, cc := speakerClips(t, dir, a), speakerClips(t, dir, b), speakerClips(t, dir, c)
	e.set(t, "voice.recognition_enabled", true, "hh")

	for i := 0; i < 3; i++ {
		e.enroll(t, "hh", 1, take(t, ca[3*i], ca[3*i+1], ca[3*i+2]))
		e.enroll(t, "hh", 2, take(t, cb[3*i], cb[3*i+1], cb[3*i+2]))
	}
	var row struct {
		model string
		dim   int
	}
	e.d.Read.QueryRow(`SELECT model_id, dim FROM stt_voiceprints LIMIT 1`).Scan(&row.model, &row.dim)
	if !strings.HasPrefix(row.model, "3dspeaker_speech_eres2net_sv_en_voxceleb_16k@") || row.dim < 128 {
		t.Errorf("voiceprint tag %q dim %d", row.model, row.dim)
	}

	scope := SpeakerScope{HouseholdID: "hh", AllMembers: true}
	hitsA, falseA := 0, 0
	for _, clip := range ca[9:] {
		sp, err := e.m.Identify(e.ctx, scope, mustDecode(t, clip))
		if err != nil {
			t.Fatal(err)
		}
		if sp.UserID != nil && *sp.UserID == 1 {
			hitsA++
		}
		t.Logf("speaker %s clip: %+v", a, sp)
	}
	for _, clip := range cc[9:] { // an unenrolled voice
		sp, _ := e.m.Identify(e.ctx, scope, mustDecode(t, clip))
		if sp.UserID != nil {
			falseA++
		}
		t.Logf("impostor %s clip: outcome=%s conf=%.3f", c, sp.Outcome, sp.Confidence)
	}
	if hitsA < len(ca[9:])-1 {
		t.Errorf("enrolled speaker identified on %d/%d clips", hitsA, len(ca[9:]))
	}
	if falseA > 0 {
		t.Errorf("unenrolled speaker attributed on %d clips", falseA)
	}

	// Same through HTTP, with the member scope from the context headers.
	_, body := e.post(t, "/transcribe", ctxH("hh", "1,2"), file("file", ca[10]))
	if uid, conf := speakerOf(t, body); uid != 1.0 || conf <= 0.43 {
		t.Errorf("HTTP transcribe speaker: %v %v", uid, conf)
	}
	_, body = e.post(t, "/voice-profiles/verify?user_id=1&household_id=hh", nil, file("file", ca[11]))
	if body["matched"] != true {
		t.Errorf("verify self: %v", body)
	}
	_, body = e.post(t, "/voice-profiles/verify?user_id=1&household_id=hh", nil, file("file", cb[11]))
	if body["matched"] != false {
		t.Errorf("verify other: %v", body)
	}
	// D37 on real audio: a take of another person is inconsistent with the user's samples.
	c2, body := e.post(t, "/voice-profiles/enroll?user_id=1&household_id=hh", nil, file("file", take(t, cc[0], cc[1], cc[2])))
	if c2 != 422 || body["reason"] != "inconsistent" {
		t.Errorf("someone else's take: %d %v", c2, body)
	}
}

func TestRealSpeakerModelIDStable(t *testing.T) {
	_, dir := realSpeaker(t)
	id1, err := modelID(filepath.Join(dir, eres2net))
	if err != nil {
		t.Fatal(err)
	}
	if err := sherpa.Load(filepath.Join(os.TempDir(), "jarvis-sherpa-test")); err != nil {
		t.Fatal(err)
	}
	id2, _ := modelID(filepath.Join(dir, eres2net))
	if id1 != id2 || len(id1) < 20 {
		t.Errorf("model id %q / %q", id1, id2)
	}
}

// TestRealWhisperServer transcribes real speech through a running whisper-server (any model).
// JARVIS_TEST_WHISPER_URL=http://127.0.0.1:<port>, plus JARVIS_SHERPA_MODELS for the clip.
func TestRealWhisperServer(t *testing.T) {
	url := os.Getenv("JARVIS_TEST_WHISPER_URL")
	dir := os.Getenv("JARVIS_SHERPA_MODELS")
	if url == "" || dir == "" {
		t.Skip("JARVIS_TEST_WHISPER_URL and JARVIS_SHERPA_MODELS required")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "speakers", "*", "*.wav"))
	if len(files) == 0 {
		t.Skip("no speech clips")
	}
	e := setup(t, func(m *Module) {
		m.Engine = ResolveFunc(func(context.Context, string) (string, error) { return url, nil })
	})
	clip, _ := os.ReadFile(files[0])
	// Upsample to 44.1 kHz first, to prove the resample path end to end.
	up := wavOf(Resample(mustDecode(t, clip), SampleRate, 44100), 44100)
	c, body := e.post(t, "/transcribe", nil, file("file", up))
	if c != 200 || len(body["text"].(string)) < 5 || len(body["segments"].([]any)) == 0 {
		t.Fatalf("%d %v", c, body)
	}
	seg := body["segments"].([]any)[0].(map[string]any)
	if int(seg["t1_ms"].(float64))%10 != 0 {
		t.Errorf("segment times are centisecond-based ms: %v", seg)
	}
	t.Logf("transcript: %q", body["text"])
}
