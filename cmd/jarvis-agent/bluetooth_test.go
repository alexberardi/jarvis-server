package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/bluetooth"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// noRadio is a BlueZ with an adapter that's off: enough to start the handler.
type noRadio struct{ agent chan struct{} }

func (noRadio) Adapter(context.Context) (bluetooth.Adapter, error)   { return bluetooth.Adapter{}, nil }
func (noRadio) SetAdapter(context.Context, string, any) error        { return nil }
func (noRadio) Devices(context.Context) ([]bluetooth.Device, error)  { return nil, nil }
func (noRadio) AudioTransport(context.Context, string) (bool, error) { return false, nil }
func (noRadio) Discover(context.Context, string) (func(), error)     { return func() {}, nil }
func (noRadio) Remove(context.Context, string) error                 { return nil }
func (noRadio) Pair(context.Context, string) error                   { return nil }
func (noRadio) Connect(context.Context, string) error                { return nil }
func (noRadio) Disconnect(context.Context, string) error             { return nil }
func (noRadio) SetTrusted(context.Context, string, bool) error       { return nil }
func (r noRadio) RegisterAgent(context.Context) error                { close(r.agent); return nil }
func (noRadio) Paired() <-chan string                                { return nil }
func (noRadio) Connections() <-chan bluetooth.Connection             { return nil }

func TestWireBluetoothSubscribesTheSixTopics(t *testing.T) {
	r := &node.Runner{Topics: map[string]func(context.Context, *node.CC, []byte){"k2/provision": nil}}
	radio := noRadio{agent: make(chan struct{})}
	closed := false
	defer func(o func(*slog.Logger) (bluetooth.Bus, func(), error)) { openBluetooth = o }(openBluetooth)
	openBluetooth = func(*slog.Logger) (bluetooth.Bus, func(), error) { return radio, func() { closed = true }, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wireBluetooth(ctx, r, t.TempDir(), slog.New(slog.DiscardHandler))()

	var got []string
	for k := range r.Topics {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"bluetooth-auto-connect", "bluetooth-disconnect", "bluetooth-discoverable", "bluetooth-pair", "bluetooth-release", "bluetooth-scan", "k2/provision"}
	if !slices.Equal(got, want) {
		t.Fatalf("topics %v", got)
	}
	<-radio.agent // the pairing agent is registered at start
	if !closed {
		t.Error("the bus connection isn't closed")
	}
}

func TestWireBluetoothWithoutBlueZRunsWithout(t *testing.T) {
	r := &node.Runner{}
	defer func(o func(*slog.Logger) (bluetooth.Bus, func(), error)) { openBluetooth = o }(openBluetooth)
	openBluetooth = func(*slog.Logger) (bluetooth.Bus, func(), error) { return nil, nil, errors.New("no system bus") }
	wireBluetooth(context.Background(), r, t.TempDir(), slog.New(slog.DiscardHandler))()
	if len(r.Topics) != 0 {
		t.Fatalf("topics %v", r.Topics)
	}
}
