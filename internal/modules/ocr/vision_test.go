package ocr

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestMapVision(t *testing.T) {
	obs := []visionObservation{
		{Text: "Spicy Beef Rice Bowls", Confidence: 0.5, X: 0.1, Y: 0.8, W: 0.5, H: 0.1},
		{Text: "1 lb ground beef", Confidence: 1, X: 0, Y: 0, W: 1, H: 0.25},
		{Text: "odd", Confidence: math.NaN()},
		{Text: "over", Confidence: 1.5},
	}
	near := func(a, b []float64) bool {
		for i := range a {
			if math.Abs(a[i]-b[i]) > 1e-9 {
				return false
			}
		}
		return len(a) == len(b)
	}

	// Pixels: a top-left origin, scaled by the image size.
	r := mapVision(obs, 200, 100, true)
	if r.Text != "Spicy Beef Rice Bowls\n1 lb ground beef\nodd\nover" {
		t.Fatalf("text: %q", r.Text)
	}
	if len(r.Blocks) != 4 {
		t.Fatalf("blocks: %+v", r.Blocks)
	}
	if b := r.Blocks[0]; b.Text != "Spicy Beef Rice Bowls" || b.Confidence != 0.5 || !near(b.BBox, []float64{20, 10, 100, 10}) {
		t.Fatalf("block 0: %+v", b)
	}
	if b := r.Blocks[1]; !near(b.BBox, []float64{0, 75, 200, 25}) || b.Confidence != 1 {
		t.Fatalf("block 1: %+v", b)
	}
	if r.Blocks[2].Confidence != 0 || r.Blocks[3].Confidence != 1 {
		t.Fatalf("confidence clamped to 0–1: %+v %+v", r.Blocks[2], r.Blocks[3])
	}

	// Unknown size: normalised, still flipped.
	r = mapVision(obs[:1], 0, 0, true)
	if !near(r.Blocks[0].BBox, []float64{0.1, 0.1, 0.5, 0.1}) {
		t.Fatalf("normalised: %+v", r.Blocks[0])
	}
	// No boxes asked: text only, Blocks non-nil.
	r = mapVision(obs, 200, 100, false)
	if r.Blocks == nil || len(r.Blocks) != 0 || !strings.HasPrefix(r.Text, "Spicy") {
		t.Fatalf("no boxes: %+v", r)
	}
	// Nothing read.
	if r := mapVision(nil, 10, 10, true); r.Text != "" || r.Blocks == nil {
		t.Fatalf("empty: %+v", r)
	}
	// Recognize's 0–100 mean confidence (B4) over Vision's 0–1 blocks.
	if c := meanConfidence(mapVision(obs[:2], 1, 1, true).Blocks); c == nil || math.Abs(*c-75) > 1e-9 {
		t.Fatalf("mean: %v", c)
	}
}

func TestVisionLanguages(t *testing.T) {
	got := visionLanguages([]string{"en", " FR ", "", "zh-Hant", "xx"})
	if want := []string{"en-US", "fr-FR", "zh-Hant", "xx"}; !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
	if visionLanguages(nil) != nil {
		t.Fatal("no hints: Vision's defaults")
	}
}

func TestAppleVisionEngineComposition(t *testing.T) {
	native := &fakeEngine{name: EngineAppleVision, text: "native"}
	remote := &fakeEngine{name: EngineAppleVision, text: "remote"}
	if appleVisionEngine(nil, nil) != nil {
		t.Fatal("neither: no engine")
	}
	if appleVisionEngine(native, nil) != Engine(native) || appleVisionEngine(nil, remote) != Engine(remote) {
		t.Fatal("one: that engine")
	}
	ctx := context.Background()
	c := appleVisionEngine(native, remote)
	if c.Name() != EngineAppleVision || !c.Available(ctx) {
		t.Fatal("chain name/availability")
	}
	r, err := c.Recognize(ctx, Image{}, Options{})
	if err != nil || r.Text != "native" || remote.calls.Load() != 0 {
		t.Fatalf("native first: %+v %v", r, err)
	}
	native.err = errors.New("vision failed")
	if r, err := c.Recognize(ctx, Image{}, Options{}); err != nil || r.Text != "remote" {
		t.Fatalf("falls back on error: %+v %v", r, err)
	}
	native.err, native.down = nil, true
	if r, err := c.Recognize(ctx, Image{}, Options{}); err != nil || r.Text != "remote" {
		t.Fatalf("falls back when unavailable: %+v %v", r, err)
	}
	if d := diagnose(ctx, c); !d.Available || d.Provider != EngineAppleVision {
		t.Fatalf("diagnose: %+v", d)
	}
	remote.down = true
	if c.Available(ctx) {
		t.Fatal("both down")
	}
	if _, err := c.Recognize(ctx, Image{}, Options{}); err == nil {
		t.Fatal("both down: error")
	}
	if d := diagnose(ctx, c); d.Available || d.Provider != EngineAppleVision || d.Reason != "unavailable" {
		t.Fatalf("diagnose down: %+v", d)
	}
	native.down, remote.down = false, false
	native.err, remote.err = errors.New("a"), errors.New("b")
	if _, err := c.Recognize(ctx, Image{}, Options{}); err == nil || !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("both fail: %v", err)
	}
}

