package island

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// The island's look, drawn in pure Go so every platform shows the same pixels. Sizes are in
// logical pixels (points); Render scales them to the backing buffer.

const (
	// CanvasW × CanvasH is the surface the platform shows: the widest pill (a note) plus room
	// to slide in. Everything outside the pill is transparent and click-through.
	CanvasW = 360
	CanvasH = 56

	pillW   = 136
	pillH   = 38
	pillTop = 9 // the pill's top edge once it has slid in

	orbX    = 20 // from the pill's left edge
	orbR    = 6.5
	ringR   = 11
	ringW   = 2.2
	nBars   = 5
	barW    = 4
	barGap  = 4
	barMinH = 4
	barMaxH = 24
	barsEnd = 18 // the last bar's right edge, from the pill's right edge

	// A note (what is being typed, a question waiting for a yes): a wider pill with text.
	noteMaxW  = 344
	noteTextX = orbX + orbR + 11 // text start, from the pill's left edge
	notePadR  = 18
	noteSize  = 13 // points
)

var (
	pillColor  = color.NRGBA{11, 11, 15, 235}
	edgeColor  = color.NRGBA{255, 255, 255, 26}
	listenCol  = color.NRGBA{90, 200, 250, 255}  // listening, follow-up
	thinkCol   = color.NRGBA{157, 140, 255, 255} // thinking
	speakCol   = color.NRGBA{235, 245, 255, 255} // speaking
	errorCol   = color.NRGBA{255, 69, 58, 255}
	noteCol    = color.NRGBA{48, 209, 88, 255} // typing, a question
	textCol    = color.NRGBA{242, 242, 247, 255}
	ringTrackA = 0.18
)

// Frame is one picture of the island: what the animation model hands the renderer.
type Frame struct {
	State  State
	Appear float64        // 0 hidden (above the canvas) .. 1 in place
	Bars   [nBars]float64 // 0..1 bar heights
	Ring   float64        // follow-up: the fraction of the window left (1 → 0)
	Glow   float64        // 0..1 halo around the orb (loudness)
	Text   string         // Note: the text shown
}

// Render draws f into dst (CanvasW·scale × CanvasH·scale), clearing it first.
func Render(dst *image.RGBA, f Frame, scale float64) {
	draw.Draw(dst, dst.Bounds(), image.Transparent, image.Point{}, draw.Src)
	if f.Appear <= 0 {
		return
	}
	e := easeOutCubic(math.Min(1, f.Appear))
	alpha := math.Min(1, f.Appear*1.6)
	c := &canvas{dst: dst, s: scale, alpha: alpha}

	w := float64(pillW)
	var face font.Face
	var text string
	if f.State == Note {
		face = noteFace(scale)
		text, w = fitNote(face, f.Text, scale)
	}
	x0 := (CanvasW - w) / 2
	y0 := pillTop - (1-e)*(pillTop+pillH+2) // slides down from above the canvas
	// The pill is the same in every state: a translucent pill (follow-up's used to be) turns
	// grey and low-contrast on a light desktop. Follow-up's "only waiting" is in its indicator:
	// a ring around a dimmed orb, and dimmed bars.
	c.fill(func(z *rast) { z.roundRect(x0, y0, w, pillH, pillH/2) }, edgeColor, 1)
	c.fill(func(z *rast) { z.roundRect(x0+0.75, y0+0.75, w-1.5, pillH-1.5, pillH/2-0.75) }, pillColor, 1)

	accent := accentFor(f.State)
	cx, cy := x0+orbX, y0+pillH/2
	if f.State == Note {
		c.fill(func(z *rast) { z.circle(cx, cy, orbR) }, accent, 1)
		c.text(face, text, x0+noteTextX, cy, textCol)
		return
	}
	switch f.State {
	case FollowUp:
		c.fill(func(z *rast) { z.ring(cx, cy, ringR, ringW, 0, 1) }, accent, ringTrackA)
		if f.Ring > 0 {
			c.fill(func(z *rast) { z.ring(cx, cy, ringR, ringW, 0, f.Ring) }, accent, 0.95)
		}
		c.fill(func(z *rast) { z.circle(cx, cy, orbR-2) }, accent, 0.55)
	default:
		if f.Glow > 0.02 {
			c.fill(func(z *rast) { z.circle(cx, cy, orbR+2+3*f.Glow) }, accent, 0.28*f.Glow)
		}
		c.fill(func(z *rast) { z.circle(cx, cy, orbR) }, accent, 1)
	}

	barsA := 1.0
	if f.State == FollowUp {
		barsA = 0.55
	}
	right := x0 + pillW - barsEnd
	left := right - nBars*barW - (nBars-1)*barGap
	for i, b := range f.Bars {
		h := barMinH + (barMaxH-barMinH)*clamp01(b)
		bx := left + float64(i)*(barW+barGap)
		c.fill(func(z *rast) { z.roundRect(bx, cy-h/2, barW, h, barW/2) }, accent, barsA)
	}
}

func accentFor(s State) color.NRGBA {
	switch s {
	case Thinking:
		return thinkCol
	case Speaking:
		return speakCol
	case Error:
		return errorCol
	case Note:
		return noteCol
	}
	return listenCol
}

var (
	fontOnce sync.Once
	fontErr  error
	goFont   *opentype.Font
	facesMu  sync.Mutex
	faces    = map[float64]font.Face{}
)

