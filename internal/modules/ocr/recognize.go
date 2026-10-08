package ocr

import (
	"context"
	"errors"
	"slices"
	"sync"
)

// The in-process OCR API (docs/recipes/00-inventory.md §7.3): recipes' photo import runs every
// available engine over the photos in one queue job and reconciles the readings itself. It
// replaces the legacy fan-out to OCR hosts and its join deadline.

// EngineRank is the legacy recipes ENGINE_RANK: engines best first. A reading's place in the
// LLM prompt follows it; engines not listed sort after the listed ones.
var EngineRank = []string{
	"apple_vision", "llm_proxy_vision", "llm_proxy_cloud", "rapidocr", "tesseract", "paddleocr", "easyocr",
}

// RankOf is an engine's place in EngineRank (len(EngineRank) when unlisted).
func RankOf(engine string) int {
	if i := slices.Index(EngineRank, engine); i >= 0 {
		return i
	}
	return len(EngineRank)
}

// ImageReading is one engine's reading of one image. Confidence is the mean word confidence on
// a 0–100 scale when the engine reports one (the legacy queue path sent 0–1 to a gate that
// tested ≥ 50, so it never scored: B4).
type ImageReading struct {
	Index      int      `json:"index"`
	Text       string   `json:"ocr_text"`
	Confidence *float64 `json:"confidence"`
	Error      string   `json:"error,omitempty"`
}

// Reading is one engine's reading of every image, in image order.
type Reading struct {
	Engine  string         `json:"provider"`
	Results []ImageReading `json:"results"`
}

// ErrNoEngines is returned when no OCR engine is enabled and available.
var ErrNoEngines = errors.New("No OCR providers available")

// Recognize runs engine names (empty = every enabled, available engine, in EngineRank order)
// over imgs and returns one reading per engine run, best engine first. Per-image errors are in
// the readings, not err; err is ErrNoEngines when nothing could run, or the context's error.
// Engines run concurrently; each reads the images in order. Text is normalised as the OCR
// service normalises it.
func (m *Module) Recognize(ctx context.Context, imgs []Image, o Options, engines []string) ([]Reading, error) {
	reg := m.registry(ctx)
	var run []Engine
	if len(engines) == 0 {
		for _, e := range reg {
			run = append(run, e)
		}
	} else {
		for _, n := range engines {
			if e, ok := reg[n]; ok {
				run = append(run, e)
			}
		}
	}
	run = slices.DeleteFunc(run, func(e Engine) bool { return !e.Available(ctx) })
	slices.SortStableFunc(run, func(a, b Engine) int { return RankOf(a.Name()) - RankOf(b.Name()) })
	if len(run) == 0 {
		return nil, ErrNoEngines
	}
	o.ReturnBoxes = true // the word confidences
	out := make([]Reading, len(run))
	var wg sync.WaitGroup
	for i, e := range run {
		wg.Go(func() {
			rd := Reading{Engine: e.Name(), Results: make([]ImageReading, 0, len(imgs))}
			for idx, img := range imgs {
				r, err := e.Recognize(ctx, img, o)
				ir := ImageReading{Index: idx}
				if err != nil {
					ir.Error = truncRunes(err.Error(), 200)
				} else {
					ir.Text = normalizeText(r.Text)
					ir.Confidence = meanConfidence(r.Blocks)
				}
				rd.Results = append(rd.Results, ir)
			}
			out[i] = rd
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// meanConfidence averages the blocks' confidences (negative = no word, skipped), scaled to
// 0–100 when the engine reports 0–1.
func meanConfidence(blocks []Block) *float64 {
	var sum, top float64
	n := 0
	for _, b := range blocks {
		if b.Confidence < 0 {
			continue
		}
		sum += b.Confidence
		top = max(top, b.Confidence)
		n++
	}
	if n == 0 {
		return nil
	}
	mean := sum / float64(n)
	if top <= 1 {
		mean *= 100
	}
	return &mean
}
