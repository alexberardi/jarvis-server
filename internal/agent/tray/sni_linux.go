//go:build linux

package tray

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// The icon on Linux is a StatusNotifierItem (the freedesktop/KDE spec waybar, KDE Plasma, GNOME's
// AppIndicator extension, XFCE's and LXQt's panels all show) with its menu over
// com.canonical.dbusmenu. Both are objects this process exports on the session bus; the
// StatusNotifierWatcher (part of the bar or desktop) is told the item's bus name and the host
// draws it. Without a watcher nothing shows: the item stays exported and registers when one
// appears (a bar started after the agent at login, or restarted).

const (
	watcherName  = "org.kde.StatusNotifierWatcher"
	watcherPath  = dbus.ObjectPath("/StatusNotifierWatcher")
	itemIface    = "org.kde.StatusNotifierItem"
	itemPath     = dbus.ObjectPath("/StatusNotifierItem")
	menuIface    = "com.canonical.dbusmenu"
	menuPath     = dbus.ObjectPath("/MenuBar")
	dbusName     = "org.freedesktop.DBus"
	dbusMenuVers = uint32(3)
)

// pixmap is one size of the icon: ARGB32, network byte order (a(iiay)).
type pixmap struct {
	W, H int32
	Data []byte
}

// toolTip is the item's tooltip ((sa(iiay)ss)).
type toolTip struct {
	IconName   string
	IconPixmap []pixmap
	Title      string
	Text       string
}

// layout is a dbusmenu node ((ia{sv}av)): children are variants of layout.
type layout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

type groupProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

type menuEvent struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

// iconSizes are the pixmaps offered; the host picks the nearest.
var iconSizes = []int{16, 22, 24, 32, 48, 64}

func pixmaps() []pixmap {
	var out []pixmap
	for _, n := range iconSizes {
		img := Color(n)
		data := make([]byte, 0, n*n*4)
		for i := 0; i < len(img.Pix); i += 4 {
			p := img.Pix[i : i+4] // R, G, B, A (not premultiplied)
			data = append(data, p[3], p[0], p[1], p[2])
		}
		out = append(out, pixmap{W: int32(n), H: int32(n), Data: data})
	}
	return out
}

// sni is the Linux backend.
type sni struct {
	conn  *dbus.Conn
	name  string
	tray  *Tray
	props *prop.Properties
	stop  chan struct{}
	done  chan struct{}

	mu       sync.Mutex
	items    []Item
	tooltip  string
	rev      uint32
	closed   bool
	register func() error // tells the watcher; replaced in tests only
}

// Start shows the icon on the session bus. ErrUnsupported without a session bus. Without a
// StatusNotifierWatcher it still starts, logs once, and registers when one appears.
func Start(o Options) (*Tray, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w: no D-Bus session bus (%v)", ErrUnsupported, err)
	}
	t, err := startOn(conn, o)
	if err != nil {
		_ = conn.Close()
	}
	return t, err
}

// startOn exports the item on conn (which it then owns) and registers it.
func startOn(conn *dbus.Conn, o Options) (*Tray, error) {
	t := newTray(o.Log, o.Actions, o.State)
	s := &sni{conn: conn, tray: t, stop: make(chan struct{}), done: make(chan struct{}),
		name: fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())}
	s.register = func() error {
		return conn.Object(watcherName, watcherPath).Call(watcherName+".RegisterStatusNotifierItem", 0, s.name).Err
	}
	if err := s.export(); err != nil {
		return nil, err
	}
	reply, err := conn.RequestName(s.name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("bus name %s is taken", s.name)
	}
	// Watch for the watcher before asking whether it's there, so one that starts in between
	// isn't missed.
	sig := make(chan *dbus.Signal, 16)
	conn.Signal(sig)
	if err := conn.AddMatchSignal(dbus.WithMatchSender(dbusName), dbus.WithMatchInterface(dbusName),
		dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, watcherName)); err != nil {
		return nil, err
	}
	go s.watch(sig)
	if err := t.attach(s); err != nil {
		s.close()
		return nil, err
	}
	var has bool
	if err := conn.BusObject().Call(dbusName+".NameHasOwner", 0, watcherName).Store(&has); err == nil && has {
		if err := s.register(); err != nil {
			t.log.Info("tray: the status icon host refused the icon", "err", err)
		}
	} else {
		t.log.Info("tray: no status icon host on this desktop yet; the icon shows when one starts " +
			"(waybar's tray module, KDE, GNOME with the AppIndicator extension)")
	}
	return t, nil
}

