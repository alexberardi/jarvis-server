package x11

import (
	"context"
	"slices"
)

// Client is one window on the screen for desktop tasks: its app, title and rectangle in root
// coordinates, and whether it is the active one.
type Client struct {
	Owner
	Title      string
	X, Y, W, H int
	Active     bool
}

// Clients lists the windows on the screen, top first: the window manager's stacking order (its
// client list without one) on the current desktop, minimised windows left out. A bare X server
// (Xvfb, a kiosk) has no window manager: the root's mapped children, in the server's stacking
// order, stand in for its clients, and none is active.
func (d *Display) Clients(context.Context) ([]Client, error) {
	c, err := d.open()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if ok, _ := wmCheck(c); !ok {
		return bareClients(c)
	}
	ids, err := prop32(c, c.Root(), "_NET_CLIENT_LIST_STACKING")
	if err == nil && len(ids) == 0 {
		ids, err = prop32(c, c.Root(), "_NET_CLIENT_LIST")
	}
	if err != nil {
		return nil, err
	}
	hidden, err := c.Atom("_NET_WM_STATE_HIDDEN")
	if err != nil {
		return nil, err
	}
	cur, _ := prop32(c, c.Root(), "_NET_CURRENT_DESKTOP")
	var active uint32
	if v, _ := prop32(c, c.Root(), "_NET_ACTIVE_WINDOW"); len(v) > 0 {
		active = v[0]
	}
	var out []Client
	for _, id := range slices.Backward(ids) { // bottom to top
		if st, _ := prop32(c, id, "_NET_WM_STATE"); slices.Contains(st, hidden) {
			continue
		}
		if desk, _ := prop32(c, id, "_NET_WM_DESKTOP"); len(cur) == 1 && len(desk) == 1 && desk[0] != cur[0] && desk[0] != 0xffffffff {
			continue
		}
		if cl, ok := client(c, id); ok {
			cl.Active = id == active
			out = append(out, cl)
		}
	}
	return out, nil
}

func bareClients(c Conn) ([]Client, error) {
	ids, err := c.Children(c.Root())
	if err != nil {
		return nil, err
	}
	var out []Client
	for _, id := range slices.Backward(ids) {
		if ok, err := c.Viewable(id); err != nil || !ok {
			continue
		}
		if cl, ok := client(c, id); ok {
			out = append(out, cl)
		}
	}
	return out, nil
}

// client is window id with its owner, title and geometry; false when it has none (gone).
func client(c Conn, id uint32) (Client, bool) {
	x, y, w, h, err := c.Geometry(id)
	if err != nil {
		return Client{}, false
	}
	title, _ := propString(c, id, "_NET_WM_NAME")
	if title == "" {
		title, _ = propString(c, id, "WM_NAME")
	}
	return Client{Owner: owner(c, id), Title: title, X: x, Y: y, W: w, H: h}, true
}
