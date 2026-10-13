package qrterm

import (
	"bytes"
	"flag"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// A setup link as `jarvisd setup-link` prints it (a documentation address, a fixed token).
const link = "http://192.0.2.10:7710/setup#token=W-GBbk_XhhFTq2_pRBHB0-BLtDEVwBJf41BG-D8MDgY"

// parse turns rendered half blocks back into a bitmap of ink (true) per module.
func parse(t *testing.T, out string) [][]bool {
	t.Helper()
	var rows [][]bool
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		line = strings.TrimSuffix(strings.TrimPrefix(line, "\x1b[30;47m"), "\x1b[0m")
		var top, bottom []bool
		for _, r := range line {
			switch string(r) {
			case full:
				top, bottom = append(top, true), append(bottom, true)
			case upper:
				top, bottom = append(top, true), append(bottom, false)
			case lower:
				top, bottom = append(top, false), append(bottom, true)
			case " ":
				top, bottom = append(top, false), append(bottom, false)
			default:
				t.Fatalf("unexpected rune %q", r)
			}
		}
		rows = append(rows, top, bottom)
	}
	return rows
}

// decode scans the bitmap like a phone would: dark = ink unless inverted, 4 px per module.
func decode(t *testing.T, mods [][]bool, inverted bool) string {
	t.Helper()
	// The last text line's lower half lies past the quiet zone (the symbol plus border is an
	// odd number of modules); the terminal shows it as background, so drop it.
	mods = mods[:len(mods[0])]
	const px = 4
	img := image.NewGray(image.Rect(0, 0, len(mods[0])*px, len(mods)*px))
	for y, row := range mods {
		for x, ink := range row {
			c := color.Gray{Y: 255}
			if ink != inverted {
				c = color.Gray{Y: 0}
			}
			for dy := range px {
				for dx := range px {
					img.SetGray(x*px+dx, y*px+dy, c)
				}
			}
		}
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_PURE_BARCODE: true})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return res.GetText()
}

func TestGoldenDecodesToTheLink(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, link, DarkOnLight); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "setup-link.txt")
	if *update {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Fatalf("rendering changed (go test -update to accept):\n%s", out.String())
	}
	if got := decode(t, parse(t, string(want)), false); got != link {
		t.Fatalf("golden decodes to %q", got)
	}
}

func TestEveryStyleDecodes(t *testing.T) {
	for _, tc := range []struct {
		style    Style
		inverted bool
	}{{DarkOnLight, false}, {LightOnDark, true}, {ANSI, false}} {
		var out bytes.Buffer
		if err := Write(&out, link, tc.style); err != nil {
			t.Fatal(err)
		}
		if tc.style == ANSI && !strings.HasPrefix(out.String(), "\x1b[30;47m") {
			t.Fatalf("ANSI without colours: %q", out.String()[:20])
		}
		if got := decode(t, parse(t, out.String()), tc.inverted); got != link {
			t.Fatalf("style %d decodes to %q", tc.style, got)
		}
	}
}

// Lengths across versions, including ones with version information (7+), still round-trip.
func TestLengthsRoundTrip(t *testing.T) {
	for _, n := range []int{1, 17, 60, 90, 150, 300} {
		text := strings.Repeat("ab#", n)[:n]
		var out bytes.Buffer
		if err := Write(&out, text, DarkOnLight); err != nil {
			t.Fatal(err)
		}
		if got := decode(t, parse(t, out.String()), false); got != text {
			t.Fatalf("%d bytes decode to %q", n, got)
		}
	}
	if _, err := Encode(strings.Repeat("x", 5000), M); err == nil {
		t.Fatal("5000 bytes should not fit")
	}
}

// The mask is the lowest-penalty one, not always mask 0, and the symbol keeps its quiet zone.
func TestQuietZoneAndMask(t *testing.T) {
	c, err := Encode(link, M)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Render(&out, c, DarkOnLight); err != nil {
		t.Fatal(err)
	}
	mods := parse(t, out.String())
	if len(mods[0]) != c.Size+2*Quiet {
		t.Fatalf("width %d, size %d", len(mods[0]), c.Size)
	}
	for _, row := range mods[:Quiet] {
		for _, ink := range row {
			if ink {
				t.Fatal("ink in the quiet zone")
			}
		}
	}
	if s := penalty(c.Size, c.Dark); s <= 0 {
		t.Fatalf("penalty %d", s)
	}
}