// watch re-registers whenever a watcher takes the name (started late, or restarted).
func (s *sni) watch(sig chan *dbus.Signal) {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			s.conn.RemoveSignal(sig)
			return
		case m, ok := <-sig:
			if !ok {
				return
			}
			if m.Name != dbusName+".NameOwnerChanged" || len(m.Body) != 3 {
				continue
			}
			if name, _ := m.Body[0].(string); name != watcherName {
				continue
			}
			if owner, _ := m.Body[2].(string); owner != "" {
				if err := s.register(); err != nil {
					s.tray.log.Info("tray: the status icon host refused the icon", "err", err)
				} else {
					s.tray.log.Info("tray: a status icon host appeared; icon registered")
				}
			}
		}
	}
}

func (s *sni) export() error {
	ti := toolTip{Title: appid.DisplayName}
	p, err := prop.Export(s.conn, itemPath, prop.Map{itemIface: {
		"Category":            {Value: "ApplicationStatus", Emit: prop.EmitFalse},
		"Id":                  {Value: appid.ID, Emit: prop.EmitFalse},
		"Title":               {Value: appid.DisplayName, Emit: prop.EmitFalse},
		"Status":              {Value: "Active", Emit: prop.EmitFalse},
		"WindowId":            {Value: int32(0), Emit: prop.EmitFalse},
		"IconThemePath":       {Value: "", Emit: prop.EmitFalse},
		"IconName":            {Value: "", Emit: prop.EmitFalse},
		"IconPixmap":          {Value: pixmaps(), Emit: prop.EmitFalse},
		"OverlayIconName":     {Value: "", Emit: prop.EmitFalse},
		"OverlayIconPixmap":   {Value: []pixmap{}, Emit: prop.EmitFalse},
		"AttentionIconName":   {Value: "", Emit: prop.EmitFalse},
		"AttentionIconPixmap": {Value: []pixmap{}, Emit: prop.EmitFalse},
		"AttentionMovieName":  {Value: "", Emit: prop.EmitFalse},
		"ToolTip":             {Value: ti, Emit: prop.EmitFalse},
		"ItemIsMenu":          {Value: true, Emit: prop.EmitFalse},
		"Menu":                {Value: menuPath, Emit: prop.EmitFalse},
	}})
	if err != nil {
		return err
	}
	s.props = p
	if err := s.conn.Export(itemMethods{s}, itemPath, itemIface); err != nil {
		return err
	}
	if _, err := prop.Export(s.conn, menuPath, prop.Map{menuIface: {
		"Version":       {Value: dbusMenuVers, Emit: prop.EmitFalse},
		"TextDirection": {Value: "ltr", Emit: prop.EmitFalse},
		"Status":        {Value: "normal", Emit: prop.EmitFalse},
		"IconThemePath": {Value: []string{}, Emit: prop.EmitFalse},
	}}); err != nil {
		return err
	}
	if err := s.conn.Export(menuMethods{s}, menuPath, menuIface); err != nil {
		return err
	}
	for path, xml := range map[dbus.ObjectPath]string{itemPath: itemIntrospect, menuPath: menuIntrospect} {
		if err := s.conn.Export(introspect.Introspectable(xml), path, "org.freedesktop.DBus.Introspectable"); err != nil {
			return err
		}
	}
	return nil
}

func (s *sni) update(items []Item, tooltip string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.items, s.tooltip = slices.Clone(items), tooltip
	s.rev++
	rev := s.rev
	s.mu.Unlock()
	if s.props != nil {
		s.props.SetMust(itemIface, "ToolTip", toolTip{Title: tooltip})
	}
	return errors.Join(
		s.conn.Emit(menuPath, menuIface+".LayoutUpdated", rev, int32(0)),
		s.conn.Emit(itemPath, itemIface+".NewToolTip"),
	)
}

