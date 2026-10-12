//go:build linux

package hat

import (
	"bytes"
	"fmt"
	"os"
	"unsafe"

	"github.com/warthog618/go-gpiocdev"
	"golang.org/x/sys/unix"
)

// SPIDevice is the HAT's LED chain (spi0, chip select 0).
const SPIDevice = "/dev/spidev0.0"

// spiSpeedHz is apa102_pi's default bus_speed_hz.
const spiSpeedHz = 8_000_000

// _IOW('k', 4, __u32): SPI_IOC_WR_MAX_SPEED_HZ.
const spiIOCWrMaxSpeedHz = 0x40046b04

type spiStrip struct{ f *os.File }

// OpenStrip opens the LED chain on path at the APA102 bus speed. Each frame is one write (one
// ioctl-free transfer of a few dozen bytes).
func OpenStrip(path string) (Strip, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	hz := uint32(spiSpeedHz)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), spiIOCWrMaxSpeedHz, uintptr(unsafe.Pointer(&hz))); e != 0 {
		f.Close()
		return nil, fmt.Errorf("%s: set speed: %w", path, e)
	}
	return spiStrip{f}, nil
}

func (s spiStrip) Write(b []byte) (int, error) { return s.f.Write(b) }
func (s spiStrip) Close() error                { return s.f.Close() }

// WatchButton requests GPIO17 on gpiochip0 as an input with pull-up and both edges, feeding b
// (pressed = line low). The returned func releases the line.
func WatchButton(b *Button) (func() error, error) {
	l, err := gpiocdev.RequestLine("gpiochip0", ButtonGPIO, gpiocdev.AsInput, gpiocdev.WithPullUp,
		gpiocdev.WithBothEdges, gpiocdev.WithConsumer("jarvis-agent"),
		gpiocdev.WithEventHandler(func(e gpiocdev.LineEvent) {
			b.Edge(e.Type == gpiocdev.LineEventFallingEdge)
		}))
	if err != nil {
		return nil, fmt.Errorf("gpiochip0 line %d: %w", ButtonGPIO, err)
	}
	if v, err := l.Value(); err == nil && v == 0 {
		b.Edge(true) // held at start
	}
	return l.Close, nil
}

// Detected reports whether this is a Raspberry Pi with the ReSpeaker 2-mics HAT: the LED chain's
// SPI device and the HAT's sound card (seeed-voicecard's "seeed2micvoicec").
func Detected() bool {
	model, err := os.ReadFile("/proc/device-tree/model")
	if err != nil || !bytes.Contains(model, []byte("Raspberry Pi")) {
		return false
	}
	if _, err := os.Stat(SPIDevice); err != nil {
		return false
	}
	cards, err := os.ReadFile("/proc/asound/cards")
	return err == nil && bytes.Contains(cards, []byte("seeed2micvoicec"))
}
