package tts

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
)

// Real-Kokoro tests: they load the embedded sherpa-onnx libraries and the model from
// JARVIS_SHERPA_MODELS/kokoro-multi-lang-v1_0 (scripts/fetch-test-models.sh), and skip
// without it.

const threeSentences = "Good evening. The kitchen lights are now off, and the front door is locked. Is there anything else I can do for you?"

func realKokoro(t *testing.T) *env {
	t.Helper()
	models := os.Getenv("JARVIS_SHERPA_MODELS")
	if models == "" {
		t.Skip("JARVIS_SHERPA_MODELS not set")
	}
	dir := filepath.Join(models, "kokoro-multi-lang-v1_0")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no Kokoro model: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	// Not t.TempDir(): extraction is content-addressed and shared (see internal/voice/sherpa).
	m := &Module{Auth: fakeAuth{}, ModelDir: dir, LibDir: filepath.Join(os.TempDir(), "jarvis-sherpa-test"), NoWarm: true}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Load (and warm) the engine so the timings below measure synthesis, not model load.
	t0 := time.Now()
	st, err := m.Speak(ctx, "Warm.", SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, st)
	t.Logf("engine load + warm: %v", time.Since(t0).Round(time.Millisecond))
	return &env{m: m, h: mux, dir: dir}
}

func TestKokoroStreamHTTP(t *testing.T) {
	e := realKokoro(t)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/speak/stream", strings.NewReader(`{"text":"`+threeSentences+`"}`))
	for k, v := range appH {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/raw" ||
		resp.Header.Get("X-Audio-Sample-Rate") != "24000" || resp.Header.Get("X-Audio-Channels") != "1" ||
		resp.Header.Get("X-Audio-Sample-Width") != "2" || resp.Header.Get("X-Audio-Provider") != "kokoro" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	if len(resp.TransferEncoding) == 0 || resp.TransferEncoding[0] != "chunked" {
		t.Fatalf("not chunked: %v", resp.TransferEncoding)
	}
	headersAt := time.Since(start)
	var body bytes.Buffer
	buf := make([]byte, 4096)
	var firstAt time.Duration
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 && firstAt == 0 {
			firstAt = time.Since(start)
		}
		body.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	total := time.Since(start)
	audioSecs := float64(body.Len()/2) / 24000
	t.Logf("stream: headers %v, first audio %v, total %v, %.2fs of audio (RTF %.2f)",
		headersAt.Round(time.Millisecond), firstAt.Round(time.Millisecond), total.Round(time.Millisecond),
		audioSecs, total.Seconds()/audioSecs)
	if body.Len() == 0 || body.Len()%2 != 0 || bytes.HasPrefix(body.Bytes(), []byte("RIFF")) {
		t.Fatalf("body %d bytes", body.Len())
	}
	if firstAt > total*6/10 {
		t.Fatalf("first audio at %v of %v: not streamed per sentence", firstAt, total)
	}
}

func TestKokoroSpeakWAV(t *testing.T) {
	e := realKokoro(t)
	rec := e.do(t, "POST", "/speak", `{"text":"At your service. How may I help?"}`, appH)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	samples, rate, err := audio.ReadWAV(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || rate != 24000 {
		t.Fatalf("rate %d err %v", rate, err)
	}
	var peak float64
	for _, s := range samples {
		peak = math.Max(peak, math.Abs(float64(s)))
	}
	secs := float64(len(samples)) / 24000
	t.Logf("wav: %.2fs, peak %.2f (gain 2.0)", secs, peak)
	if secs < 0.8 || secs > 6 || peak < 0.2 {
		t.Fatalf("implausible audio: %.2fs peak %.2f", secs, peak)
	}
}

// TestKokoroSentenceOrder checks each streamed chunk is its sentence, in order: chunk i must
// be as long as a standalone render of sentence i (within 2%: multi-threaded CPU inference is
// not bit-exact, a few samples differ), and the three sentences have very different lengths.
func TestKokoroSentenceOrder(t *testing.T) {
	e := realKokoro(t)
	ctx := context.Background()
	sentences := splitSentences(threeSentences)
	if len(sentences) != 3 {
		t.Fatalf("split: %q", sentences)
	}
	st, err := e.m.Speak(ctx, threeSentences, SpeakOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var chunks [][]byte
	for {
		b, err := st.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, b)
	}
	if len(chunks) != len(sentences) {
		t.Fatalf("%d chunks for %d sentences", len(chunks), len(sentences))
	}
	for i, s := range sentences {
		one, err := e.m.Speak(ctx, s, SpeakOptions{})
		if err != nil {
			t.Fatal(err)
		}
		want, _ := io.ReadAll(one)
		if d := math.Abs(float64(len(chunks[i])-len(want))) / float64(len(want)); d > 0.02 {
			t.Fatalf("chunk %d (%d bytes) is not sentence %q (%d bytes)", i, len(chunks[i]), s, len(want))
		}
	}
}

func TestKokoroAmericanVoice(t *testing.T) {
	e := realKokoro(t)
	st, err := e.m.Speak(context.Background(), "Hello from an American voice.", SpeakOptions{Voice: "af_heart"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(st)
	if err != nil || len(b) < 2*24000/2 {
		t.Fatalf("%d bytes, err %v", len(b), err)
	}
}
