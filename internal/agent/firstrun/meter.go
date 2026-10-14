package firstrun

import (
	"math"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// MicOpener opens the microphone the agent listens with (16 kHz mono).
type MicOpener func() (device.Capture, error)

// meterRate and meterChunk: the meter reads 100 ms at a time.
const (
	meterRate  = 16000
	meterChunk = meterRate / 10
)

// meterIdle closes the microphone when the page stops asking for the level (the tab was
// closed or hidden), so the meter never keeps it open.
const meterIdle = 3 * time.Second

// meter is the page's microphone level: the microphone opens on the first ask and closes once
// nobody has asked for meterIdle.
type meter struct {
	open MicOpener
	now  func() time.Time

	mu      sync.Mutex
	running bool
	level   float64
	err     string
	asked   time.Time
	stop    bool
	done    chan struct{} // closed when the reader has closed the microphone
}

func newMeter(open MicOpener, now func() time.Time) *meter {
	return &meter{open: open, now: now}
}

// Level is the latest level (0..1), starting the microphone if it isn't open. errText is set
// when the microphone can't be opened or read.
func (m *meter) Level() (level float64, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = m.now()
	if e := m.err; e != "" {
		m.err = "" // shown once; the next ask tries again
		return 0, e
	}
	if !m.running && !m.stop {
		m.running, m.level = true, 0
		m.done = make(chan struct{})
		go m.read(m.done)
	}
	return m.level, m.err
}

// read runs the microphone until nobody asks or the meter closes.
func (m *meter) read(done chan struct{}) {
	defer close(done)
	c, err := m.open()
	if err != nil {
		m.mu.Lock()
		m.running, m.err = false, micError(err)
		m.mu.Unlock()
		return
	}
	defer c.Close()
	buf := make([]int16, meterChunk)
	for {
		err := c.Read(buf)
		m.mu.Lock()
		if err != nil {
			m.running, m.err = false, micError(err)
			m.mu.Unlock()
			return
		}
		m.level = Level(buf)
		if m.stop || m.now().Sub(m.asked) >= meterIdle {
			m.running, m.level = false, 0
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

// close stops the meter for good and waits for the microphone to close.
func (m *meter) close() {
	m.mu.Lock()
	m.stop = true
	done := m.done
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}

func micError(err error) string {
	return "The microphone couldn't be opened (" + err.Error() + "). Check that one is connected and that " +
		"Jarvis Automation Agent may use it."
}

// Level maps a chunk's loudness to 0..1 for the meter: -60 dBFS (a quiet room) and below is 0,
// full scale is 1, linear in dB between, as a speech meter reads.
func Level(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		f := float64(v)
		sum += f * f
	}
	rms := math.Sqrt(sum / float64(len(s)))
	if rms < 1 {
		return 0
	}
	db := 20 * math.Log10(rms/32768)
	return math.Max(0, math.Min(1, (db+60)/60))
}
