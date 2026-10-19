//go:build linux

package tray

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// These run the real StatusNotifierItem and dbusmenu objects on a private dbus-daemon (never the
// session bus of whoever runs the tests), with this test playing the StatusNotifierWatcher and
// the host that draws the icon and its menu: the calls waybar, KDE and the AppIndicator
// extension make.

const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// privateBus starts a dbus-daemon and returns its address.
func privateBus(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jt") // a short path: the socket goes in it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(cfg, fmt.Appendf(nil, busConfig, dir), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("dbus-daemon", "--config-file="+cfg, "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("no dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line)
}

func dial(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	c, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// watcher is a minimal StatusNotifierWatcher: it records registrations.
type watcher struct{ got chan string }

func (w watcher) RegisterStatusNotifierItem(service string) *dbus.Error {
	w.got <- service
	return nil
}

func (w watcher) RegisterStatusNotifierHost(service string) *dbus.Error { return nil }

// startWatcher exports a watcher on c and takes its name.
func startWatcher(t *testing.T, c *dbus.Conn, host bool) watcher {
	t.Helper()
	w := watcher{got: make(chan string, 4)}
	if err := c.Export(w, watcherPath, watcherName); err != nil {
		t.Fatal(err)
	}
	if _, err := prop.Export(c, watcherPath, prop.Map{watcherName: {
		"IsStatusNotifierHostRegistered": {Value: host},
		"RegisteredStatusNotifierItems":  {Value: []string{}},
		"ProtocolVersion":                {Value: int32(0)},
	}}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.RequestName(watcherName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("watcher name: %v %v", r, err)
	}
	return w
}

// within receives from ch, failing after a hang guard (not a sleep: it returns at once on a value).
func within[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// menuLabels reads the menu as a host does: GetLayout from the root, every level.
func menuLabels(t *testing.T, menu dbus.BusObject) (labels []string, voiceState int32) {
	t.Helper()
	var rev uint32
	var root layout
	if err := menu.Call(menuIface+".GetLayout", 0, int32(0), int32(-1), []string{}).Store(&rev, &root); err != nil {
		t.Fatal(err)
	}
	voiceState = -1
	for _, v := range root.Children {
		var n layout
		if err := dbus.Store([]any{v.Value()}, &n); err != nil {
			t.Fatal(err)
		}
		if typ, ok := n.Props["type"]; ok && typ.Value() == "separator" {
			labels = append(labels, "--")
			continue
		}
		label := n.Props["label"].Value().(string)
		if !n.Props["enabled"].Value().(bool) {
			label = "(" + label + ")"
		}
		if n.ID == IDVoice {
			if n.Props["toggle-type"].Value() != "checkmark" {
				t.Fatalf("voice item isn't a checkmark: %v", n.Props)
			}
			voiceState = n.Props["toggle-state"].Value().(int32)
		}
		labels = append(labels, label)
	}
	return labels, voiceState
}

func TestStatusNotifierItemOnAPrivateBus(t *testing.T) {
	addr := privateBus(t)
	agent, host := dial(t, addr), dial(t, addr)

	voice := make(chan bool, 4)
	quit := make(chan struct{}, 1)
	opened := make(chan string, 1)
	stopped := make(chan struct{}, 1)
	tr, err := startOn(agent, Options{
		State: State{Status: "Running", VoiceAvailable: true, OpenURL: "http://jarvis.example:7710/"},
		Actions: Actions{
			SetVoice: func(on bool) { voice <- on },
			Open:     func(u string) { opened <- u },
			Quit:     func() { quit <- struct{}{} },
			StopTask: func() { stopped <- struct{}{} },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	// No watcher yet (a bar starting after the agent): the item registers when one appears.
	w := startWatcher(t, host, true)
	service := within(t, w.got, "the item to register with a watcher that started late")
	if !strings.HasPrefix(service, "org.kde.StatusNotifierItem-") {
		t.Fatalf("registered %q", service)
	}

	item := host.Object(service, itemPath)
	for name, want := range map[string]any{"Id": appid.ID, "Title": appid.DisplayName, "Status": "Active",
		"Category": "ApplicationStatus", "ItemIsMenu": true, "Menu": menuPath} {
		v, err := item.GetProperty(itemIface + "." + name)
		if err != nil || v.Value() != want {
			t.Fatalf("%s = %v (%v), want %v", name, v.Value(), err, want)
		}
	}
	v, err := item.GetProperty(itemIface + ".IconPixmap")
	if err != nil {
		t.Fatal(err)
	}
	var pix []pixmap
	if err := dbus.Store([]any{v.Value()}, &pix); err != nil {
		t.Fatal(err)
	}
	if len(pix) != len(iconSizes) || pix[0].W != 16 || len(pix[0].Data) != 16*16*4 {
		t.Fatalf("pixmaps: %d, first %dx%d %d bytes", len(pix), pix[0].W, pix[0].H, len(pix[0].Data))
	}
	var xml string
	if err := item.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml); err != nil ||
		!strings.Contains(xml, `interface name="org.kde.StatusNotifierItem"`) {
		t.Fatalf("introspection: %v %.200s", err, xml)
	}

	menu := host.Object(service, menuPath)
	if v, err := menu.GetProperty(menuIface + ".Version"); err != nil || v.Value() != uint32(3) {
		t.Fatalf("dbusmenu Version = %v (%v)", v, err)
	}
	labels, state := menuLabels(t, menu)
	want := []string{"(Jarvis Automation Agent — Running)", "--", `Listen for "Hey Jarvis"`, "Open Jarvis Automation", "--", "Quit Jarvis Automation Agent"}
	if strings.Join(labels, "|") != strings.Join(want, "|") || state != 0 {
		t.Fatalf("menu %q voice %d", labels, state)
	}

	// Clicking the toggle asks for voice on; the agent then shows it checked, and the host hears
	// LayoutUpdated.
	sig := make(chan *dbus.Signal, 8)
	host.Signal(sig)
	if err := host.AddMatchSignal(dbus.WithMatchObjectPath(menuPath), dbus.WithMatchInterface(menuIface), dbus.WithMatchMember("LayoutUpdated")); err != nil {
		t.Fatal(err)
	}
	if err := menu.Call(menuIface+".Event", 0, IDVoice, "clicked", dbus.MakeVariant(""), uint32(0)).Err; err != nil {
		t.Fatal(err)
	}
	if !within(t, voice, "SetVoice") {
		t.Fatal("SetVoice(false) for an unchecked toggle")
	}
	tr.Update(func(s *State) { s.Voice = true })
	for s := within(t, sig, "LayoutUpdated"); s.Name != menuIface+".LayoutUpdated"; s = within(t, sig, "LayoutUpdated") {
	}
	if _, state := menuLabels(t, menu); state != 1 {
		t.Fatalf("voice toggle-state %d after turning it on", state)
	}
	if err := menu.Call(menuIface+".Event", 0, IDOpen, "clicked", dbus.MakeVariant(""), uint32(0)).Err; err != nil {
		t.Fatal(err)
	}
	if u := within(t, opened, "Open"); u != "http://jarvis.example:7710/" {
		t.Fatalf("opened %q", u)
	}
	// The header is disabled: a click on it is refused, and does nothing.
	if err := menu.Call(menuIface+".Event", 0, IDHeader, "clicked", dbus.MakeVariant(""), uint32(0)).Err; err == nil {
		t.Fatal("a click on the disabled header was accepted")
	}
	if err := menu.Call(menuIface+".Event", 0, IDQuit, "clicked", dbus.MakeVariant(""), uint32(0)).Err; err != nil {
		t.Fatal(err)
	}
	within(t, quit, "Quit")

	// Jarvis takes control: the icon gets its badge (NewIcon), the menu its Stop item.
	if err := host.AddMatchSignal(dbus.WithMatchObjectPath(itemPath), dbus.WithMatchInterface(itemIface), dbus.WithMatchMember("NewIcon")); err != nil {
		t.Fatal(err)
	}
	tr.Update(func(s *State) { s.Activity = ActivityControlling })
	for s := within(t, sig, "NewIcon"); s.Name != itemIface+".NewIcon"; s = within(t, sig, "NewIcon") {
	}
	v, err = item.GetProperty(itemIface + ".IconPixmap")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbus.Store([]any{v.Value()}, &pix); err != nil {
		t.Fatal(err)
	}
	if want := pixmapsFor(true); len(pix) != len(want) || !bytes.Equal(pix[0].Data, want[0].Data) {
		t.Fatal("the icon has no badge while Jarvis controls the computer")
	}
	if labels, _ := menuLabels(t, menu); !slices.Contains(labels, StopTaskLabel) || !slices.Contains(labels, "(Jarvis is controlling this computer)") {
		t.Fatalf("menu while controlling %q", labels)
	}
	if err := menu.Call(menuIface+".Event", 0, IDStopTask, "clicked", dbus.MakeVariant(""), uint32(0)).Err; err != nil {
		t.Fatal(err)
	}
	within(t, stopped, "StopTask")

	// The bar restarts: the item registers again with the new watcher.
	if _, err := host.ReleaseName(watcherName); err != nil {
		t.Fatal(err)
	}
	host2 := dial(t, addr)
	w2 := startWatcher(t, host2, true)
	if got := within(t, w2.got, "re-registration with a restarted watcher"); got != service {
		t.Fatalf("re-registered %q, want %q", got, service)
	}
	if got := probeOn(host2); !strings.HasPrefix(got, "on") {
		t.Fatalf("probe with a host: %q", got)
	}

	// Close drops the bus name: that is how hosts learn the icon is gone.
	tr.Close()
	var has bool
	if err := host2.BusObject().Call(dbusName+".NameHasOwner", 0, service).Store(&has); err != nil || has {
		t.Fatalf("item name still owned after Close (%v)", err)
	}
	tr.Update(func(s *State) { s.Voice = false }) // after Close: harmless
}

func TestProbeWithoutAHost(t *testing.T) {
	addr := privateBus(t)
	c := dial(t, addr)
	if got := probeOn(c); !strings.HasPrefix(got, "off: no status icon host") {
		t.Fatalf("no watcher: %q", got)
	}
	startWatcher(t, c, false)
	if got := probeOn(c); !strings.HasPrefix(got, "off: a StatusNotifierWatcher runs but no tray") {
		t.Fatalf("watcher, no host: %q", got)
	}
}
