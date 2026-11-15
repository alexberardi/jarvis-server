package atspi

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/vptr"
)

// TestLiveLanded clicks a cell of a spreadsheet whose window isn't the focused one with a real pointer in the isolated headless
// desktop of `scripts/appclick-live landed` and expects Landings to name the cell and its row and column.
func TestLiveLanded(t *testing.T) {
	if os.Getenv("JARVIS_LANDED_LIVE") == "" {
		t.Skip("set by scripts/appclick-live landed, inside its isolated session only")
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if !strings.HasPrefix(rt, os.TempDir()) || os.Getenv("JARVIS_APPCLICK_ISOLATED") != rt {
		t.Fatalf("not the isolated session (XDG_RUNTIME_DIR=%q): refusing to touch a real desktop", rt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// The sheet's window, from sway: its process and where it is on the screen.
	out, err := exec.CommandContext(ctx, "swaymsg", "-t", "get_tree", "-r").Output()
	if err != nil {
		t.Fatal(err)
	}
	type node struct {
		PID   int
		Name  string
		Rect  struct{ X, Y int }
		Nodes []node
	}
	var root, win node
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	var walk func(n node)
	walk = func(n node) {
		if n.PID > 0 && strings.Contains(n.Name, "budget") {
			win = n
		}
		for _, k := range n.Nodes {
			walk(k)
		}
	}
	walk(root)
	if win.PID == 0 {
		t.Fatalf("no budget window in sway's tree")
	}

	// The sheet isn't the focused window (a document opened after it is): the click focuses
	// its window on the way to the cell, as a click into another app's window does.
	c, err := bus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	top, err := findApp(ctx, c, win.PID)
	if err != nil {
		t.Fatal(err)
	}
	var table *obj
	var find func(o obj, depth int)
	find = func(o obj, depth int) {
		if table != nil || depth > maxDepth {
			return
		}
		if r, _ := o.role(ctx); r == 55 { // ATSPI_ROLE_TABLE
			table = &o
			return
		}
		kids, _ := o.children(ctx)
		for _, k := range kids {
			find(k, depth+1)
		}
	}
	find(top, 0)
	if table == nil {
		t.Fatal("no table in the sheet's window")
	}
	var cell objRef
	if err := table.call(ctx, ifTable+".GetAccessibleAt", int32(2), int32(1)).Store(&cell); err != nil {
		t.Fatal(err)
	}
	var e struct{ X, Y, W, H int32 }
	if err := (obj{table.c, cell}).call(ctx, ifComponent+".GetExtents", coordWindow).Store(&e); err != nil || e.W <= 0 {
		t.Fatalf("B3's extents: %+v, %v", e, err)
	}

	l, err := Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := &vptr.Pointer{}
	defer p.Close()
	since := time.Now()
	x, y := win.Rect.X+int(e.X+e.W/2), win.Rect.Y+int(e.Y+e.H/2)
	if err := p.Send(ctx, vptr.Motion(x, y), vptr.Press(vptr.BtnLeft), vptr.Release(vptr.BtnLeft)); err != nil {
		t.Fatal(err)
	}
	el := l.After(ctx, win.PID, since, 5*time.Second)
	if el == nil {
		t.Fatal("the click landed nowhere")
	}
	t.Logf("landed: %+v", *el)
	if want := "October row, Amount column"; el.Name != want {
		t.Errorf("name = %q, want %q", el.Name, want)
	}
}
