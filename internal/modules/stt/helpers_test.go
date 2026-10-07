package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/voice/sherpa"
)

// Shared by the unit tests and the parity harness (same package, any build tag).

// fakeAuth accepts app "jarvis-cc"/"k".
type fakeAuth struct{}

func (fakeAuth) ValidateApp(_ context.Context, id, key string) (authn.App, bool, error) {
	return authn.App{ID: id}, id == "jarvis-cc" && key == "k", nil
}
func (fakeAuth) ValidateNode(context.Context, string, string, string) (authn.NodeValidation, error) {
	return authn.NodeValidation{}, nil
}
func (fakeAuth) HouseholdRole(context.Context, int64, string) (authn.Role, bool, error) {
	return "", false, nil
}

// fakeWhisper is a whisper-server /inference double: it records each request's form and
// decoded WAV and answers verbose_json.
type fakeWhisper struct {
	srv *httptest.Server

	mu       sync.Mutex
	forms    []map[string]string
	rates    []int
	lengths  []int
	status   int    // 0 = 200
	response string // "" = the default two-segment transcript
}

const fakeTranscript = `{"task":"transcribe","language":"english","duration":1.0,"text":" turn off the lights\n",
 "segments":[{"id":0,"text":" turn off","start":0.0,"end":0.84,"tokens":[1]},
             {"id":1,"text":" the lights","start":0.84,"end":1.84,"tokens":[2]}]}`

func newFakeWhisper(t testing.TB) *fakeWhisper {
	f := &fakeWhisper{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	if t != nil {
		t.Cleanup(f.srv.Close)
	}
	return f
}

func (f *fakeWhisper) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/inference" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	form := map[string]string{}
	for k, v := range r.MultipartForm.Value {
		form[k] = v[0]
	}
	rate, n := -1, -1
	if fhs := r.MultipartForm.File["file"]; len(fhs) > 0 {
		file, _ := fhs[0].Open()
		s, sr, err := audio.ReadWAV(file)
		file.Close()
		if err == nil {
			rate, n = sr, len(s)
		}
	}
	f.mu.Lock()
	f.forms = append(f.forms, form)
	f.rates = append(f.rates, rate)
	f.lengths = append(f.lengths, n)
	status, body := f.status, f.response
	f.mu.Unlock()
	if status == 0 {
		status = 200
	}
	if body == "" {
		body = fakeTranscript
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func (f *fakeWhisper) last() (map[string]string, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forms) == 0 {
		return nil, 0, 0
	}
	i := len(f.forms) - 1
	return f.forms[i], f.rates[i], f.lengths[i]
}

func (f *fakeWhisper) resolver() STTEngine {
	return ResolveFunc(func(_ context.Context, label string) (string, error) {
		if label != "stt" {
			return "", io.ErrUnexpectedEOF
		}
		return f.srv.URL, nil
	})
}

// toneEmbedder is a stand-in speaker model: the embedding is the clip's energy at a few
// pitches (Goertzel), so sines of different frequencies are different "speakers".
type toneEmbedder struct{ id string }

var toneBins = []float64{110, 165, 220, 275, 330, 440, 550, 660, 880, 1100}

func (e toneEmbedder) ID() string {
	if e.id == "" {
		return "tone-v1"
	}
	return e.id
}

func (toneEmbedder) Embed(s []float32, rate int) ([]float32, error) {
	if len(s) < rate/2 {
		return nil, sherpa.ErrTooShort
	}
	out := make([]float32, len(toneBins))
	for i, f := range toneBins {
		k := 2 * math.Cos(2*math.Pi*f/float64(rate))
		var s1, s2 float64
		for _, x := range s {
			s0 := float64(x) + k*s1 - s2
			s2, s1 = s1, s0
		}
		out[i] = float32(math.Sqrt(s1*s1+s2*s2-k*s1*s2) / float64(len(s)))
	}
	return out, nil
}

// sine is mono samples of a tone at amplitude 0.3 (the contract's SineWAV level).
func sine(rate int, seconds, freq float64) []float32 {
	n := int(float64(rate) * seconds)
	s := make([]float32, n)
	for i := range s {
		s[i] = float32(0.3 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return s
}

func wavOf(s []float32, rate int) []byte {
	var b bytes.Buffer
	if err := audio.WriteWAV(&b, s, rate); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func sineWAV(rate int, seconds, freq float64) []byte { return wavOf(sine(rate, seconds, freq), rate) }

type part struct {
	name, filename string
	data           []byte
}

func multipartBody(parts ...part) (*bytes.Buffer, string) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, p := range parts {
		if p.filename != "" {
			w, _ := mw.CreateFormFile(p.name, p.filename)
			w.Write(p.data)
		} else {
			mw.WriteField(p.name, string(p.data))
		}
	}
	mw.Close()
	return &b, mw.FormDataContentType()
}

func file(name string, data []byte) part {
	return part{name: name, filename: name + ".wav", data: data}
}
func field(name, v string) part { return part{name: name, data: []byte(v)} }

func decodeJSON(t testing.TB, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	return v
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
