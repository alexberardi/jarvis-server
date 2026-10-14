package tray

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/vector"
)

// The shield drawn for small sizes. A tray shows the icon at 16–32 px, where a downscale of
// the artwork loses the shield's outline and its pointed bottom and the butler blurs into a
// blob (DESIGN.md §15). So at those sizes the shield is drawn as vectors on the pixel grid:
// a bold silhouette with a thick light rim (which carries the shape on a dark bar), the brand
// purple inside (which carries it on a light one), the robot's head and the jacket's
// shoulders in white with purple margins all round, so the point at the bottom stays visible.

// glyphMax is the largest size drawn as a glyph; larger icons scale the artwork down.
const glyphMax = 32

var (
	shieldPurple = color.NRGBA{49, 38, 125, 255}   // the artwork's shield
	shieldLight  = color.NRGBA{236, 233, 250, 255} // its rim and the figure
)

// glyphGeom is the shield's layout in pixels for an n × n icon.
type glyphGeom struct {
	n      float64
	x0, x1 float64 // the shield's left and right edges
	rim    float64 // outline thickness
	detail bool    // room for the visor (22 px and up)
}

func geom(n int) glyphGeom {
	// The artwork's shield is 0.745 as wide as it is tall; keep the width's parity equal to
	// n's so it centres on whole pixels.
	w := float64(int(math.Round(0.745*float64(n)/2)) * 2)
	return glyphGeom{
		n:      float64(n),
		x0:     (float64(n) - w) / 2,
		x1:     (float64(n) + w) / 2,
		rim:    math.Max(1, math.Round(float64(n)/14)),
		detail: n >= 22,
	}
}

// shield adds the shield inset by d pixels: a flat top with small rounded corners, straight
// sides down to 62 %, then curves meeting in a point at the bottom.
func (g glyphGeom) shield(z *vector.Rasterizer, d float64) {
	x0, x1, top, bot := g.x0+d, g.x1-d, d, g.n-d*1.6
	cx := (x0 + x1) / 2
	r := math.Max(0.5, g.n/16-d/2)
	side := g.n * 0.62
	z.MoveTo(float32(x0+r), float32(top))
	z.LineTo(float32(x1-r), float32(top))
	z.QuadTo(float32(x1), float32(top), float32(x1), float32(top+r))
	z.LineTo(float32(x1), float32(side))
	z.CubeTo(float32(x1), float32(side+(bot-side)*0.45), float32(cx+(x1-cx)*0.45), float32(bot-(bot-side)*0.12), float32(cx), float32(bot))
	z.CubeTo(float32(cx-(cx-x0)*0.45), float32(bot-(bot-side)*0.12), float32(x0), float32(side+(bot-side)*0.45), float32(x0), float32(side))
	z.LineTo(float32(x0), float32(top+r))
	z.QuadTo(float32(x0), float32(top), float32(x0+r), float32(top))
	z.ClosePath()
}

// The figure is laid out on a 16-unit grid (one unit is a pixel at 16 px), each edge rounded
// to a whole pixel so it stays crisp at every size.
func (g glyphGeom) px(units float64) float64 { return math.Round(units * g.n / 16) }

// figure adds the butler: the robot's head and the jacket's shoulders, a purple gap between
// them and purple below, clear of the shield's point.
func (g glyphGeom) figure(z *vector.Rasterizer) {
	cx := g.n / 2
	hw, sw := g.px(2), g.px(3.5)
	r := math.Max(0.5, g.n/32)
	roundRect(z, cx-hw, g.px(3), cx+hw, g.px(6), r, r)
	roundRect(z, cx-sw, g.px(8), cx+sw, g.px(11), g.px(2.5), 0) // shoulders: round on top only
}

// cuts adds the visor across the head in purple. Only from 22 px, where it gets a whole pixel
// with light around it.
func (g glyphGeom) cuts(z *vector.Rasterizer) {
	if !g.detail {
		return
	}
	cx := g.n / 2
	vw := g.px(1.2)
	vy := g.px(4.3)
	roundRect(z, cx-vw, vy, cx+vw, vy+math.Max(1, g.px(0.6)), 0, 0)
}

// roundRect adds a box with rounded corners: rt at the top, rb at the bottom (0: square).
func roundRect(z *vector.Rasterizer, x0, y0, x1, y1, rt, rb float64) {
	lim := math.Min(x1-x0, y1-y0) / 2
	rt, rb = math.Max(0, math.Min(rt, lim)), math.Max(0, math.Min(rb, lim))
	f := func(v float64) float32 { return float32(v) }
	z.MoveTo(f(x0+rt), f(y0))
	z.LineTo(f(x1-rt), f(y0))
	z.QuadTo(f(x1), f(y0), f(x1), f(y0+rt))
	z.LineTo(f(x1), f(y1-rb))
	z.QuadTo(f(x1), f(y1), f(x1-rb), f(y1))
	z.LineTo(f(x0+rb), f(y1))
	z.QuadTo(f(x0), f(y1), f(x0), f(y1-rb))
	z.LineTo(f(x0), f(y0+rt))
	z.QuadTo(f(x0), f(y0), f(x0+rt), f(y0))
	z.ClosePath()
}

// mask rasterizes one layer of an n × n glyph as coverage.
func mask(n int, add func(*vector.Rasterizer)) *image.Alpha {
	z := vector.NewRasterizer(n, n)
	z.DrawOp = draw.Src
	add(z)
	m := image.NewAlpha(image.Rect(0, 0, n, n))
	z.Draw(m, m.Bounds(), image.Opaque, image.Point{})
	return m
}

// glyph is the colour shield drawn for an n × n icon (n ≤ glyphMax).
func glyph(n int) *image.NRGBA {
	g := geom(n)
	out := image.NewRGBA(image.Rect(0, 0, n, n))
	layer := func(col color.NRGBA, add func(*vector.Rasterizer)) {
		draw.DrawMask(out, out.Bounds(), image.NewUniform(col), image.Point{}, mask(n, add), image.Point{}, draw.Over)
	}
	layer(shieldLight, func(z *vector.Rasterizer) { g.shield(z, 0) })
	layer(shieldPurple, func(z *vector.Rasterizer) { g.shield(z, g.rim) })
	layer(shieldLight, g.figure)
	layer(shieldPurple, g.cuts)
	return toNRGBA(out)
}

// templateGlyph is the macOS template drawn for an icon height px tall: the shield's
// silhouette in black, the butler cut out (clear, with the visor back in black), so
// macOS tints it for light and dark menu bars. As wide as the shield.
func templateGlyph(height int) *image.NRGBA {
	g := geom(height)
	shield := mask(height, func(z *vector.Rasterizer) { g.shield(z, 0) })
	fig := mask(height, g.figure)
	cut := mask(height, g.cuts)
	x0 := int(g.x0)
	w := int(g.x1) - x0
	out := image.NewNRGBA(image.Rect(0, 0, w, height))
	for y := 0; y < height; y++ {
		for x := 0; x < w; x++ {
			s := float64(shield.AlphaAt(x+x0, y).A) / 255
			f := float64(fig.AlphaAt(x+x0, y).A) / 255
			c := float64(cut.AlphaAt(x+x0, y).A) / 255
			a := s * (1 - f*(1-c))
			out.SetNRGBA(x, y, color.NRGBA{A: uint8(math.Round(a * 255))})
		}
	}
	return out
}

func toNRGBA(src *image.RGBA) *image.NRGBA {
	out := image.NewNRGBA(src.Bounds())
	draw.Draw(out, out.Bounds(), src, src.Bounds().Min, draw.Src)
	return out
}
