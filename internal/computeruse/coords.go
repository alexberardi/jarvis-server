package computeruse

import (
	"math"
	"strings"
)

// Adapter turns a model's coordinates into image pixels of the capture it was shown. Models differ:
// some answer in pixels, Qwen3.5/3.8 in thousandths of the image (0–999), some in fractions (0–1).
//
// ToImage takes integers, which is what the action schema carries; ToImageF takes the model's
// numbers as parsed, and is the one to use for Unit, whose integer form only reaches the corners.
// Both clamp to the image.
type Adapter struct {
	Name     string
	ToImage  func(x, y, imgW, imgH int) (int, int)
	ToImageF func(x, y float64, imgW, imgH int) (int, int)
}

// The adapters. Pixels rounds half up; Norm1000 and Unit floor, so each bin maps inside the image.
var (
	Pixels   = adapter("pixels", func(v float64, size int) int { return roundHalfUp(v) })
	Norm1000 = adapter("norm1000", func(v float64, size int) int { return int(math.Floor(v * float64(size) / 1000)) })
	Unit     = adapter("unit", func(v float64, size int) int { return int(math.Floor(v * float64(size))) })
)

func adapter(name string, conv func(v float64, size int) int) Adapter {
	f := func(x, y float64, w, h int) (int, int) {
		return clamp(conv(x, w), 0, w-1), clamp(conv(y, h), 0, h-1)
	}
	return Adapter{
		Name:     name,
		ToImageF: f,
		ToImage:  func(x, y, w, h int) (int, int) { return f(float64(x), float64(y), w, h) },
	}
}

// AdapterFor is the adapter for a model id: Qwen3.5 and Qwen3.8 answer in thousandths, everything
// else in pixels.
func AdapterFor(modelID string) Adapter {
	m := strings.ToLower(modelID)
	if strings.Contains(m, "qwen3.5") || strings.Contains(m, "qwen3.8") {
		return Norm1000
	}
	return Pixels
}

// AdapterNamed looks an adapter up by Name (for a per-model setting).
func AdapterNamed(name string) (Adapter, bool) {
	for _, a := range []Adapter{Pixels, Norm1000, Unit} {
		if a.Name == name {
			return a, true
		}
	}
	return Adapter{}, false
}

// Geometry is where a capture sits on the desktop: its origin and size in logical (point) global
// coordinates, its scale (physical pixels per logical pixel), its monitor, and the size of the
// image the model was shown (which may be downscaled from the physical capture).
type Geometry struct {
	OriginX, OriginY   int
	LogicalW, LogicalH int
	Scale              float64
	Monitor            string
	ImageW, ImageH     int
}

// ToGlobal maps image pixels (x, y) of the capture to logical global pixels, rounding half up and
// clamping to the capture. With the image size set, Scale is informational: the image-to-logical
// ratio is LogicalW/ImageW. Without it, the image is taken to be the physical capture at Scale.
func (g Geometry) ToGlobal(x, y int) (int, int) {
	return g.OriginX + axis(x, g.ImageW, g.LogicalW, g.Scale),
		g.OriginY + axis(y, g.ImageH, g.LogicalH, g.Scale)
}

func axis(v, image, logical int, scale float64) int {
	var l int
	switch {
	case image > 0:
		v = clamp(v, 0, image-1)
		// round half up of v*logical/image, in integers: (2*v*logical + image) / (2*image)
		l = (2*v*logical + image) / (2 * image)
	case scale > 0:
		l = roundHalfUp(float64(max(v, 0)) / scale)
	default:
		l = v
	}
	return clamp(l, 0, logical-1)
}

func roundHalfUp(v float64) int { return int(math.Floor(v + 0.5)) }

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}
