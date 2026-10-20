package computer

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"

	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// A marked keyframe (Wayland): a red ring around the click (or the cursor) on the capture, the
// point in the sent JPEG's pixels, and the cropSide px around it at full resolution, so a
// vision model sees what was clicked even where the whole screen is scaled down.
const (
	markRadius = 18 // capture pixels
	markWidth  = 4
	cropSide   = 256
)

var markColor = color.RGBA{R: 0xff, G: 0x20, B: 0x20, A: 0xff}

// markFrame encodes img, a capture whose place on the desktop is geo, with pt (global layout
// coordinates; nil: none) marked when it is on the capture.
func markFrame(img image.Image, geo computeruse.Geometry, pt *Point) (Frame, error) {
	b := img.Bounds()
	px, py, on := capturePoint(b, geo, pt)
	if !on {
		data, _, _, err := screen.Encode(img)
		return Frame{JPEG: data}, err
	}
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	ring(rgba, px, py)
	data, w, h, err := screen.Encode(rgba)
	if err != nil {
		return Frame{}, err
	}
	f := Frame{JPEG: data, Point: &KeyframePoint{
		X: int(math.Round(float64(px) * float64(w) / float64(b.Dx()))),
		Y: int(math.Round(float64(py) * float64(h) / float64(b.Dy())))}}
	x0 := min(max(px-cropSide/2, 0), max(b.Dx()-cropSide, 0))
	y0 := min(max(py-cropSide/2, 0), max(b.Dy()-cropSide, 0))
	var buf bytes.Buffer
	crop := rgba.SubImage(image.Rect(x0, y0, min(x0+cropSide, b.Dx()), min(y0+cropSide, b.Dy())))
	if err := jpeg.Encode(&buf, crop, &jpeg.Options{Quality: 85}); err == nil {
		f.Crop = buf.Bytes()
	}
	return f, nil
}

// capturePoint is global point pt in the capture's pixels, and whether it is on it.
func capturePoint(b image.Rectangle, geo computeruse.Geometry, pt *Point) (int, int, bool) {
	if pt == nil || b.Dx() <= 0 || b.Dy() <= 0 {
		return 0, 0, false
	}
	sx, sy := geo.Scale, geo.Scale
	if geo.LogicalW > 0 && geo.LogicalH > 0 {
		sx, sy = float64(b.Dx())/float64(geo.LogicalW), float64(b.Dy())/float64(geo.LogicalH)
	}
	if sx <= 0 || sy <= 0 {
		return 0, 0, false
	}
	x := int(math.Floor((pt.X - float64(geo.OriginX)) * sx))
	y := int(math.Floor((pt.Y - float64(geo.OriginY)) * sy))
	return x, y, x >= 0 && y >= 0 && x < b.Dx() && y < b.Dy()
}

// ring draws the mark around (cx, cy).
func ring(img *image.RGBA, cx, cy int) {
	outer, inner := markRadius+markWidth/2, markRadius-markWidth/2
	for y := cy - outer; y <= cy+outer; y++ {
		for x := cx - outer; x <= cx+outer; x++ {
			d2 := (x-cx)*(x-cx) + (y-cy)*(y-cy)
			if d2 <= outer*outer && d2 >= inner*inner && image.Pt(x, y).In(img.Rect) {
				img.SetRGBA(x, y, markColor)
			}
		}
	}
}
