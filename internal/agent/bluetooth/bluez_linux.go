//go:build linux

package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	bluezName   = "org.bluez"
	ifAdapter   = "org.bluez.Adapter1"
	ifDevice    = "org.bluez.Device1"
	ifAgent     = "org.bluez.Agent1"
	ifAgentMgr  = "org.bluez.AgentManager1"
	ifProps     = "org.freedesktop.DBus.Properties"
	agentPath   = dbus.ObjectPath("/net/jarvisautomation/agent/bluetooth")
	agentCaps   = "NoInputNoOutput" // N:'s; with the adapter's audio Class iOS takes Just Works
	callTimeout = 10 * time.Second
)

// BlueZ is Bus over D-Bus (bluetoothd on the system bus; the node's user is in group
// bluetooth, which Debian's bluetooth.conf lets talk to it).
type BlueZ struct {
	conn   *dbus.Conn
	log    *slog.Logger
	paired chan string

	mu       sync.Mutex
	exported bool
}

// Open connects to BlueZ on the system bus. Close it with the returned func.
func Open(log *slog.Logger) (Bus, func(), error) {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, nil, err
	}
	b, err := newBlueZ(c, log)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	return b, func() { _ = c.Close() }, nil
}

func newBlueZ(c *dbus.Conn, log *slog.Logger) (*BlueZ, error) {
	if log == nil {
		log = slog.Default()
	}
	b := &BlueZ{conn: c, log: log, paired: make(chan string, 8)}
	if err := c.AddMatchSignal(dbus.WithMatchInterface(ifProps), dbus.WithMatchMember("PropertiesChanged"),
		dbus.WithMatchArg(0, ifDevice)); err != nil {
		return nil, err
	}
	sig := make(chan *dbus.Signal, 16)
	c.Signal(sig)
	go b.signals(sig)
	return b, nil
}

// signals turns a Device1 Paired: true into an address on Paired.
func (b *BlueZ) signals(sig chan *dbus.Signal) {
	for s := range sig {
		if s.Name != ifProps+".PropertiesChanged" || len(s.Body) < 2 {
			continue
		}
		if iface, _ := s.Body[0].(string); iface != ifDevice {
			continue
		}
		changed, _ := s.Body[1].(map[string]dbus.Variant)
		if v, ok := changed["Paired"]; !ok || v.Value() != true {
			continue
		}
		mac := macFromPath(s.Path)
		if mac == "" {
			continue
		}
		select {
		case b.paired <- mac:
		default: // nobody is reading; the reconnect loop trusts nothing, so dropping is safe
		}
	}
}

// macFromPath is the address in a device path (/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF).
func macFromPath(p dbus.ObjectPath) string {
	base := path.Base(string(p))
	if !strings.HasPrefix(base, "dev_") {
		return ""
	}
	return normMAC(strings.ReplaceAll(strings.TrimPrefix(base, "dev_"), "_", ":"))
}

func (b *BlueZ) Paired() <-chan string { return b.paired }

type objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func (b *BlueZ) objects(ctx context.Context) (objects, error) {
	var objs objects
	err := b.call(ctx, "/", "org.freedesktop.DBus.ObjectManager.GetManagedObjects").Store(&objs)
	return objs, mapErr(err)
}

func (b *BlueZ) call(ctx context.Context, p dbus.ObjectPath, method string, args ...any) *dbus.Call {
	ctx, cancel := withDefault(ctx, callTimeout)
	defer cancel()
	return b.conn.Object(bluezName, p).CallWithContext(ctx, method, 0, args...)
}

