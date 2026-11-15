package atspi

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

const (
	ifEventObject = "org.a11y.atspi.Event.Object"
	ifTable       = "org.a11y.atspi.Table"
	registryPath  = "/org/a11y/atspi/registry"
	// landedLabel is the longest row or column label kept with a table cell.
	landedLabel = 40
	// landedLabelCells is how many cells of a row are read for its label.
	landedLabelCells = 3
)

// landedEvents are the events that say where a click landed: the element that took the
// keyboard focus, or the cell (list row, tree item) a table made its active one.
var landedEvents = []string{"object:state-changed:focused", "object:active-descendant-changed"}

// Landings is what took the keyboard focus in each app, from the accessibility bus's events:
// what a click landed on where nothing says where the pointer is (sway). A click that moves no
// focus (a toolbar button) lands nowhere.
type Landings struct {
	c *dbus.Conn

	mu   sync.Mutex
	last map[string]landing // by the app's bus name
	pids map[string]int
	wake chan struct{} // closed at each event
}

type landing struct {
	ref objRef
	at  time.Time
}

// Listen starts listening on its own connection to the accessibility bus; Close ends it.
func Listen(ctx context.Context) (*Landings, error) {
	addr, err := address(ctx)
	if err != nil {
		return nil, err
	}
	c, err := dbus.Connect(addr)
	if err != nil {
		return nil, &Error{Code: CodeUnsupported, Msg: "connect to the accessibility bus: " + err.Error()}
	}
	l := &Landings{c: c, last: map[string]landing{}, pids: map[string]int{}, wake: make(chan struct{})}
	sigs := make(chan *dbus.Signal, 64)
	c.Signal(sigs)
	if err := c.AddMatchSignal(dbus.WithMatchInterface(ifEventObject)); err != nil {
		c.Close()
		return nil, callErr("AddMatch", err)
	}
	// Apps send only the events someone registered for. The app argument came with at-spi 2.50.
	reg := c.Object(registryName, registryPath)
	for _, ev := range landedEvents {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		if reg.CallWithContext(cctx, "org.a11y.atspi.Registry.RegisterEvent", 0, ev, []string{}, "").Err != nil {
			_ = reg.CallWithContext(cctx, "org.a11y.atspi.Registry.RegisterEvent", 0, ev, []string{}).Err
		}
		cancel()
	}
	go func() {
		for s := range sigs { // closed with the connection
			l.note(s, time.Now())
		}
	}()
	return l, nil
}

func (l *Landings) Close() { l.c.Close() }

// note keeps a signal's element as its app's latest landing.
func (l *Landings) note(s *dbus.Signal, at time.Time) {
	ref := objRef{Name: s.Sender, Path: s.Path}
	switch {
	case strings.HasSuffix(s.Name, ".StateChanged"):
		// (detail, on, _, …): only gaining the focus.
		if len(s.Body) < 2 || s.Body[0] != "focused" || s.Body[1] != int32(1) {
			return
		}
	case strings.HasSuffix(s.Name, ".ActiveDescendantChanged"):
		// (_, _, _, the child, …)
		if len(s.Body) < 4 || dbus.Store([]any{s.Body[3]}, &ref) != nil || ref.null() {
			return
		}
	default:
		return
	}
	l.mu.Lock()
	l.last[s.Sender] = landing{ref, at}
	close(l.wake)
	l.wake = make(chan struct{})
	l.mu.Unlock()
}

// After is the element that took the focus in the app with this pid at or after since, waited
// for up to wait; nil when none did.
func (l *Landings) After(ctx context.Context, pid int, since time.Time, wait time.Duration) *computeruse.Element {
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		l.mu.Lock()
		ref, ok := l.find(ctx, pid, since)
		wake := l.wake
		l.mu.Unlock()
		if ok {
			return landedElement(ctx, obj{l.c, ref})
		}
		select {
		case <-wake:
		case <-t.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func (l *Landings) find(ctx context.Context, pid int, since time.Time) (objRef, bool) {
	for name, ln := range l.last {
		if ln.at.Before(since) {
			continue
		}
		p, ok := l.pids[name]
		if !ok {
			var u uint32
			cctx, cancel := context.WithTimeout(ctx, callTimeout)
			err := l.c.BusObject().CallWithContext(cctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, name).Store(&u)
			cancel()
			if err != nil {
				continue
			}
			p = int(u)
			l.pids[name] = p
		}
		if p == pid {
			return ln.ref, true
		}
	}
	return objRef{}, false
}

// landedElement describes o; a table's cell with a row or column label is named by them
// ("October row, Amount column"), which is how a person would say which cell they meant. Its
// address (B3) is left out: a run for November would be told "B3" and "November" at once.
func landedElement(ctx context.Context, o obj) *computeruse.Element {
	r, err := o.role(ctx)
	if err != nil {
		return nil
	}
	el := &computeruse.Element{Role: roleNames[r], Name: o.name(ctx), Secure: r == rolePasswordText}
	if where := cellLabels(ctx, o); where != "" {
		el.Name = where
	}
	return el
}

// cellLabels are a table cell's row and column labels: the first text in its row to its left,
// and the text at the top of its column. "" for anything that isn't in a table.
func cellLabels(ctx context.Context, o obj) string {
	var parent objRef
	if v, err := o.prop(ctx, "Parent"); err != nil || dbus.Store([]any{v}, &parent) != nil || parent.null() {
		return ""
	}
	t := obj{o.c, parent}
	var idx, row, col int32
	if o.call(ctx, ifAccessible+".GetIndexInParent").Store(&idx) != nil ||
		t.call(ctx, ifTable+".GetRowAtIndex", idx).Store(&row) != nil ||
		t.call(ctx, ifTable+".GetColumnAtIndex", idx).Store(&col) != nil || row < 0 || col < 0 {
		return ""
	}
	cell := func(r, c int32) string {
		var ref objRef
		if t.call(ctx, ifTable+".GetAccessibleAt", r, c).Store(&ref) != nil || ref.null() {
			return ""
		}
		s := []rune(strings.Join(strings.Fields(obj{o.c, ref}.text(ctx)), " "))
		if len(s) > landedLabel {
			s = s[:landedLabel]
		}
		return string(s)
	}
	var parts []string
	for c := int32(0); c < min(col, landedLabelCells); c++ {
		if s := cell(row, c); s != "" {
			parts = append(parts, s+" row")
			break
		}
	}
	if row > 0 {
		if s := cell(0, col); s != "" {
			parts = append(parts, s+" column")
		}
	}
	return strings.Join(parts, ", ")
}
