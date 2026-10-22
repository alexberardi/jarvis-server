package device

import (
	"fmt"
	"sync"
	"time"
)

// Switch is a microphone that can move to another device between reads: the Pi's echo-cancelled
// capture while its own music plays (internal/agent/music's EchoCancel). Use names the device
// the next Read opens; a Read never spans two devices. When that other device fails to open,
// errors or stays dead silent for SilentFor, the Switch goes back to its base by itself and reports
// it once through OnFallback, so a broken echo-cancel stack never costs the wake word.
//
// Read and Close belong to one goroutine, like any Capture; Use and Current are safe from any.
type Switch struct {
	Rate, Channels int
	// SilentFor is how long the other device may read nothing but samples within ±SilenceFloor
	// before it counts as dead (0: never). A live microphone is never that flat; a module whose
	// source lost its master is.
	SilentFor    time.Duration
	SilenceFloor int16
	// OnFallback is told the device given up on and why ("open", "read", "silent"). It runs on
	// the reading goroutine and must not block.
	OnFallback func(name, reason string, err error)
	// Open opens a device (nil: OpenCapture).
	Open func(name string, rate, channels int) (Capture, error)

	mu   sync.Mutex
	base string
	want string // "" is the base

	cur      Capture
	curName  string
	quiet    int // consecutive samples within the floor on a device other than Base
	overruns int // of the devices already closed
}

// NewSwitch is a Switch with N:'s silent-capture watchdog (3 s of pure digital silence).
func NewSwitch(rate, channels int) *Switch {
	return &Switch{Rate: rate, Channels: channels, SilentFor: 3 * time.Second, SilenceFloor: 1}
}

// Start makes base the device to fall back to and opens the wanted one now, so a missing
// microphone fails at open time as with OpenCapture. The voice pipeline calls it each time it
// (re)starts on its input.
func (s *Switch) Start(base string) error {
	s.mu.Lock()
	s.base = base
	if s.want == base {
		s.want = ""
	}
	s.mu.Unlock()
	return s.sync()
}

// Base is the device Start was given ("" before it).
func (s *Switch) Base() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base
}

// Use moves capture to name ("" or the base: back to the base) from the next Read on.
func (s *Switch) Use(name string) {
	s.mu.Lock()
	if name == s.base {
		name = ""
	}
	s.want = name
	s.mu.Unlock()
}

// Current is the device Reads come from now, or are about to (Use's last word).
func (s *Switch) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.want == "" {
		return s.base
	}
	return s.want
}

func (s *Switch) open(name string) (Capture, error) {
	if s.Open != nil {
		return s.Open(name, s.Rate, s.Channels)
	}
	return OpenCapture(name, s.Rate, s.Channels)
}

// sync makes cur the wanted device.
func (s *Switch) sync() error {
	name, base := s.Current(), s.Base()
	if s.cur != nil && s.curName == name {
		return nil
	}
	s.closeCur()
	c, err := s.open(name)
	if err != nil && name != base {
		s.fallBack(name, "open", err)
		return s.sync()
	}
	if err != nil {
		return err
	}
	s.cur, s.curName, s.quiet = c, name, 0
	return nil
}

func (s *Switch) closeCur() {
	if s.cur == nil {
		return
	}
	s.overruns += s.cur.Overruns()
	_ = s.cur.Close()
	s.cur, s.curName = nil, ""
}

// fallBack gives up on name: back to the base unless Use moved on meanwhile.
func (s *Switch) fallBack(name, reason string, err error) {
	s.mu.Lock()
	if s.want == name {
		s.want = ""
	}
	s.mu.Unlock()
	if s.OnFallback != nil {
		s.OnFallback(name, reason, err)
	}
}

func (s *Switch) Read(buf []int16) error {
	if err := s.sync(); err != nil {
		return err
	}
	onBase := s.curName == s.Base()
	if err := s.cur.Read(buf); err != nil {
		if onBase {
			return err
		}
		name := s.curName
		s.closeCur()
		s.fallBack(name, "read", err)
		return s.Read(buf)
	}
	if !onBase && s.SilentFor > 0 {
		s.watch(buf)
	}
	return nil
}

// watch counts the other device's flat samples; SilentFor of them is a dead capture.
func (s *Switch) watch(buf []int16) {
	for _, v := range buf {
		if v > s.SilenceFloor || v < -s.SilenceFloor {
			s.quiet = 0
			return
		}
	}
	s.quiet += len(buf)
	if limit := int(s.SilentFor.Seconds() * float64(s.Rate*s.Channels)); s.quiet >= limit {
		name := s.curName
		s.closeCur()
		s.fallBack(name, "silent", fmt.Errorf("%s read %v of silence", name, s.SilentFor))
	}
}

func (s *Switch) Overruns() int {
	n := s.overruns
	if s.cur != nil {
		n += s.cur.Overruns()
	}
	return n
}

// Close closes the current device; a later Read opens the wanted one again.
func (s *Switch) Close() error {
	if s.cur == nil {
		return nil
	}
	s.overruns += s.cur.Overruns()
	err := s.cur.Close()
	s.cur, s.curName = nil, ""
	return err
}
