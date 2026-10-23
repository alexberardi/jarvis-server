package admin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sttmod "github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/voice/fallback"
	"github.com/alexberardi/jarvis-server/internal/voice/speechpresets"
)

func TestSpeechPresets(t *testing.T) {
	e := newBFF(t)
	if w := send(e.mux, "GET", "/api/speech/presets", ""); w.Code != 401 {
		t.Fatalf("anonymous presets: %d", w.Code)
	}
	w := send(e.mux, "GET", "/api/speech/presets", "", root...)
	if w.Code != 200 {
		t.Fatalf("presets: %d %s", w.Code, w.Body)
	}
	var out struct {
		STT []speechpresets.Preset `json:"stt"`
		TTS []speechpresets.Preset `json:"tts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The route serves the static catalog as is.
	for i, p := range speechpresets.STT {
		if out.STT[i] != p {
			t.Errorf("stt[%d] = %+v, catalog has %+v", i, out.STT[i], p)
		}
	}
	for i, p := range speechpresets.TTS {
		if out.TTS[i] != p {
			t.Errorf("tts[%d] = %+v, catalog has %+v", i, out.TTS[i], p)
		}
	}
}

type fakeTranscriber struct {
	res sttmod.Result
	err error
	// wav is what the handler sent: the embedded clip.
	wav []byte
}

func (f *fakeTranscriber) Transcribe(_ context.Context, wav []byte, _ sttmod.TranscribeOptions) (sttmod.Result, error) {
	f.wav = wav
	return f.res, f.err
}

func TestSTTTest(t *testing.T) {
	e := newBFF(t)
	hdr := append([]string{"Content-Type", "application/json"}, root...)
	if w := send(e.mux, "POST", "/api/speech/stt-test", "", hdr...); w.Code != 503 {
		t.Fatalf("no stt module: %d", w.Code)
	}
	tr := &fakeTranscriber{res: sttmod.Result{Text: "Tell me a joke.", Engine: "remote"}}
	e.m.STT = tr
	w := send(e.mux, "POST", "/api/speech/stt-test", "", hdr...)
	if w.Code != 200 {
		t.Fatalf("stt test: %d %s", w.Code, w.Body)
	}
	out := decode(t, w)
	if out["text"] != "Tell me a joke." || out["engine"] != "remote" || out["clip_says"] != speechTestClipSays {
		t.Fatalf("stt test: %v", out)
	}
	if _, ok := out["latency_ms"].(float64); !ok {
		t.Fatalf("latency_ms missing: %v", out)
	}
	if len(tr.wav) != len(speechTestClip) || len(tr.wav) == 0 {
		t.Fatalf("clip: sent %d bytes, embedded %d", len(tr.wav), len(speechTestClip))
	}

	tr.err = sttmod.ErrEngineUnavailable
	if w := send(e.mux, "POST", "/api/speech/stt-test", "", hdr...); w.Code != 409 {
		t.Fatalf("engine unavailable: %d", w.Code)
	}
	tr.err = errors.New("provider said 500")
	if w := send(e.mux, "POST", "/api/speech/stt-test", "", hdr...); w.Code != 502 {
		t.Fatalf("provider failure: %d", w.Code)
	}
	if w := send(e.mux, "POST", "/api/speech/stt-test", ""); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
}

// reportingSpeech is a tts module whose fallback gate has something to say.
type reportingSpeech struct {
	fakeSpeech
	st fallback.Status
	ok bool
}

func (r *reportingSpeech) FallbackStatus() (fallback.Status, bool) { return r.st, r.ok }

func TestVoiceSampleEngineHeader(t *testing.T) {
	e := newBFF(t)
	hdr := append([]string{"Content-Type", "application/json"}, root...)

	// A plain Speech (Kokoro only) is local.
	e.m.TTS = &fakeSpeech{}
	if w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"af_heart"}`, hdr...); w.Code != 200 ||
		w.Header().Get("X-Speech-Engine") != "local" {
		t.Fatalf("kokoro: %d %q", w.Code, w.Header().Get("X-Speech-Engine"))
	}
	// A remote provider that served itself.
	sp := &reportingSpeech{ok: true, st: fallback.Status{LastPath: fallback.PathRemote}}
	e.m.TTS = sp
	if w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"af_heart"}`, hdr...); w.Code != 200 ||
		w.Header().Get("X-Speech-Engine") != "remote" {
		t.Fatalf("remote: %d %q", w.Code, w.Header().Get("X-Speech-Engine"))
	}
	// The remote failed and the local CPU fallback answered.
	sp.st.LastPath = fallback.PathLocalCPU
	if w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"af_heart"}`, hdr...); w.Code != 200 ||
		w.Header().Get("X-Speech-Engine") != "local-cpu" {
		t.Fatalf("fallback: %d %q", w.Code, w.Header().Get("X-Speech-Engine"))
	}
	// The reporter says Kokoro speaks everything.
	sp.ok, sp.st = false, fallback.Status{}
	if w := send(e.mux, "POST", "/api/tts/sample", `{"voice":"af_heart"}`, hdr...); w.Code != 200 ||
		w.Header().Get("X-Speech-Engine") != "local" {
		t.Fatalf("reporter local: %d %q", w.Code, w.Header().Get("X-Speech-Engine"))
	}
}
