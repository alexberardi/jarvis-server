package admin

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	llmmod "github.com/alexberardi/jarvis-server/internal/modules/llm"
	ttsmod "github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func jobByID(t *testing.T, jobs []JobSummary, id string) JobSummary {
	t.Helper()
	for _, j := range jobs {
		if j.Job == id {
			return j
		}
	}
	t.Fatalf("no job %s in %+v", id, jobs)
	return JobSummary{}
}

func TestSummarizeJobs(t *testing.T) {
	labels := map[string]string{
		"live": "starting", "background": "starting", "stt": "not_configured", "tts": "ready",
		"speaker": "not_configured", "embeddings": "no_engine_build",
	}
	installs := []llmmod.SetupInstall{ // newest first
		{ID: 9, ModelID: "eres2net", Assign: []string{"speaker"}, State: "failed", Error: "sha256 mismatch"},
		{ID: 8, ModelID: "whisper-base.en", Assign: []string{"stt"}, State: "queued", BytesTotal: 10},
		{ID: 7, ModelID: "whisper-small.en", Assign: []string{"stt"}, State: "failed"},
		{ID: 6, ModelID: "qwen3-4b", Assign: []string{"live", "background"}, State: "done"},
	}
	jobs := summarizeJobs(labels, installs)
	if len(jobs) != 5 || jobs[0].Job != "llm" || jobs[4].Job != "memory" {
		t.Fatalf("order: %+v", jobs)
	}
	for id, want := range map[string]string{
		"llm": JobLoading, "stt": JobDownloading, "voice": JobReady, "speaker": JobFailed, "memory": JobFailed,
	} {
		if got := jobByID(t, jobs, id); got.State != want {
			t.Errorf("%s: %s, want %s (%+v)", id, got.State, want, got)
		}
	}
	if j := jobByID(t, jobs, "stt"); j.Install == nil || j.Install.ID != 8 || !j.Required {
		t.Errorf("stt waits on the active install: %+v", j)
	}
	if j := jobByID(t, jobs, "speaker"); j.Install == nil || j.Install.Error != "sha256 mismatch" || j.Required {
		t.Errorf("speaker reports its failed install: %+v", j)
	}
	if j := jobByID(t, jobs, "llm"); len(j.Labels) != 2 || j.Labels[1] != "background" {
		t.Errorf("llm labels: %+v", j)
	}

	// Nothing at all: every job missing, a cancelled install doesn't count.
	jobs = summarizeJobs(nil, []llmmod.SetupInstall{{ID: 1, Assign: []string{"tts"}, State: "cancelled"}})
	for _, j := range jobs {
		if j.State != JobMissing || j.LabelState != llmmod.StateNotConfigured {
			t.Errorf("empty: %+v", j)
		}
	}
	// A model refused for a lost GPU needs the operator (a reboot), not patience.
	if j := summarizeJobs(map[string]string{"live": llmmod.StateGPUUnavailable}, nil)[0]; j.State != JobFailed {
		t.Errorf("gpu_unavailable: %+v", j)
	}
	// Remote and degraded count as ready.
	if j := summarizeJobs(map[string]string{"live": "remote"}, nil)[0]; j.State != JobReady {
		t.Errorf("remote: %+v", j)
	}
}

func TestSetupStepFromJobs(t *testing.T) {
	missing := summarizeJobs(nil, nil)
	if s := setupStep(false, "", missing); s != "hardware" {
		t.Errorf("fresh: %q", s)
	}
	if s := setupStep(true, "llm", missing); s != "" {
		t.Errorf("completed: %q", s)
	}
	if s := setupStep(false, "memory", missing); s != "memory" {
		t.Errorf("saved: %q", s)
	}
	if s := setupStep(false, "models", missing); s != "hardware" { // pre-AD3b value: ignored
		t.Errorf("stale saved: %q", s)
	}
	jobs := summarizeJobs(map[string]string{"live": "ready"},
		[]llmmod.SetupInstall{{Assign: []string{"stt"}, State: "running"}})
	if s := setupStep(false, "", jobs); s != "voice" {
		t.Errorf("stt under way: %q", s)
	}
	jobs = summarizeJobs(map[string]string{"live": "ready", "stt": "ready", "tts": "ready"}, nil)
	if s := setupStep(false, "", jobs); s != "privacy" {
		t.Errorf("required done: %q", s)
	}
}

type fakeSpeech struct {
	err        error
	voice, txt string
}

func (f *fakeSpeech) Sample(_ context.Context, voice, text string) ([]byte, error) {
	f.voice, f.txt = voice, text
	if f.err != nil {
		return nil, f.err
	}
	return []byte("RIFF....WAVE"), nil
}

func TestVoiceBFF(t *testing.T) {
	e := newBFF(t)
	// No tts settings source: the default voice is current.
	out := decode(t, send(e.mux, "GET", "/api/tts/voices", "", root...))
	if out["current"] != ttsmod.DefaultVoice || out["default"] != ttsmod.DefaultVoice ||
		len(out["voices"].([]any)) != len(ttsmod.Voices()) {
		t.Fatalf("voices: %v", out)
	}
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "tts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ttsSet := newSettings(t, d, "tts", []settings.Definition{
		{Key: "tts.kokoro_voice", Category: "tts", Type: settings.String, Default: ttsmod.DefaultVoice},
	})
	if err := ttsSet.Set(context.Background(), "tts.kokoro_voice", "af_heart", settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	e.m.SettingsSources = append(e.m.SettingsSources, fakeSettings{"tts", ttsSet})
	if out := decode(t, send(e.mux, "GET", "/api/tts/voices", "", root...)); out["current"] != "af_heart" {
		t.Fatalf("current: %v", out)
	}
	if w := send(e.mux, "GET", "/api/tts/voices", ""); w.Code != 401 {
		t.Fatalf("anonymous voices: %d", w.Code)
	}

	hdr := append([]string{"Content-Type", "application/json"}, root...)
	if w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"bm_george"}`, hdr...); w.Code != 503 {
		t.Fatalf("no tts: %d", w.Code)
	}
	sp := &fakeSpeech{}
	e.m.TTS = sp
	w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"BM_George"}`, hdr...)
	if w.Code != 200 || w.Header().Get("Content-Type") != "audio/wav" || sp.voice != "bm_george" || sp.txt != SampleText {
		t.Fatalf("sample: %d %q %+v", w.Code, w.Header().Get("Content-Type"), sp)
	}
	if w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"af_heart","text":"Testing"}`, hdr...); w.Code != 200 || sp.txt != "Testing" {
		t.Fatalf("text: %d %+v", w.Code, sp)
	}
	for _, body := range []string{`{"voice":"nobody"}`, `{}`, `{"voice":"af_heart","text":"` + string(make([]byte, 201)) + `"}`} {
		if w := send(e.mux, "POST", "/api/tts/sample", body, hdr...); w.Code != 422 && w.Code != 400 {
			t.Errorf("%.40s: %d", body, w.Code)
		}
	}
	sp.err = ttsmod.ErrNotInstalled
	if w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"af_heart"}`, hdr...); w.Code != 409 {
		t.Fatalf("not installed: %d", w.Code)
	}
	sp.err = errors.New("boom")
	if w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"af_heart"}`, hdr...); w.Code != 500 {
		t.Fatalf("failed: %d", w.Code)
	}
	if w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"af_heart"}`, "Content-Type", "application/json"); w.Code != 401 {
		t.Fatalf("anonymous sample: %d", w.Code)
	}
}
