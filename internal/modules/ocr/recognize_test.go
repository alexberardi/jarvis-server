package ocr

import (
	"errors"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestRecognize(t *testing.T) {
	tess := &fakeEngine{name: EngineTesseract, text: "  tesseract   text \r\n\n\n\nmore"}
	av := &fakeEngine{name: EngineAppleVision, text: "apple"}
	llm := &fakeEngine{name: EngineLLMVision, err: errors.New("model down")}
	e := setup(t, &Module{Engines: []Engine{tess, av, llm}})
	ctx := e.ctx
	imgs := []Image{{Data: []byte("a")}, {Data: []byte("b")}}

	// Apple Vision and LLM vision are off by default: only tesseract runs.
	rs, err := e.m.Recognize(ctx, imgs, Options{}, nil)
	if err != nil || len(rs) != 1 || rs[0].Engine != EngineTesseract || len(rs[0].Results) != 2 {
		t.Fatalf("default: %+v %v", rs, err)
	}
	r0 := rs[0].Results[0]
	if r0.Text != "tesseract text\n\nmore" || r0.Index != 0 || rs[0].Results[1].Index != 1 {
		t.Fatalf("normalised text: %+v", r0)
	}
	// 0–1 block confidences are reported on 0–100 (B4).
	if r0.Confidence == nil || *r0.Confidence < 89.9 || *r0.Confidence > 90.1 {
		t.Fatalf("confidence: %v", r0.Confidence)
	}

	for _, k := range []string{"ocr.enable_apple_vision", "ocr.enable_llm_proxy_vision"} {
		if err := e.m.settings.Set(ctx, k, true, settings.Scope{}); err != nil {
			t.Fatal(err)
		}
	}
	rs, err = e.m.Recognize(ctx, imgs, Options{}, nil)
	if err != nil || len(rs) != 3 {
		t.Fatalf("all: %+v %v", rs, err)
	}
	// Rank order, not registration order; a failing engine's images carry the error.
	if rs[0].Engine != EngineAppleVision || rs[1].Engine != EngineLLMVision || rs[2].Engine != EngineTesseract {
		t.Fatalf("order: %s %s %s", rs[0].Engine, rs[1].Engine, rs[2].Engine)
	}
	if rs[1].Results[0].Error != "model down" || rs[1].Results[0].Text != "" {
		t.Fatalf("error reading: %+v", rs[1].Results[0])
	}
	rs, err = e.m.Recognize(ctx, imgs, Options{}, []string{EngineTesseract})
	if err != nil || len(rs) != 1 || rs[0].Engine != EngineTesseract {
		t.Fatalf("named: %+v %v", rs, err)
	}
	tess.down = true
	if _, err := e.m.Recognize(ctx, imgs, Options{}, []string{EngineTesseract}); !errors.Is(err, ErrNoEngines) {
		t.Fatalf("nothing available: %v", err)
	}
	if RankOf("apple_vision") != 0 || RankOf("tesseract") != 4 || RankOf("mystery") != len(EngineRank) {
		t.Fatal("rank")
	}
}
