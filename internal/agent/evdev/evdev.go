// Package evdev reads the kernel's input events (/dev/input/event*) from the keyboards and
// pointers: what a desktop recording on Wayland sees of the user's keys and clicks, since the
// compositor shows no app another's input. Reading them needs the user in the input group (the
// device nodes are root:input 0660): Linux's counterpart of macOS's Input Monitoring grant.
// Nothing here logs what was typed.
package evdev

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event types and codes (linux/input-event-codes.h) a recording tells apart.
const (
	EvSyn = 0x00
	EvKey = 0x01
	EvRel = 0x02

	KeyEsc        = 1
	KeyBackspace  = 14
	KeyTab        = 15
	KeyEnter      = 28
	KeyLeftCtrl   = 29
	KeyA          = 30
	KeyLeftShift  = 42
	KeyZ          = 44
	KeyRightShift = 54
	KeyLeftAlt    = 56
	KeyCapsLock   = 58
	KeyKPEnter    = 96
	KeyRightCtrl  = 97
	KeyRightAlt   = 100
	KeyLeftMeta   = 125
	KeyRightMeta  = 126
	BtnLeft       = 0x110
	BtnRight      = 0x111
	BtnMiddle     = 0x112

	RelHWheel = 0x06
	RelWheel  = 0x08
)

// Event is one input event. Value: a key or button's 0 (up), 1 (down) or 2 (autorepeat); a
// relative axis's motion.
type Event struct {
	Time  time.Time
	Type  uint16
	Code  uint16
	Value int32
}

// frameSize is struct input_event's size: a timeval (two longs), two u16 and an s32.
var frameSize = 2*strconv.IntSize/8 + 8

// Read decodes r's input events until it ends (nil at a clean end), sending each but the EV_SYN markers. A torn
// last frame is an error (the whole ones before it are sent).
func Read(r io.Reader, send func(Event)) error {
	buf := make([]byte, frameSize*64) // the kernel hands over whole frames, several at once
	long := strconv.IntSize / 8
	var pending []byte
	for {
		n, err := r.Read(buf)
		pending = append(pending, buf[:n]...)
		for len(pending) >= frameSize {
			f := pending[:frameSize]
			var sec, usec int64
			if long == 8 {
				sec, usec = int64(binary.LittleEndian.Uint64(f)), int64(binary.LittleEndian.Uint64(f[8:]))
			} else {
				sec, usec = int64(int32(binary.LittleEndian.Uint32(f))), int64(int32(binary.LittleEndian.Uint32(f[4:])))
			}
			e := Event{Time: time.Unix(sec, usec*1000), Type: binary.LittleEndian.Uint16(f[2*long:]),
				Code: binary.LittleEndian.Uint16(f[2*long+2:]), Value: int32(binary.LittleEndian.Uint32(f[2*long+4:]))}
			pending = pending[frameSize:]
			if e.Type != EvSyn {
				send(e)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(pending) > 0 {
					return io.ErrUnexpectedEOF
				}
				return nil
			}
			return err
		}
	}
}

// Where the kernel lists the input devices and where their nodes are.
const (
	SysRoot = "/sys/class/input"
	DevRoot = "/dev/input"
)

// Device is an input device a recording reads.
type Device struct {
	Path     string // /dev/input/eventN
	Name     string
	Keyboard bool // has the letter keys and Enter
	Pointer  bool // has a left button (a mouse, a clickpad)
}

// Devices lists the keyboards and pointers among sys's event nodes (/sys/class/input), at
// their dev paths (/dev/input). Other devices (power buttons, lid switches, cameras) are left
// out.
func Devices(sys, dev string) ([]Device, error) {
	nodes, err := filepath.Glob(filepath.Join(sys, "event*"))
	if err != nil {
		return nil, err
	}
	var out []Device
	for _, n := range nodes {
		base := filepath.Base(n)
		key, err := os.ReadFile(filepath.Join(n, "device", "capabilities", "key"))
		if err != nil {
			continue
		}
		name, _ := os.ReadFile(filepath.Join(n, "device", "name"))
		d := Device{Path: filepath.Join(dev, base), Name: strings.TrimSpace(string(name)),
			Keyboard: hasBits(string(key), KeyA, KeyZ, KeyEnter), Pointer: hasBits(string(key), BtnLeft)}
		if d.Keyboard || d.Pointer {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b Device) int { return eventNumber(a.Path) - eventNumber(b.Path) })
	return out, nil
}

func eventNumber(path string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(path), "event"))
	return n
}

// hasBits reads a sysfs capability bitmap (hex longs, most significant first) and reports
// whether every bit is set.
func hasBits(bitmap string, want ...int) bool {
	words := strings.Fields(bitmap)
	if len(words) == 0 {
		return false
	}
	for _, b := range want {
		i := len(words) - 1 - b/bits.UintSize
		if i < 0 {
			return false
		}
		w, err := strconv.ParseUint(words[i], 16, bits.UintSize)
		if err != nil || w&(1<<(b%bits.UintSize)) == 0 {
			return false
		}
	}
	return true
}

var (
	// ErrNoAccess: the event devices can't be opened (the user isn't in the input group, or
	// this login started before they were added).
	ErrNoAccess = errors.New("can't read the keyboard and mouse devices")
	// ErrNoKeyboard: no keyboard among the devices.
	ErrNoKeyboard = errors.New("no keyboard found")
)

// openDevice opens a device node to read (tests replace it).
var openDevice = func(path string) (*os.File, error) { return os.OpenFile(path, os.O_RDONLY, 0) }

// Access reports whether devs can be read: nil once a keyboard opens, ErrNoAccess when
// permission is refused.
func Access(devs []Device) error {
	stop, err := Open(devs, nil)
	if err == nil {
		stop()
	}
	return err
}

// Open reads every device in devs, sending each event (from one goroutine per device; send
// must be safe for that) until stop, which closes them and waits. It fails unless a keyboard
// opens: ErrNoKeyboard without one, ErrNoAccess when permission is refused. send nil only
// opens them (Access).
func Open(devs []Device, send func(Event)) (stop func(), err error) {
	var files []*os.File
	keyboard, refused := false, false
	var lastErr error
	for _, d := range devs {
		f, err := openDevice(d.Path)
		if err != nil {
			refused = refused || errors.Is(err, fs.ErrPermission)
			lastErr = err
			continue
		}
		files = append(files, f)
		keyboard = keyboard || d.Keyboard
	}
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	if !keyboard {
		closeAll()
		switch {
		case refused:
			return nil, fmt.Errorf("%w: %v", ErrNoAccess, lastErr)
		case lastErr != nil:
			return nil, fmt.Errorf("%w: %v", ErrNoKeyboard, lastErr)
		}
		return nil, ErrNoKeyboard
	}
	if send == nil {
		return closeAll, nil
	}
	var wg sync.WaitGroup
	for _, f := range files {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Read(f, send) // ends when stop closes it (or the device goes away)
		}()
	}
	var once sync.Once
	return func() { once.Do(func() { closeAll(); wg.Wait() }) }, nil
}
