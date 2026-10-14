package tray

import "image"

// The Windows backend's pure parts, built and tested on every OS (win_windows.go calls them).

// Win32 menu flags (winuser.h).
const (
	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfSeparator = 0x0800
)

// winMenuFlags is AppendMenuW's flags for it.
func winMenuFlags(it Item) uint32 {
	if it.Separator {
		return mfSeparator
	}
	f := uint32(mfString)
	if !it.Enabled {
		f |= mfGrayed
	}
	if it.Toggle && it.Checked {
		f |= mfChecked
	}
	return f
}

// winTip is the notification icon's tooltip: szTip holds 127 UTF-16 units and a NUL.
func winTip(s string) [128]uint16 {
	var out [128]uint16
	i := 0
	for _, r := range s {
		if r > 0xFFFF {
			r = '?' // no surrogate pairs in a fixed buffer
		}
		if i == len(out)-1 {
			break
		}
		out[i] = uint16(r)
		i++
	}
	return out
}

// winIconBits is img as a top-down 32-bit DIB's pixels: BGRA, straight (not premultiplied)
// alpha, which is what an icon's colour bitmap holds.
func winIconBits(img *image.NRGBA) []byte {
	b := img.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			out = append(out, c.B, c.G, c.R, c.A)
		}
	}
	return out
}
