package firstrun

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

func TestLevel(t *testing.T) {
	full := make([]int16, 160)
	quiet := make([]int16, 160)
	for i := range full {
		full[i] = 32767
		if i%2 == 1 {
			full[i] = -32767
		}
		quiet[i] = 16 // about -66 dBFS
	}
	if l := Level(full); l < 0.99 {
		t.Fatalf("full scale: %v", l)
	}
	if l := Level(quiet); l != 0 {
		t.Fatalf("a quiet room: %v", l)
	}
	if l := Level(make([]int16, 160)); l != 0 {
		t.Fatalf("silence: %v", l)
	}
	mid := make([]int16, 160)
	for i := range mid {
		mid[i] = 1036 // about -30 dBFS
	}
	if l := Level(mid); l < 0.45 || l > 0.55 {
		t.Fatalf("-30 dBFS: %v", l)
	}
}

// scriptedMic is a microphone the test steps: each Read takes the next chunk and moves the
// clock.
type scriptedMic struct {
	mu     sync.Mutex
	value  int16
	reads  int
	onRead func(n int)
	closed bool
}

func (m *scriptedMic) Read(buf []int16) error {
	m.mu.Lock()
	m.reads++
	n, v := m.reads, m.value
	m.mu.Unlock()
	for i := range buf {
		buf[i] = v
	}
	if m.onRead != nil {
		m.onRead(n)
	}
	return nil
}
func (m *scriptedMic) Overruns() int { return 0 }
func (m *scriptedMic) Close() error  { m.mu.Lock(); m.closed = true; m.mu.Unlock(); return nil }

func TestMeterOpensOnAskAndClosesWhenNobodyAsks(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(0, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	opened := 0
	mic := &scriptedMic{value: 1036}
	firstRead := make(chan struct{})
	mic.onRead = func(n int) {
		if n == 1 {
			close(firstRead)
		}
		mu.Lock()
		now = now.Add(time.Second) // 100 ms of audio, a slow clock: idle after a few reads
		mu.Unlock()
	}
	m := newMeter(func() (device.Capture, error) { opened++; return mic, nil }, clock)
	if l, e := m.Level(); l != 0 || e != "" {
		t.Fatalf("first ask: %v %q", l, e)
	}
	<-firstRead
	<-m.done // nobody asked again: closed after meterIdle of clock
	if !mic.closed || opened != 1 {
		t.Fatalf("closed=%v opened=%d", mic.closed, opened)
	}
	// Asking again opens it again.
	mic.closed = false
	m.Level()
	m.close()
	if opened != 2 || !mic.closed {
		t.Fatalf("reopened=%d closed=%v", opened, mic.closed)
	}
	// Closed for good: asking doesn't open it.
	m.Level()
	if opened != 2 {
		t.Fatal("opened after close")
	}
}

func TestMeterReportsAMicrophoneThatWontOpen(t *testing.T) {
	m := newMeter(func() (device.Capture, error) { return nil, errors.New("busy") }, time.Now)
	m.Level()
	<-m.done
	if _, e := m.Level(); e == "" {
		t.Fatal("no error shown")
	}
	first := m.done
	m.Level() // tries again
	if m.done == first {
		t.Fatal("didn't try again")
	}
	<-m.done
}
