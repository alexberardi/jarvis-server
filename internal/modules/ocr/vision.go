package ocr

import (
	"context"
	"errors"
	"runtime"
	"strings"
)

// Native Apple Vision (ID13). On darwin jarvisd calls Vision's VNRecognizeTextRequest in
// process through purego's Objective-C runtime (vision_darwin.go, no cgo); elsewhere there is
// no native engine (vision_other.go). This file is the platform-independent part: the mapping
// from Vision's observations to a Result, and the engine that falls back from the native
// reader to the legacy jarvis-osx-api one.

// defaultAppleVision is ocr.enable_apple_vision's default: on where Vision is built in.
var defaultAppleVision = runtime.GOOS == "darwin"

// visionObservation is one VNRecognizedTextObservation's top candidate: the line's text, its
// confidence (0–1) and its bounding box as Vision reports it, normalised to [0,1] with a
// bottom-left origin.
type visionObservation struct {
	Text       string
	Confidence float64
	X, Y, W, H float64
}

// mapVision turns Vision's observations into a Result the way jarvis-osx-api did: one line per
// observation joined with "\n" (the recipes gate and the LLM read line structure), boxes flipped
// to a top-left origin and scaled to pixels when the image's size is known (w, h > 0; otherwise
// they stay normalised, as the HTTP engine leaves them), confidence kept on 0–1 like the other
// engines' blocks (Recognize reports the mean on 0–100).
func mapVision(obs []visionObservation, w, h int, returnBoxes bool) Result {
	lines := make([]string, 0, len(obs))
	res := Result{Blocks: []Block{}}
	sx, sy := 1.0, 1.0
	if w > 0 && h > 0 {
		sx, sy = float64(w), float64(h)
	}
	for _, o := range obs {
		lines = append(lines, o.Text)
		if !returnBoxes {
			continue
		}
		conf := o.Confidence
		if conf < 0 || conf != conf { // negative or NaN: no confidence
			conf = 0
		}
		res.Blocks = append(res.Blocks, Block{
			Text:       o.Text,
			BBox:       []float64{o.X * sx, (1 - o.Y - o.H) * sy, o.W * sx, o.H * sy},
			Confidence: min(conf, 1),
		})
	}
	res.Text = strings.Join(lines, "\n")
	return res
}

// visionLanguages turns the callers' language hints into Vision recognition languages. Vision
// wants BCP 47 tags ("en-US"); the legacy osx-api passed the bare hints, which Vision accepts
// for English, so two-letter hints map to their usual Vision tag and anything else is passed
// as is.
func visionLanguages(hints []string) []string {
	tags := map[string]string{"en": "en-US", "fr": "fr-FR", "de": "de-DE", "es": "es-ES", "it": "it-IT",
		"pt": "pt-BR", "zh": "zh-Hans", "ja": "ja-JP", "ko": "ko-KR"}
	var out []string
	for _, h := range hints {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if t, ok := tags[strings.ToLower(h)]; ok {
			h = t
		}
		out = append(out, h)
	}
	return out
}

// appleVisionChain is the apple_vision engine when both readers exist: the native one first,
// the jarvis-osx-api one when the native one is unavailable or fails.
type appleVisionChain struct {
	engines []Engine
}

func (c *appleVisionChain) Name() string { return EngineAppleVision }

func (c *appleVisionChain) Available(ctx context.Context) bool {
	for _, e := range c.engines {
		if e.Available(ctx) {
			return true
		}
	}
	return false
}

func (c *appleVisionChain) Diagnose(ctx context.Context) Diagnostic {
	var first Diagnostic
	for i, e := range c.engines {
		d := diagnose(ctx, e)
		if d.Available {
			d.Provider = EngineAppleVision
			return d
		}
		if i == 0 {
			first = d
		}
	}
	first.Provider = EngineAppleVision
	return first
}

func (c *appleVisionChain) Recognize(ctx context.Context, img Image, o Options) (Result, error) {
	var errs []error
	for _, e := range c.engines {
		if !e.Available(ctx) {
			continue
		}
		r, err := e.Recognize(ctx, img, o)
		if err == nil {
			return r, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 0 {
		return Result{}, errors.New("apple_vision is not available")
	}
	return Result{}, errors.Join(errs...)
}

// appleVisionEngine combines the native reader (nil when there is none) and the HTTP one (nil
// when JARVIS_OSX_API_URL is unset) into the apple_vision engine, or nil when neither exists.
func appleVisionEngine(native, remote Engine) Engine {
	switch {
	case native != nil && remote != nil:
		return &appleVisionChain{engines: []Engine{native, remote}}
	case native != nil:
		return native
	case remote != nil:
		return remote
	}
	return nil
}