// withDefault bounds ctx by d unless it already has a deadline.
func withDefault(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// adapter is the first adapter's path (hci0 on a Pi).
func (objs objects) adapter() (dbus.ObjectPath, map[string]dbus.Variant, bool) {
	var paths []string
	for p, ifs := range objs {
		if _, ok := ifs[ifAdapter]; ok {
			paths = append(paths, string(p))
		}
	}
	if len(paths) == 0 {
		return "", nil, false
	}
	sort.Strings(paths)
	p := dbus.ObjectPath(paths[0])
	return p, objs[p][ifAdapter], true
}

func (objs objects) device(mac string) (dbus.ObjectPath, bool) {
	for p, ifs := range objs {
		if d, ok := ifs[ifDevice]; ok && strings.EqualFold(str(d["Address"]), mac) {
			return p, true
		}
	}
	return "", false
}

func str(v dbus.Variant) string   { s, _ := v.Value().(string); return s }
func boolean(v dbus.Variant) bool { x, _ := v.Value().(bool); return x }

func (b *BlueZ) adapterPath(ctx context.Context) (dbus.ObjectPath, error) {
	objs, err := b.objects(ctx)
	if err != nil {
		return "", err
	}
	p, _, ok := objs.adapter()
	if !ok {
		return "", ErrNoAdapter
	}
	return p, nil
}

func (b *BlueZ) devicePath(ctx context.Context, mac string) (dbus.ObjectPath, error) {
	objs, err := b.objects(ctx)
	if err != nil {
		return "", err
	}
	p, ok := objs.device(mac)
	if !ok {
		return "", ErrUnknownDevice
	}
	return p, nil
}

func (b *BlueZ) Adapter(ctx context.Context) (Adapter, error) {
	objs, err := b.objects(ctx)
	if err != nil {
		return Adapter{}, err
	}
	_, props, ok := objs.adapter()
	if !ok {
		return Adapter{}, ErrNoAdapter
	}
	return Adapter{Powered: boolean(props["Powered"])}, nil
}

func (b *BlueZ) SetAdapter(ctx context.Context, prop string, v any) error {
	p, err := b.adapterPath(ctx)
	if err != nil {
		return err
	}
	return mapErr(b.call(ctx, p, ifProps+".Set", ifAdapter, prop, dbus.MakeVariant(v)).Err)
}

func (b *BlueZ) Devices(ctx context.Context) ([]Device, error) {
	objs, err := b.objects(ctx)
	if err != nil {
		return nil, err
	}
	var out []Device
	for _, ifs := range objs {
		d, ok := ifs[ifDevice]
		if !ok {
			continue
		}
		uuids, _ := d["UUIDs"].Value().([]string)
		out = append(out, Device{Address: strings.ToUpper(str(d["Address"])), Name: str(d["Name"]), Alias: str(d["Alias"]),
			Icon: str(d["Icon"]), UUIDs: uuids, Paired: boolean(d["Paired"]), Connected: boolean(d["Connected"]), Trusted: boolean(d["Trusted"])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

func (b *BlueZ) Discover(ctx context.Context, transport string) (func(), error) {
	p, err := b.adapterPath(ctx)
	if err != nil {
		return nil, err
	}
	filter := map[string]dbus.Variant{"Transport": dbus.MakeVariant(transport)}
	if err := b.call(ctx, p, ifAdapter+".SetDiscoveryFilter", filter).Err; err != nil {
		return nil, mapErr(err)
	}
	if err := b.call(ctx, p, ifAdapter+".StartDiscovery").Err; err != nil {
		return nil, mapErr(err)
	}
	return func() {
		if err := b.call(context.Background(), p, ifAdapter+".StopDiscovery").Err; err != nil {
			b.log.Debug("bluetooth: StopDiscovery", "err", err)
		}
	}, nil
}

func (b *BlueZ) Remove(ctx context.Context, mac string) error {
	objs, err := b.objects(ctx)
	if err != nil {
		return err
	}
	a, _, ok := objs.adapter()
	if !ok {
		return ErrNoAdapter
	}
	d, ok := objs.device(mac)
	if !ok {
		return ErrUnknownDevice
	}
	return mapErr(b.call(ctx, a, ifAdapter+".RemoveDevice", d).Err)
}

func (b *BlueZ) deviceCall(ctx context.Context, mac, method string) error {
	p, err := b.devicePath(ctx, mac)
	if err != nil {
		return err
	}
	return mapErr(b.call(ctx, p, ifDevice+"."+method).Err)
}

func (b *BlueZ) Pair(ctx context.Context, mac string) error { return b.deviceCall(ctx, mac, "Pair") }

func (b *BlueZ) Connect(ctx context.Context, mac string) error {
	err := b.deviceCall(ctx, mac, "Connect")
	var de dbus.Error
	if errors.As(err, &de) && de.Name == "org.bluez.Error.AlreadyConnected" {
		return nil
	}
	return err
}

func (b *BlueZ) Disconnect(ctx context.Context, mac string) error {
	return b.deviceCall(ctx, mac, "Disconnect")
}

func (b *BlueZ) SetTrusted(ctx context.Context, mac string, on bool) error {
	p, err := b.devicePath(ctx, mac)
	if err != nil {
		return err
	}
	return mapErr(b.call(ctx, p, ifProps+".Set", ifDevice, "Trusted", dbus.MakeVariant(on)).Err)
}

// RegisterAgent exports the agent (once) and makes it BlueZ's default.
func (b *BlueZ) RegisterAgent(ctx context.Context) error {
	b.mu.Lock()
	if !b.exported {
		if err := b.conn.Export(agent{log: b.log}, agentPath, ifAgent); err != nil {
			b.mu.Unlock()
			return err
		}
		b.exported = true
	}
	b.mu.Unlock()
	err := mapErr(b.call(ctx, "/org/bluez", ifAgentMgr+".RegisterAgent", agentPath, agentCaps).Err)
	if err != nil && !errors.Is(err, ErrAlreadyExists) {
		return err
	}
	return mapErr(b.call(ctx, "/org/bluez", ifAgentMgr+".RequestDefaultAgent", agentPath).Err)
}

// mapErr turns BlueZ's errors into the package's.
func mapErr(err error) error {
	var de dbus.Error
	if !errors.As(err, &de) {
		return err
	}
	switch de.Name {
	case "org.bluez.Error.AlreadyExists":
		return ErrAlreadyExists
	case "org.bluez.Error.InProgress":
		return ErrInProgress
	case "org.bluez.Error.DoesNotExist":
		return ErrUnknownDevice
	}
	msg := ""
	if len(de.Body) > 0 {
		msg = fmt.Sprint(de.Body[0])
	}
	return dbus.Error{Name: de.Name, Body: []any{msg}}
}

// agent is org.bluez.Agent1 with no input and no output (N:services/bluetooth_pair_agent.py
// answers bluetoothctl's prompts "yes"): it accepts every pairing and service, so a phone can
// pair while the node is discoverable and a speaker in pairing mode pairs without a prompt.
type agent struct{ log *slog.Logger }

func (a agent) Release() *dbus.Error { return nil }

func (a agent) RequestPinCode(dev dbus.ObjectPath) (string, *dbus.Error) {
	a.log.Info("bluetooth agent: pin code requested", "device", macFromPath(dev))
	return "0000", nil
}

func (a agent) DisplayPinCode(dev dbus.ObjectPath, pin string) *dbus.Error { return nil }

func (a agent) RequestPasskey(dev dbus.ObjectPath) (uint32, *dbus.Error) {
	a.log.Info("bluetooth agent: passkey requested", "device", macFromPath(dev))
	return 0, nil
}

func (a agent) DisplayPasskey(dev dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return nil
}

func (a agent) RequestConfirmation(dev dbus.ObjectPath, passkey uint32) *dbus.Error {
	a.log.Info("bluetooth agent: confirmed a pairing", "device", macFromPath(dev))
	return nil
}

func (a agent) RequestAuthorization(dev dbus.ObjectPath) *dbus.Error {
	a.log.Info("bluetooth agent: authorized a pairing", "device", macFromPath(dev))
	return nil
}

func (a agent) AuthorizeService(dev dbus.ObjectPath, uuid string) *dbus.Error { return nil }

func (a agent) Cancel() *dbus.Error {
	a.log.Info("bluetooth agent: request canceled")
	return nil
}
