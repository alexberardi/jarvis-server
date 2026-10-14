package main

import (
	"bytes"
	"image"
	"image/color"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

var grey = color.NRGBA{40, 40, 40, 255}

func TestDiffBoundsFindsTheChangedBox(t *testing.T) {
	a := solid(100, 50, grey)
	b := solid(100, 50, grey)
	for y := 10; y < 20; y++ {
		for x := 30; x < 45; x++ {
			b.SetNRGBA(x, y, color.NRGBA{200, 10, 10, 255})
		}
	}
	got, ok := diffBounds(a, b, image.Rectangle{}, 24)
	if !ok || got != image.Rect(30, 10, 45, 20) {
		t.Fatalf("got %v %v, want (30,10)-(45,20)", got, ok)
	}
	// A region that excludes the change finds nothing.
	if _, ok := diffBounds(a, b, image.Rect(0, 30, 100, 50), 24); ok {
		t.Fatal("found a change outside the region")
	}
	// A region cutting through the change clips the box.
	got, ok = diffBounds(a, b, image.Rect(0, 15, 100, 50), 24)
	if !ok || got != image.Rect(30, 15, 45, 20) {
		t.Fatalf("clipped: got %v %v", got, ok)
	}
}

func TestDiffBoundsIgnoresChangesUnderTheThreshold(t *testing.T) {
	a := solid(10, 10, grey)
	b := solid(10, 10, grey)
	b.SetNRGBA(5, 5, color.NRGBA{50, 40, 40, 255}) // 10 off in red
	if _, ok := diffBounds(a, b, image.Rectangle{}, 24); ok {
		t.Fatal("a change of 10 counted at threshold 24")
	}
	if _, ok := diffBounds(a, b, image.Rectangle{}, 5); !ok {
		t.Fatal("a change of 10 missed at threshold 5")
	}
}

func TestCropScaledPadsClampsAndEnlarges(t *testing.T) {
	img := solid(20, 20, grey)
	img.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	out := cropScaled(img, image.Rect(0, 0, 2, 2), 3, 4)
	// pad 3 is clamped at the top-left edge: (0,0)-(5,5), times 4.
	if out.Bounds() != image.Rect(0, 0, 20, 20) {
		t.Fatalf("bounds %v", out.Bounds())
	}
	if c := out.NRGBAAt(3, 3); c.R != 255 {
		t.Fatalf("the red pixel should fill the first 4x4 block, got %v", c)
	}
	if c := out.NRGBAAt(4, 4); c != grey {
		t.Fatalf("(4,4) should be the next source pixel, got %v", c)
	}
	if b := cropScaled(img, image.Rect(5, 5, 7, 7), 0, 0).Bounds(); b != image.Rect(0, 0, 2, 2) {
		t.Fatalf("scale 0 should mean 1, got %v", b)
	}
}

func TestParseRegion(t *testing.T) {
	r, err := parseRegion("10, 20,30,40")
	if err != nil || r != image.Rect(10, 20, 40, 60) {
		t.Fatalf("got %v %v", r, err)
	}
	if r, err := parseRegion(""); err != nil || !r.Empty() {
		t.Fatalf("empty: %v %v", r, err)
	}
	for _, bad := range []string{"1,2,3", "a,b,c,d", "1,2,0,4", "-1,0,5,5"} {
		if _, err := parseRegion(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPNGRoundTripAndDiffCommand(t *testing.T) {
	dir := t.TempDir()
	a, b := solid(40, 40, grey), solid(40, 40, grey)
	b.SetNRGBA(20, 20, color.NRGBA{255, 255, 255, 255})
	for y := 20; y < 26; y++ {
		for x := 20; x < 26; x++ {
			b.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}
	pa, pb, pc := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png"), filepath.Join(dir, "c.png")
	if err := writePNG(pa, a); err != nil {
		t.Fatal(err)
	}
	if err := writePNG(pb, b); err != nil {
		t.Fatal(err)
	}
	if err := diffCmd([]string{"--base", pa, "--img", pb, "--pad", "2", "--scale", "2", "--out", pc}); err != nil {
		t.Fatal(err)
	}
	c, err := readPNG(pc)
	if err != nil {
		t.Fatal(err)
	}
	if c.Bounds().Dx() != (6+4)*2 {
		t.Fatalf("crop width %d, want 20", c.Bounds().Dx())
	}
	if err := diffCmd([]string{"--base", pa, "--img", pa}); err != errNothing {
		t.Fatalf("identical images: %v, want errNothing", err)
	}
	if err := diffCmd([]string{"--base", pa, "--img", pb, "--min", "10"}); err != errNothing {
		t.Fatalf("a 6px change under --min 10: %v, want errNothing", err)
	}
}

func item(id int32, props map[string]dbus.Variant, kids ...layout) dbus.Variant {
	l := layout{ID: id, Props: props}
	for _, k := range kids {
		l.Children = append(l.Children, dbus.MakeVariant(k))
	}
	return dbus.MakeVariant(l)
}

func TestWriteMenuDrawsWhatATrayWould(t *testing.T) {
	v := dbus.MakeVariant
	root := layout{Children: []dbus.Variant{
		item(1, map[string]dbus.Variant{"label": v("Jarvis: Running"), "enabled": v(false)}),
		item(2, map[string]dbus.Variant{"type": v("separator")}),
		item(3, map[string]dbus.Variant{"label": v("_Listen for \"Hey Jarvis\""), "toggle-type": v("checkmark"), "toggle-state": v(int32(1))}),
		item(4, map[string]dbus.Variant{"label": v("Hidden"), "visible": v(false)}),
		item(5, map[string]dbus.Variant{"label": v("More"), "children-display": v("submenu")},
			layout{ID: 6, Props: map[string]dbus.Variant{"label": v("Quit"), "toggle-type": v("checkmark"), "toggle-state": v(int32(0))}}),
	}}
	var buf bytes.Buffer
	writeMenu(&buf, root, 0)
	want := strings.Join([]string{
		"Jarvis: Running  (disabled)",
		"--------",
		`[x] Listen for "Hey Jarvis"`,
		"More",
		"  [ ] Quit",
		"",
	}, "\n")
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestLargestPixmapDecodesARGB(t *testing.T) {
	small := pixmap{W: 1, H: 1, Data: []byte{255, 1, 2, 3}}
	big := pixmap{W: 2, H: 1, Data: []byte{128, 10, 20, 30, 255, 40, 50, 60}}
	broken := pixmap{W: 9, H: 9, Data: []byte{1, 2}}
	img, ok := largestPixmap([]pixmap{small, broken, big})
	if !ok || img.Bounds().Dx() != 2 {
		t.Fatalf("got %v %v", img, ok)
	}
	if c := img.NRGBAAt(0, 0); c != (color.NRGBA{10, 20, 30, 128}) {
		t.Fatalf("pixel 0: %v", c)
	}
	if _, ok := largestPixmap([]pixmap{broken}); ok {
		t.Fatal("a pixmap whose data doesn't match its size was used")
	}
}

func TestWriteDumpAndSplitAddress(t *testing.T) {
	if n, p := splitAddress(":1.5/org/ayatana/Item"); n != ":1.5" || p != "/org/ayatana/Item" {
		t.Fatalf("got %s %s", n, p)
	}
	if n, p := splitAddress("org.kde.StatusNotifierItem-1-1"); n != "org.kde.StatusNotifierItem-1-1" || p != "/StatusNotifierItem" {
		t.Fatalf("got %s %s", n, p)
	}
	var buf bytes.Buffer
	writeDump(&buf, nil)
	if !strings.Contains(buf.String(), "no StatusNotifierItems") {
		t.Fatal(buf.String())
	}
	buf.Reset()
	writeDump(&buf, []itemDump{{Address: "x/y", Props: map[string]dbus.Variant{
		"Title":      dbus.MakeVariant("Jarvis"),
		"IconPixmap": dbus.MakeVariant([]pixmap{{W: 16, H: 16}, {W: 32, H: 32}}),
	}}})
	for _, s := range []string{"item x/y", "IconPixmap: [16x16 32x32]", "Title: Jarvis"} {
		if !strings.Contains(buf.String(), s) {
			t.Errorf("missing %q in\n%s", s, buf.String())
		}
	}
}
