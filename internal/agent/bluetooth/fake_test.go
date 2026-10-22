package bluetooth

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// fakeBus is BlueZ in memory: an adapter and its devices. inRange are the devices a discovery
// finds; pairFails and connectFails make those calls fail.
type fakeBus struct {
	mu           sync.Mutex
	adapter      Adapter
	noAdapter    bool
	props        map[string]any // SetAdapter values
	devices      map[string]*Device
	inRange      map[string]Device
	pairErr      map[string]error
	connectErr   map[string]error
	removeErr    error
	pairErrOnce  map[string][]error // returned by the next Pair calls, in order, before pairErr
	connErrOnce  map[string][]error // returned by the next Connect calls, in order, before connectErr
	transport    map[string]bool    // a MediaTransport1 under the device (its audio profile is up)
	discovering  []string           // transports, in order
	calls        []string
	agentCount   int
	paired       chan string
	conns        chan Connection
	onConnect    func(mac string) // e.g. Pulse makes the sink
	onDisconnect func(mac string)
}

func newFakeBus() *fakeBus {
	return &fakeBus{adapter: Adapter{Powered: true}, props: map[string]any{}, devices: map[string]*Device{},
		inRange: map[string]Device{}, pairErr: map[string]error{}, connectErr: map[string]error{}, paired: make(chan string, 4),
		conns: make(chan Connection, 4)}
}

func (b *fakeBus) call(f string, a ...any) {
	parts := []string{f}
	for _, v := range a {
		parts = append(parts, fmt.Sprint(v))
	}
	b.calls = append(b.calls, strings.Join(parts, " "))
}

func (b *fakeBus) Calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.calls)
}

func (b *fakeBus) Adapter(context.Context) (Adapter, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.noAdapter {
		return Adapter{}, ErrNoAdapter
	}
	return b.adapter, nil
}

func (b *fakeBus) SetAdapter(_ context.Context, prop string, v any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.call("SetAdapter", prop, v)
	b.props[prop] = v
	if prop == "Powered" {
		b.adapter.Powered = v.(bool)
	}
	return nil
}

func (b *fakeBus) Devices(context.Context) ([]Device, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Device
	for _, d := range b.devices {
		c := *d
		out = append(out, c)
	}
	slices.SortFunc(out, func(x, y Device) int { return strings.Compare(x.Address, y.Address) })
	return out, nil
}

func (b *fakeBus) Discover(_ context.Context, transport string) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.call("Discover", transport)
	b.discovering = append(b.discovering, transport)
	for mac, d := range b.inRange {
		if _, ok := b.devices[mac]; !ok {
			c := d
			b.devices[mac] = &c
		}
		b.devices[mac].RSSI = -60
	}
	return func() {
		b.mu.Lock()
		b.call("StopDiscovery", transport)
		for _, d := range b.devices {
			d.RSSI = 0
		}
		b.mu.Unlock()
	}, nil
}

func (b *fakeBus) get(mac string) (*Device, error) {
	d, ok := b.devices[mac]
	if !ok {
		return nil, ErrUnknownDevice
	}
	return d, nil
}

func (b *fakeBus) Remove(_ context.Context, mac string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.call("Remove", mac)
	if _, err := b.get(mac); err != nil {
		return err
	}
	if b.removeErr != nil {
		return b.removeErr
	}
	delete(b.devices, mac)
	return nil
}

func (b *fakeBus) Pair(_ context.Context, mac string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.call("Pair", mac)
	d, err := b.get(mac)
	if err != nil {
		return err
	}
	if q := b.pairErrOnce[mac]; len(q) > 0 {
		b.pairErrOnce[mac] = q[1:]
		return q[0]
	}
	if err := b.pairErr[mac]; err != nil {
		return err
	}
	if d.Paired {
		return ErrAlreadyExists
	}
	d.Paired = true
	b.paired <- mac
	return nil
}

func (b *fakeBus) Connect(_ context.Context, mac string) error {
	b.mu.Lock()
	b.call("Connect", mac)
	d, err := b.get(mac)
	if q := b.connErrOnce[mac]; err == nil && len(q) > 0 {
		b.connErrOnce[mac] = q[1:]
		err = q[0]
	} else if err == nil {
		err = b.connectErr[mac]
	}
	if err == nil {
		d.Connected = true
	}
	hook := b.onConnect
	b.mu.Unlock()
	if err == nil && hook != nil {
		hook(mac)
	}
	return err
}

