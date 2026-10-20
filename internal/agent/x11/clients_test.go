package x11

import (
	"context"
	"testing"
)

// Clients is the windows on this desktop top first, with their geometry, process, class and
// title; the active one is marked and a minimised one or another desktop's is left out.
func TestClients(t *testing.T) {
	f := ewmhFake()
	f.setProp32(root, "_NET_CLIENT_LIST_STACKING", 0x400003, 0x400001, 0x400002, 0x400004) // bottom to top
	f.setProp32(root, "_NET_ACTIVE_WINDOW", 0x400001)
	f.setProp32(0x400001, "_NET_WM_DESKTOP", 0)
	f.setProp32(0x400001, "_NET_WM_PID", 4242)
	f.setStr(0x400003, "WM_CLASS", "notes\x00Notes\x00")
	f.setStr(0x400003, "WM_NAME", "Jarvis Test Notes")
	f.setProp32(0x400003, "_NET_WM_PID", 77)
	f.setProp32(0x400004, "_NET_WM_DESKTOP", 3) // another desktop
	f.geoms = map[uint32][4]int{
		0x400001: {100, 100, 400, 300},
		0x400002: {0, 0, 1000, 1000}, // minimised
		0x400003: {0, 0, 800, 600},
		0x400004: {0, 0, 1000, 1000},
	}
	d, _ := display(f)
	cs, err := d.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Client{
		{Owner: Owner{ID: 0x400001, PID: 4242, Class: "Gedit"}, Title: "notes — ✓", X: 100, Y: 100, W: 400, H: 300, Active: true},
		{Owner: Owner{ID: 0x400003, PID: 77, Class: "Notes"}, Title: "Jarvis Test Notes", W: 800, H: 600},
	}
	if len(cs) != len(want) {
		t.Fatalf("clients = %+v", cs)
	}
	for i := range want {
		if cs[i] != want[i] {
			t.Errorf("client %d = %+v, want %+v", i, cs[i], want[i])
		}
	}
}

// A bare X server (Xvfb, a kiosk) has no window manager: the root's mapped children, in the
// server's stacking order, stand in for its client list.
func TestClientsWithoutAWindowManager(t *testing.T) {
	f := newFake()
	f.children = map[uint32][]uint32{root: {0x600001, 0x600002, 0x600003}} // bottom to top
	f.unmapped = map[uint32]bool{0x600003: true}
	f.setStr(0x600001, "WM_CLASS", "notes\x00Notes\x00")
	f.setStr(0x600001, "WM_NAME", "Jarvis Test Notes")
	f.setProp32(0x600001, "_NET_WM_PID", 77)
	f.setStr(0x600002, "WM_CLASS", "xterm\x00XTerm\x00")
	f.geoms = map[uint32][4]int{
		0x600001: {0, 0, 800, 600},
		0x600002: {10, 10, 100, 100},
		0x600003: {0, 0, 1000, 1000},
	}
	d, _ := display(f)
	cs, err := d.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].ID != 0x600002 || cs[1].ID != 0x600001 || cs[1].PID != 77 || cs[1].Class != "Notes" ||
		cs[1].Title != "Jarvis Test Notes" || cs[1].W != 800 || cs[0].Active || cs[1].Active {
		t.Fatalf("clients = %+v", cs)
	}
}
