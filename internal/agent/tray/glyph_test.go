package tray

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.png")

// The small icons as golden PNGs (look at them: go test -run Golden -update, then open
// testdata/). Compared with a tolerance: the rasterizer's accumulation differs by
// architecture.
func TestGlyphGolden(t *testing.T) {
	cases := map[string]*image.NRGBA{"template-36": Template(36)}
	for _, n := range []int{16, 20, 22, 24, 32} {
		cases[fmt.Sprintf("color-%d", n)] = Color(n)
	}
	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("testdata", name+".png")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, PNG(img), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			defer f.Close()
			want, err := png.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			if want.Bounds() != img.Bounds() {
				t.Fatalf("%v, golden %v", img.Bounds(), want.Bounds())
			}
			bad := 0
			for y := 0; y < img.Bounds().Dy(); y++ {
				for x := 0; x < img.Bounds().Dx(); x++ {
					a := img.NRGBAAt(x, y)
					b := color.NRGBAModel.Convert(want.At(x, y)).(color.NRGBA)
					if d(a.R, b.R) > 8 || d(a.G, b.G) > 8 || d(a.B, b.B) > 8 || d(a.A, b.A) > 8 {
						bad++
					}
				}
			}
			if bad > 2 {
				t.Fatalf("%d pixels differ from %s", bad, path)
			}
		})
	}
}

func d(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// The small sizes keep what made the downscale unreadable: a light rim all the way round
// (the shape on a dark bar), purple between the rim and the figure (the shape on a light
// bar), and a pointed bottom.
func TestGlyphShape(t *testing.T) {
	for _, n := range []int{16, 20, 22, 24, 32} {
		img := Color(n)
		g := geom(n)
		mid := n / 2
		// Left edge at mid-height: rim (light) then purple.
		x := int(g.x0)
		if c := img.NRGBAAt(x, mid); c.A < 200 || c.R < 200 {
			t.Errorf("Color(%d): no light rim at (%d,%d): %v", n, x, mid, c)
		}
		x += int(g.rim)
		if c := img.NRGBAAt(x, mid); c != shieldPurple {
			t.Errorf("Color(%d): no purple inside the rim at (%d,%d): %v", n, x, mid, c)
		}
		// The bottom is a point: the last opaque row is narrow and centred.
		var lastRow, lastW int
		for y := n - 1; y >= 0; y-- {
			w := 0
			for x := 0; x < n; x++ {
				if img.NRGBAAt(x, y).A > 128 {
					w++
				}
			}
			if w > 0 {
				lastRow, lastW = y, w
				break
			}
		}
		if lastRow < n-3 || lastW > 4 {
			t.Errorf("Color(%d): bottom row %d is %d px wide, want a point near the bottom", n, lastRow, lastW)
		}
		// Above the point, purple separates the figure from the rim (the point doesn't fill
		// with white as the downscale's did).
		yb := int(g.px(12))
		if c := img.NRGBAAt(mid, yb); c.R > 120 {
			t.Errorf("Color(%d): (%d,%d) under the figure is %v, want purple", n, mid, yb, c)
		}
		// The head is light, centred.
		if c := img.NRGBAAt(mid, int(float64(n)*0.36)); c.R < 200 {
			t.Errorf("Color(%d): no head at the centre: %v", n, c)
		}
		// Transparent corners.
		if img.NRGBAAt(0, n-1).A != 0 || img.NRGBAAt(n-1, n-1).A != 0 {
			t.Errorf("Color(%d): bottom corners aren't clear", n)
		}
	}
}

// Larger sizes still come from the artwork.
func TestColorLargeIsArtwork(t *testing.T) {
	for _, n := range []int{48, 64} {
		img := Color(n)
		if img.Bounds().Dx() != n || img.Bounds().Dy() != n {
			t.Fatalf("Color(%d) is %v", n, img.Bounds())
		}
		if c := img.NRGBAAt(n/2, n/2); c.A < 200 {
			t.Fatalf("Color(%d) centre %v", n, c)
		}
	}
}
