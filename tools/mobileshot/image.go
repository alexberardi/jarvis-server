package main

import (
	"fmt"
	"image"
	_ "image/png" // screencap's format
	"os"
)

// Luminance is the mean relative luminance (0 black … 1 white) of the image between the status
// bar and the navigation bar (skipTop/skipBottom pixels), sampled on a grid.
func Luminance(img image.Image, skipTop, skipBottom int) float64 {
	b := img.Bounds()
	y0, y1 := b.Min.Y+skipTop, b.Max.Y-skipBottom
	if y1 <= y0 {
		y0, y1 = b.Min.Y, b.Max.Y
	}
	var sum float64
	var n int
	for y := y0; y < y1; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / 65535
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// ThemeMatches is a sanity check of a screenshot against the theme it was taken in: a dark
// screen is mostly dark. Loose on purpose (images and coloured cards move the mean).
func ThemeMatches(theme string, lum float64) bool {
	if theme == "dark" {
		return lum < 0.45
	}
	return lum > 0.55
}

func fileLuminance(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	// Status bar and gesture bar are ~5% of a phone screen each; they follow the system, not
	// the app.
	h := img.Bounds().Dy()
	return Luminance(img, h/20, h/25), nil
}
