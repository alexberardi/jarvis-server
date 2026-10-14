// Package qrterm draws a QR code in a terminal with Unicode half blocks, two module rows per
// text line (install plan item 1.2: `jarvisd setup-link --qr`, so a headless server's setup
// link can be opened from a phone).
//
// The encoding is rsc.io/qr/coding (pure Go, BSD, no dependencies, by the Go authors). Its
// top-level qr.Encode always uses mask 0; Encode here scores all eight masks with the
// standard's penalty rules and keeps the best, as an encoder should, which keeps long runs and
// finder-like patterns out of the data area.
package qrterm

import (
	"errors"
	"io"
	"strings"

	"rsc.io/qr/coding"
)

// Level is the error correction level. M (15%) suits a screen: a URL of ~90 bytes fits in a
// version 5 or 6 code (37 or 41 modules).
type Level = coding.Level

const (
	L = coding.L
	M = coding.M
	Q = coding.Q
	H = coding.H
)

// Code is an encoded QR symbol.
type Code struct {
	Size int
	dark func(x, y int) bool
}

// Dark reports whether module (x, y) is dark; outside the symbol it is light.
func (c *Code) Dark(x, y int) bool {
	return x >= 0 && y >= 0 && x < c.Size && y < c.Size && c.dark(x, y)
}

// Encode encodes text in the smallest version that holds it at level, with the mask of the
// lowest penalty.
func Encode(text string, level Level) (*Code, error) {
	var enc coding.Encoding
	switch {
	case coding.Num(text).Check() == nil:
		enc = coding.Num(text)
	case coding.Alpha(text).Check() == nil:
		enc = coding.Alpha(text)
	default:
		enc = coding.String(text)
	}
	v := coding.Version(coding.MinVersion)
	for ; enc.Bits(v) > v.DataBytes(level)*8; v++ {
		if v == coding.MaxVersion {
			return nil, errors.New("qrterm: text too long for a QR code")
		}
	}
	var best *coding.Code
	bestScore := -1
	for mask := range coding.Mask(8) {
		p, err := coding.NewPlan(v, level, mask)
		if err != nil {
			return nil, err
		}
		c, err := p.Encode(enc)
		if err != nil {
			return nil, err
		}
		if s := penalty(c.Size, c.Black); bestScore < 0 || s < bestScore {
			best, bestScore = c, s
		}
	}
	return &Code{Size: best.Size, dark: best.Black}, nil
}

// penalty is ISO/IEC 18004 §7.8.3's mask score: runs of five or more (N1 = 3), 2x2 blocks
// (N2 = 3), finder-like 1:1:3:1:1 patterns with four light modules on a side (N3 = 40), and
// the dark proportion's distance from 50% (N4 = 10).
func penalty(n int, dark func(x, y int) bool) int {
	score := 0
	at := func(x, y int, rows bool) bool {
		if rows {
			return dark(x, y)
		}
		return dark(y, x)
	}
	for _, rows := range []bool{true, false} {
		for y := range n {
			run := 1
			for x := 1; x <= n; x++ {
				if x < n && at(x, y, rows) == at(x-1, y, rows) {
					run++
					continue
				}
				if run >= 5 {
					score += 3 + run - 5
				}
				run = 1
			}
			for x := 0; x+11 <= n; x++ {
				if finderLike(func(i int) bool { return at(x+i, y, rows) }) {
					score += 40
				}
			}
		}
	}
	darkCount := 0
	for y := range n {
		for x := range n {
			d := dark(x, y)
			if d {
				darkCount++
			}
			if x+1 < n && y+1 < n && d == dark(x+1, y) && d == dark(x, y+1) && d == dark(x+1, y+1) {
				score += 3
			}
		}
	}
	pct := darkCount * 100 / (n * n)
	dev := pct - 50
	if dev < 0 {
		dev = -dev
	}
	return score + dev/5*10
}

// finderLike matches 1011101 with four light modules before or after, over 11 modules.
func finderLike(at func(i int) bool) bool {
	core := [7]bool{true, false, true, true, true, false, true}
	match := func(off int) bool {
		for i, d := range core {
			if at(off+i) != d {
				return false
			}
		}
		return true
	}
	light := func(from int) bool {
		for i := range 4 {
			if at(from + i) {
				return false
			}
		}
		return true
	}
	return (match(4) && light(0)) || (match(0) && light(7))
}

// Style is how dark and light modules are drawn.
type Style int

const (
	// DarkOnLight draws dark modules as ink: right on a light background, and what a plain
	// text file or a pasted log shows.
	DarkOnLight Style = iota
	// LightOnDark draws light modules as ink, for a terminal with a dark background and no
	// colour control.
	LightOnDark
	// ANSI sets black on white with escape codes, so it scans on any terminal theme.
	ANSI
)

// Quiet is the light border, in modules, around the symbol. The standard asks for 4.
const Quiet = 4

const (
	upper = "▀"
	lower = "▄"
	full  = "█"
)

// Render writes the code with a quiet zone, two module rows per line.
func Render(w io.Writer, c *Code, style Style) error {
	var b strings.Builder
	ink := func(x, y int) bool {
		d := c.Dark(x, y)
		if style == LightOnDark {
			return !d
		}
		return d
	}
	for y := -Quiet; y < c.Size+Quiet; y += 2 {
		if style == ANSI {
			b.WriteString("\x1b[30;47m")
		}
		for x := -Quiet; x < c.Size+Quiet; x++ {
			top, bottom := ink(x, y), y+1 < c.Size+Quiet && ink(x, y+1)
			switch {
			case top && bottom:
				b.WriteString(full)
			case top:
				b.WriteString(upper)
			case bottom:
				b.WriteString(lower)
			default:
				b.WriteByte(' ')
			}
		}
		if style == ANSI {
			b.WriteString("\x1b[0m")
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Write encodes text at level M and renders it.
func Write(w io.Writer, text string, style Style) error {
	c, err := Encode(text, M)
	if err != nil {
		return err
	}
	return Render(w, c, style)
}
