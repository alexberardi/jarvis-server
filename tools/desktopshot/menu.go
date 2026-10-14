package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

// What a StatusNotifierItem host reads, read the same way (org.kde.StatusNotifierItem and
// com.canonical.dbusmenu), so menu.txt shows the menu a tray would draw.
const (
	watcherName = "org.kde.StatusNotifierWatcher"
	watcherPath = dbus.ObjectPath("/StatusNotifierWatcher")
	itemIface   = "org.kde.StatusNotifierItem"
	menuIface   = "com.canonical.dbusmenu"
)

// layout is a dbusmenu node ((ia{sv}av)).
type layout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

// pixmap is one icon size: ARGB32 in network byte order.
type pixmap struct {
	W, H int32
	Data []byte
}

// itemDump is one registered item.
type itemDump struct {
	Address string // "bus-name/object/path" as registered
	Props   map[string]dbus.Variant
	Pixmaps []pixmap
	Menu    *layout
	Err     error
}

// dumpItems reads every item the session's StatusNotifierWatcher knows.
func dumpItems(conn *dbus.Conn) ([]itemDump, error) {
	v, err := conn.Object(watcherName, watcherPath).GetProperty(watcherName + ".RegisteredStatusNotifierItems")
	if err != nil {
		return nil, fmt.Errorf("no StatusNotifierWatcher on this bus: %w", err)
	}
	addrs, _ := v.Value().([]string)
	var out []itemDump
	for _, a := range addrs {
		out = append(out, dumpItem(conn, a))
	}
	return out, nil
}

func dumpItem(conn *dbus.Conn, addr string) itemDump {
	d := itemDump{Address: addr, Props: map[string]dbus.Variant{}}
	name, path := splitAddress(addr)
	obj := conn.Object(name, path)
	if err := obj.Call("org.freedesktop.DBus.Properties.GetAll", 0, itemIface).Store(&d.Props); err != nil {
		d.Err = err
		return d
	}
	if v, ok := d.Props["IconPixmap"]; ok {
		_ = dbus.Store([]any{v.Value()}, &d.Pixmaps)
	}
	if v, ok := d.Props["Menu"]; ok {
		if mp, ok := v.Value().(dbus.ObjectPath); ok && mp != "" && mp != "/" {
			var rev uint32
			var l layout
			if err := conn.Object(name, mp).Call(menuIface+".GetLayout", 0, int32(0), int32(-1), []string{}).Store(&rev, &l); err != nil {
				d.Err = err
			} else {
				d.Menu = &l
			}
		}
	}
	return d
}

// splitAddress splits a registered item ("name/path", or just a name: the default path).
func splitAddress(a string) (string, dbus.ObjectPath) {
	if i := strings.Index(a, "/"); i > 0 {
		return a[:i], dbus.ObjectPath(a[i:])
	}
	return a, "/StatusNotifierItem"
}

// writeDump prints items as text: the item's properties (pixmaps as sizes), then its menu.
func writeDump(w io.Writer, items []itemDump) {
	if len(items) == 0 {
		fmt.Fprintln(w, "no StatusNotifierItems registered")
		return
	}
	for _, it := range items {
		fmt.Fprintf(w, "item %s\n", it.Address)
		keys := make([]string, 0, len(it.Props))
		for k := range it.Props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch k {
			case "IconPixmap", "AttentionIconPixmap", "OverlayIconPixmap":
				var px []pixmap
				_ = dbus.Store([]any{it.Props[k].Value()}, &px)
				var sizes []string
				for _, p := range px {
					sizes = append(sizes, fmt.Sprintf("%dx%d", p.W, p.H))
				}
				fmt.Fprintf(w, "  %s: [%s]\n", k, strings.Join(sizes, " "))
			default:
				fmt.Fprintf(w, "  %s: %v\n", k, it.Props[k].Value())
			}
		}
		if it.Err != nil {
			fmt.Fprintf(w, "  error: %v\n", it.Err)
		}
		if it.Menu != nil {
			fmt.Fprintln(w, "  menu:")
			writeMenu(w, *it.Menu, 2)
		}
	}
}

// writeMenu prints a dbusmenu tree the way a tray draws it: separators as a rule, checkmarks as
// [x]/[ ], disabled entries marked, hidden ones skipped.
func writeMenu(w io.Writer, l layout, depth int) {
	for _, cv := range l.Children {
		var c layout
		if err := dbus.Store([]any{cv.Value()}, &c); err != nil {
			fmt.Fprintf(w, "%s(unreadable entry: %v)\n", strings.Repeat("  ", depth), err)
			continue
		}
		if v, ok := c.Props["visible"]; ok && v.Value() == false {
			continue
		}
		ind := strings.Repeat("  ", depth)
		if t, _ := c.Props["type"].Value().(string); t == "separator" {
			fmt.Fprintf(w, "%s--------\n", ind)
			continue
		}
		label, _ := c.Props["label"].Value().(string)
		label = strings.ReplaceAll(label, "_", "") // mnemonics
		mark := ""
		if tt, _ := c.Props["toggle-type"].Value().(string); tt != "" {
			st, _ := c.Props["toggle-state"].Value().(int32)
			mark = "[ ] "
			if st == 1 {
				mark = "[x] "
			}
		}
		suffix := ""
		if v, ok := c.Props["enabled"]; ok && v.Value() == false {
			suffix = "  (disabled)"
		}
		fmt.Fprintf(w, "%s%s%s%s\n", ind, mark, label, suffix)
		if len(c.Children) > 0 {
			writeMenu(w, c, depth+1)
		}
	}
}

// largestPixmap is the biggest icon size offered, as an image (ARGB32, network byte order).
func largestPixmap(px []pixmap) (*image.NRGBA, bool) {
	best := -1
	for i, p := range px {
		if int(p.W)*int(p.H)*4 != len(p.Data) || p.W <= 0 {
			continue
		}
		if best < 0 || p.W*p.H > px[best].W*px[best].H {
			best = i
		}
	}
	if best < 0 {
		return nil, false
	}
	p := px[best]
	img := image.NewNRGBA(image.Rect(0, 0, int(p.W), int(p.H)))
	for i := 0; i < int(p.W*p.H); i++ {
		argb := binary.BigEndian.Uint32(p.Data[i*4:])
		img.SetNRGBA(i%int(p.W), i/int(p.W), color.NRGBA{
			A: uint8(argb >> 24), R: uint8(argb >> 16), G: uint8(argb >> 8), B: uint8(argb)})
	}
	return img, true
}