func (s *sni) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop)
	<-s.done
	// Releasing the name is what tells the watcher the item is gone.
	_, _ = s.conn.ReleaseName(s.name)
	_ = s.conn.Close()
}

// node is item it as a dbusmenu node.
func node(it Item) layout {
	p := map[string]dbus.Variant{}
	if it.Separator {
		p["type"] = dbus.MakeVariant("separator")
	} else {
		p["label"] = dbus.MakeVariant(it.Label)
		p["enabled"] = dbus.MakeVariant(it.Enabled)
	}
	if it.Toggle {
		p["toggle-type"] = dbus.MakeVariant("checkmark")
		st := int32(0)
		if it.Checked {
			st = 1
		}
		p["toggle-state"] = dbus.MakeVariant(st)
	}
	return layout{ID: it.ID, Props: p, Children: []dbus.Variant{}}
}

func (s *sni) snapshot() (uint32, []Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev, s.items
}

func (s *sni) root(items []Item) layout {
	l := layout{ID: 0, Props: map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")}}
	for _, it := range items {
		l.Children = append(l.Children, dbus.MakeVariant(node(it)))
	}
	return l
}

// itemMethods are org.kde.StatusNotifierItem's methods. With ItemIsMenu, hosts open the menu on a
// click; the activations do nothing more.
type itemMethods struct{ s *sni }

func (itemMethods) Activate(x, y int32) *dbus.Error          { return nil }
func (itemMethods) SecondaryActivate(x, y int32) *dbus.Error { return nil }
func (itemMethods) ContextMenu(x, y int32) *dbus.Error       { return nil }
func (itemMethods) Scroll(delta int32, orientation string) *dbus.Error {
	return nil
}

// menuMethods are com.canonical.dbusmenu's methods.
type menuMethods struct{ s *sni }

var errNoItem = dbus.NewError("com.canonical.dbusmenu.Error.NoSuchItem", []any{"no such menu item"})

func (m menuMethods) GetLayout(parent, depth int32, names []string) (uint32, layout, *dbus.Error) {
	rev, items := m.s.snapshot()
	if parent == 0 {
		l := m.s.root(items)
		if depth == 0 {
			l.Children = []dbus.Variant{}
		}
		return rev, l, nil
	}
	for _, it := range items {
		if it.ID == parent {
			return rev, node(it), nil
		}
	}
	return rev, layout{}, errNoItem
}

func (m menuMethods) GetGroupProperties(ids []int32, names []string) ([]groupProps, *dbus.Error) {
	_, items := m.s.snapshot()
	var out []groupProps
	for _, it := range items {
		if len(ids) == 0 || slices.Contains(ids, it.ID) {
			out = append(out, groupProps{ID: it.ID, Props: node(it).Props})
		}
	}
	return out, nil
}

func (m menuMethods) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	_, items := m.s.snapshot()
	for _, it := range items {
		if it.ID == id {
			if v, ok := node(it).Props[name]; ok {
				return v, nil
			}
		}
	}
	return dbus.Variant{}, errNoItem
}

func (m menuMethods) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID != "clicked" {
		return nil
	}
	_, items := m.s.snapshot()
	for _, it := range items {
		if it.ID == id && it.Enabled && !it.Separator {
			m.s.tray.clicked(id)
			return nil
		}
	}
	return errNoItem
}

func (m menuMethods) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	var bad []int32
	for _, e := range events {
		if err := m.Event(e.ID, e.EventID, e.Data, e.Timestamp); err != nil {
			bad = append(bad, e.ID)
		}
	}
	return bad, nil
}

func (menuMethods) AboutToShow(id int32) (bool, *dbus.Error) { return false, nil }

func (menuMethods) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}

// Probe says, for `status`, whether this desktop shows status icons. It only reads the bus.
func Probe() string {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "off: no D-Bus session bus"
	}
	defer conn.Close()
	return probeOn(conn)
}