func TestAppleVisionDefaultFollowsOS(t *testing.T) {
	for _, d := range Definitions {
		if d.Key == "ocr.enable_apple_vision" {
			if d.Default != (runtime.GOOS == "darwin") {
				t.Fatalf("default %v on %s", d.Default, runtime.GOOS)
			}
			return
		}
	}
	t.Fatal("ocr.enable_apple_vision not defined")
}

func TestLLMVisionTimeoutSetting(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"page1\":{\"text\":\"late\"}}"}}]}`)
	}))
	defer srv.Close()
	defer close(release)

	// buildEngines wires the setting into the engine it builds.
	var built *LLMVision
	for _, en := range (&Module{LLMURL: srv.URL, LLMAppID: "id", LLMAppKey: "k", TesseractPath: "-"}).buildEngines() {
		if v, ok := en.(*LLMVision); ok {
			built = v
		}
	}
	if built == nil || built.TimeoutFn == nil {
		t.Fatalf("LLM vision engine without the timeout setting: %+v", built)
	}
	e := setup(t, &Module{LLMURL: srv.URL, LLMAppID: "id", LLMAppKey: "k"})
	l := &LLMVision{URL: srv.URL, AppID: "id", AppKey: "k", TimeoutFn: e.m.llmVisionTimeout}
	if got := l.timeout(e.ctx); got != 180*time.Second {
		t.Fatalf("default timeout %v, want 180s (M4)", got)
	}
	if err := e.m.settings.Set(e.ctx, "ocr.llm_vision_timeout_seconds", 1, settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := l.Recognize(e.ctx, Image{Data: testPNG("HI")}, Options{})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("1 s setting: %v after %v", err, time.Since(start))
	}
	if (&LLMVision{}).timeout(context.Background()) != defaultLLMVisionTimeout {
		t.Fatal("no TimeoutFn: default")
	}
}

// slowEngine blocks until its context ends.
type slowEngine struct{ name string }

func (s slowEngine) Name() string                   { return s.name }
func (s slowEngine) Available(context.Context) bool { return true }
func (s slowEngine) Recognize(ctx context.Context, _ Image, _ Options) (Result, error) {
	<-ctx.Done()
	return Result{}, ctx.Err()
}

// TestRecognizeDeadlineKeepsFastReadings: a slow engine running into the caller's deadline
// must not throw away what a fast engine read; a cancelled context is still an error.
func TestRecognizeDeadlineKeepsFastReadings(t *testing.T) {
	fast := &fakeEngine{name: EngineAppleVision, text: "read"}
	e := setup(t, &Module{Engines: []Engine{fast, slowEngine{name: EngineLLMVision}}})
	for _, k := range []string{"ocr.enable_apple_vision", "ocr.enable_llm_proxy_vision"} {
		if err := e.m.settings.Set(e.ctx, k, true, settings.Scope{}); err != nil {
			t.Fatal(err)
		}
	}
	imgs := []Image{{Data: []byte("a")}, {Data: []byte("b")}}
	dctx, cancel := context.WithTimeout(e.ctx, 100*time.Millisecond)
	defer cancel()
	rs, err := e.m.Recognize(dctx, imgs, Options{}, nil)
	if err != nil || len(rs) != 2 {
		t.Fatalf("deadline: %+v %v", rs, err)
	}
	if rs[0].Engine != EngineAppleVision || rs[0].Results[0].Text != "read" || rs[0].Results[1].Text != "read" {
		t.Fatalf("fast reading kept: %+v", rs[0])
	}
	if len(rs[1].Results) != 2 || rs[1].Results[0].Error == "" || rs[1].Results[1].Error == "" {
		t.Fatalf("slow reading carries the deadline: %+v", rs[1])
	}
	cctx, ccancel := context.WithCancel(e.ctx)
	go func() { time.Sleep(50 * time.Millisecond); ccancel() }()
	if _, err := e.m.Recognize(cctx, imgs, Options{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
