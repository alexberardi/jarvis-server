// Command desktopshot is scripts/desktop-shot's image and D-Bus helper. It runs inside the
// disposable headless session (the D-Bus part on that session's private bus only).
//
//	desktopshot diff --base A.png --img B.png [--region x,y,w,h] [--threshold 24]
//	                 [--pad 12] [--scale 1] [--out C.png] [--min 4]
//	    prints the bounding box of what changed from A to B ("x y w h"), and crops it out of B
//	    into C. Exit 3 when nothing changed (or the box is smaller than --min on a side).
//	desktopshot menu [--icon icon.png]
//	    prints every StatusNotifierItem on the session bus: its properties and its dbusmenu, as
//	    a tray host reads them; --icon saves the largest IconPixmap.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// errNothing is diff's "no change" (exit 3), distinct from a failure (exit 1).
var errNothing = errors.New("no change")

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: desktopshot diff|menu ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "diff":
		err = diffCmd(os.Args[2:])
	case "menu":
		err = menuCmd(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	switch {
	case errors.Is(err, errNothing):
		os.Exit(3)
	case err != nil:
		fmt.Fprintln(os.Stderr, "desktopshot:", err)
		os.Exit(1)
	}
}

func diffCmd(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	base := fs.String("base", "", "the reference PNG")
	img := fs.String("img", "", "the PNG to compare")
	region := fs.String("region", "", "only look inside x,y,w,h")
	threshold := fs.Int("threshold", 24, "per-channel difference (0-255) that counts as changed")
	pad := fs.Int("pad", 12, "grow the crop by this many pixels")
	scale := fs.Int("scale", 1, "enlarge the crop this many times (nearest neighbour)")
	out := fs.String("out", "", "write the crop here")
	minSide := fs.Int("min", 4, "a box narrower or shorter than this counts as no change")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *base == "" || *img == "" {
		return errors.New("diff: --base and --img are required")
	}
	r, err := parseRegion(*region)
	if err != nil {
		return err
	}
	a, err := readPNG(*base)
	if err != nil {
		return err
	}
	b, err := readPNG(*img)
	if err != nil {
		return err
	}
	box, ok := diffBounds(a, b, r, *threshold)
	if !ok || box.Dx() < *minSide || box.Dy() < *minSide {
		return errNothing
	}
	fmt.Printf("%d %d %d %d\n", box.Min.X, box.Min.Y, box.Dx(), box.Dy())
	if *out != "" {
		return writePNG(*out, cropScaled(b, box, *pad, *scale))
	}
	return nil
}

// parseRegion reads "x,y,w,h"; "" is the whole image (an empty rectangle).
func parseRegion(s string) (image.Rectangle, error) {
	if s == "" {
		return image.Rectangle{}, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("--region %q: want x,y,w,h", s)
	}
	var n [4]int
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 0 {
			return image.Rectangle{}, fmt.Errorf("--region %q: want x,y,w,h as non-negative integers", s)
		}
		n[i] = v
	}
	if n[2] == 0 || n[3] == 0 {
		return image.Rectangle{}, fmt.Errorf("--region %q: width and height must be > 0", s)
	}
	return image.Rect(n[0], n[1], n[0]+n[2], n[1]+n[3]), nil
}

func menuCmd(args []string) error {
	fs := flag.NewFlagSet("menu", flag.ContinueOnError)
	icon := fs.String("icon", "", "save the first item's largest IconPixmap here")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The session bus of the sandbox (dbus-run-session's), never the user's: the script runs
	// this under env -i with only that bus's address.
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	items, err := dumpItems(conn)
	if err != nil {
		return err
	}
	writeDump(os.Stdout, items)
	if *icon != "" && len(items) > 0 {
		if img, ok := largestPixmap(items[0].Pixmaps); ok {
			return writePNG(*icon, img)
		}
	}
	return nil
}
