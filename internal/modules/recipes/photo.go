package recipes

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	_ "image/png"
	"math"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Photo preparation for the photo import (#19, from_image._resize_for_vision): decode, apply the
// EXIF orientation (phones record rotation as metadata; the re-encoded JPEG carries none, so it
// must be baked in or OCR sees the page sideways), flatten to RGB, shrink to the vision pixel
// budget (never upscale) and re-encode as JPEG q90. HEIC does not decode in pure Go and is
// refused as "Unrecognized image file" (the app always sends JPEG).

// maxVisionPixels is 1280·28·28 (the Hugging Face guidance legacy followed).
const maxVisionPixels = 1280 * 28 * 28

// visionSize is the target size: unchanged within the budget, else scaled to fit with each side
// rounded to a multiple of 28 (at least 28; Python's round, half to even).
func visionSize(w, h int) (int, int) {
	if w*h <= maxVisionPixels {
		return w, h
	}
	scale := math.Sqrt(float64(maxVisionPixels) / float64(w*h))
	round28 := func(x int) int { return max(28, int(math.RoundToEven(float64(x)/28))*28) }
	return round28(int(float64(w) * scale)), round28(int(float64(h) * scale))
}

// exifOrientation reads the orientation tag (0x0112) from a JPEG's APP1 Exif segment; 1 when
// there is none.
func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // start of scan / end: no more metadata
			return 1
		}
		size := int(binary.BigEndian.Uint16(data[i+2:]))
		if size < 2 || i+2+size > len(data) {
			return 1
		}
		seg := data[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) >= 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			o := int(bo.Uint16(t[e+8:]))
			if o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation (PIL ImageOps.exif_transpose) to an RGBA image.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 CW
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 90 CCW
				dx, dy = y, w-1-x
			}
			dst.SetRGBA(dx, dy, src.RGBAAt(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// preparePhoto is _resize_for_vision. ok is false when the bytes are not a decodable image.
func preparePhoto(data []byte) ([]byte, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	b := img.Bounds()
	rgb := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	// RGB: transparent areas flatten onto black, as PIL's convert("RGB") leaves them.
	draw.Draw(rgb, rgb.Bounds(), &image.Uniform{C: color.Black}, image.Point{}, draw.Src)
	draw.Draw(rgb, rgb.Bounds(), img, b.Min, draw.Over)
	out := orient(rgb, exifOrientation(data))
	w, h := out.Bounds().Dx(), out.Bounds().Dy()
	if nw, nh := visionSize(w, h); nw != w || nh != h {
		scaled := image.NewRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), out, out.Bounds(), xdraw.Src, nil)
		out = scaled
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 90}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}