func probeOn(conn *dbus.Conn) string {
	var has bool
	if err := conn.BusObject().Call(dbusName+".NameHasOwner", 0, watcherName).Store(&has); err != nil || !has {
		return "off: no status icon host on this desktop (a StatusNotifierItem tray: waybar's tray module, " +
			"KDE, or GNOME with the AppIndicator extension); the agent runs without an icon"
	}
	v, err := conn.Object(watcherName, watcherPath).GetProperty(watcherName + ".IsStatusNotifierHostRegistered")
	if host, ok := v.Value().(bool); err == nil && ok && !host {
		return "off: a StatusNotifierWatcher runs but no tray shows its icons"
	}
	return "on (a StatusNotifierItem tray is running)"
}

const introspectHead = introspect.IntrospectDeclarationString + "<node>" +
	introspect.IntrospectDataString + prop.IntrospectDataString

var itemIntrospect = introspectHead + `
 <interface name="org.kde.StatusNotifierItem">
  <method name="Activate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
  <method name="SecondaryActivate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
  <method name="ContextMenu"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
  <method name="Scroll"><arg name="delta" type="i" direction="in"/><arg name="orientation" type="s" direction="in"/></method>
  <signal name="NewTitle"/><signal name="NewIcon"/><signal name="NewAttentionIcon"/><signal name="NewOverlayIcon"/>
  <signal name="NewToolTip"/><signal name="NewStatus"><arg name="status" type="s"/></signal>
  <property name="Category" type="s" access="read"/>
  <property name="Id" type="s" access="read"/>
  <property name="Title" type="s" access="read"/>
  <property name="Status" type="s" access="read"/>
  <property name="WindowId" type="i" access="read"/>
  <property name="IconThemePath" type="s" access="read"/>
  <property name="IconName" type="s" access="read"/>
  <property name="IconPixmap" type="a(iiay)" access="read"/>
  <property name="OverlayIconName" type="s" access="read"/>
  <property name="OverlayIconPixmap" type="a(iiay)" access="read"/>
  <property name="AttentionIconName" type="s" access="read"/>
  <property name="AttentionIconPixmap" type="a(iiay)" access="read"/>
  <property name="AttentionMovieName" type="s" access="read"/>
  <property name="ToolTip" type="(sa(iiay)ss)" access="read"/>
  <property name="ItemIsMenu" type="b" access="read"/>
  <property name="Menu" type="o" access="read"/>
 </interface>
</node>`

var menuIntrospect = introspectHead + `
 <interface name="com.canonical.dbusmenu">
  <method name="GetLayout"><arg type="i" name="parentId" direction="in"/><arg type="i" name="recursionDepth" direction="in"/><arg type="as" name="propertyNames" direction="in"/><arg type="u" name="revision" direction="out"/><arg type="(ia{sv}av)" name="layout" direction="out"/></method>
  <method name="GetGroupProperties"><arg type="ai" name="ids" direction="in"/><arg type="as" name="propertyNames" direction="in"/><arg type="a(ia{sv})" name="properties" direction="out"/></method>
  <method name="GetProperty"><arg type="i" name="id" direction="in"/><arg type="s" name="name" direction="in"/><arg type="v" name="value" direction="out"/></method>
  <method name="Event"><arg type="i" name="id" direction="in"/><arg type="s" name="eventId" direction="in"/><arg type="v" name="data" direction="in"/><arg type="u" name="timestamp" direction="in"/></method>
  <method name="EventGroup"><arg type="a(isvu)" name="events" direction="in"/><arg type="ai" name="idErrors" direction="out"/></method>
  <method name="AboutToShow"><arg type="i" name="id" direction="in"/><arg type="b" name="needUpdate" direction="out"/></method>
  <method name="AboutToShowGroup"><arg type="ai" name="ids" direction="in"/><arg type="ai" name="updatesNeeded" direction="out"/><arg type="ai" name="idErrors" direction="out"/></method>
  <signal name="ItemsPropertiesUpdated"><arg type="a(ia{sv})" name="updatedProps"/><arg type="a(ias)" name="removedProps"/></signal>
  <signal name="LayoutUpdated"><arg type="u" name="revision"/><arg type="i" name="parent"/></signal>
  <signal name="ItemActivationRequested"><arg type="i" name="id"/><arg type="u" name="timestamp"/></signal>
  <property name="Version" type="u" access="read"/>
  <property name="TextDirection" type="s" access="read"/>
  <property name="Status" type="s" access="read"/>
  <property name="IconThemePath" type="as" access="read"/>
 </interface>
</node>`
