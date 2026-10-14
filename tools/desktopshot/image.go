package main

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

// diffBounds is the smallest rectangle, within region, holding every pixel where a and b differ
// by more than threshold in any channel (0-255). ok is false when nothing differs. An empty
// region means the whole image.
func diffBounds(a, b image.Image, region image.Rectangle, threshold int) (image.Rectangle, bool) {
	r := a.Bounds().Intersect(b.Bounds())
	if !region.Empty() {
		r = r.Intersect(region)
	}
	var out image.Rectangle
	found := false
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if !differs(a.At(x, y), b.At(x, y), threshold) {
				continue
			}
			p := image.Rect(x, y, x+1, y+1)
			if !found {
				out, found = p, true
			} else {
				out = out.Union(p)
			}
		}
	}
	return out, found
}

func differs(c1, c2 color.Color, threshold int) bool {
	r1, g1, b1, a1 := c1.RGBA()
	r2, g2, b2, a2 := c2.RGBA()
	t := uint32(threshold) * 0x101
	return absDiff(r1, r2) > t || absDiff(g1, g2) > t || absDiff(b1, b2) > t || absDiff(a1, a2) > t
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// cropScaled is img's rect, grown by pad on every side (clamped to the image), enlarged scale
// times with nearest-neighbour so single pixels stay visible.
func cropScaled(img image.Image, rect image.Rectangle, pad, scale int) *image.NRGBA {
	if scale < 1 {
		scale = 1
	}
	r := rect.Inset(-pad).Intersect(img.Bounds())
	out := image.NewNRGBA(image.Rect(0, 0, r.Dx()*scale, r.Dy()*scale))
	if scale == 1 {
		draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
		return out
	}
	for y := 0; y < r.Dy(); y++ {
		for x := 0; x < r.Dx(); x++ {
			c := color.NRGBAModel.Convert(img.At(r.Min.X+x, r.Min.Y+y)).(color.NRGBA)
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					out.SetNRGBA(x*scale+dx, y*scale+dy, c)
				}
			}
		}
	}
	return out
}

func readPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func writePNG(path string, img image.Image) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return png.Encode(f, img)
}