func (b *fakeBus) Disconnect(_ context.Context, mac string) error {
	b.mu.Lock()
	b.call("Disconnect", mac)
	d, err := b.get(mac)
	if err == nil {
		d.Connected = false
	}
	hook := b.onDisconnect
	b.mu.Unlock()
	if err == nil && hook != nil {
		hook(mac)
	}
	return err
}

func (b *fakeBus) SetTrusted(_ context.Context, mac string, on bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.call("SetTrusted", mac, on)
	d, err := b.get(mac)
	if err != nil {
		return err
	}
	d.Trusted = on
	return nil
}

func (b *fakeBus) RegisterAgent(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.agentCount++
	return nil
}

func (b *fakeBus) Paired() <-chan string { return b.paired }

func (b *fakeBus) Connections() <-chan Connection { return b.conns }

func (b *fakeBus) AudioTransport(_ context.Context, mac string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.get(mac); err != nil {
		return false, err
	}
	return b.transport[mac], nil
}

// fakePulse is PulseAudio's sinks, sources and modules.
type fakePulse struct {
	mu        sync.Mutex
	sinks     []string
	sources   []string
	cards     []string
	modules   []Module
	next      int
	def       string            // the default sink
	listErr   error             // every pactl call fails (no PulseAudio reachable)
	profiles  map[string]string // card -> the profile set
	onProfile func(card, profile string)
}

func (p *fakePulse) List(_ context.Context, kind string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listErr != nil {
		return nil, p.listErr
	}
	switch kind {
	case "sinks":
		return slices.Clone(p.sinks), nil
	case "cards":
		return slices.Clone(p.cards), nil
	}
	return slices.Clone(p.sources), nil
}

func (p *fakePulse) DefaultSink(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listErr != nil {
		return "", p.listErr
	}
	return p.def, nil
}

func (p *fakePulse) SetDefaultSink(_ context.Context, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.Contains(p.sinks, name) {
		return fmt.Errorf("no sink %s", name)
	}
	p.def = name
	return nil
}

func (p *fakePulse) SetCardProfile(_ context.Context, card, profile string) error {
	p.mu.Lock()
	if p.profiles == nil {
		p.profiles = map[string]string{}
	}
	p.profiles[card] = profile
	hook := p.onProfile
	p.mu.Unlock()
	if hook != nil {
		hook(card, profile)
	}
	return nil
}

func (p *fakePulse) Modules(context.Context) ([]Module, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.modules), nil
}

func (p *fakePulse) LoadModule(_ context.Context, name, args string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	id := fmt.Sprint(20 + p.next)
	p.modules = append(p.modules, Module{ID: id, Name: name, Args: args})
	return id, nil
}

func (p *fakePulse) UnloadModule(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.modules = slices.DeleteFunc(p.modules, func(m Module) bool { return m.ID == id })
	return nil
}

func (p *fakePulse) add(sink, source string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sink != "" {
		p.sinks = append(p.sinks, sink)
	}
	if source != "" {
		p.sources = append(p.sources, source)
	}
}

func (p *fakePulse) drop(mac string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := macUnderscored(mac)
	keep := func(s string) bool { return strings.Contains(s, m) }
	p.sinks = slices.DeleteFunc(p.sinks, keep)
	p.sources = slices.DeleteFunc(p.sources, keep)
	p.cards = slices.DeleteFunc(p.cards, keep)
	if keep(p.def) { // Pulse falls back to another sink
		p.def = "jarvis_output"
	}
}

// fakeAPI records the result POSTs, and the progress ones in order.
type fakeAPI struct {
	mu       sync.Mutex
	scans    map[string]map[string]any
	pairs    map[string]map[string]any
	progress []string // "scan rid N devices" / "pair rid step"
}

func (a *fakeAPI) PostBluetoothScanProgress(_ context.Context, rid string, body map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progress = append(a.progress, fmt.Sprintf("scan %s %v %d devices", rid, body["progress"], len(body["devices"].([]ScanDevice))))
	return nil
}

func (a *fakeAPI) PostBluetoothPairProgress(_ context.Context, rid string, body map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progress = append(a.progress, fmt.Sprintf("pair %s %v", rid, body["progress"]))
	return nil
}

func (a *fakeAPI) PostBluetoothScanResult(_ context.Context, rid string, body map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.scans == nil {
		a.scans = map[string]map[string]any{}
	}
	a.scans[rid] = body
	return nil
}

func (a *fakeAPI) PostBluetoothPairResult(_ context.Context, rid string, body map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pairs == nil {
		a.pairs = map[string]map[string]any{}
	}
	a.pairs[rid] = body
	return nil
}
