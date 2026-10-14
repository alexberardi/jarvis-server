package tray

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
	"sync"

	"golang.org/x/image/draw"
)

// icon.png is the Jarvis Automation shield (the browser extension's icon).
//
//go:embed icon.png
var iconPNG []byte

var (
	iconOnce sync.Once
	iconImg  image.Image
)

// source is the decoded shield, cropped to what isn't transparent.
func source() image.Image {
	iconOnce.Do(func() {
		img, err := png.Decode(bytes.NewReader(iconPNG))
		if err != nil {
			panic("tray: icon.png: " + err.Error()) // embedded: a test catches it
		}
		iconImg = crop(img)
	})
	return iconImg
}

// crop trims fully transparent rows and columns.
func crop(img image.Image) image.Image {
	b := img.Bounds()
	r := image.Rectangle{Min: b.Max, Max: b.Min}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if r.Empty() {
		return img
	}
	out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
	return out
}

// Color is the shield in colour, centred in a size × size square (Linux and Windows icons).
func Color(size int) *image.NRGBA {
	src := source()
	sb := src.Bounds()
	h := size
	w := max(1, sb.Dx()*size/sb.Dy())
	if sb.Dx() > sb.Dy() {
		w, h = size, max(1, sb.Dy()*size/sb.Dx())
	}
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	at := image.Rect((size-w)/2, (size-h)/2, (size-w)/2+w, (size-h)/2+h)
	draw.CatmullRom.Scale(out, at, src, sb, draw.Over, nil)
	return out
}

// Template is the shield as a macOS menu-bar template image, height px tall: black where the
// shield is dark, transparent where the figure is light, so the robot butler shows as a cut-out
// and macOS tints it for light and dark menu bars.
func Template(height int) *image.NRGBA {
	src := source()
	sb := src.Bounds()
	w := max(1, sb.Dx()*height/sb.Dy())
	scaled := image.NewNRGBA(image.Rect(0, 0, w, height))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), src, sb, draw.Src, nil)
	out := image.NewNRGBA(scaled.Bounds())
	for y := 0; y < height; y++ {
		for x := 0; x < w; x++ {
			c := scaled.NRGBAAt(x, y)
			// Luma 0..1; the shield's purple is ~0.2, the figure ~0.9.
			l := (0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)) / 255
			k := min(1, max(0, (0.75-l)/0.35))
			out.SetNRGBA(x, y, color.NRGBA{A: uint8(float64(c.A) * k)})
		}
	}
	return out
}

// PNG encodes img.
func PNG(img image.Image) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, img) // in-memory: can't fail for an NRGBA
	return b.Bytes()
}