// noteFace is the Go font at noteSize for a buffer scale (nil if it can't be parsed, which
// leaves the note without text rather than failing the island).
func noteFace(scale float64) font.Face {
	fontOnce.Do(func() { goFont, fontErr = opentype.Parse(goregular.TTF) })
	if fontErr != nil {
		return nil
	}
	facesMu.Lock()
	defer facesMu.Unlock()
	if f, ok := faces[scale]; ok {
		return f
	}
	f, err := opentype.NewFace(goFont, &opentype.FaceOptions{Size: noteSize * scale, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil
	}
	faces[scale] = f
	return f
}

// fitNote cuts text to fit the widest note (with "…") and returns it with the pill's width
// in logical pixels: as wide as the text needs, never narrower than the voice pill.
func fitNote(face font.Face, text string, scale float64) (string, float64) {
	if face == nil {
		return "", pillW
	}
	room := fixed.Int26_6((noteMaxW - noteTextX - notePadR) * scale * 64)
	r, ell := []rune(text), ""
	for len(r) > 0 && font.MeasureString(face, string(r)+ell) > room {
		r, ell = r[:len(r)-1], "…"
	}
	if ell != "" {
		text = string(r) + ell
	}
	tw := float64(font.MeasureString(face, text)) / 64 / scale
	return text, math.Max(pillW, math.Min(noteMaxW, noteTextX+tw+notePadR))
}

// text draws one line with its vertical centre at cy (logical pixels).
func (c *canvas) text(face font.Face, s string, x, cy float64, col color.NRGBA) {
	if face == nil || s == "" {
		return
	}
	col.A = uint8(math.Round(float64(col.A) * clamp01(c.alpha)))
	m := face.Metrics()
	base := cy*c.s + float64(m.Ascent-m.Descent)/64/2
	d := font.Drawer{Dst: c.dst, Src: image.NewUniform(col), Face: face,
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * c.s * 64), Y: fixed.Int26_6(base * 64)}}
	d.DrawString(s)
}

// canvas draws anti-aliased shapes given in logical pixels.
type canvas struct {
	dst   *image.RGBA
	s     float64
	alpha float64 // applied to everything (the slide-in fade)
}

func (c *canvas) fill(shape func(*rast), col color.NRGBA, a float64) {
	b := c.dst.Bounds()
	z := &rast{Rasterizer: vector.NewRasterizer(b.Dx(), b.Dy()), s: c.s}
	shape(z)
	col.A = uint8(math.Round(float64(col.A) * clamp01(a*c.alpha)))
	if col.A == 0 {
		return
	}
	z.Draw(c.dst, b, image.NewUniform(col), image.Point{})
}

// rast is a vector.Rasterizer taking logical coordinates.
type rast struct {
	*vector.Rasterizer
	s float64
}

func (z *rast) p(x, y float64) (float32, float32) { return float32(x * z.s), float32(y * z.s) }

func (z *rast) move(x, y float64) { z.MoveTo(z.p(x, y)) }
func (z *rast) line(x, y float64) { z.LineTo(z.p(x, y)) }
func (z *rast) cube(x1, y1, x2, y2, x, y float64) {
	ax, ay := z.p(x1, y1)
	bx, by := z.p(x2, y2)
	cx, cy := z.p(x, y)
	z.CubeTo(ax, ay, bx, by, cx, cy)
}

// kappa places cubic control points for a quarter circle.
const kappa = 0.5522847498

func (z *rast) roundRect(x, y, w, h, r float64) {
	r = math.Min(r, math.Min(w, h)/2)
	k := r * kappa
	z.move(x+r, y)
	z.line(x+w-r, y)
	z.cube(x+w-r+k, y, x+w, y+r-k, x+w, y+r)
	z.line(x+w, y+h-r)
	z.cube(x+w, y+h-r+k, x+w-r+k, y+h, x+w-r, y+h)
	z.line(x+r, y+h)
	z.cube(x+r-k, y+h, x, y+h-r+k, x, y+h-r)
	z.line(x, y+r)
	z.cube(x, y+r-k, x+r-k, y, x+r, y)
	z.ClosePath()
}

func (z *rast) circle(cx, cy, r float64) { z.roundRect(cx-r, cy-r, 2*r, 2*r, r) }

// ring is the band between radius r-w/2 and r+w/2, clockwise from 12 o'clock over the
// fraction from..to of a full turn.
func (z *rast) ring(cx, cy, r, w, from, to float64) {
	const seg = 64
	ro, ri := r+w/2, r-w/2
	a0, a1 := 2*math.Pi*from-math.Pi/2, 2*math.Pi*to-math.Pi/2
	n := max(2, int(math.Ceil(seg*(to-from))))
	for i := 0; i <= n; i++ {
		a := a0 + (a1-a0)*float64(i)/float64(n)
		x, y := cx+ro*math.Cos(a), cy+ro*math.Sin(a)
		if i == 0 {
			z.move(x, y)
		} else {
			z.line(x, y)
		}
	}
	for i := n; i >= 0; i-- {
		a := a0 + (a1-a0)*float64(i)/float64(n)
		z.line(cx+ri*math.Cos(a), cy+ri*math.Sin(a))
	}
	z.ClosePath()
}

func easeOutCubic(t float64) float64 { u := 1 - t; return 1 - u*u*u }

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }
